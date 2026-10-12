# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
"""Take-a-number queue, with independent counters per business day and group.

Pass business_day explicitly to keep a late-night shift on the same day.
Without it, the local calendar date is used. Counter allocation and ticket
insertion share one transaction, so failed insertions do not consume numbers.
"""
from __future__ import annotations

import sqlite3
from datetime import date

from ..db.connection import Database, now_ms
from ..domain.errors import NotFoundError, ValidationError

SIZE_GROUPS = [
    ("small", "Small (1-2 people)"),
    ("medium", "Medium (3-4 people)"),
    ("large", "Large (5 or more people)"),
]
_GROUP_LABELS = dict(SIZE_GROUPS)
_VALID_GROUPS = set(_GROUP_LABELS)
_WAIT_PER_TABLE_MIN = 10


def ticket_label(ticket) -> str:
    return f"{ticket['size_group']} {ticket['number']}"


def _day(conn: sqlite3.Connection, business_day: str | None) -> sqlite3.Row:
    if business_day is None:
        business_day = date.today().isoformat()
    try:
        parsed = date.fromisoformat(business_day)
    except (TypeError, ValueError):
        raise ValidationError("Business day must be a date in YYYY-MM-DD form.") from None
    if parsed.isoformat() != business_day:
        raise ValidationError("Business day must be a date in YYYY-MM-DD form.")
    conn.execute(
        "INSERT OR IGNORE INTO business_days(biz_date, opened_at) VALUES(?,?)",
        (business_day, now_ms()),
    )
    return conn.execute("SELECT * FROM business_days WHERE biz_date=?", (business_day,)).fetchone()


def _ticket(conn: sqlite3.Connection, ticket_id: int) -> sqlite3.Row:
    row = conn.execute("SELECT * FROM queue_tickets WHERE id=?", (ticket_id,)).fetchone()
    if row is None:
        raise NotFoundError("Queue ticket was not found.")
    return row


def _alloc_number(conn: sqlite3.Connection, day_id: int, size_group: str) -> int:
    conn.execute(
        "INSERT OR IGNORE INTO queue_counters(business_day_id, size_group, next_number) "
        "VALUES(?,?,1)", (day_id, size_group),
    )
    number = conn.execute(
        "SELECT next_number FROM queue_counters WHERE business_day_id=? AND size_group=?",
        (day_id, size_group),
    ).fetchone()["next_number"]
    conn.execute(
        "UPDATE queue_counters SET next_number=next_number+1 "
        "WHERE business_day_id=? AND size_group=?", (day_id, size_group),
    )
    return number


def _waiting_ahead(conn, day_id: int, size_group: str, number: int) -> int:
    return conn.execute(
        "SELECT COUNT(*) c FROM queue_tickets "
        "WHERE business_day_id=? AND size_group=? AND state='waiting' AND number<?",
        (day_id, size_group, number),
    ).fetchone()["c"]


def _bump(conn, ticket_id: int) -> None:
    conn.execute("UPDATE queue_tickets SET version=version+1 WHERE id=?", (ticket_id,))


def _audit(conn, kind: str, ticket_id: int, detail: str) -> None:
    conn.execute(
        "INSERT INTO audit_log(kind, ref_table, ref_id, detail, created_at) "
        "VALUES(?,'queue_tickets',?,?,?)", (kind, ticket_id, detail, now_ms()),
    )


def take_number(db: Database, *, size_group: str, party_size=None,
                business_day: str | None = None) -> dict:
    if size_group not in _VALID_GROUPS:
        raise ValidationError("Choose small, medium, or large.")
    if party_size is not None and party_size != "":
        try:
            party_size = int(party_size)
        except (TypeError, ValueError, OverflowError):
            raise ValidationError("Party size must be an integer.") from None
        if not 0 <= party_size <= 999:
            raise ValidationError("Party size must be between 0 and 999.")
    else:
        party_size = None
    with db.transaction() as conn:
        day = _day(conn, business_day)
        number = _alloc_number(conn, day["id"], size_group)
        cursor = conn.execute(
            "INSERT INTO queue_tickets(business_day_id, size_group, number, party_size, "
            "state, created_at) VALUES(?,?,?,?,'waiting',?)",
            (day["id"], size_group, number, party_size, now_ms()),
        )
        ahead = _waiting_ahead(conn, day["id"], size_group, number)
        return {"id": cursor.lastrowid, "size_group": size_group, "number": number,
                "waiting_ahead": ahead, "est_wait_min": ahead * _WAIT_PER_TABLE_MIN}


