#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
"""Run the queue in memory. No database file or printer is used."""
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from app.db.connection import Database
from app.services import queue


def main() -> None:
    db = Database(":memory:")
    try:
        db.init_schema()
        first = queue.take_number(db, size_group="small", party_size=2)
        second = queue.take_number(db, size_group="small", party_size=1)
        queue.take_number(db, size_group="medium", party_size=4)
        queue.call(db, first["id"])
        print("Called ticket:", queue.ticket_label(first))
        print("Next small ticket:", queue.ticket_label(second))
        print(json.dumps(queue.summary(db), indent=2))
        print("The wait estimate is a simple rule: 10 minutes per waiting table ahead.")
    finally:
        db.close()


if __name__ == "__main__":
    main()
