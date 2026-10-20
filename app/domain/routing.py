# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
"""Pure station routing, including per-dish tickets and combo expansion."""
from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum

# --------------------------------------------------------------------------- #
# Inputs (resolved, immutable)                                                #
# --------------------------------------------------------------------------- #

class Aggregation(str, Enum):
    AGGREGATE = "aggregate"
    PER_DISH = "per_dish"

@dataclass(frozen=True)
class StationDef:
    code: str                   # expo|kitchen|cold|front
    name: str
    aggregation: Aggregation
    footer: str
    prints_bills: bool = False
    move_table_ticket: bool = True
    enabled: bool = True

@dataclass(frozen=True)
class ItemBinding:
    station_code: str
    print_rule: str = "total"
    aggregation: str | None = None  # per-binding override of station aggregation
    copies: int = 1

@dataclass
class PrintItem:
    """A resolved order item to be routed (a combo is pre-expanded into these)."""
    name: str
    qty: int
    bindings: list[ItemBinding]
    unit: str = "份"
    spec_name: str | None = None
    notes: str | None = None
    addons: list[tuple[str, int]] = field(default_factory=list)  # (name, qty)
    created_seq: int = 0        # created_at-based order within the operation
    combo_parent: str | None = None   # combo name if this item is a component
    combo_uid: int | None = None
    combo_qty: int = 0
    ret_reason: str | None = None     # for retreat
    held: bool = False

# --------------------------------------------------------------------------- #
# Outputs                                                                     #
# --------------------------------------------------------------------------- #

@dataclass
class TicketLine:
    name: str
    qty: int
    unit: str = "份"
    spec_name: str | None = None
    notes: str | None = None
    addons: list[tuple[str, int]] = field(default_factory=list)
    ret: bool = False
    ret_reason: str | None = None
    transfer: bool = False
    held: bool = False
    combo_parent: str | None = None
    combo_uid: int | None = None
    is_combo_header: bool = False

@dataclass
class PlannedTicket:
    station_code: str
    footer: str                 # bottom-of-ticket station label
    ticket_type: str
    marker: str | None
    lines: list[TicketLine]
    whole_note: str | None = None
    seq: int = 0                # created order, used to assign enqueue_seq
    copies: int = 1
    extra: dict = field(default_factory=dict)

class Op(str, Enum):
    NEW = "new"                 # first order on a table
    ADD = "add"
    RETREAT = "retreat"
    TRANSFER = "transfer"
    URGE = "urge"
    CALL_UP = "call_up"
    MODIFY = "modify"
    VOID = "void"
    MERGE = "merge"
    MOVE = "move"

# notice-style ops: one aggregate ticket per affected station, with a marker.
_NOTICE = {
    Op.URGE: ("催菜单", "【催】"),
    Op.CALL_UP: ("起菜单", "【起】"),
    Op.MODIFY: ("改菜单", "【改】"),
    Op.VOID: ("作废单", "【作废】"),
    Op.MERGE: ("合台单", "【合台】"),
    Op.MOVE: ("移菜单", "【移】"),
}

# --------------------------------------------------------------------------- #
# Core                                                                        #
# --------------------------------------------------------------------------- #

def _line_from(item: PrintItem, *, ret: bool = False, transfer: bool = False,
               allow_held: bool = True, qty: int | None = None) -> TicketLine:
    return TicketLine(
        name=item.name,
        qty=item.qty if qty is None else qty,
        unit=item.unit,
        spec_name=item.spec_name,
        notes=item.notes,
        addons=list(item.addons),
        ret=ret,
        ret_reason=item.ret_reason if ret else None,
        transfer=transfer,

        held=item.held and not ret and not transfer and allow_held,
        combo_parent=item.combo_parent,
        combo_uid=item.combo_uid,
    )

def _combo_header_line(name: str, qty: int, sample: PrintItem) -> TicketLine:

    return TicketLine(name=name, qty=qty, unit="份", held=sample.held,
                      combo_parent=name, combo_uid=sample.combo_uid, is_combo_header=True)

def _station_aggregation(station: StationDef, binding: ItemBinding) -> Aggregation:

    if binding.aggregation:
        try:
            return Aggregation(binding.aggregation)
        except ValueError:
            return station.aggregation
    return station.aggregation

def _bound_stations(item: PrintItem, stations: dict[str, StationDef]) -> list[tuple[StationDef, ItemBinding]]:
    out: list[tuple[StationDef, ItemBinding]] = []
    for b in item.bindings:
        st = stations.get(b.station_code)
        if st is not None and st.enabled:
            out.append((st, b))
    return out

def _ticket_type(station: StationDef, op: Op) -> str:
    if op is Op.RETREAT:
        return "退菜单"
    if op is Op.TRANSFER:
        return "转台单"
    # NEW / ADD
    return "厨总单" if station.aggregation is Aggregation.AGGREGATE and station.code == "expo" else "堂口单"

def _marker(op: Op) -> str | None:
    if op is Op.ADD:
        return "【加菜】"
    if op is Op.RETREAT:
        return "【退】"
    return None

