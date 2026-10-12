# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
"""SQLite access with one serialized writer and nested transaction savepoints."""
from __future__ import annotations

import sqlite3
import threading
import time
from contextlib import contextmanager
from pathlib import Path
from typing import Any, Iterator, Sequence


def now_ms() -> int:
    """Current UTC epoch time in integer milliseconds."""
    return int(time.time() * 1000)


class Database:
    """Use ':memory:' for demos, or a file path for durable queue and print jobs."""

    def __init__(self, path: Path | str = ":memory:"):
        self.path = Path(path)
        self._lock = threading.RLock()
        self._writer: sqlite3.Connection | None = None
        self._local = threading.local()

    def connect(self) -> None:
        with self._lock:
            if self._writer is not None:
                return
            if str(self.path) != ":memory:":
                self.path.parent.mkdir(parents=True, exist_ok=True)
            conn = sqlite3.connect(str(self.path), check_same_thread=False, isolation_level=None)
            conn.row_factory = sqlite3.Row
            conn.execute("PRAGMA journal_mode=WAL")
            conn.execute("PRAGMA foreign_keys=ON")
            conn.execute("PRAGMA synchronous=FULL")
            conn.execute("PRAGMA busy_timeout=5000")
            conn.execute("PRAGMA temp_store=MEMORY")
            conn.execute("PRAGMA wal_autocheckpoint=1000")
            self._writer = conn

    def close(self) -> None:
        with self._lock:
            if self._writer is not None:
                self._writer.close()
                self._writer = None

    @property
    def writer(self) -> sqlite3.Connection:
        if self._writer is None:
            self.connect()
        assert self._writer is not None
        return self._writer

    @contextmanager
    def transaction(self) -> Iterator[sqlite3.Connection]:
        """Commit on success. A caught nested failure rolls back only its savepoint."""
        with self._lock:
            depth = getattr(self._local, "txn_depth", 0)
            conn = self.writer
            savepoint = f"sp_{depth}"
            conn.execute("BEGIN IMMEDIATE" if depth == 0 else f"SAVEPOINT {savepoint}")
            self._local.txn_depth = depth + 1
            try:
                try:
                    yield conn
                except BaseException:
                    self._rollback(conn, depth, savepoint)
                    raise
                else:
                    try:
                        conn.execute("COMMIT" if depth == 0 else f"RELEASE {savepoint}")
                    except BaseException:
                        self._rollback(conn, depth, savepoint)
                        raise
            finally:
                self._local.txn_depth = depth

    @staticmethod
    def _rollback(conn: sqlite3.Connection, depth: int, savepoint: str) -> None:
        # A rollback error must not hide the error that caused it.
        try:
            if depth == 0:
                if conn.in_transaction:
                    conn.execute("ROLLBACK")
            else:
                conn.execute(f"ROLLBACK TO {savepoint}")
                conn.execute(f"RELEASE {savepoint}")
        except sqlite3.Error:
            pass

    def execute(self, sql: str, params: Sequence[Any] = ()) -> sqlite3.Cursor:
        with self.transaction() as conn:
            return conn.execute(sql, params)

    def executemany(self, sql: str, params: Sequence[Sequence[Any]]) -> None:
        with self.transaction() as conn:
            conn.executemany(sql, params)

    def query(self, sql: str, params: Sequence[Any] = ()) -> list[sqlite3.Row]:
        with self._lock:
            return self.writer.execute(sql, params).fetchall()

    def query_one(self, sql: str, params: Sequence[Any] = ()) -> sqlite3.Row | None:
        rows = self.query(sql, params)
        return rows[0] if rows else None

    def init_schema(self) -> None:
        """Create the kit's empty schema; no source data or seed is loaded."""
        schema = Path(__file__).with_name("schema.sql").read_text(encoding="utf-8")
        with self._lock:
            if self.writer.in_transaction:
                raise RuntimeError("Initialize the schema outside a transaction.")
            self.writer.executescript(schema)

    def integrity_ok(self) -> bool:
        row = self.query_one("PRAGMA integrity_check")
        return row is not None and row[0] == "ok"
