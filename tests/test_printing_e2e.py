# SPDX-License-Identifier: GPL-3.0-only
from collections import Counter
from app.db.connection import now_ms
from app.domain import routing
from app.print import render, transport, worker
from app.services import printing

def _dish_id(db, name):
    return db.query_one('SELECT id FROM dishes WHERE name=?', (name,))['id']

def _add_drink(db, name='柠檬茶'):
    with db.transaction() as conn:
        cat = conn.execute("SELECT id FROM categories WHERE name='Drinks'").fetchone()['id']
        front = conn.execute("SELECT id FROM stations WHERE code='front'").fetchone()['id']
        cur = conn.execute("INSERT INTO dishes(name,category_id,price_jiao,unit,temp,is_active) VALUES(?,?,?, '瓶','drink',1)", (name, cat, 73))
        did = cur.lastrowid
        conn.execute("INSERT INTO dish_station_bindings(dish_id,station_id,print_rule) VALUES(?,?, 'total')", (did, front))
    return did

def _demo_requests(db):
    drink = _add_drink(db)
    return [printing.ItemRequest(_dish_id(db, '宫保鸡丁'), 1, created_seq=1), printing.ItemRequest(_dish_id(db, '麻婆豆腐'), 1, created_seq=2), printing.ItemRequest(_dish_id(db, '凉拌黄瓜'), 1, created_seq=3), printing.ItemRequest(drink, 2, created_seq=4)]

def _ctx():
    return render.RenderContext(table_label='Table 1', party_size=4, operator='Demo Operator', print_time_ms=now_ms())

def _fakes(mgr):
    fs = {}
    for code in ('expo', 'kitchen', 'cold', 'front'):
        f = transport.FakeTransport(code)
        fs[code] = f
        mgr.set_transport(code, f)
    return fs

def _station_counts(db):
    rows = db.query('SELECT s.code AS code FROM print_jobs pj JOIN stations s ON s.id=pj.station_id')
    return Counter((r['code'] for r in rows))

def test_seed_summary(seeded_db):
    assert seeded_db.query_one('SELECT COUNT(*) c FROM dishes')['c'] == 3
    assert seeded_db.query_one('SELECT COUNT(*) c FROM stations')['c'] == 4
    assert seeded_db.query_one('SELECT COUNT(*) c FROM dish_station_bindings WHERE needs_review!=0')['c'] == 0
    cold = seeded_db.query("SELECT st.code FROM dish_station_bindings b JOIN dishes d ON d.id=b.dish_id JOIN stations st ON st.id=b.station_id WHERE d.name='凉拌黄瓜'")
    assert [r['code'] for r in cold] == ['cold']
    hot = sorted((r['code'] for r in seeded_db.query("SELECT st.code FROM dish_station_bindings b JOIN dishes d ON d.id=b.dish_id JOIN stations st ON st.id=b.station_id WHERE d.name='宫保鸡丁'")))
    assert hot == ['expo', 'kitchen']

def test_plan_and_enqueue_routes_to_stations(seeded_db):
    ids = printing.plan_and_enqueue(seeded_db, op=routing.Op.NEW, requests=_demo_requests(seeded_db), ctx=_ctx())
    assert len(ids) == 5
    c = _station_counts(seeded_db)
    assert c['expo'] == 1 and c['kitchen'] == 2 and (c['cold'] == 1) and (c['front'] == 1)
    rows = seeded_db.query('SELECT payload, ticket_type FROM print_jobs')
    assert all((isinstance(r['payload'], bytes) and len(r['payload']) > 0 for r in rows))
    expo_tt = seeded_db.query_one("SELECT ticket_type FROM print_jobs pj JOIN stations s ON s.id=pj.station_id WHERE s.code='expo'")['ticket_type']
    assert expo_tt == '厨总单'

