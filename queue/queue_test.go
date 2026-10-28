// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package queue

import (
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/ninondev/stove/db"
	"github.com/ninondev/stove/perr"
)

func newDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	d.InitSchema()
	return d
}

func take(t *testing.T, d *db.DB, group string) map[string]any {
	t.Helper()
	return TakeNumber(d, group, nil, nil)
}

func ticketRow(d *db.DB, id any) *db.Row {
	return d.QueryOne("SELECT * FROM queue_tickets WHERE id=?", id)
}

func mustFail(t *testing.T, what string, fn func()) *perr.Error {
	t.Helper()
	e := perr.Catch(fn)
	if e == nil {
		t.Fatalf("%s: expected an error", what)
	}
	return e
}

func TestNumbersCountPerGroup(t *testing.T) {
	d := newDB(t)
	two := int64(2)
	first := TakeNumber(d, "small", &two, nil)
	second := TakeNumber(d, "small", &two, nil)
	medium := take(t, d, "medium")
	if first["number"] != int64(1) || second["number"] != int64(2) || medium["number"] != int64(1) {
		t.Fatalf("numbers %v %v %v", first["number"], second["number"], medium["number"])
	}
}

func TestWaitingAheadAndEstimate(t *testing.T) {
	d := newDB(t)
	take(t, d, "small")
	take(t, d, "small")
	third := take(t, d, "small")
	if third["waiting_ahead"] != int64(2) || third["est_wait_min"] != int64(20) {
		t.Fatalf("third %v", third)
	}
	if take(t, d, "large")["waiting_ahead"] != int64(0) {
		t.Fatal("another group must not count")
	}
}

func TestListWaitingPositions(t *testing.T) {
	d := newDB(t)
	take(t, d, "small")
	take(t, d, "small")
	g := "small"
	rows := ListWaiting(d, &g, nil)
	if len(rows) != 2 || rows[0]["number"] != int64(1) || rows[1]["number"] != int64(2) ||
		rows[0]["position"] != int64(0) || rows[1]["position"] != int64(1) {
		t.Fatalf("rows %v", rows)
	}
}

func TestCallCountsRepeatedCalls(t *testing.T) {
	d := newDB(t)
	id := take(t, d, "small")["id"].(int64)
	if Call(d, id)["state"] != "called" {
		t.Fatal("not called")
	}
	row := ticketRow(d, id)
	if row.Str("state") != "called" || row.IsNull("called_at") {
		t.Fatalf("row %v", row.Map())
	}
	if Call(d, id)["call_count"] != int64(2) || ticketRow(d, id).Int("version") != 2 {
		t.Fatal("second call not counted")
	}
}

func TestPassAndCancel(t *testing.T) {
	d := newDB(t)
	passed := take(t, d, "small")["id"].(int64)
	cancelled := take(t, d, "small")["id"].(int64)
	if MarkPass(d, passed)["state"] != "passed" || Cancel(d, cancelled)["state"] != "cancelled" {
		t.Fatal("states")
	}
	rows := ListWaiting(d, nil, nil)
	if len(rows) != 1 || rows[0]["id"] != passed || rows[0]["state"] != "passed" {
		t.Fatalf("rows %v", rows)
	}
	if n := d.QueryOne("SELECT COUNT(*) c FROM audit_log").Int("c"); n != 2 {
		t.Fatalf("%d audit rows", n)
	}
}

func TestRequeueKeepsTheNumber(t *testing.T) {
	d := newDB(t)
	tk := take(t, d, "small")
	id := tk["id"].(int64)
	Call(d, id)
	MarkPass(d, id)
	if Requeue(d, id)["state"] != "waiting" {
		t.Fatal("not waiting")
	}
	row := ticketRow(d, id)
	if row.Get("number") != tk["number"] || !row.IsNull("called_at") || row.Int("call_count") != 1 {
		t.Fatalf("row %v", row.Map())
	}
}

func TestRequeueOnlyPassed(t *testing.T) {
	d := newDB(t)
	id := take(t, d, "small")["id"].(int64)
	mustFail(t, "requeue a waiting ticket", func() { Requeue(d, id) })
}

func TestSummaryCounts(t *testing.T) {
	d := newDB(t)
	take(t, d, "small")
	called := take(t, d, "small")["id"].(int64)
	take(t, d, "medium")
	Call(d, called)
	sum := Summary(d, nil)
	groups := map[string]map[string]any{}
	for _, g := range sum["groups"].([]any) {
		m := g.(map[string]any)
		groups[m["size_group"].(string)] = m
	}
	if groups["small"]["waiting"] != int64(1) || groups["small"]["called"] != int64(1) ||
		groups["medium"]["waiting"] != int64(1) || groups["large"]["waiting"] != int64(0) ||
		sum["total_waiting"] != int64(2) {
		t.Fatalf("summary %v", sum)
	}
}

func TestBusinessDaysAreSeparate(t *testing.T) {
	d := newDB(t)
	for _, day := range []string{"2026-01-01", "2026-01-02"} {
		day := day
		first := TakeNumber(d, "small", nil, &day)
		second := TakeNumber(d, "small", nil, &day)
		if first["number"] != int64(1) || second["number"] != int64(2) || second["waiting_ahead"] != int64(1) {
			t.Fatalf("day %s: %v %v", day, first, second)
		}
	}
	d1, d2 := "2026-01-01", "2026-01-02"
	if n := len(ListWaiting(d, nil, &d1)); n != 2 {
		t.Fatalf("%d on day 1", n)
	}
	if Summary(d, &d2)["total_waiting"] != int64(2) {
		t.Fatal("day 2 total")
	}
}

