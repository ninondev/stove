# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
"""Resolve menu bindings, route, render, and enqueue in a single transaction."""
from __future__ import annotations

import sqlite3
from dataclasses import dataclass, field

from ..db.connection import now_ms
from ..domain import routing
from ..domain.errors import ValidationError
from ..print import escpos, render, serial_no
from . import settings as settings_svc

def _ticket_category(ticket_type: str) -> str:

    if ticket_type == "预结单":
        return "prebill"
    if ticket_type in ("结账单", "销账单"):
        return "final"
    if ticket_type == "交班单":
        return "shift"
    if ticket_type == "日结单":
        return "day"
    if ticket_type == "合台单":
        return "merge"
    if ticket_type == "移菜单":
        return "move"
    if ticket_type == "预点确认单":

        return "prebill"
    if ticket_type == "结账单（补打）":

        return "final"
    if ticket_type == "开钱箱":

        return "final"

    return "kitchen"

def _initial_state(conn, ticket_type: str) -> str | None:

    pol = settings_svc.get_print_policy_conn(conn).get(_ticket_category(ticket_type))
    if pol == "off":
        return None
    return "hold" if pol == "manual" else "pending"

@dataclass
class ItemRequest:
    """A requested line to route (combos are expanded here)."""
    dish_id: int
    qty: int = 1
    spec_id: int | None = None
    notes: str | None = None
    addons: list[tuple[str, int]] = field(default_factory=list)
    created_seq: int = 0
    ret_reason: str | None = None

# --------------------------------------------------------------------------- #
# Station loading                                                             #
# --------------------------------------------------------------------------- #

def load_stations(conn: sqlite3.Connection) -> dict[str, routing.StationDef]:
    out: dict[str, routing.StationDef] = {}
    for r in conn.execute("SELECT * FROM stations").fetchall():
        out[r["code"]] = routing.StationDef(
            code=r["code"],
            name=r["name"],
            aggregation=routing.Aggregation(r["aggregation"]),
            footer=_footer_for(r),
            prints_bills=bool(r["prints_bills"]),
            move_table_ticket=bool(r["move_table_ticket"]),
            enabled=bool(r["enabled"]),
        )
    return out

def _footer_for(station_row: sqlite3.Row) -> str:

    return {"expo": "传菜", "kitchen": "厨房", "cold": "凉菜", "front": "酒水"}.get(
        station_row["code"], station_row["name"]
    )

def station_ids_by_code(conn: sqlite3.Connection) -> dict[str, int]:
    return {r["code"]: r["id"] for r in conn.execute("SELECT id, code FROM stations").fetchall()}

# --------------------------------------------------------------------------- #
# Item resolution (bindings + combo expansion + spec/price snapshot)          #
# --------------------------------------------------------------------------- #

def _bindings_for(conn: sqlite3.Connection, dish_id: int) -> list[routing.ItemBinding]:
    rows = conn.execute(
        """SELECT s.code AS code, b.print_rule AS pr, b.aggregation AS agg, b.copies AS copies
           FROM dish_station_bindings b JOIN stations s ON s.id = b.station_id
           WHERE b.dish_id=? AND s.enabled=1""",
        (dish_id,),
    ).fetchall()
    return [
        routing.ItemBinding(
            station_code=r["code"], print_rule=r["pr"],
            aggregation=r["agg"], copies=r["copies"] or 1,
        )
        for r in rows
    ]

def _dish(conn: sqlite3.Connection, dish_id: int) -> sqlite3.Row | None:
    return conn.execute("SELECT * FROM dishes WHERE id=?", (dish_id,)).fetchone()

def _spec_name(conn: sqlite3.Connection, spec_id: int | None) -> str | None:
    if not spec_id:
        return None
    r = conn.execute("SELECT name FROM dish_specs WHERE id=?", (spec_id,)).fetchone()
    return r["name"] if r else None

def _row_status(r) -> str | None:

    try:
        return r["status"]
    except (KeyError, IndexError):
        return None