def list_waiting(db: Database, size_group: str | None = None, *,
                 business_day: str | None = None) -> list[dict]:
    """Include passed tickets so someone who returns can keep their number."""
    if size_group is not None and size_group not in _VALID_GROUPS:
        raise ValidationError("Choose small, medium, or large.")
    with db.transaction() as conn:
        day = _day(conn, business_day)
        sql = ("SELECT * FROM queue_tickets WHERE business_day_id=? "
               "AND state IN ('waiting','called','passed')")
        params = [day["id"]]
        if size_group is not None:
            sql += " AND size_group=?"
            params.append(size_group)
        rows = conn.execute(sql + " ORDER BY size_group, number", params).fetchall()
        result = []
        for row in rows:
            ahead = _waiting_ahead(conn, day["id"], row["size_group"], row["number"])
            result.append({"id": row["id"], "size_group": row["size_group"],
                           "label": _GROUP_LABELS[row["size_group"]], "number": row["number"],
                           "party_size": row["party_size"], "state": row["state"],
                           "position": ahead, "est_wait_min": ahead * _WAIT_PER_TABLE_MIN,
                           "created_at": row["created_at"], "called_at": row["called_at"],
                           "call_count": row["call_count"], "version": row["version"]})
        return result


def summary(db: Database, *, business_day: str | None = None) -> dict:
    with db.transaction() as conn:
        day = _day(conn, business_day)
        rows = conn.execute(
            "SELECT size_group,state,COUNT(*) c FROM queue_tickets "
            "WHERE business_day_id=? AND state IN ('waiting','called') "
            "GROUP BY size_group,state", (day["id"],),
        ).fetchall()
        counts = {(row["size_group"], row["state"]): row["c"] for row in rows}
        groups = [{"size_group": group, "label": label,
                   "waiting": counts.get((group, "waiting"), 0),
                   "called": counts.get((group, "called"), 0)}
                  for group, label in SIZE_GROUPS]
        return {"groups": groups, "total_waiting": sum(group["waiting"] for group in groups)}


def call(db: Database, ticket_id: int) -> dict:
    """Repeated calls increment call_count; tickets are never passed automatically."""
    with db.transaction() as conn:
        ticket = _ticket(conn, ticket_id)
        if ticket["state"] not in ("waiting", "called"):
            raise ValidationError("This ticket cannot be called in its current state.")
        conn.execute(
            "UPDATE queue_tickets SET state='called',called_at=?,call_count=call_count+1 WHERE id=?",
            (now_ms(), ticket_id),
        )
        _bump(conn, ticket_id)
        return {"id": ticket_id, "state": "called", "call_count": ticket["call_count"] + 1}


def mark_pass(db: Database, ticket_id: int) -> dict:
    with db.transaction() as conn:
        ticket = _ticket(conn, ticket_id)
        if ticket["state"] not in ("waiting", "called"):
            raise ValidationError("This ticket cannot be passed in its current state.")
        conn.execute("UPDATE queue_tickets SET state='passed' WHERE id=?", (ticket_id,))
        _audit(conn, "queue_pass", ticket_id, ticket_label(ticket) + " passed")
        _bump(conn, ticket_id)
        return {"id": ticket_id, "state": "passed"}


def requeue(db: Database, ticket_id: int) -> dict:
    """Restore a passed ticket's original number and clear the last call time."""
    with db.transaction() as conn:
        ticket = _ticket(conn, ticket_id)
        if ticket["state"] != "passed":
            raise ValidationError("Only a passed ticket can return to waiting.")
        conn.execute("UPDATE queue_tickets SET state='waiting',called_at=NULL WHERE id=?", (ticket_id,))
        _bump(conn, ticket_id)
        return {"id": ticket_id, "state": "waiting"}


def cancel(db: Database, ticket_id: int) -> dict:
    with db.transaction() as conn:
        ticket = _ticket(conn, ticket_id)
        if ticket["state"] not in ("waiting", "called", "passed"):
            raise ValidationError("This ticket cannot be cancelled in its current state.")
        conn.execute("UPDATE queue_tickets SET state='cancelled' WHERE id=?", (ticket_id,))
        _audit(conn, "queue_cancel", ticket_id, ticket_label(ticket) + " cancelled")
        _bump(conn, ticket_id)
        return {"id": ticket_id, "state": "cancelled"}
