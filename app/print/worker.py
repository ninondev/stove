# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
"""Durable per-station printing with retry backoff and lease recovery."""
from __future__ import annotations

import logging
import random
import threading
import time
from dataclasses import dataclass, field

from .. import config
from ..db.connection import Database, now_ms
from . import transport as tp

def backoff_seconds(attempts: int) -> float:
    schedule = config.RETRY_BACKOFF_S
    base = schedule[min(attempts, len(schedule) - 1)]
    base = min(base, config.RETRY_BACKOFF_CAP_S)
    # full jitter to avoid thundering herd
    return random.uniform(base * 0.5, base)

STUCK_BACKLOG_MS = 180_000

SENDING_LEASE_MS = 60_000

SEND_FAIL_COOLDOWN_MS = 30_000

@dataclass
class StationHealth:
    code: str
    name: str
    reachable: bool = True
    reason: str | None = None
    pending: int = 0
    dead: int = 0
    last_error: str | None = None
    oldest_pending_ms: int | None = None

@dataclass
class _Station:
    id: int
    code: str
    name: str
    transport: tp.Transport
    enabled: bool = True

    _send_lock: threading.Lock = field(default_factory=threading.Lock)
    _send_fail_until_ms: int = 0

class PrintManager:
    def __init__(self, db: Database):
        self.db = db
        self._stations: dict[int, _Station] = {}

        self._misconfigured: list[tuple[int, str, str, str]] = []
        self._health: dict[str, StationHealth] = {}
        self._lock = threading.Lock()
        self._stop = threading.Event()
        self._wake = threading.Event()
        self._thread: threading.Thread | None = None
        self._alert_cb = None  # callable(list[StationHealth]) for WS push

    # -- setup ----------------------------------------------------------- #
    def reload_stations(self) -> None:
        rows = self.db.query("SELECT * FROM stations WHERE enabled=1")
        stations: dict[int, _Station] = {}
        misconfigured: list[tuple[int, str, str, str]] = []
        for r in rows:
            try:
                trans = tp.build_transport(
                    transport=r["transport"], net_host=r["net_host"],
                    net_port=r["net_port"], serial_path=r["serial_path"],
                    serial_baud=r["serial_baud"],
                )
            except ValueError as ve:

                logging.getLogger("stove.print").warning(
                    "Station %s (id=%s) has invalid configuration: %s. Set its printer address.",
                    r["name"], r["id"], ve,
                )
                misconfigured.append((r["id"], r["code"], r["name"], str(ve)))
                continue
            stations[r["id"]] = _Station(r["id"], r["code"], r["name"], trans)
        with self._lock:
            self._stations = stations
            self._misconfigured = misconfigured

    def set_alert_callback(self, cb) -> None:
        self._alert_cb = cb

    def set_transport(self, station_code: str, transport: tp.Transport) -> None:
        """Override a station's transport (tests, or a runtime swap)."""
        with self._lock:
            for st in self._stations.values():
                if st.code == station_code:
                    st.transport = transport

    # -- claiming & recording ------------------------------------------- #
    def _claim_next(self, station_id: int) -> dict | None:
        ts = now_ms()
        with self.db.transaction() as conn:
            # Check the oldest job before its retry time; newer jobs cannot overtake it.
            row = conn.execute(
                """SELECT * FROM print_jobs
                   WHERE station_id=? AND state IN ('pending','failed')
                     ORDER BY enqueue_seq ASC LIMIT 1""",
                (station_id,),
            ).fetchone()
            if row is None or row["next_attempt_at"] > ts:
                return None
            conn.execute(
                "UPDATE print_jobs SET state='sending', next_attempt_at=? WHERE id=?",
                (ts + SENDING_LEASE_MS, row["id"]),
            )
            return dict(row)

    def _record_success(self, job_id: int) -> None:
        with self.db.transaction() as conn:
            conn.execute(
                "UPDATE print_jobs SET state='done', sent_at=?, last_error=NULL WHERE id=?",
                (now_ms(), job_id),
            )

    def _record_failure(self, job: dict, err: str) -> None:
        attempts = job["attempts"] + 1
        dead = attempts >= config.RETRY_MAX_ATTEMPTS
        with self.db.transaction() as conn:
            conn.execute(
                """UPDATE print_jobs
                   SET state=?, attempts=?, last_error=?, next_attempt_at=?
                   WHERE id=?""",
                (
                    "dead" if dead else "failed",
                    attempts,
                    err[:500],
                    now_ms() + int(backoff_seconds(attempts) * 1000),
                    job["id"],
                ),
            )
            if dead:
                conn.execute(
                    "INSERT INTO audit_log(kind, ref_table, ref_id, detail, created_at) "
                    "VALUES('print_dead','print_jobs',?,?,?)",
                    (job["id"], err[:500], now_ms()),
                )

    # -- processing ------------------------------------------------------ #
    def process_station_once(self, station_id: int, max_jobs: int = 50) -> int:
        """Send due jobs for one station, in order, until one fails or none left."""
        with self._lock:
            station = self._stations.get(station_id)
            health = self._health.get(station.code) if station else None
        if station is None:
            return 0

        if health is not None and not health.reachable:
            return 0

        if not station._send_lock.acquire(blocking=False):
            return 0
        try:
            sent = 0
            for _ in range(max_jobs):
                job = self._claim_next(station_id)
                if job is None:
                    break
                try:
                    station.transport.send(job["payload"])
                except tp.TransportError as e:

                    station._send_fail_until_ms = now_ms() + SEND_FAIL_COOLDOWN_MS
                    if health is not None:
                        health.reachable = False
                        health.reason = "发送失败/无响应"

                    try:
                        self._record_failure(job, str(e))
                    except Exception:
                        logging.getLogger("stove.print").exception("Could not record failed job %s", job["id"])
                    break  # preserve order: do not skip ahead past a stuck job
                else:
                    try:
                        self._record_success(job["id"])
                    except Exception:
                        logging.getLogger("stove.print").exception("Could not record printed job %s", job["id"])
                    sent += 1
            return sent
        finally:
            station._send_lock.release()

    def tick(self) -> int:
        with self._lock:
            ids = list(self._stations.keys())
        total = 0
        for sid in ids:
            total += self.process_station_once(sid)
        return total

    def wake(self) -> None:

        t = self._thread
        if t is not None and t.is_alive() and not self._stop.is_set():
            self._wake.set()
        else:
            self.tick()

    def _sweep_stuck_sending(self) -> int:

        with self.db.transaction() as conn:
            cur = conn.execute(
                "UPDATE print_jobs SET state='pending', next_attempt_at=0 "
                "WHERE state='sending' AND next_attempt_at < ?", (now_ms(),))
            return cur.rowcount

    # -- health ---------------------------------------------------------- #
    def refresh_health(self, probe: bool = True) -> list[StationHealth]:
        with self._lock:
            stations = list(self._stations.values())
        result: list[StationHealth] = []
        for st in stations:
            counts = self.db.query_one(
                """SELECT
                     SUM(CASE WHEN state IN ('pending','failed') THEN 1 ELSE 0 END) AS pending,
                     SUM(CASE WHEN state='dead' THEN 1 ELSE 0 END) AS dead,
                     MIN(CASE WHEN state IN ('pending','failed') THEN created_at END) AS oldest,
                     MAX(CASE WHEN state IN ('failed','dead') THEN last_error END) AS err
                   FROM print_jobs WHERE station_id=?""",
                (st.id,),
            )
            h = StationHealth(code=st.code, name=st.name)
            h.pending = (counts["pending"] or 0) if counts else 0
            h.dead = (counts["dead"] or 0) if counts else 0
            h.oldest_pending_ms = counts["oldest"] if counts else None
            h.last_error = counts["err"] if counts else None
            if probe:
                status = st.transport.probe()
                h.reachable = status.reachable
                h.reason = status.reason

            if st._send_fail_until_ms > now_ms():
                h.reachable = False
                h.reason = h.reason or "发送失败冷却中"
            result.append(h)

        with self._lock:
            misconf = list(self._misconfigured)
        for (sid, scode, sname, err) in misconf:
            counts = self.db.query_one(
                "SELECT SUM(CASE WHEN state IN ('pending','failed') THEN 1 ELSE 0 END) AS pending, "
                "MIN(CASE WHEN state IN ('pending','failed') THEN created_at END) AS oldest "
                "FROM print_jobs WHERE station_id=?",
                (sid,),
            )
            h = StationHealth(code=scode, name=sname, reachable=False,
                              reason=f"配置错误：{err}")
            h.pending = (counts["pending"] or 0) if counts else 0
            h.oldest_pending_ms = counts["oldest"] if counts else None
            result.append(h)
        with self._lock:
            self._health = {h.code: h for h in result}

        now = now_ms()
        bad = []
        for h in result:
            stuck = (h.oldest_pending_ms is not None
                     and now - h.oldest_pending_ms > STUCK_BACKLOG_MS)
            if stuck and h.reachable and not h.reason:
                h.reason = f"积压 {h.pending} 张未出"
            if (not h.reachable) or h.dead > 0 or stuck:
                bad.append(h)
        if bad and self._alert_cb:
            try:
                self._alert_cb(bad)
            except Exception:
                logging.getLogger("stove.print").exception("Printer alert callback failed")
        return result

    def health_snapshot(self) -> list[StationHealth]:
        with self._lock:
            return list(self._health.values())

    # -- background loop ------------------------------------------------- #
    def start(self, interval_s: float = 1.0, health_every: int = 5, sweep_every: int = 30) -> None:
        self.reload_stations()
        self._stop.clear()

        def _loop():
            i = 0
            while not self._stop.is_set():
                try:
                    self.tick()
                    if i % health_every == 0:
                        self.refresh_health(probe=True)
                    if i % sweep_every == 0:
                        self._sweep_stuck_sending()
                except Exception:
                    pass
                i += 1

                self._wake.wait(interval_s)
                self._wake.clear()

        self._thread = threading.Thread(target=_loop, name="print-manager", daemon=True)
        self._thread.start()

    def stop(self) -> None:
        self._stop.set()
        self._wake.set()
        if self._thread:
            self._thread.join(timeout=3)

    def ensure_alive(self) -> bool:

        if self._stop.is_set():
            return True
        t = self._thread
        if t is not None and t.is_alive():
            return True
        self.start()
        return False
