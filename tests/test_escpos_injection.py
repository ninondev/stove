# SPDX-License-Identifier: GPL-3.0-only
from app.domain import routing
from app.print import escpos, render
from app.services import printing

KICK = b'\x1bp\x00\x19\xfa'
INIT = b'\x1b@'
SIZE = b'\x1d!\x77'
PAYLOAD_STR = '菜\x1bp\x00\x19\xfa锅\x1dVB\x03辣\x1b@粒\x1d!w花\x10\x04\x01椒\x7fDEL\x01末'


def _enqueue(db, *, operation=routing.Op.NEW, notes=None, whole_note=None, reason=None):
    dish = db.query_one("SELECT id FROM dishes WHERE name='宫保鸡丁'")['id']
    return printing.plan_and_enqueue(
        db, op=operation,
        requests=[printing.ItemRequest(dish, qty=2, notes=notes, ret_reason=reason)],
        whole_note=whole_note,
        ctx=render.RenderContext(table_label='Table 1', party_size=3, operator='Demo Operator'),
    )


def _assert_no_injection(payload):
    assert KICK not in payload
    assert b'\x10\x04' not in payload
    assert SIZE not in payload
    assert b'\x7f' not in payload
    assert payload.count(INIT) == 1


def test_attack_item_note_and_whole_note_stripped(seeded_db):
    ids = _enqueue(seeded_db, notes=PAYLOAD_STR, whole_note=PAYLOAD_STR)
    assert ids
    for row in seeded_db.query('SELECT payload FROM print_jobs'):
        _assert_no_injection(bytes(row['payload']))
    clean = escpos.Builder().init().line('锅辣粒花椒末').build()
    injected = escpos.Builder().init().line(PAYLOAD_STR).build()
    for control in (b'\x1b', b'\x1d', b'\x1c', b'\x10', b'\x7f'):
        assert clean.count(control) == injected.count(control)


def test_attack_dish_name_operator_and_header_stripped(seeded_db):
    seeded_db.execute("UPDATE dishes SET name=? WHERE name='宫保鸡丁'", (PAYLOAD_STR,))
    dish = seeded_db.query_one('SELECT id FROM dishes WHERE name=?', (PAYLOAD_STR,))['id']
    ids = printing.plan_and_enqueue(
        seeded_db, op=routing.Op.NEW, requests=[printing.ItemRequest(dish)],
        ctx=render.RenderContext(table_label=PAYLOAD_STR, operator=PAYLOAD_STR, store_name=PAYLOAD_STR),
    )
    assert ids
    for row in seeded_db.query('SELECT payload FROM print_jobs'):
        _assert_no_injection(bytes(row['payload']))


def test_attack_retreat_reason_stripped(seeded_db):
    ids = _enqueue(seeded_db, operation=routing.Op.RETREAT, reason=PAYLOAD_STR)
    assert ids
    rows = seeded_db.query("SELECT payload FROM print_jobs WHERE ticket_type='退菜单'")
    assert rows
    for row in rows:
        payload = bytes(row['payload'])
        _assert_no_injection(payload)
        assert '退菜原因：菜'.encode('gb18030') in payload


def test_legitimate_drawer_kick_and_cut_survive():
    payload = escpos.Builder().init().line('Demo').cut().build() + escpos.CMD_KICK_PIN2
    assert escpos.CMD_KICK_PIN2 in payload
    assert escpos.CMD_CUT in payload
    assert payload.endswith(escpos.CMD_KICK_PIN2)


def test_chinese_and_cut_intact(seeded_db):
    assert _enqueue(seeded_db, notes='微辣多放花椒')
    payloads = [bytes(row['payload']) for row in seeded_db.query('SELECT payload FROM print_jobs')]
    assert any('宫保鸡丁'.encode('gb18030') in payload for payload in payloads)
    assert any('微辣多放花椒'.encode('gb18030') in payload for payload in payloads)
    assert all(escpos.CMD_CUT in payload for payload in payloads)


def test_strip_ctrl_unit_boundaries():
    text = '宫保鸡丁/微辣多放花椒（大份）；备注：勿放香菜！ABC123 ¥17.3'
    assert escpos._strip_ctrl(text) == text
    assert escpos._strip_ctrl(text).encode('gb18030').decode('gb18030') == text
    injected = '菜\x1bp\x00锅\x1dV辣\x1b@\x10\x04粒\x7f末\x01\x1f尾'
    output = escpos._strip_ctrl(injected)
    for control in ('\x1b', '\x1d', '\x00', '\x10', '\x04', '\x7f', '\x01', '\x1f'):
        assert control not in output
    assert output == '菜p锅V辣@粒末尾'
    assert escpos._strip_ctrl('行1\n行2\t列') == '行1\n行2\t列'
    rare = '\U00020000ก菜'
    assert escpos._strip_ctrl(rare) == rare
    assert escpos._strip_ctrl(rare).encode('gb18030').decode('gb18030') == rare
