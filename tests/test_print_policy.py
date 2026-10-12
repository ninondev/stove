# SPDX-License-Identifier: GPL-3.0-only
from collections import defaultdict

import pytest

from app.domain import routing
from app.domain.errors import ValidationError
from app.print import escpos, render
from app.services import printing as P, settings as S


def _enqueue(db):
    dish = db.query_one("SELECT id FROM dishes WHERE name='宫保鸡丁'")['id']
    return P.plan_and_enqueue(
        db, op=routing.Op.NEW, requests=[P.ItemRequest(dish)],
        ctx=render.RenderContext(table_label='Table 1', party_size=2, operator='Demo Operator'),
    )


def test_kitchen_manual_holds_then_release(seeded_db):
    S.set_print_policy(seeded_db, {'kitchen': 'manual'})
    _enqueue(seeded_db)
    jobs = seeded_db.query('SELECT state,serial_no,payload FROM print_jobs')
    assert jobs and all(row['state'] == 'hold' for row in jobs)
    assert all(row['serial_no'] == 0 for row in jobs)
    assert all('流水号 No.'.encode('gb18030') not in bytes(row['payload']) for row in jobs)
    assert P.release_holds(seeded_db) == len(jobs)
    assert seeded_db.query_one("SELECT COUNT(*) c FROM print_jobs WHERE state='hold'")['c'] == 0
    assert all(row['state'] == 'pending' for row in seeded_db.query('SELECT state FROM print_jobs'))
    assert P.release_holds(seeded_db) == 0


def test_auto_default_pending(seeded_db):
    _enqueue(seeded_db)
    assert seeded_db.query_one("SELECT COUNT(*) c FROM print_jobs WHERE state='hold'")['c'] == 0
    assert seeded_db.query_one("SELECT COUNT(*) c FROM print_jobs WHERE state='pending'")['c'] >= 1


def test_off_generates_no_jobs_or_serials(seeded_db):
    S.set_print_policy(seeded_db, {'kitchen': 'off'})
    assert _enqueue(seeded_db) == []
    assert seeded_db.query_one('SELECT COUNT(*) c FROM print_jobs')['c'] == 0
    assert seeded_db.query_one('SELECT COUNT(*) c FROM station_serials')['c'] == 0


def test_hold_defers_serial_no_phantom_gap(seeded_db):
    for policy in ('auto', 'manual', 'auto'):
        S.set_print_policy(seeded_db, {'kitchen': policy})
        _enqueue(seeded_db)
    autos, holds = defaultdict(list), defaultdict(list)
    for row in seeded_db.query('SELECT station_id,serial_no,state FROM print_jobs ORDER BY station_id,enqueue_seq'):
        (holds if row['state'] == 'hold' else autos)[row['station_id']].append(row['serial_no'])
    assert holds
    for serials in autos.values():
        assert serials == list(range(1, len(serials) + 1))
    assert all(serial == 0 for serials in holds.values() for serial in serials)
    assert P.release_holds(seeded_db) == sum(map(len, holds.values()))
    for station, held in holds.items():
        rows = seeded_db.query('SELECT serial_no,payload FROM print_jobs WHERE station_id=? ORDER BY serial_no', (station,))
        assert [row['serial_no'] for row in rows] == list(range(1, len(autos[station]) + len(held) + 1))
        for row in rows:
            assert escpos.serial_footer_bytes(row['serial_no']) in bytes(row['payload'])


def test_inject_serial_byte_identical():
    with_serial = escpos.Builder().init().line('宫保鸡丁').line('流水号 No.00007').cut().build()
    without = escpos.Builder().init().line('宫保鸡丁').cut().build()
    assert escpos.inject_serial(without, 7) == with_serial


def test_reprint_hold_injects_serial(seeded_db):
    S.set_print_policy(seeded_db, {'kitchen': 'manual'})
    original_id = _enqueue(seeded_db)[0]
    original = seeded_db.query_one('SELECT serial_no,payload FROM print_jobs WHERE id=?', (original_id,))
    assert original['serial_no'] == 0
    new_id = P.reprint_job(seeded_db, original_id)
    new = seeded_db.query_one('SELECT serial_no,payload,state FROM print_jobs WHERE id=?', (new_id,))
    assert new['state'] == 'pending' and new['serial_no'] > 0
    assert escpos.serial_footer_bytes(new['serial_no']) in bytes(new['payload'])


def test_reprint_rejects_archived_payload(seeded_db):
    job_id = _enqueue(seeded_db)[0]
    seeded_db.execute("UPDATE print_jobs SET payload=X'' WHERE id=?", (job_id,))
    with pytest.raises(ValidationError):
        P.reprint_job(seeded_db, job_id)


@pytest.mark.parametrize('kick', [escpos.CMD_KICK_PIN2, escpos.CMD_KICK_PIN5])
def test_reprint_job_strips_drawer_kick(seeded_db, kick):
    payload = escpos.Builder().init().line('Demo').cut().build()
    with seeded_db.transaction() as conn:
        original_id = P.enqueue_payload(conn, 'front', '结账单', payload + kick)
    new_id = P.reprint_job(seeded_db, original_id)
    new = bytes(seeded_db.query_one('SELECT payload FROM print_jobs WHERE id=?', (new_id,))['payload'])
    assert new == payload
    assert escpos.CMD_KICK_PIN2 not in new and escpos.CMD_KICK_PIN5 not in new


def test_copies_emits_multiple_print_jobs(seeded_db):
    dish = seeded_db.query_one("SELECT id FROM dishes WHERE name='凉拌黄瓜'")['id']
    seeded_db.execute('UPDATE dish_station_bindings SET copies=2 WHERE dish_id=?', (dish,))
    ids = P.plan_and_enqueue(
        seeded_db, op=routing.Op.NEW, requests=[P.ItemRequest(dish)],
        ctx=render.RenderContext(table_label='Table 1'), dedupe_prefix='demo',
    )
    jobs = seeded_db.query('SELECT serial_no,enqueue_seq,dedupe_key FROM print_jobs ORDER BY id')
    assert len(ids) == len(jobs) == 2
    for field in ('serial_no', 'enqueue_seq', 'dedupe_key'):
        assert jobs[0][field] != jobs[1][field]
