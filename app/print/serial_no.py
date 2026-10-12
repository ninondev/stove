# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
from __future__ import annotations

import sqlite3

def next_serial(conn: sqlite3.Connection, station_id: int) -> int:
    """Atomically allocate the next per-station serial. Caller is inside a txn."""
    conn.execute(
        "INSERT INTO station_serials(station_id, next_serial) VALUES(?, 1) "
        "ON CONFLICT(station_id) DO NOTHING",
        (station_id,),
    )
    row = conn.execute(
        "SELECT next_serial FROM station_serials WHERE station_id=?", (station_id,)
    ).fetchone()
    serial = int(row[0])
    conn.execute(
        "UPDATE station_serials SET next_serial = next_serial + 1 WHERE station_id=?",
        (station_id,),
    )
    return serial

def reconcile(conn: sqlite3.Connection) -> None:
    """On boot, ensure next_serial > max(serial_no) per station (crash safety)."""
    rows = conn.execute(
        "SELECT station_id, MAX(serial_no) AS m FROM print_jobs GROUP BY station_id"
    ).fetchall()
    for r in rows:
        conn.execute(
            "INSERT INTO station_serials(station_id, next_serial) VALUES(?, ?) "
            "ON CONFLICT(station_id) DO UPDATE SET next_serial = MAX(next_serial, ?)",
            (r["station_id"], (r["m"] or 0) + 1, (r["m"] or 0) + 1),
        )
