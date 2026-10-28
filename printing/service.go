// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

// Package printing turns dishes into durable print jobs and sends them.
//
// Planning runs in one transaction: dishes are resolved from the menu
// tables, routed to stations, rendered as ESC/POS bytes and written to
// print_jobs. A separate worker (see Manager) sends the jobs, one station at
// a time and in order, and retries failures.
package printing

import (
	"fmt"
	"sort"

	"github.com/ninondev/stove/db"
	"github.com/ninondev/stove/internal/pyfmt"
	"github.com/ninondev/stove/perr"
	"github.com/ninondev/stove/print/escpos"
	"github.com/ninondev/stove/print/render"
	"github.com/ninondev/stove/print/routing"
)

// EnqueueOpts are the optional arguments of EnqueueTickets.
type EnqueueOpts struct {
	OrderID      *int64
	DedupePrefix *string
	IsReprint    bool
}

// PayloadOpts are the optional arguments of EnqueuePayload.
type PayloadOpts struct {
	OrderID      *int64
	DedupeKey    *string
	IsReprint    bool
	ForcePending bool
}

// ItemRequest is a requested line to route. Combos are expanded by
// ResolveItems.
type ItemRequest struct {
	DishID     int64
	Qty        int64
	SpecID     *int64
	Notes      *string
	Addons     []routing.Addon
	CreatedSeq int64
	RetReason  *string
}

// NewItemRequest asks for one portion of a dish.
func NewItemRequest(dishID int64) ItemRequest { return ItemRequest{DishID: dishID, Qty: 1} }

// PlanOpts are the optional arguments of PlanAndEnqueue.
type PlanOpts struct {
	OrderID      *int64
	WholeNote    *string
	OrigTable    *string
	TargetTable  *string
	DedupePrefix *string
}

// ticketCategory names the print policy class a ticket type falls under.
func ticketCategory(ticketType string) string {
	switch ticketType {
	case "合台单":
		return "merge"
	case "移菜单":
		return "move"
	}
	return "kitchen"
}

// initialState maps the policy of a ticket type to the first job state:
// off gives "" (no job), manual gives "hold", auto gives "pending".
func initialState(q db.Q, ticketType string) string {
	switch GetPolicy(q)[ticketCategory(ticketType)] {
	case PolicyOff:
		return ""
	case PolicyManual:
		return "hold"
	}
	return "pending"
}

// --- station loading ---------------------------------------------------------

// LoadStations reads every station by code, in table order. A station's
// ticket footer is its own name.
func LoadStations(q db.Q) *routing.Stations {
	out := routing.NewStations()
	rows := q.Query("SELECT * FROM stations")
	for i := range rows {
		r := &rows[i]
		agg := routing.Aggregation(r.Str("aggregation"))
		if agg != routing.Aggregate && agg != routing.PerDish {
			panic(perr.Validation(fmt.Sprintf("Station %q has an unknown aggregation %q.", r.Str("code"), r.Str("aggregation"))))
		}
		out.Put(routing.StationDef{
			Code:            r.Str("code"),
			Name:            r.Str("name"),
			Aggregation:     agg,
			Footer:          r.Str("name"),
			PrintsBills:     r.Truthy("prints_bills"),
			MoveTableTicket: r.Truthy("move_table_ticket"),
			Enabled:         r.Truthy("enabled"),
		})
	}
	return out
}

// StationIDsByCode maps station codes to ids.
func StationIDsByCode(q db.Q) map[string]int64 {
	out := map[string]int64{}
	rows := q.Query("SELECT id, code FROM stations")
	for i := range rows {
		out[rows[i].Str("code")] = rows[i].Int("id")
	}
	return out
}

// --- item resolution -----------------------------------------------------------

func bindingsFor(q db.Q, dishID int64) []routing.ItemBinding {
	rows := q.Query(`SELECT s.code AS code, b.print_rule AS pr, b.aggregation AS agg, b.copies AS copies
           FROM dish_station_bindings b JOIN stations s ON s.id = b.station_id
           WHERE b.dish_id=? AND s.enabled=1`, dishID)
	out := make([]routing.ItemBinding, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		var agg *routing.Aggregation
		if !r.IsNull("agg") {
			a := routing.Aggregation(r.Str("agg"))
			agg = &a
		}
		copies := int64(1)
		if r.Truthy("copies") {
			copies = r.Int("copies")
		}
		out = append(out, routing.ItemBinding{StationCode: r.Str("code"), PrintRule: r.Str("pr"),
			Aggregation: agg, Copies: copies})
	}
	return out
}