def _plan_order_like(
    items: list[PrintItem],
    stations: dict[str, StationDef],
    op: Op,
    *,
    whole_note: str | None = None,
) -> list[PlannedTicket]:

    tickets: list[PlannedTicket] = []
    seq = 0
    is_ret = op is Op.RETREAT

    # Group by station, respecting aggregation. We iterate items in created order
    # so kitchen per-dish tickets come out in time sequence.
    ordered = sorted(items, key=lambda it: it.created_seq)

    # Bucket aggregate stations; emit per-dish stations immediately (in order).
    aggregate_buckets: dict[str, PlannedTicket] = {}

    for item in ordered:
        bound = _bound_stations(item, stations)
        if not bound:

            seq += 1
            notice = aggregate_buckets.get("__unconfigured__")
            if notice is None:
                front = stations.get("front")
                notice = PlannedTicket(
                    station_code="front",
                    footer=front.footer if front else "前台",
                    ticket_type="未配置单",
                    marker=_marker(op),
                    lines=[],
                    whole_note="以下菜品未配置出单口，请配置菜品的打印站点",
                    seq=seq,
                )
                aggregate_buckets["__unconfigured__"] = notice
            notice.lines.append(_line_from(item, ret=is_ret))
            continue

        for station, binding in bound:
            agg = _station_aggregation(station, binding)
            if agg is Aggregation.PER_DISH:

                seq += 1
                tickets.append(
                    PlannedTicket(
                        station_code=station.code,
                        footer=station.footer,
                        ticket_type=_ticket_type(station, op),
                        marker=_marker(op),
                        lines=[_line_from(item, ret=is_ret)],
                        whole_note=whole_note,
                        seq=seq,
                        copies=max(1, binding.copies),
                    )
                )
            else:
                bucket = aggregate_buckets.get(station.code)
                if bucket is None:
                    seq += 1
                    bucket = PlannedTicket(
                        station_code=station.code,
                        footer=station.footer,
                        ticket_type=_ticket_type(station, op),
                        marker=_marker(op),
                        lines=[],
                        whole_note=whole_note,
                        seq=seq,
                        copies=max(1, binding.copies),
                    )
                    aggregate_buckets[station.code] = bucket
                bucket.copies = max(bucket.copies, binding.copies)
                bucket.lines.append(_line_from(item, ret=is_ret))

    tickets.extend(aggregate_buckets.values())
    tickets.sort(key=lambda t: t.seq)
    return tickets