func TestCalledAndPassedDoNotCountAsAhead(t *testing.T) {
	d := newDB(t)
	first := take(t, d, "small")["id"].(int64)
	second := take(t, d, "small")["id"].(int64)
	Call(d, first)
	MarkPass(d, second)
	if take(t, d, "small")["waiting_ahead"] != int64(0) {
		t.Fatal("called and passed tickets counted as ahead")
	}
	Requeue(d, second)
	g := "small"
	rows := ListWaiting(d, &g, nil)
	if rows[len(rows)-1]["position"] != int64(1) {
		t.Fatalf("rows %v", rows)
	}
}

func TestBadInputDoesNotUseUpNumbers(t *testing.T) {
	d := newDB(t)
	for _, size := range []int64{-1, 1000} {
		size := size
		mustFail(t, "party size", func() { TakeNumber(d, "small", &size, nil) })
	}
	mustFail(t, "unknown group", func() { take(t, d, "unknown") })
	bad := "unknown"
	mustFail(t, "unknown group in list", func() { ListWaiting(d, &bad, nil) })
	for _, day := range []string{"2026-02-30", "20260101", "bad"} {
		day := day
		mustFail(t, "day "+day, func() { TakeNumber(d, "small", nil, &day) })
	}
	if take(t, d, "small")["number"] != int64(1) {
		t.Fatal("a refused request used up a number")
	}
}

func TestCancelledTicketRefusesEveryTransition(t *testing.T) {
	d := newDB(t)
	id := take(t, d, "small")["id"].(int64)
	Cancel(d, id)
	for name, fn := range map[string]func(){
		"call": func() { Call(d, id) }, "pass": func() { MarkPass(d, id) },
		"requeue": func() { Requeue(d, id) }, "cancel": func() { Cancel(d, id) },
	} {
		mustFail(t, name, fn)
	}
	if ticketRow(d, id).Int("version") != 1 {
		t.Fatal("a refused change bumped the version")
	}
}

func TestMissingTicket(t *testing.T) {
	d := newDB(t)
	for name, fn := range map[string]func(){
		"call": func() { Call(d, 1234) }, "pass": func() { MarkPass(d, 1234) },
		"requeue": func() { Requeue(d, 1234) }, "cancel": func() { Cancel(d, 1234) },
	} {
		if e := mustFail(t, name, fn); e.Kind != perr.KindNotFound {
			t.Errorf("%s: kind %s", name, e.Kind)
		}
	}
}

func TestFailedOuterTransactionRollsBackTheNumber(t *testing.T) {
	d := newDB(t)
	func() {
		defer func() { _ = recover() }()
		d.Tx(func(tx *db.Tx) {
			take(t, d, "small")
			panic("abort")
		})
	}()
	if take(t, d, "small")["number"] != int64(1) || len(ListWaiting(d, nil, nil)) != 1 {
		t.Fatal("the aborted number survived")
	}
}

func TestCaughtNestedFailureRollsBackOnlyTheInnerWork(t *testing.T) {
	d := newDB(t)
	var first, second map[string]any
	d.Tx(func(tx *db.Tx) {
		first = take(t, d, "small")
		func() {
			defer func() { _ = recover() }()
			d.Tx(func(tx *db.Tx) {
				take(t, d, "small")
				panic("abort inner")
			})
		}()
		second = take(t, d, "small")
	})
	if first["number"] != int64(1) || second["number"] != int64(2) || len(ListWaiting(d, nil, nil)) != 2 {
		t.Fatalf("%v %v", first, second)
	}
}

func TestConcurrentTakersGetUniqueNumbers(t *testing.T) {
	d := newDB(t)
	var mu sync.Mutex
	var wg sync.WaitGroup
	var got []int
	for i := 0; i < 80; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := int(take(t, d, "small")["number"].(int64))
			mu.Lock()
			got = append(got, n)
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Ints(got)
	for i, n := range got {
		if n != i+1 {
			t.Fatalf("numbers are not 1..80 without gaps: %v", got)
		}
	}
	if !d.IntegrityOK() {
		t.Fatal("integrity check failed")
	}
}

func TestTwoConnectionsShareTheCounter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.sqlite")
	first, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	first.InitSchema()
	second, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var mu sync.Mutex
	var wg sync.WaitGroup
	var got []int
	for i := 0; i < 40; i++ {
		d := first
		if i%2 == 0 {
			d = second
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := int(TakeNumber(d, "small", nil, nil)["number"].(int64))
			mu.Lock()
			got = append(got, n)
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Ints(got)
	for i, n := range got {
		if n != i+1 {
			t.Fatalf("numbers are not 1..40 without gaps: %v", got)
		}
	}
	if n := TakeNumber(first, "small", nil, nil)["number"]; n != int64(41) {
		t.Fatalf("next number %v", n)
	}
}

func TestSchemaHasNoCustomerOrStaffTables(t *testing.T) {
	d := newDB(t)
	for _, name := range []string{"staff", "customers", "orders", "payments", "queue_ticket_items"} {
		if d.QueryOne("SELECT name FROM sqlite_master WHERE type='table' AND name=?", name) != nil {
			t.Errorf("table %s exists", name)
		}
	}
}
