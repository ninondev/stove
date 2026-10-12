# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
"""Chinese kitchen ticket layouts; dish names keep their width at double height."""
from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime

from .. import config
from ..domain import routing
from ..domain.money import format_yuan, format_yuan_sign
from . import escpos

def fmt_dt(ms: int | None) -> str:
    if not ms:
        return ""
    return datetime.fromtimestamp(ms / 1000, tz=config.TZ).strftime("%Y/%m/%d %H:%M:%S")

@dataclass
class RenderContext:
    table_label: str = ""
    party_size: int | None = None
    operator: str | None = None
    print_time_ms: int | None = None
    open_time_ms: int | None = None
    settle_time_ms: int | None = None
    daily_seq: str | None = None
    order_no: str | None = None        # long unique number
    order_type_label: str = "堂食"
    brand: str = "Stove Demo Kitchen"
    store_name: str | None = None
    source_label: str | None = None
    serial_no: int = 0                 # per-station footer serial (gap detection)
    turn_no: int | None = None

def _turn_suffix(ctx: "RenderContext") -> str:

    return f"·当日第{ctx.turn_no}单" if ctx.turn_no else ""

def _dish_name(line: routing.TicketLine) -> str:
    name = line.name
    if line.spec_name:
        name = f"{name}（{line.spec_name}）"
    if line.ret:
        name = f"[退]{name}"
    elif line.transfer:
        name = f"[转]{name}"
    elif line.held:
        name = f"[等叫]{name}"
    return name

def _emit_lines(b: escpos.Builder, lines: list[routing.TicketLine]) -> None:

    has_header = any(getattr(ln, "is_combo_header", False) for ln in lines)
    current_parent: str | None = None
    for ln in lines:
        if getattr(ln, "is_combo_header", False):

            b.size(1, 2).bold(True).two_col(_dish_name(ln), f"{ln.qty}/{ln.unit}").bold(False).size(1, 1)
            current_parent = ln.combo_parent or ln.name
            continue
        if ln.combo_parent:
            if not has_header and ln.combo_parent != current_parent:

                b.size(1, 2).bold(True).line(escpos.truncate_to_width(ln.combo_parent, b.columns)).bold(False).size(1, 1)
                current_parent = ln.combo_parent
            prefix = "-"
        else:
            current_parent = None
            prefix = ""
        qty = f"{ln.qty}/{ln.unit}"
        b.size(1, 2).bold(True).two_col(prefix + _dish_name(ln), qty).bold(False).size(1, 1)
        if ln.notes:
            b.size(1, 2).line(f"-备注:{ln.notes}").size(1, 1)
        for aname, aqty in ln.addons:
            b.size(1, 2).line(f"  +{aname} x{aqty}").size(1, 1)

def render_station_ticket(ticket: routing.PlannedTicket, ctx: RenderContext) -> bytes:
    b = escpos.Builder()
    b.init()

    if ticket.ticket_type == "转台单":
        return _render_transfer(b, ticket, ctx)

    if ticket.marker:
        b.align(1).bold(True).size(2, 2).line(ticket.marker).size(1, 1).bold(False).align(0)

    b.align(1).bold(True).size(1, 2).line(f"桌台号：{ctx.table_label}{_turn_suffix(ctx)}").size(1, 1)
    b.line(ticket.ticket_type).bold(False).align(0)

    # source + header row
    b.line(f"{ctx.brand}-{ctx.order_type_label}")
    party = f"{ctx.party_size}人" if ctx.party_size else ""
    b.two_col("菜品", f"{party}  数量".strip())
    b.hr()

    _emit_lines(b, ticket.lines)

    reasons = [ln for ln in ticket.lines if ln.ret]
    # ret_reason is carried on PrintItem; routing copies it onto nothing — handle via extra
    if ticket.extra.get("ret_reason"):
        b.line(f"退菜原因：{ticket.extra['ret_reason']}")

    b.hr()
    if ticket.whole_note:
        b.line(f"整单备注 {ticket.whole_note}")
        b.hr()
    if ctx.operator:
        b.line(f"操作员：{ctx.operator}")
    b.line(f"打印时间：{fmt_dt(ctx.print_time_ms)}")
    b.line(ticket.footer)
    if ctx.serial_no:
        b.line(f"流水号 No.{ctx.serial_no % 100000:05d}")
    b.cut()
    return b.build()