def build_print_items_from_rows(
    conn: sqlite3.Connection, rows: list, *, as_retreat: bool = False
) -> list[routing.PrintItem]:

    items: list[routing.PrintItem] = []
    for r in rows:
        d = _dish(conn, r["dish_id"])
        if d is None:
            continue
        if d["is_combo"]:
            continue  # parent combo line never prints; its components do
        if d["temp"] == "special":
            continue  # billing-only special item
        combo_parent = None
        combo_uid = None
        combo_qty = 0
        if r["combo_item_id"]:
            parent = conn.execute(
                "SELECT name_snapshot, qty, ret_qty FROM order_items WHERE id=?", (r["combo_item_id"],)
            ).fetchone()
            if parent:
                combo_parent = parent["name_snapshot"]
                combo_uid = r["combo_item_id"]

                combo_qty = max(0, (parent["qty"] or 0) - (parent["ret_qty"] or 0))
        addons = [
            (a["name_snapshot"], a["qty"])
            for a in conn.execute(
                "SELECT name_snapshot, qty FROM order_item_addons WHERE order_item_id=?",
                (r["id"],),
            ).fetchall()
        ]
        qty = r["ret_qty"] if as_retreat else (r["qty"] - (r["ret_qty"] or 0))
        if qty <= 0 and not as_retreat:
            continue
        items.append(
            routing.PrintItem(
                name=r["name_snapshot"],
                qty=qty,
                bindings=_bindings_for(conn, r["dish_id"]),
                unit=d["unit"],
                spec_name=r["spec_name"],
                notes=r["notes"],
                addons=addons,
                created_seq=r["seq_in_order"],
                combo_parent=combo_parent,
                combo_uid=combo_uid,
                combo_qty=combo_qty,
                ret_reason=(r["ret_reason"] if as_retreat else None),

                held=(not as_retreat and _row_status(r) == "held"),
            )
        )
    return items

def resolve_items(conn: sqlite3.Connection, requests: list[ItemRequest]) -> list[routing.PrintItem]:
    """Resolve requests to PrintItems, expanding combos into their components."""
    items: list[routing.PrintItem] = []
    seq = 0
    combo_seq = 0
    for req in requests:
        d = _dish(conn, req.dish_id)
        if d is None:
            continue
        seq = req.created_seq or (seq + 1)
        if d["is_combo"]:
            comps = conn.execute(
                "SELECT * FROM combo_components WHERE combo_dish_id=? ORDER BY sort, id",
                (req.dish_id,),
            ).fetchall()
            combo_seq -= 1
            for c in comps:
                cd = _dish(conn, c["component_dish_id"])
                if cd is None:
                    continue
                seq += 1
                items.append(
                    routing.PrintItem(
                        name=cd["name"],
                        qty=req.qty * (c["qty"] or 1),
                        bindings=_bindings_for(conn, cd["id"]),
                        unit=cd["unit"],
                        spec_name=_spec_name(conn, c["spec_id"]),
                        created_seq=seq,
                        combo_parent=d["name"],
                        combo_uid=combo_seq,
                        combo_qty=req.qty,
                    )
                )
        else:
            items.append(
                routing.PrintItem(
                    name=d["name"],
                    qty=req.qty,
                    bindings=_bindings_for(conn, req.dish_id),
                    unit=d["unit"],
                    spec_name=_spec_name(conn, req.spec_id),
                    notes=req.notes,
                    addons=list(req.addons),
                    created_seq=seq,
                    ret_reason=req.ret_reason,
                )
            )
    return items

# --------------------------------------------------------------------------- #
# Plan + enqueue                                                              #
# --------------------------------------------------------------------------- #

def enqueue_tickets(
    conn: sqlite3.Connection,
    tickets: list[routing.PlannedTicket],
    ctx: render.RenderContext,
    st_ids: dict[str, int],
    *,
    order_id: int | None = None,
    dedupe_prefix: str | None = None,
    is_reprint: bool = False,
) -> list[int]:
    """Render each PlannedTicket to bytes and insert a durable print_jobs row.

    enqueue_seq is a global monotonic so that, within a station, jobs print in
    the exact order fan_out produced them (created-time order for per-dish).
    Returns the inserted print_jobs ids.
    """
    ts = now_ms()
    base = conn.execute("SELECT COALESCE(MAX(enqueue_seq),0) AS m FROM print_jobs").fetchone()["m"]
    job_ids: list[int] = []
    seq_n = 0
    for t in sorted(tickets, key=lambda x: x.seq):
        st_id = st_ids.get(t.station_code)
        if st_id is None:
            continue

        for copy_i in range(max(1, t.copies)):

            state = "pending" if is_reprint else _initial_state(conn, t.ticket_type)
            if state is None:
                continue
            seq_n += 1
            if state == "hold":

                serial = 0
                line_ctx = render.RenderContext(**{**ctx.__dict__, "serial_no": 0})
            else:
                serial = serial_no.next_serial(conn, st_id)
                line_ctx = render.RenderContext(**{**ctx.__dict__, "serial_no": serial})
            payload = render.render_station_ticket(t, line_ctx)

            dedupe = (f"{dedupe_prefix}:{t.station_code}:{t.ticket_type}:{t.seq}:{copy_i}"
                      if dedupe_prefix else None)
            cur = conn.execute(
                """INSERT INTO print_jobs
                   (station_id, order_id, ticket_type, serial_no, payload, state,
                    dedupe_key, enqueue_seq, created_at, next_attempt_at, is_reprint)
                   VALUES(?,?,?,?,?, ?, ?, ?, ?, 0, ?)""",
                (st_id, order_id, t.ticket_type, serial, payload, state, dedupe,
                 base + seq_n, ts, 1 if is_reprint else 0),
            )
            job_ids.append(cur.lastrowid)
    return job_ids

