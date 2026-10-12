# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
import sys, pathlib
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))

from app import config
from app.domain import routing as R
from app.print import render, escpos

def _ctx():
    return render.RenderContext(
        table_label="A1", party_size=2, operator="Demo Operator",
        print_time_ms=1_700_000_000_000, serial_no=63, order_no="1126" + "0" * 17 + "00063",
    )

def test_station_ticket_is_gbk_not_utf8_and_well_formed():
    ticket = R.PlannedTicket(
        "expo", "传菜", "厨总单", "【加菜】",
        [R.TicketLine("宫保鸡丁", 1, "份", notes="不要蒜")],
    )
    b = render.render_station_ticket(ticket, _ctx())
    assert "厨总单".encode("gb18030") in b
    assert "宫保鸡丁".encode("gb18030") in b
    assert "-备注:不要蒜".encode("gb18030") in b
    # GBK, not UTF-8 — this is the lockstep with the printer's codepage.
    assert "厨总单".encode("utf-8") not in b
    # framing
    assert b.startswith(escpos.CMD_INIT)
    assert escpos.CMD_KANJI_ON in b
    assert escpos.CMD_CUT in b
    # a station ticket must NEVER kick the cash drawer
    assert escpos.CMD_KICK_PIN2 not in b
    assert escpos.CMD_KICK_PIN5 not in b
    # footer station + serial
    assert "传菜".encode("gb18030") in b
    assert "No.00063".encode("gb18030") in b

def test_transfer_ticket_has_both_tables_and_times():
    ticket = R.PlannedTicket(
        "expo", "传菜", "转台单", None,
        [R.TicketLine("宫保鸡丁", 1, "份", transfer=True)],
        extra={"orig_table": "B5", "target_table": "外1"},
    )
    ctx = _ctx()
    ctx.open_time_ms = 1_700_000_000_000
    b = render.render_station_ticket(ticket, ctx)
    assert "转台单".encode("gb18030") in b
    assert "原桌台：".encode("gb18030") in b
    assert "目标桌台：".encode("gb18030") in b
    assert "[转]宫保鸡丁".encode("gb18030") in b
    assert "开单时间：".encode("gb18030") in b

def test_bill_renders_store_name_and_payment_detail():
    bill = render.Bill(
        is_final=True,
        groups=[render.BillGroup("热菜", 1560, [render.BillItem("宫保鸡丁", 880, 1, 880)])],
        original_jiao=1560,
        adjustments=[("手动抹零", -90)],
        payable_jiao=1470,
        paid_jiao=1470,
        payments=[render.BillPayment("现金", 1470)],
    )
    b = render.render_bill(bill, _ctx())
    assert config.STORE_NAME.encode("gb18030") in b
    assert "结账单".encode("gb18030") in b
    assert "应付金额".encode("gb18030") in b
    assert "支付明细".encode("gb18030") in b
    assert "-现金".encode("gb18030") in b
    assert "手动抹零".encode("gb18030") in b

def test_combo_components_grouped_under_parent():
    ticket = R.PlannedTicket(
        "cold", "凉菜", "堂口单", None,
        [R.TicketLine("清蒸南瓜", 1, "份", combo_parent="Demo Sharing Set")],
    )
    b = render.render_station_ticket(ticket, _ctx())
    assert "Demo Sharing Set".encode("gb18030") in b
    assert "-清蒸南瓜".encode("gb18030") in b