def _render_transfer(b: escpos.Builder, ticket: routing.PlannedTicket, ctx: RenderContext) -> bytes:
    b.align(1).bold(True).size(1, 2).line("转台单").size(1, 1).bold(False).align(0)
    b.line(f"原桌台：{ticket.extra.get('orig_table', '')}")
    b.line(f"目标桌台：{ticket.extra.get('target_table', '')}")
    if ctx.daily_seq:
        b.line(f"流水号：{ctx.daily_seq}")
    if ctx.party_size:
        b.line(f"人数：{ctx.party_size}")
    if ctx.order_no:
        b.line(f"订单号：{ctx.order_no}")
    b.hr()
    b.two_col("菜品", "数量")
    _emit_lines(b, ticket.lines)
    b.hr()
    if ctx.operator:
        b.line(f"操作员：{ctx.operator}")
    b.line(f"开单时间：{fmt_dt(ctx.open_time_ms)}")
    b.line(f"打印时间：{fmt_dt(ctx.print_time_ms)}")
    b.line(ticket.footer)
    if ctx.serial_no:
        b.line(f"流水号 No.{ctx.serial_no % 100000:05d}")
    b.cut()
    return b.build()

# --------------------------------------------------------------------------- #

# --------------------------------------------------------------------------- #

@dataclass
class BillItem:
    name: str
    unit_price_jiao: int
    qty: int
    subtotal_jiao: int
    combo_children: list[tuple[str, int]] = field(default_factory=list)  # (name, qty)
    note: str | None = None
    comp_jiao: int = 0

@dataclass
class BillGroup:
    category: str
    subtotal_jiao: int
    items: list[BillItem]

@dataclass
class BillPayment:
    method: str
    amount_jiao: int

@dataclass
class Bill:
    is_final: bool
    groups: list[BillGroup]
    original_jiao: int
    adjustments: list[tuple[str, int]]
    payable_jiao: int
    paid_jiao: int | None = None
    payments: list[BillPayment] = field(default_factory=list)
    flat: list[BillItem] = field(default_factory=list)
    title: str | None = None

def _emit_bill_item(b: escpos.Builder, it: BillItem) -> None:

    b.three_col(it.name, f"{format_yuan(it.unit_price_jiao)}  {it.qty}", format_yuan(it.subtotal_jiao))
    for cname, cqty in it.combo_children:
        b.three_col(f"-{cname}", str(cqty), "")
    if it.comp_jiao:
        b.two_col("-单品赠送", format_yuan_sign(-it.comp_jiao))
    if it.note:
        b.line(f"－备注：{it.note}")