func dish(q db.Q, dishID int64) *db.Row {
	return q.QueryOne("SELECT * FROM dishes WHERE id=?", dishID)
}

func specName(q db.Q, specID *int64) *string {
	if specID == nil || *specID == 0 {
		return nil
	}
	r := q.QueryOne("SELECT name FROM dish_specs WHERE id=?", *specID)
	if r == nil {
		return nil
	}
	return r.StrP("name")
}

// rowStatus is the row's status, nil if it has no such column.
func rowStatus(r *db.Row) *string {
	if !r.Has("status") {
		return nil
	}
	return r.StrP("status")
}

// BuildPrintItemsFromRows makes print items from saved order_items rows.
// Combo parents and "special" dishes never print. With asRetreat the item
// carries its returned quantity and reason, for a return ticket.
func BuildPrintItemsFromRows(q db.Q, rows []db.Row, asRetreat bool) []routing.PrintItem {
	items := []routing.PrintItem{}
	for i := range rows {
		r := &rows[i]
		d := dish(q, r.Int("dish_id"))
		if d == nil {
			continue
		}
		if d.Truthy("is_combo") {
			continue
		}
		if d.Str("temp") == "special" {
			continue
		}
		var comboParent *string
		var comboUID *int64
		var comboQty int64
		if r.Truthy("combo_item_id") {
			parent := q.QueryOne("SELECT name_snapshot, qty, ret_qty FROM order_items WHERE id=?", r.Get("combo_item_id"))
			if parent != nil {
				comboParent = parent.StrP("name_snapshot")
				uid := r.Int("combo_item_id")
				comboUID = &uid
				comboQty = pyfmt.MaxI(0, parent.Int("qty")-parent.Int("ret_qty"))
			}
		}
		addonRows := q.Query("SELECT name_snapshot, qty FROM order_item_addons WHERE order_item_id=?", r.Get("id"))
		addons := make([]routing.Addon, 0, len(addonRows))
		for j := range addonRows {
			addons = append(addons, routing.Addon{Name: pyfmt.Str(addonRows[j].Get("name_snapshot")),
				Qty: addonRows[j].Int("qty")})
		}
		var qty int64
		if asRetreat {
			qty = r.Int("ret_qty")
		} else {
			qty = r.Int("qty") - r.Int("ret_qty")
		}
		if qty <= 0 && !asRetreat {
			continue
		}
		var retReason *string
		if asRetreat {
			retReason = r.StrP("ret_reason")
		}
		st := rowStatus(r)
		items = append(items, routing.PrintItem{
			Name:        r.Str("name_snapshot"),
			Qty:         qty,
			Bindings:    bindingsFor(q, r.Int("dish_id")),
			Unit:        d.Str("unit"),
			SpecName:    r.StrP("spec_name"),
			Notes:       r.StrP("notes"),
			Addons:      addons,
			CreatedSeq:  r.Int("seq_in_order"),
			ComboParent: comboParent,
			ComboUID:    comboUID,
			ComboQty:    comboQty,
			RetReason:   retReason,
			Held:        !asRetreat && st != nil && *st == "held",
		})
	}
	return items
}