def test_worker_delivers_in_order_with_fakes(seeded_db):
    printing.plan_and_enqueue(seeded_db, op=routing.Op.NEW, requests=_demo_requests(seeded_db), ctx=_ctx())
    mgr = worker.PrintManager(seeded_db)
    mgr.reload_stations()
    fs = _fakes(mgr)
    mgr.tick()
    assert seeded_db.query_one("SELECT COUNT(*) c FROM print_jobs WHERE state!='done'")['c'] == 0
    assert len(fs['kitchen'].sent) == 2
    assert len(fs['expo'].sent) == 1
    assert len(fs['cold'].sent) == 1
    assert len(fs['front'].sent) == 1

def test_offline_queues_then_flushes_on_reconnect(seeded_db):
    printing.plan_and_enqueue(seeded_db, op=routing.Op.NEW, requests=_demo_requests(seeded_db), ctx=_ctx())
    mgr = worker.PrintManager(seeded_db)
    mgr.reload_stations()
    fs = _fakes(mgr)
    fs['expo'].online = False
    mgr.tick()
    expo_state = seeded_db.query_one("SELECT state FROM print_jobs pj JOIN stations s ON s.id=pj.station_id WHERE s.code='expo'")['state']
    assert expo_state == 'failed'
    assert len(fs['expo'].sent) == 0
    assert len(fs['kitchen'].sent) == 2
    fs['expo'].online = True
    seeded_db.execute("UPDATE print_jobs SET next_attempt_at=0 WHERE state='failed'")
    mgr.tick()
    assert seeded_db.query_one("SELECT COUNT(*) c FROM print_jobs WHERE state!='done'")['c'] == 0
    assert len(fs['expo'].sent) == 1

def test_health_reports_offline_with_reason(seeded_db):
    mgr = worker.PrintManager(seeded_db)
    mgr.reload_stations()
    fs = _fakes(mgr)
    fs['expo'].online = False
    health = mgr.refresh_health(probe=True)
    expo = next((h for h in health if h.code == 'expo'))
    assert expo.reachable is False
    assert expo.reason

def test_misconfigured_station_surfaces_not_silently_dropped(seeded_db):
    seeded_db.execute("INSERT INTO stations(code,name,transport,net_host,net_port,serial_path,aggregation,dish_temp,has_drawer,prints_bills,move_table_ticket,sort,enabled) VALUES('badnet','坏网口','net',NULL,9100,NULL,'per_dish','hot',0,0,0,99,1)")
    sid = seeded_db.query_one("SELECT id FROM stations WHERE code='badnet'")['id']
    seeded_db.execute("INSERT INTO print_jobs(station_id,ticket_type,serial_no,payload,dedupe_key,state,enqueue_seq,created_at,next_attempt_at,attempts) VALUES(?,'kitchen',0,X'01',NULL,'pending',999999,1,0,0)", (sid,))
    mgr = worker.PrintManager(seeded_db)
    mgr.reload_stations()
    health = mgr.refresh_health(probe=False)
    bad = next((h for h in health if h.code == 'badnet'), None)
    assert bad is not None, '配置错误的出单口被静默丢弃了（回归！）'
    assert bad.reachable is False
    assert '配置错误' in (bad.reason or '')
    assert bad.pending >= 1

def test_reprint_clones_payload_with_fresh_serial(seeded_db):
    printing.plan_and_enqueue(seeded_db, op=routing.Op.NEW, requests=_demo_requests(seeded_db), ctx=_ctx())
    src = seeded_db.query_one('SELECT * FROM print_jobs ORDER BY id LIMIT 1')
    new_id = printing.reprint_job(seeded_db, src['id'])
    new = seeded_db.query_one('SELECT * FROM print_jobs WHERE id=?', (new_id,))
    assert new['payload'] == src['payload']
    assert new['serial_no'] != src['serial_no']
    assert new['is_reprint'] == 1
    assert seeded_db.query_one("SELECT COUNT(*) c FROM audit_log WHERE kind='reprint'")['c'] == 1

def test_serials_unique_per_station(seeded_db):
    printing.plan_and_enqueue(seeded_db, op=routing.Op.NEW, requests=_demo_requests(seeded_db), ctx=_ctx())
    printing.plan_and_enqueue(seeded_db, op=routing.Op.ADD, requests=_demo_requests(seeded_db), ctx=_ctx())
    rows = seeded_db.query('SELECT s.code AS code, pj.serial_no AS sn FROM print_jobs pj JOIN stations s ON s.id=pj.station_id')
    seen = set()
    for r in rows:
        key = (r['code'], r['sn'])
        assert key not in seen, f'duplicate serial {key}'
        seen.add(key)