def render_bill(bill: Bill, ctx: RenderContext) -> bytes:
    b = escpos.Builder()
    b.init()
    b.align(1).bold(True).size(1, 2).line(ctx.store_name or config.STORE_NAME).size(1, 1)
    b.line(bill.title or ("结账单" if bill.is_final else "预结单")).bold(False).align(0)

    party = f"  {ctx.party_size}人" if ctx.party_size else ""
    b.line(f"桌台号：{ctx.table_label}{_turn_suffix(ctx)}{party}")
    b.line(f"{ctx.brand}-{ctx.order_type_label}")
    if ctx.source_label:
        b.line(ctx.source_label)
    if bill.is_final and ctx.settle_time_ms:
        b.line(f"结账时间：{fmt_dt(ctx.settle_time_ms)}")
    b.three_col("菜品", "单价/数量", "小计")
    b.hr()

    if bill.is_final:

        for it in bill.flat:
            _emit_bill_item(b, it)
    else:

        for g in bill.groups:
            b.align(1).line(f"----{g.category}（{format_yuan(g.subtotal_jiao)}）----").align(0)
            for it in g.items:
                _emit_bill_item(b, it)

    b.hr()
    b.two_col("菜品原价", format_yuan(bill.original_jiao))
    for label, amt in bill.adjustments:
        b.two_col(label, format_yuan_sign(amt))

    if bill.is_final:

        b.bold(True).two_col("应付金额", format_yuan(bill.payable_jiao)).bold(False)
        if bill.paid_jiao is not None:
            b.bold(True).two_col("已付金额", format_yuan(bill.paid_jiao)).bold(False)
        if bill.payments:
            b.line("支付明细")
            for p in bill.payments:
                b.two_col(f"-{p.method}", format_yuan(p.amount_jiao))
    else:

        if bill.paid_jiao:
            b.two_col("已付金额合计", format_yuan_sign(-bill.paid_jiao))
            for p in bill.payments:
                b.two_col(f"-{p.method}", format_yuan_sign(-p.amount_jiao))
        b.bold(True).two_col("应付金额", format_yuan(bill.payable_jiao - (bill.paid_jiao or 0))).bold(False)

    b.hr()
    if ctx.operator:
        b.line(f"操作员：{ctx.operator}")
    b.line(f"打印时间：{fmt_dt(ctx.print_time_ms)}")
    if ctx.order_no:
        b.line(f"单号：{ctx.order_no}")
    b.cut()
    return b.build()

# --------------------------------------------------------------------------- #

# --------------------------------------------------------------------------- #

def render_shift_ticket(ctx: RenderContext, data: dict) -> bytes:
    b = escpos.Builder()
    b.init()
    b.align(1).bold(True).size(1, 2).line(ctx.store_name or config.STORE_NAME).size(1, 1)
    b.line("交班结款单").bold(False).align(0)
    if ctx.operator:
        b.line(f"收银员：{ctx.operator}")
    b.line(f"打印时间：{fmt_dt(ctx.print_time_ms)}")
    b.three_col("支付方式", "", "金额")
    b.hr()
    for m in data.get("by_method", []):
        b.two_col(m["method"], format_yuan(m["amount_jiao"]))
    b.hr()
    b.two_col("备用金", format_yuan(data.get("opening_float_jiao", 0)))
    b.two_col("本班现金净收", format_yuan(data.get("net_cash_jiao", 0)))
    b.two_col("应有现金", format_yuan(data.get("expected_cash_jiao", 0)))
    b.two_col("实点现金", format_yuan(data.get("counted_cash_jiao", 0)))
    b.bold(True).two_col("长短款", format_yuan_sign(data.get("over_short_jiao", 0))).bold(False)
    if data.get("note"):
        b.line(f"备注：{data['note']}")
    b.hr()
    b.cut()
    return b.build()

def render_day_ticket(ctx: RenderContext, data: dict) -> bytes:
    b = escpos.Builder()
    b.init()
    b.align(1).bold(True).size(1, 2).line(ctx.store_name or config.STORE_NAME).size(1, 1)
    b.line("日结单").bold(False).align(0)
    b.line(f"营业日：{data.get('biz_date', '')}")
    b.line(f"打印时间：{fmt_dt(ctx.print_time_ms)}")
    b.hr()
    b.two_col("结账订单", f"{data.get('settled', data.get('orders', 0))} 单")
    b.bold(True).two_col("应收营业额", format_yuan(data.get("receivable_jiao", 0))).bold(False)
    if data.get("discount_jiao"):
        b.two_col("优惠/折扣", format_yuan_sign(-data["discount_jiao"]))
    if data.get("seat_fee_jiao"):
        b.two_col("餐位费", format_yuan(data["seat_fee_jiao"]))
    if data.get("refund_jiao"):
        b.two_col("退款", format_yuan_sign(-data["refund_jiao"]))
    if data.get("voided"):
        b.two_col("作废单数", f"{data['voided']} 单")
    b.hr()
    b.line("按支付方式")
    for m in data.get("by_method", []):
        b.two_col(f"-{m['method']}", format_yuan(m["amount_jiao"]))
    b.hr()
    b.cut()
    return b.build()