// ResolveItems turns requests into print items, expanding combos into their
// components.
func ResolveItems(q db.Q, reqs []ItemRequest) []routing.PrintItem {
	items := []routing.PrintItem{}
	var seq int64
	var comboSeq int64 // synthetic combo instance ids (negative)
	for _, req := range reqs {
		d := dish(q, req.DishID)
		if d == nil {
			continue
		}
		if req.CreatedSeq != 0 {
			seq = req.CreatedSeq
		} else {
			seq = seq + 1
		}
		if d.Truthy("is_combo") {
			comps := q.Query("SELECT * FROM combo_components WHERE combo_dish_id=? ORDER BY sort, id", req.DishID)
			comboSeq--
			for i := range comps {
				c := &comps[i]
				cd := dish(q, c.Int("component_dish_id"))
				if cd == nil {
					continue
				}
				seq++
				cq := int64(1)
				if c.Truthy("qty") {
					cq = c.Int("qty")
				}
				parent := d.Str("name")
				uid := comboSeq
				items = append(items, routing.PrintItem{
					Name:        cd.Str("name"),
					Qty:         req.Qty * cq,
					Bindings:    bindingsFor(q, cd.Int("id")),
					Unit:        cd.Str("unit"),
					SpecName:    specName(q, c.IntP("spec_id")),
					Addons:      []routing.Addon{},
					CreatedSeq:  seq,
					ComboParent: &parent,
					ComboUID:    &uid,
					ComboQty:    req.Qty,
				})
			}
		} else {
			addons := append([]routing.Addon{}, req.Addons...)
			items = append(items, routing.PrintItem{
				Name:       d.Str("name"),
				Qty:        req.Qty,
				Bindings:   bindingsFor(q, req.DishID),
				Unit:       d.Str("unit"),
				SpecName:   specName(q, req.SpecID),
				Notes:      req.Notes,
				Addons:     addons,
				CreatedSeq: seq,
				RetReason:  req.RetReason,
			})
		}
	}
	return items
}

// --- plan + enqueue --------------------------------------------------------------

func maxEnqueueSeq(q db.Q) int64 {
	return q.QueryOne("SELECT COALESCE(MAX(enqueue_seq),0) AS m FROM print_jobs").Int("m")
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// EnqueueTickets renders each planned ticket and inserts one durable
// print_jobs row per copy. It returns the new job ids. A ticket whose policy
// is "off" makes no job and uses no serial number; a "manual" ticket is held
// without a serial until ReleaseHolds.
func EnqueueTickets(q db.Q, tickets []routing.PlannedTicket, ctx render.Context,
	stIDs map[string]int64, o EnqueueOpts) []int64 {
	ts := db.NowMs()
	base := maxEnqueueSeq(q)
	jobIDs := []int64{}
	var seqN int64
	ordered := append([]routing.PlannedTicket(nil), tickets...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })
	for _, t := range ordered {
		stID, ok := stIDs[t.StationCode]
		if !ok {
			continue
		}
		copies := pyfmt.MaxI(1, t.Copies)
		for copyI := int64(0); copyI < copies; copyI++ {
			state := "pending"
			if !o.IsReprint {
				state = initialState(q, t.TicketType)
			}
			if state == "" {
				continue
			}
			seqN++
			var serial int64
			lineCtx := ctx
			if state != "hold" {
				serial = NextSerial(q, stID)
			}
			lineCtx.SerialNo = serial
			payload := render.StationTicket(t, lineCtx)
			var dedupe *string
			if o.DedupePrefix != nil && *o.DedupePrefix != "" {
				k := fmt.Sprintf("%s:%s:%s:%d:%d", *o.DedupePrefix, t.StationCode, t.TicketType, t.Seq, copyI)
				dedupe = &k
			}
			res := q.Exec(`INSERT INTO print_jobs
                   (station_id, order_id, ticket_type, serial_no, payload, state,
                    dedupe_key, enqueue_seq, created_at, next_attempt_at, is_reprint)
                   VALUES(?,?,?,?,?, ?, ?, ?, ?, 0, ?)`,
				stID, o.OrderID, t.TicketType, serial, payload, state, dedupe,
				base+seqN, ts, boolInt(o.IsReprint))
			jobIDs = append(jobIDs, res.LastInsertID)
		}
	}
	return jobIDs
}

// ReleaseHolds moves held jobs to the queue, giving each its serial number
// now so printed serials never skip. A nil jobID releases all of them.
func ReleaseHolds(d *db.DB, jobID *int64) int64 {
	var n int64
	d.Tx(func(tx *db.Tx) {
		sqlText := "SELECT id, station_id, payload, serial_no FROM print_jobs WHERE state='hold'"
		var args []any
		if jobID != nil {
			sqlText += " AND id=?"
			args = append(args, *jobID)
		}
		sqlText += " ORDER BY enqueue_seq"
		rows := tx.Query(sqlText, args...)
		for i := range rows {
			r := &rows[i]
			if r.Truthy("serial_no") {
				tx.Exec("UPDATE print_jobs SET state='pending', next_attempt_at=0 WHERE id=?", r.Get("id"))
				continue
			}
			serial := NextSerial(tx, r.Int("station_id"))
			payload := escpos.InjectSerial(r.Bytes("payload"), serial)
			tx.Exec("UPDATE print_jobs SET state='pending', next_attempt_at=0, serial_no=?, payload=? WHERE id=?",
				serial, payload, r.Get("id"))
		}
		n = int64(len(rows))
	})
	return n
}