def test_combo_expands_only_this_stations_components(seeded_db):
    hot = _dish_id(seeded_db, '宫保鸡丁')
    cold = _dish_id(seeded_db, '凉拌黄瓜')
    with seeded_db.transaction() as conn:
        cat = conn.execute("SELECT id FROM categories WHERE name='Combo'").fetchone()['id']
        cur = conn.execute("INSERT INTO dishes(name,category_id,price_jiao,unit,temp,is_combo,is_active) VALUES('Demo Combo',?,?, '份','special',1,1)", (cat, 917))
        combo_id = cur.lastrowid
        conn.execute('INSERT INTO combo_components(combo_dish_id,component_dish_id,qty) VALUES(?,?,1)', (combo_id, hot))
        conn.execute('INSERT INTO combo_components(combo_dish_id,component_dish_id,qty) VALUES(?,?,1)', (combo_id, cold))
    printing.plan_and_enqueue(seeded_db, op=routing.Op.NEW, requests=[printing.ItemRequest(combo_id, 1, created_seq=1)], ctx=_ctx())
    c = _station_counts(seeded_db)
    assert c['expo'] == 1 and c['kitchen'] == 1 and (c['cold'] == 1)
    assert c.get('front', 0) == 0
    cold_payload = seeded_db.query_one("SELECT payload FROM print_jobs pj JOIN stations s ON s.id=pj.station_id WHERE s.code='cold'")['payload']
    assert 'Demo Combo'.encode('gb18030') in cold_payload
    assert '-凉拌黄瓜'.encode('gb18030') in cold_payload

def test_combo_ticket_layout_req4(seeded_db):
    h1 = _dish_id(seeded_db, '宫保鸡丁')
    h2 = _dish_id(seeded_db, '麻婆豆腐')
    cold = _dish_id(seeded_db, '凉拌黄瓜')
    with seeded_db.transaction() as conn:
        cat = conn.execute("SELECT id FROM categories WHERE name='Combo'").fetchone()['id']
        cur = conn.execute("INSERT INTO dishes(name,category_id,price_jiao,unit,temp,is_combo,is_active) VALUES('Demo Layout Combo',?,?, '份','special',1,1)", (cat, 913))
        combo_id = cur.lastrowid
        for cid in (h1, h2, cold):
            conn.execute('INSERT INTO combo_components(combo_dish_id,component_dish_id,qty) VALUES(?,?,1)', (combo_id, cid))
    printing.plan_and_enqueue(seeded_db, op=routing.Op.NEW, requests=[printing.ItemRequest(combo_id, 1, created_seq=1)], ctx=_ctx())
    c = _station_counts(seeded_db)
    assert c['expo'] == 1, f'传菜口应把所有热菜聚合成 1 张总单，实际 {dict(c)}'
    assert c['kitchen'] == 2, f'厨房必须一菜一纸(2 张, 绝不聚合)，实际 {dict(c)}'
    assert c['cold'] == 1, f'凉菜房一菜一纸，实际 {dict(c)}'
    assert c.get('front', 0) == 0
    name = 'Demo Layout Combo'.encode('gb18030')
    kp = [r['payload'] for r in seeded_db.query("SELECT payload FROM print_jobs pj JOIN stations s ON s.id=pj.station_id WHERE s.code='kitchen'")]
    assert len(kp) == 2 and all((name in p for p in kp)), '每张厨房单都应在菜名上方备注Combo名'
    cold_payload = seeded_db.query_one("SELECT payload FROM print_jobs pj JOIN stations s ON s.id=pj.station_id WHERE s.code='cold'")['payload']
    assert name in cold_payload, '凉菜房单应备注Combo名'
    joined = b''.join(kp)
    assert '宫保鸡丁'.encode('gb18030') in joined and '麻婆豆腐'.encode('gb18030') in joined
