// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

// Package queue is a take-a-number waiting list. Numbers run per business
// day and size group, starting at 1. The counter and the ticket are saved in
// one transaction. A ticket can be called, passed, put back, or cancelled.
package queue

import (
	"fmt"
	"time"

	"github.com/ninondev/stove/db"
	"github.com/ninondev/stove/internal/config"
	"github.com/ninondev/stove/internal/pyfmt"
	"github.com/ninondev/stove/perr"
)

// WaitPerTableMin is the fixed wait estimate for each party ahead in the same
// group. It is an estimate, not a forecast.
const WaitPerTableMin = 10

// SizeGroups are the party-size groups, in display order: value and label.
var SizeGroups = [][2]string{
	{"small", "Small (1-2 people)"},
	{"medium", "Medium (3-4 people)"},
	{"large", "Large (5 or more people)"},
}

var groupLabels = map[string]string{
	"small": "Small (1-2 people)", "medium": "Medium (3-4 people)", "large": "Large (5 or more people)",
}

// TicketLabel is the short label of a ticket, such as "small 8".
func TicketLabel(t *db.Row) string {
	return fmt.Sprintf("%s %s", t.Str("size_group"), pyfmt.Str(t.Get("number")))
}

// --- helpers --------------------------------------------------------------------

// ensureDay returns the business day row, creating it on first use. A nil
// bizDate means today's local date. Pass a date in YYYY-MM-DD form to keep a
// shift on one day after midnight.
func ensureDay(q db.Q, bizDate *string) *db.Row {
	var day string
	if bizDate == nil {
		day = config.Now().Format("2006-01-02")
	} else {
		day = *bizDate
		parsed, err := time.Parse("2006-01-02", day)
		if err != nil || parsed.Format("2006-01-02") != day {
			panic(perr.Validation("Business day must be a date in YYYY-MM-DD form."))
		}
	}
	q.Exec("INSERT OR IGNORE INTO business_days(biz_date, opened_at) VALUES(?,?)", day, db.NowMs())
	return q.QueryOne("SELECT * FROM business_days WHERE biz_date=?", day)
}

func ticket(q db.Q, ticketID int64) *db.Row {
	r := q.QueryOne("SELECT * FROM queue_tickets WHERE id=?", ticketID)
	if r == nil {
		panic(perr.NotFound("Queue ticket was not found."))
	}
	return r
}

func allocNumber(q db.Q, dayID int64, sizeGroup string) int64 {
	q.Exec("INSERT OR IGNORE INTO queue_counters(business_day_id, size_group, next_number) VALUES(?,?,1)",
		dayID, sizeGroup)
	number := q.QueryOne("SELECT next_number FROM queue_counters WHERE business_day_id=? AND size_group=?",
		dayID, sizeGroup).Int("next_number")
	q.Exec("UPDATE queue_counters SET next_number = next_number + 1 WHERE business_day_id=? AND size_group=?",
		dayID, sizeGroup)
	return number
}

func waitingAhead(q db.Q, dayID int64, sizeGroup string, number any) int64 {
	return q.QueryOne("SELECT COUNT(*) c FROM queue_tickets "+
		"WHERE business_day_id=? AND size_group=? AND state='waiting' AND number<?",
		dayID, sizeGroup, number).Int("c")
}

func bump(q db.Q, ticketID int64) {
	q.Exec("UPDATE queue_tickets SET version = version + 1 WHERE id=?", ticketID)
}

func audit(q db.Q, kind string, refID int64, detail string) {
	q.Exec("INSERT INTO audit_log(kind, ref_table, ref_id, detail, created_at) "+
		"VALUES(?, 'queue_tickets', ?, ?, ?)", kind, refID, detail, db.NowMs())
}

func orZero(v any) any {
	if !pyfmt.Truthy(v) {
		return int64(0)
	}
	return v
}

// --- take a number --------------------------------------------------------------

// TakeNumber gives the next number of a size group. partySize may be nil; if
// given it must be 0 to 999. bizDate nil means today.
func TakeNumber(d *db.DB, sizeGroup string, partySize *int64, bizDate *string) (out map[string]any) {
	if _, ok := groupLabels[sizeGroup]; !ok {
		panic(perr.Validation("Choose small, medium, or large."))
	}
	if partySize != nil && (*partySize < 0 || *partySize > 999) {
		panic(perr.Validation("Party size must be between 0 and 999."))
	}
	d.Tx(func(tx *db.Tx) {
		dayID := ensureDay(tx, bizDate).Int("id")
		number := allocNumber(tx, dayID, sizeGroup)
		res := tx.Exec("INSERT INTO queue_tickets(business_day_id, size_group, number, party_size, "+
			"state, created_at) VALUES(?,?,?,?, 'waiting', ?)",
			dayID, sizeGroup, number, partySize, db.NowMs())
		ahead := waitingAhead(tx, dayID, sizeGroup, number)
		out = map[string]any{
			"id":            res.LastInsertID,
			"size_group":    sizeGroup,
			"number":        number,
			"waiting_ahead": ahead,
			"est_wait_min":  ahead * WaitPerTableMin,
		}
	})
	return out
}

// --- read -----------------------------------------------------------------------