def release_holds(db, job_id: int | None = None) -> int:

    with db.transaction() as conn:
        sql = ("SELECT id, station_id, payload, serial_no FROM print_jobs "
               "WHERE state='hold'" + (" AND id=?" if job_id is not None else "") + " ORDER BY enqueue_seq")
        rows = conn.execute(sql, (job_id,) if job_id is not None else ()).fetchall()
        for r in rows:
            if r["serial_no"]:
                conn.execute("UPDATE print_jobs SET state='pending', next_attempt_at=0 WHERE id=?", (r["id"],))
                continue
            serial = serial_no.next_serial(conn, r["station_id"])
            payload = escpos.inject_serial(bytes(r["payload"]), serial)
            conn.execute(
                "UPDATE print_jobs SET state='pending', next_attempt_at=0, serial_no=?, payload=? WHERE id=?",
                (serial, payload, r["id"]),
            )
        return len(rows)

def plan_and_enqueue(
    db,
    *,
    op: routing.Op,
    requests: list[ItemRequest],
    ctx: render.RenderContext,
    order_id: int | None = None,
    whole_note: str | None = None,
    orig_table: str | None = None,
    target_table: str | None = None,
    dedupe_prefix: str | None = None,
) -> list[int]:
    """High-level: resolve -> fan_out -> render -> enqueue, in one transaction."""
    with db.transaction() as conn:
        stations = load_stations(conn)
        st_ids = station_ids_by_code(conn)
        items = resolve_items(conn, requests)
        tickets = routing.fan_out(
            op, items, stations,
            whole_note=whole_note, orig_table=orig_table, target_table=target_table,
        )
        return enqueue_tickets(
            conn, tickets, ctx, st_ids, order_id=order_id, dedupe_prefix=dedupe_prefix,
        )

def enqueue_payload(
    conn: sqlite3.Connection,
    station_code: str,
    ticket_type: str,
    payload: bytes,
    *,
    order_id: int | None = None,
    dedupe_key: str | None = None,
    is_reprint: bool = False,
    force_pending: bool = False,
) -> int | None:

    st = conn.execute("SELECT id FROM stations WHERE code=?", (station_code,)).fetchone()
    if st is None:
        return None
    state = "pending" if (is_reprint or force_pending) else _initial_state(conn, ticket_type)
    if state is None:
        return None
    serial = serial_no.next_serial(conn, st["id"])
    base = conn.execute("SELECT COALESCE(MAX(enqueue_seq),0) m FROM print_jobs").fetchone()["m"]
    cur = conn.execute(
        """INSERT INTO print_jobs
           (station_id, order_id, ticket_type, serial_no, payload, state,
            dedupe_key, enqueue_seq, created_at, next_attempt_at, is_reprint)
           VALUES(?,?,?,?,?, ?, ?, ?, ?, 0, ?)""",
        (st["id"], order_id, ticket_type, serial, payload, state, dedupe_key, base + 1, now_ms(),
         1 if is_reprint else 0),
    )
    return cur.lastrowid

def reprint_job(db, job_id: int) -> int | None:

    with db.transaction() as conn:
        src = conn.execute("SELECT * FROM print_jobs WHERE id=?", (job_id,)).fetchone()
        if src is None:
            return None

        if not src["payload"] or len(bytes(src["payload"])) == 0:
            raise ValidationError("This print job has no saved payload. Submit a new ticket to print it again.")
        serial = serial_no.next_serial(conn, src["station_id"])

        payload = bytes(src["payload"])

        for _kick in (escpos.CMD_KICK_PIN2, escpos.CMD_KICK_PIN5):
            if payload.endswith(_kick):
                payload = payload[: -len(_kick)]
                break
        if not src["serial_no"]:
            payload = escpos.inject_serial(payload, serial)
        base = conn.execute("SELECT COALESCE(MAX(enqueue_seq),0) AS m FROM print_jobs").fetchone()["m"]
        cur = conn.execute(
            """INSERT INTO print_jobs
               (station_id, order_id, ticket_type, serial_no, payload, state,
                enqueue_seq, created_at, next_attempt_at, is_reprint)
               VALUES(?,?,?,?,?, 'pending', ?, ?, 0, 1)""",
            (src["station_id"], src["order_id"], src["ticket_type"], serial,
             payload, base + 1, now_ms()),
        )
        conn.execute(
            "INSERT INTO audit_log(kind, ref_table, ref_id, detail, created_at) "
            "VALUES('reprint','print_jobs',?,?,?)",
            (job_id, f"补打 job {job_id} -> {cur.lastrowid}", now_ms()),
        )
        return cur.lastrowid