def render_queue_preorder(ctx: RenderContext, items: list[dict]) -> bytes:

    b = escpos.Builder()
    b.init()
    b.align(1).bold(True).size(1, 2).line(ctx.store_name or config.STORE_NAME).size(1, 1)
    b.line("预点确认单").bold(False)
    b.size(1, 2).bold(True).line(ctx.table_label).bold(False).size(1, 1).align(0)
    if ctx.party_size:
        b.line(f"人数：{ctx.party_size} 位")
    b.line(f"取号时间：{fmt_dt(ctx.open_time_ms)}")
    b.line(f"打印时间：{fmt_dt(ctx.print_time_ms)}")
    b.hr()
    total = 0
    for it in items:
        name = it["name"] + (f"（{it['spec_name']}）" if it.get("spec_name") else "")
        qty = int(it.get("qty") or 1)
        b.size(1, 2).two_col(name, f"{qty}/{it.get('unit') or '份'}").size(1, 1)
        if it.get("note"):
            b.size(1, 2).line(f"-备注:{it['note']}").size(1, 1)
        total += int(it.get("price_jiao") or 0) * qty
    b.hr()
    b.bold(True).two_col("参考合计", format_yuan(total)).bold(False)
    b.line("以入座下单时价格为准；如遇沽清以店内为准。")
    b.hr()
    b.cut()
    return b.build()

# --------------------------------------------------------------------------- #
# Test ticket (used by test_printer.py)                                       #
# --------------------------------------------------------------------------- #

def render_banquet_prep(ctx: RenderContext, resv: dict, items: list[dict], temp_label: str) -> bytes:

    b = escpos.Builder()
    b.init()
    b.align(1).bold(True).size(1, 2).line(ctx.store_name or config.STORE_NAME).size(1, 1)
    b.line(f"备菜清单 · {temp_label}").bold(False)
    b.size(1, 2).bold(True).line(resv.get("table_label") or "宴席").bold(False).size(1, 1).align(0)
    when = " ".join(x for x in (resv.get("reserve_date"), resv.get("reserve_time")) if x)
    if when:
        b.line(f"用餐时间：{when}")
    if resv.get("party_size"):
        b.line(f"人数：{resv['party_size']} 位")
    if resv.get("customer_name"):
        b.line(f"客户：{resv['customer_name']}")
    b.line(f"打印时间：{fmt_dt(ctx.print_time_ms)}")
    b.hr()
    total = 0
    for it in items:
        qty = int(it.get("qty") or 1)
        total += qty
        b.size(1, 2).two_col(str(it.get("name") or ""), f"{qty} 份").size(1, 1)
    b.hr()
    b.two_col("合计", f"{len(items)} 道 / {total} 份")
    b.bold(True).line("提前备料，先别做——等前厅下单再出菜。").bold(False)
    b.hr()
    b.cut()
    return b.build()

def render_price_list(ctx: RenderContext, cats: list[dict]) -> bytes:

    b = escpos.Builder()
    b.init()
    b.align(1).bold(True).size(1, 2).line(ctx.store_name or config.STORE_NAME).size(1, 1)
    b.line("菜价核对单").bold(False).align(0)
    b.line(f"打印时间：{fmt_dt(ctx.print_time_ms)}")
    b.hr()
    total = 0
    for c in cats:
        b.bold(True).line(f"【{c['name']}】").bold(False)
        for d in c["dishes"]:
            total += 1
            b.two_col(("□ " + d["name"])[:22], format_yuan(d["price_jiao"]))
            for sp in d.get("specs", []):
                delta = sp["price_delta_jiao"]
                sign = "+" if delta >= 0 else "−"
                b.line(f"    {sp['name']} {sign}{format_yuan(abs(delta))}")
    b.hr()
    b.two_col("共", f"{total} 道")
    b.line("请逐道勾对。价格不对的,到后台「菜品管理」改。")
    b.hr()
    b.cut()
    return b.build()