// PlanAndEnqueue resolves the requests, fans them out to stations, renders
// the tickets and enqueues them, all in one transaction.
func PlanAndEnqueue(d *db.DB, op routing.Op, reqs []ItemRequest, ctx render.Context, o PlanOpts) []int64 {
	var ids []int64
	d.Tx(func(tx *db.Tx) {
		stations := LoadStations(tx)
		stIDs := StationIDsByCode(tx)
		items := ResolveItems(tx, reqs)
		tickets := routing.FanOut(op, items, stations, routing.FanOutOpts{
			WholeNote: o.WholeNote, OrigTable: o.OrigTable, TargetTable: o.TargetTable})
		ids = EnqueueTickets(tx, tickets, ctx, stIDs, EnqueueOpts{OrderID: o.OrderID, DedupePrefix: o.DedupePrefix})
	})
	return ids
}

// EnqueuePayload queues bytes that are already rendered, straight to a
// station. It returns the job id, or nil when there is no such station or
// the policy is off. ForcePending skips the policy.
func EnqueuePayload(q db.Q, stationCode, ticketType string, payload []byte, o PayloadOpts) *int64 {
	st := q.QueryOne("SELECT id FROM stations WHERE code=?", stationCode)
	if st == nil {
		return nil
	}
	state := "pending"
	if !(o.IsReprint || o.ForcePending) {
		state = initialState(q, ticketType)
	}
	if state == "" {
		return nil
	}
	stID := st.Int("id")
	payload = append([]byte{}, payload...)
	serial := NextSerial(q, stID)
	res := q.Exec(`INSERT INTO print_jobs
           (station_id, order_id, ticket_type, serial_no, payload, state,
            dedupe_key, enqueue_seq, created_at, next_attempt_at, is_reprint)
           VALUES(?,?,?,?,?, ?, ?, ?, ?, 0, ?)`,
		stID, o.OrderID, ticketType, serial, payload, state, o.DedupeKey,
		maxEnqueueSeq(q)+1, db.NowMs(), boolInt(o.IsReprint))
	id := res.LastInsertID
	return &id
}

// ReprintJob copies a job's payload into a new pending job with a fresh
// serial number. It returns nil when there is no such job.
func ReprintJob(d *db.DB, jobID int64) *int64 {
	var out *int64
	d.Tx(func(tx *db.Tx) {
		src := tx.QueryOne("SELECT * FROM print_jobs WHERE id=?", jobID)
		if src == nil {
			return
		}
		if len(src.Bytes("payload")) == 0 {
			panic(perr.Validation("This ticket's content was cleared and cannot be printed again."))
		}
		stID := src.Int("station_id")
		serial := NextSerial(tx, stID)
		payload := append([]byte(nil), src.Bytes("payload")...)
		// A reprint never opens the cash drawer.
		payload, _ = escpos.StripTrailingKick(payload)
		if !src.Truthy("serial_no") {
			payload = escpos.InjectSerial(payload, serial)
		}
		res := tx.Exec(`INSERT INTO print_jobs
               (station_id, order_id, ticket_type, serial_no, payload, state,
                enqueue_seq, created_at, next_attempt_at, is_reprint)
               VALUES(?,?,?,?,?, 'pending', ?, ?, 0, 1)`,
			stID, src.Get("order_id"), src.Get("ticket_type"), serial,
			payload, maxEnqueueSeq(tx)+1, db.NowMs())
		newID := res.LastInsertID
		tx.Exec("INSERT INTO audit_log(kind, ref_table, ref_id, detail, created_at) "+
			"VALUES('reprint','print_jobs',?,?,?)",
			jobID, fmt.Sprintf("reprint of job %d as job %d", jobID, newID), db.NowMs())
		out = &newID
	})
	return out
}