def _expo_lines(items: list[PrintItem]) -> list[TicketLine]:

    blocks: list = []          # ["solo", sample, qty] | ["combo", uid, [items]]
    solo_idx: dict = {}
    combo_idx: dict = {}
    for it in items:
        if it.combo_uid is None:
            skey = (it.name, it.spec_name, it.notes, it.held)
            if skey not in solo_idx:
                solo_idx[skey] = len(blocks)
                blocks.append(["solo", it, 0])
            blocks[solo_idx[skey]][2] += it.qty
        else:
            if it.combo_uid not in combo_idx:
                combo_idx[it.combo_uid] = len(blocks)
                blocks.append(["combo", it.combo_uid, []])
            blocks[combo_idx[it.combo_uid]][2].append(it)
    lines: list[TicketLine] = []
    for b in blocks:
        if b[0] == "solo":
            lines.append(_line_from(b[1], qty=b[2]))
        else:
            comps = b[2]
            sets = max(1, comps[0].combo_qty)
            pname = comps[0].combo_parent or comps[0].name
            for _ in range(sets):
                lines.append(_combo_header_line(pname, 1, comps[0]))
                for c in comps:
                    lines.append(_line_from(c, qty=(c.qty // sets if sets else c.qty)))
    return lines

def _perdish_tickets(item_bindings, station: StationDef, op: Op,
                     whole_note: str | None, next_seq) -> list[PlannedTicket]:

    groups: dict = {}
    order: list = []
    for it, binding in item_bindings:
        key = (it.combo_parent, it.name, it.spec_name, it.notes, it.held)
        g = groups.get(key)
        if g is None:
            g = {"sample": it, "qty": 0, "sets": 0, "first": it.created_seq, "copies": 1}
            groups[key] = g
            order.append(key)
        g["qty"] += it.qty
        g["sets"] += it.combo_qty
        g["copies"] = max(g["copies"], binding.copies)
        g["first"] = min(g["first"], it.created_seq)
    out: list[PlannedTicket] = []
    for key in sorted(order, key=lambda k: groups[k]["first"]):
        g = groups[key]
        s = g["sample"]
        lines: list[TicketLine] = []
        if s.combo_parent:
            lines.append(_combo_header_line(s.combo_parent, g["sets"], s))
        lines.append(_line_from(s, qty=g["qty"]))
        out.append(PlannedTicket(
            station_code=station.code, footer=station.footer,
            ticket_type=_ticket_type(station, op), marker=_marker(op),
            lines=lines, whole_note=whole_note, seq=next_seq(), copies=max(1, g["copies"]),
        ))
    return out

def _plan_new_add(items: list[PrintItem], stations: dict[str, StationDef], op: Op,
                  *, whole_note: str | None = None) -> list[PlannedTicket]:

    seq = 0

    def next_seq():
        nonlocal seq
        seq += 1
        return seq

    ordered = sorted(items, key=lambda it: it.created_seq)
    agg: dict = {}     # code -> [StationDef, [(item,binding)]]
    pd: dict = {}      # code -> [StationDef, [(item,binding)]]
    unconfigured: list[PrintItem] = []
    for item in ordered:
        bound = _bound_stations(item, stations)
        if not bound:
            unconfigured.append(item)
            continue
        for station, binding in bound:
            tgt = agg if _station_aggregation(station, binding) is Aggregation.AGGREGATE else pd
            tgt.setdefault(station.code, [station, []])[1].append((item, binding))

    tickets: list[PlannedTicket] = []

    if unconfigured:
        front = stations.get("front")
        t = PlannedTicket(
            station_code="front", footer=front.footer if front else "前台",
            ticket_type="未配置单", marker=_marker(op), lines=[],
            whole_note="以下菜品未配置出单口，请配置菜品的打印站点", seq=next_seq(),
        )
        for item in unconfigured:
            t.lines.append(_line_from(item))
        tickets.append(t)

    for _code, (station, ib) in agg.items():
        copies = max([b.copies for _, b in ib] + [1])
        tickets.append(PlannedTicket(
            station_code=station.code, footer=station.footer,
            ticket_type=_ticket_type(station, op), marker=_marker(op),
            lines=_expo_lines([it for it, _ in ib]), whole_note=whole_note,
            seq=next_seq(), copies=copies,
        ))

    for _code, (station, ib) in pd.items():
        tickets.extend(_perdish_tickets(ib, station, op, whole_note, next_seq))

    tickets.sort(key=lambda t: t.seq)
    return tickets

def _plan_transfer(
    items: list[PrintItem],
    stations: dict[str, StationDef],
    *,
    orig_table: str,
    target_table: str,
    extra: dict,
) -> list[PlannedTicket]:

    tickets: list[PlannedTicket] = []
    seq = 0
    buckets: dict[str, PlannedTicket] = {}
    ordered = sorted(items, key=lambda it: it.created_seq)

    for item in ordered:
        for station, _binding in _bound_stations(item, stations):
            if station.code == "kitchen" and not station.move_table_ticket:
                continue
            bucket = buckets.get(station.code)
            if bucket is None:
                seq += 1
                bucket = PlannedTicket(
                    station_code=station.code,
                    footer=station.footer,
                    ticket_type="转台单",
                    marker=None,
                    lines=[],
                    seq=seq,
                    extra={"orig_table": orig_table, "target_table": target_table, **extra},
                )
                buckets[station.code] = bucket
            bucket.lines.append(_line_from(item, transfer=True))

    tickets.extend(buckets.values())
    tickets.sort(key=lambda t: t.seq)
    return tickets

def _plan_notice(
    items: list[PrintItem],
    stations: dict[str, StationDef],
    ticket_type: str,
    marker: str,
    *,
    allow_held: bool = False,
) -> list[PlannedTicket]:

    seq = 0
    buckets: dict[str, PlannedTicket] = {}
    for item in sorted(items, key=lambda it: it.created_seq):
        bound = _bound_stations(item, stations)
        if not bound:

            notice = buckets.get("__unconfigured__")
            if notice is None:
                seq += 1
                front = stations.get("front")
                notice = PlannedTicket(
                    station_code="front", footer=front.footer if front else "前台",
                    ticket_type=ticket_type, marker=marker, lines=[],
                    whole_note="以下菜品未配置出单口，请配置菜品的打印站点", seq=seq,
                )
                buckets["__unconfigured__"] = notice
            notice.lines.append(_line_from(item, allow_held=allow_held))
            continue
        for station, _b in bound:
            bucket = buckets.get(station.code)
            if bucket is None:
                seq += 1
                bucket = PlannedTicket(
                    station_code=station.code, footer=station.footer,
                    ticket_type=ticket_type, marker=marker, lines=[], seq=seq,
                )
                buckets[station.code] = bucket
            bucket.lines.append(_line_from(item, allow_held=allow_held))
    tickets = sorted(buckets.values(), key=lambda t: t.seq)
    return tickets

def fan_out(
    op: Op,
    items: list[PrintItem],
    stations: dict[str, StationDef],
    *,
    whole_note: str | None = None,
    orig_table: str | None = None,
    target_table: str | None = None,
    extra: dict | None = None,
) -> list[PlannedTicket]:
    """Single entry point. Returns PlannedTickets in the order they should be
    enqueued (per-dish kitchen tickets in created order)."""
    if op is Op.TRANSFER:
        return _plan_transfer(
            items, stations,
            orig_table=orig_table or "", target_table=target_table or "",
            extra=extra or {},
        )
    if op in _NOTICE:
        ticket_type, marker = _NOTICE[op]

        return _plan_notice(items, stations, ticket_type, marker, allow_held=(op is Op.MODIFY))
    if op in (Op.NEW, Op.ADD):

        return _plan_new_add(items, stations, op, whole_note=whole_note)
    return _plan_order_like(items, stations, op, whole_note=whole_note)