// ListWaiting returns the day's waiting, called and passed tickets with
// their position and estimated wait. A nil sizeGroup lists every group.
func ListWaiting(d *db.DB, sizeGroup *string, bizDate *string) []map[string]any {
	out := []map[string]any{}
	d.Tx(func(tx *db.Tx) {
		dayID := ensureDay(tx, bizDate).Int("id")
		sql := "SELECT * FROM queue_tickets " +
			"WHERE business_day_id=? AND state IN ('waiting','called','passed')"
		params := []any{dayID}
		if sizeGroup != nil {
			if _, ok := groupLabels[*sizeGroup]; !ok {
				panic(perr.Validation("Choose small, medium, or large."))
			}
			sql += " AND size_group=?"
			params = append(params, *sizeGroup)
		}
		sql += " ORDER BY size_group, number"
		rows := tx.Query(sql, params...)
		for i := range rows {
			r := &rows[i]
			ahead := waitingAhead(tx, dayID, r.Str("size_group"), r.Get("number"))
			label, ok := groupLabels[r.Str("size_group")]
			var labelV any = label
			if !ok {
				labelV = r.Get("size_group")
			}
			out = append(out, map[string]any{
				"id":           r.Get("id"),
				"size_group":   r.Get("size_group"),
				"label":        labelV,
				"number":       r.Get("number"),
				"party_size":   r.Get("party_size"),
				"state":        r.Get("state"),
				"position":     ahead,
				"est_wait_min": ahead * WaitPerTableMin,
				"created_at":   r.Get("created_at"),
				"called_at":    r.Get("called_at"),
				"call_count":   orZero(r.Get("call_count")),
				"version":      r.Get("version"),
			})
		}
	})
	return out
}

// Summary counts the waiting and called tickets of each group.
func Summary(d *db.DB, bizDate *string) (out map[string]any) {
	d.Tx(func(tx *db.Tx) {
		dayID := ensureDay(tx, bizDate).Int("id")
		rows := tx.Query("SELECT size_group, state, COUNT(*) c FROM queue_tickets "+
			"WHERE business_day_id=? AND state IN ('waiting','called') GROUP BY size_group, state", dayID)
		counts := map[string]map[string]int64{}
		for i := range rows {
			g := rows[i].Str("size_group")
			if counts[g] == nil {
				counts[g] = map[string]int64{}
			}
			counts[g][rows[i].Str("state")] = rows[i].Int("c")
		}
		groups := []any{}
		var totalWaiting int64
		for _, gl := range SizeGroups {
			waiting := counts[gl[0]]["waiting"]
			called := counts[gl[0]]["called"]
			totalWaiting += waiting
			groups = append(groups, map[string]any{
				"size_group": gl[0], "label": gl[1], "waiting": waiting, "called": called,
			})
		}
		out = map[string]any{"groups": groups, "total_waiting": totalWaiting}
	})
	return out
}

// --- state transitions -------------------------------------------------------------

// Call moves a waiting or called ticket to called, counting each call.
func Call(d *db.DB, ticketID int64) (out map[string]any) {
	d.Tx(func(tx *db.Tx) {
		t := ticket(tx, ticketID)
		if st := t.Str("state"); st != "waiting" && st != "called" {
			panic(perr.Validation("This ticket cannot be called in its current state."))
		}
		tx.Exec("UPDATE queue_tickets SET state='called', called_at=?, "+
			"call_count=COALESCE(call_count,0)+1 WHERE id=?", db.NowMs(), ticketID)
		bump(tx, ticketID)
		out = map[string]any{"id": ticketID, "state": "called", "call_count": t.Int("call_count") + 1}
	})
	return out
}

// MarkPass moves a waiting or called ticket to passed (the party missed its call).
func MarkPass(d *db.DB, ticketID int64) (out map[string]any) {
	d.Tx(func(tx *db.Tx) {
		t := ticket(tx, ticketID)
		if st := t.Str("state"); st != "waiting" && st != "called" {
			panic(perr.Validation("This ticket cannot be passed in its current state."))
		}
		tx.Exec("UPDATE queue_tickets SET state='passed' WHERE id=?", ticketID)
		audit(tx, "queue_pass", ticketID, TicketLabel(t)+" passed")
		bump(tx, ticketID)
		out = map[string]any{"id": ticketID, "state": "passed"}
	})
	return out
}

// Requeue moves a passed ticket back to waiting. It keeps its number.
func Requeue(d *db.DB, ticketID int64) (out map[string]any) {
	d.Tx(func(tx *db.Tx) {
		t := ticket(tx, ticketID)
		if t.Str("state") != "passed" {
			panic(perr.Validation("Only a passed ticket can return to waiting."))
		}
		tx.Exec("UPDATE queue_tickets SET state='waiting', called_at=NULL WHERE id=?", ticketID)
		bump(tx, ticketID)
		out = map[string]any{"id": ticketID, "state": "waiting"}
	})
	return out
}

// Cancel moves a waiting, called or passed ticket to cancelled.
func Cancel(d *db.DB, ticketID int64) (out map[string]any) {
	d.Tx(func(tx *db.Tx) {
		t := ticket(tx, ticketID)
		switch t.Str("state") {
		case "waiting", "called", "passed":
		default:
			panic(perr.Validation("This ticket cannot be cancelled in its current state."))
		}
		tx.Exec("UPDATE queue_tickets SET state='cancelled' WHERE id=?", ticketID)
		audit(tx, "queue_cancel", ticketID, TicketLabel(t)+" cancelled")
		bump(tx, ticketID)
		out = map[string]any{"id": ticketID, "state": "cancelled"}
	})
	return out
}
