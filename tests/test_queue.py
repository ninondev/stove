# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
"""Ported queue rules, plus standalone day, rollback, and concurrency checks."""
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from app.db.connection import Database
from app.domain.errors import NotFoundError, ValidationError
from app.services import queue as Q


class QueueTests(unittest.TestCase):
    def setUp(self):
        self.db = Database(":memory:")
        self.db.init_schema()
        self.addCleanup(self.db.close)

    def ticket(self, ticket_id):
        return self.db.query_one("SELECT * FROM queue_tickets WHERE id=?", (ticket_id,))

    def test_take_number_increments_per_group(self):
        first = Q.take_number(self.db, size_group="small", party_size=2)
        second = Q.take_number(self.db, size_group="small", party_size=2)
        medium = Q.take_number(self.db, size_group="medium", party_size=3)
        self.assertEqual((first["number"], second["number"], medium["number"]), (1, 2, 1))

    def test_waiting_ahead(self):
        Q.take_number(self.db, size_group="small")
        Q.take_number(self.db, size_group="small")
        third = Q.take_number(self.db, size_group="small")
        self.assertEqual(third["waiting_ahead"], 2)
        self.assertEqual(third["est_wait_min"], 20)
        self.assertEqual(Q.take_number(self.db, size_group="large")["waiting_ahead"], 0)

    def test_list_waiting_positions(self):
        Q.take_number(self.db, size_group="small")
        Q.take_number(self.db, size_group="small")
        rows = Q.list_waiting(self.db, size_group="small")
        self.assertEqual([row["number"] for row in rows], [1, 2])
        self.assertEqual([row["position"] for row in rows], [0, 1])

    def test_call_sets_called_and_counts_repeated_calls(self):
        ticket = Q.take_number(self.db, size_group="small")
        self.assertEqual(Q.call(self.db, ticket["id"])["state"], "called")
        row = self.ticket(ticket["id"])
        self.assertEqual(row["state"], "called")
        self.assertIsNotNone(row["called_at"])
        self.assertEqual(Q.call(self.db, ticket["id"])["call_count"], 2)
        self.assertEqual(self.ticket(ticket["id"])["version"], 2)

    def test_pass_and_cancel(self):
        passed = Q.take_number(self.db, size_group="small")
        cancelled = Q.take_number(self.db, size_group="small")
        self.assertEqual(Q.mark_pass(self.db, passed["id"])["state"], "passed")
        self.assertEqual(Q.cancel(self.db, cancelled["id"])["state"], "cancelled")
        rows = Q.list_waiting(self.db)
        self.assertEqual([row["id"] for row in rows], [passed["id"]])
        self.assertEqual(rows[0]["state"], "passed")
        self.assertEqual(self.db.query_one("SELECT COUNT(*) FROM audit_log")[0], 2)

    def test_requeue_restores_waiting(self):
        ticket = Q.take_number(self.db, size_group="small")
        Q.call(self.db, ticket["id"])
        Q.mark_pass(self.db, ticket["id"])
        self.assertEqual(Q.requeue(self.db, ticket["id"])["state"], "waiting")
        row = self.ticket(ticket["id"])
        self.assertEqual(row["number"], ticket["number"])
        self.assertIsNone(row["called_at"])
        self.assertEqual(row["call_count"], 1)

    def test_requeue_only_passed(self):
        ticket = Q.take_number(self.db, size_group="small")
        with self.assertRaises(ValidationError):
            Q.requeue(self.db, ticket["id"])

    def test_summary_counts(self):
        Q.take_number(self.db, size_group="small")
        called = Q.take_number(self.db, size_group="small")
        Q.take_number(self.db, size_group="medium")
        Q.call(self.db, called["id"])
        summary = Q.summary(self.db)
        groups = {group["size_group"]: group for group in summary["groups"]}
        self.assertEqual((groups["small"]["waiting"], groups["small"]["called"]), (1, 1))
        self.assertEqual(groups["medium"]["waiting"], 1)
        self.assertEqual(groups["large"]["waiting"], 0)
        self.assertEqual(summary["total_waiting"], 2)

    def test_counters_and_lists_are_scoped_to_explicit_business_day(self):
        for day in ("2026-01-01", "2026-01-02"):
            first = Q.take_number(self.db, size_group="small", business_day=day)
            second = Q.take_number(self.db, size_group="small", business_day=day)
            self.assertEqual((first["number"], second["number"]), (1, 2))
            self.assertEqual(second["waiting_ahead"], 1)
        self.assertEqual(len(Q.list_waiting(self.db, business_day="2026-01-01")), 2)
        self.assertEqual(Q.summary(self.db, business_day="2026-01-02")["total_waiting"], 2)

    def test_called_and_passed_do_not_count_as_waiting_ahead(self):
        first = Q.take_number(self.db, size_group="small")
        second = Q.take_number(self.db, size_group="small")
        Q.call(self.db, first["id"])
        Q.mark_pass(self.db, second["id"])
        self.assertEqual(Q.take_number(self.db, size_group="small")["waiting_ahead"], 0)
        Q.requeue(self.db, second["id"])
        self.assertEqual(Q.list_waiting(self.db, "small")[-1]["position"], 1)

    def test_invalid_group_size_and_day_do_not_consume_numbers(self):
        for size in (-1, 1000, "many", float("inf")):
            with self.subTest(size=size), self.assertRaises(ValidationError):
                Q.take_number(self.db, size_group="small", party_size=size)
        with self.assertRaises(ValidationError):
            Q.take_number(self.db, size_group="unknown")
        with self.assertRaises(ValidationError):
            Q.list_waiting(self.db, "unknown")
        for day in ("2026-02-30", "20260101", "bad"):
            with self.subTest(day=day), self.assertRaises(ValidationError):
                Q.take_number(self.db, size_group="small", business_day=day)
        self.assertEqual(Q.take_number(self.db, size_group="small")["number"], 1)

    def test_cancelled_ticket_rejects_every_later_transition(self):
        ticket = Q.take_number(self.db, size_group="small")
        Q.cancel(self.db, ticket["id"])
        for action in (Q.call, Q.mark_pass, Q.requeue, Q.cancel):
            with self.subTest(action=action.__name__), self.assertRaises(ValidationError):
                action(self.db, ticket["id"])
        self.assertEqual(self.ticket(ticket["id"])["version"], 1)

    def test_missing_ticket(self):
        for action in (Q.call, Q.mark_pass, Q.requeue, Q.cancel):
            with self.subTest(action=action.__name__), self.assertRaises(NotFoundError):
                action(self.db, 1234)

    def test_failed_outer_transaction_rolls_back_ticket_and_number(self):
        with self.assertRaisesRegex(RuntimeError, "abort"):
            with self.db.transaction():
                Q.take_number(self.db, size_group="small")
                raise RuntimeError("abort")
        self.assertEqual(Q.take_number(self.db, size_group="small")["number"], 1)
        self.assertEqual(len(Q.list_waiting(self.db)), 1)

    def test_caught_nested_failure_rolls_back_only_inner_changes(self):
        with self.db.transaction():
            first = Q.take_number(self.db, size_group="small")
            try:
                with self.db.transaction():
                    Q.take_number(self.db, size_group="small")
                    raise RuntimeError("abort inner")
            except RuntimeError:
                pass
            second = Q.take_number(self.db, size_group="small")
        self.assertEqual((first["number"], second["number"]), (1, 2))
        self.assertEqual(len(Q.list_waiting(self.db)), 2)

    def test_concurrent_allocations_have_no_duplicates_or_gaps(self):
        def allocate(_):
            return Q.take_number(self.db, size_group="small")["number"]
        with ThreadPoolExecutor(max_workers=8) as executor:
            numbers = list(executor.map(allocate, range(80)))
        self.assertEqual(sorted(numbers), list(range(1, 81)))
        self.assertTrue(self.db.integrity_ok())

    def test_two_database_connections_share_atomic_counter(self):
        with tempfile.TemporaryDirectory() as temporary:
            first = Database(Path(temporary) / "queue.sqlite")
            second = Database(Path(temporary) / "queue.sqlite")
            try:
                first.init_schema()
                second.connect()
                def allocate(index):
                    db = first if index % 2 else second
                    return Q.take_number(db, size_group="small")["number"]
                with ThreadPoolExecutor(max_workers=4) as executor:
                    numbers = list(executor.map(allocate, range(40)))
                self.assertEqual(sorted(numbers), list(range(1, 41)))
                first.close()
                first.connect()
                self.assertEqual(Q.take_number(first, size_group="small")["number"], 41)
            finally:
                first.close()
                second.close()

    def test_schema_has_no_customer_or_staff_tables(self):
        names = {row[0] for row in self.db.query("SELECT name FROM sqlite_master WHERE type='table'")}
        self.assertFalse(names & {"staff", "customers", "orders", "payments", "queue_ticket_items"})


if __name__ == "__main__":
    unittest.main()