def render_reservations_today(ctx: RenderContext, biz_date: str, items: list[dict]) -> bytes:

    b = escpos.Builder()
    b.init()
    b.align(1).bold(True).size(1, 2).line(ctx.store_name or config.STORE_NAME).size(1, 1)
    b.line("今日预订一览").bold(False).align(0)
    b.line(f"营业日：{biz_date}")
    b.line(f"打印时间：{fmt_dt(ctx.print_time_ms)}")
    b.hr()
    for it in items:
        who = " ".join(x for x in (
            it.get("name") or "",
            f"{it['party_size']}位" if it.get("party_size") else "",
            f"[{it['table_name']}]" if it.get("table_name") else "[未排桌]",
        ) if x)
        b.size(1, 2).bold(True).two_col(str(it.get("time") or "全天"), who).bold(False).size(1, 1)
        if it.get("phone"):
            b.line(f"  电话：{it['phone']}")
        if it.get("deposit_jiao"):
            b.line(f"  已收订金：{format_yuan(it['deposit_jiao'])}（结账自动抵）")
        if it.get("note"):
            b.line(f"  备注：{it['note']}")
        if it.get("banquet"):
            b.bold(True).line("  ★宴席·已挂菜单（备菜清单已发厨房）").bold(False)
    b.hr()
    b.line(f"共 {len(items)} 桌预订。客人到了在「预订」页点「入座」。")
    b.hr()
    b.cut()
    return b.build()

def render_cash_count(ctx: RenderContext, data: dict) -> bytes:

    b = escpos.Builder()
    b.init()
    b.align(1).bold(True).size(1, 2).line(ctx.store_name or config.STORE_NAME).size(1, 1)
    b.line("现金核对单").bold(False).align(0)
    b.line(f"营业日：{data.get('biz_date', '')}")
    b.line(f"打印时间：{fmt_dt(ctx.print_time_ms)}")
    b.hr()
    b.size(1, 2).bold(True).two_col("系统记的现金", format_yuan(data.get("cash_jiao", 0))).bold(False).size(1, 1)
    b.two_col("其中收现单数", f"{data.get('orders_with_cash', 0)} 单")
    b.hr()
    b.line(str(data.get("note") or ""))
    b.line("实点：__________ 元    差额：__________ 元")
    b.line("清点人：__________")
    b.hr()
    b.cut()
    return b.build()

def render_stocking_advice(ctx: RenderContext, data: dict) -> bytes:

    b = escpos.Builder()
    b.init()
    b.align(1).bold(True).size(1, 2).line(ctx.store_name or config.STORE_NAME).size(1, 1)
    b.line("备货建议单").bold(False).align(0)
    b.line(f"打印时间：{fmt_dt(ctx.print_time_ms)}")
    days = data.get("days_covered") or 0
    b.line(f"依据：最近 {days} 天的实际卖量" + ("（不足 7 天，仅供参考）" if data.get("partial") else ""))
    b.hr()
    b.three_col("菜品", "日均", "建议")
    for r in data.get("rows", []):
        b.size(1, 2).three_col(str(r.get("name") or ""), str(r.get("daily_avg")), str(r.get("suggest_qty"))).size(1, 1)

        if r.get("sold_out_days"):
            b.line(f"  （卖断过{r['sold_out_days']}天,已多备）")
    b.hr()
    b.line("按最近卖量估的量，师傅按实际手感增减。")
    b.hr()
    b.cut()
    return b.build()
