// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package render_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/ninondev/stove/print/render"
	"github.com/ninondev/stove/print/routing"
)

// kitchen_cases.json was recorded from the earlier Python version of this
// kit: made-up dishes and stations go in, planned tickets and ESC/POS bytes
// come out. The Go code has to give the same tickets and the same bytes.

type caseFile struct {
	Name        string `json:"name"`
	Op          string `json:"op"`
	Items       []jItem
	Stations    []jStation
	WholeNote   *string `json:"whole_note"`
	OrigTable   *string `json:"orig_table"`
	TargetTable *string `json:"target_table"`
	Ctx         string  `json:"ctx"`
	Tickets     []map[string]any
	Payloads    []string
}

type jBinding struct {
	StationCode string  `json:"station_code"`
	PrintRule   string  `json:"print_rule"`
	Aggregation *string `json:"aggregation"`
	Copies      int64   `json:"copies"`
}

type jItem struct {
	Name        string          `json:"name"`
	Qty         int64           `json:"qty"`
	Bindings    []jBinding      `json:"bindings"`
	Unit        string          `json:"unit"`
	SpecName    *string         `json:"spec_name"`
	Notes       *string         `json:"notes"`
	Addons      [][]interface{} `json:"addons"`
	CreatedSeq  int64           `json:"created_seq"`
	ComboParent *string         `json:"combo_parent"`
	ComboUID    *int64          `json:"combo_uid"`
	ComboQty    int64           `json:"combo_qty"`
	RetReason   *string         `json:"ret_reason"`
	Held        bool            `json:"held"`
}

type jStation struct {
	Code            string `json:"code"`
	Name            string `json:"name"`
	Aggregation     string `json:"aggregation"`
	Footer          string `json:"footer"`
	PrintsBills     bool   `json:"prints_bills"`
	MoveTableTicket bool   `json:"move_table_ticket"`
	Enabled         bool   `json:"enabled"`
}

func s(v string) *string { return &v }
func i(v int64) *int64   { return &v }

func ctxFor(name string) render.Context {
	c := render.NewContext()
	switch name {
	case "plain":
		c.TableLabel, c.PartySize, c.Operator = "A1", i(2), s("Demo Operator")
		c.PrintTimeMs, c.SerialNo = i(1790879400000), 63
	case "full":
		c.TableLabel, c.PartySize, c.Operator = "B5", i(6), s("Demo Operator")
		c.PrintTimeMs, c.OpenTimeMs = i(1790879400000), i(1790878000000)
		c.DailySeq, c.OrderNo = s("00012"), s("1126000000000000000063")
		c.Brand, c.StoreName, c.SerialNo, c.TurnNo = "Stove Demo", s("Stove Demo Kitchen"), 7, i(12)
	case "bare":
		c.TableLabel, c.PrintTimeMs = "外卖1", i(0)
	}
	return c
}

func addons(in [][]interface{}) []routing.Addon {
	out := []routing.Addon{}
	for _, a := range in {
		out = append(out, routing.Addon{Name: a[0].(string), Qty: int64(a[1].(float64))})
	}
	return out
}

func (c caseFile) stations() *routing.Stations {
	st := routing.NewStations()
	for _, d := range c.Stations {
		st.Put(routing.StationDef{Code: d.Code, Name: d.Name, Aggregation: routing.Aggregation(d.Aggregation),
			Footer: d.Footer, PrintsBills: d.PrintsBills, MoveTableTicket: d.MoveTableTicket, Enabled: d.Enabled})
	}
	return st
}

func (c caseFile) items() []routing.PrintItem {
	out := []routing.PrintItem{}
	for _, it := range c.Items {
		var bs []routing.ItemBinding
		for _, b := range it.Bindings {
			var agg *routing.Aggregation
			if b.Aggregation != nil {
				a := routing.Aggregation(*b.Aggregation)
				agg = &a
			}
			bs = append(bs, routing.ItemBinding{StationCode: b.StationCode, PrintRule: b.PrintRule,
				Aggregation: agg, Copies: b.Copies})
		}
		out = append(out, routing.PrintItem{Name: it.Name, Qty: it.Qty, Bindings: bs, Unit: it.Unit,
			SpecName: it.SpecName, Notes: it.Notes, Addons: addons(it.Addons), CreatedSeq: it.CreatedSeq,
			ComboParent: it.ComboParent, ComboUID: it.ComboUID, ComboQty: it.ComboQty,
			RetReason: it.RetReason, Held: it.Held})
	}
	return out
}

// plain turns a planned ticket into the JSON shape of the recorded tickets,
// without the keys the Go types keep elsewhere (extra, per-line reason).
func plain(t routing.PlannedTicket) map[string]any {
	lines := []any{}
	for _, l := range t.Lines {
		ad := []any{}
		for _, a := range l.Addons {
			ad = append(ad, []any{a.Name, a.Qty})
		}
		lines = append(lines, map[string]any{
			"name": l.Name, "qty": l.Qty, "unit": l.Unit, "spec_name": l.SpecName, "notes": l.Notes,
			"addons": ad, "ret": l.Ret, "ret_reason": l.RetReason, "transfer": l.Transfer, "held": l.Held,
			"combo_parent": l.ComboParent, "combo_uid": l.ComboUID, "is_combo_header": l.IsComboHeader,
		})
	}
	return map[string]any{
		"station_code": t.StationCode, "footer": t.Footer, "ticket_type": t.TicketType, "marker": t.Marker,
		"lines": lines, "whole_note": t.WholeNote, "seq": t.Seq, "copies": t.Copies,
	}
}

// normal round-trips a value through JSON so both sides compare as parsed JSON.
func normal(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestKitchenTicketsMatchRecordedCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/kitchen_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []caseFile
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			got := routing.FanOut(routing.Op(c.Op), c.items(), c.stations(),
				routing.FanOutOpts{WholeNote: c.WholeNote, OrigTable: c.OrigTable, TargetTable: c.TargetTable})
			if len(got) != len(c.Tickets) {
				t.Fatalf("%d tickets, recorded %d", len(got), len(c.Tickets))
			}
			for k, tk := range got {
				want := map[string]any{}
				for key, v := range c.Tickets[k] {
					if key != "extra" {
						want[key] = v
					}
				}
				if g := normal(t, plain(tk)); !reflect.DeepEqual(g, normal(t, want)) {
					t.Errorf("ticket %d:\n got  %v\n want %v", k, g, want)
				}
				if hex.EncodeToString(render.StationTicket(tk, ctxFor(c.Ctx))) != c.Payloads[k] {
					t.Errorf("ticket %d: ESC/POS bytes differ from the recorded ones", k)
				}
			}
		})
	}
}

func TestStationTicketNeverKicksTheDrawer(t *testing.T) {
	lines := []routing.TicketLine{{Name: "宫保鸡丁", Qty: 1, Unit: "份"}}
	tk := routing.PlannedTicket{StationCode: "expo", Footer: "传菜", TicketType: "厨总单", Lines: lines}
	b := render.StationTicket(tk, ctxFor("plain"))
	for _, kick := range []string{"\x1bp\x00\x19\xfa", "\x1bp\x01\x19\xfa"} {
		if contains(b, []byte(kick)) {
			t.Errorf("station ticket contains a drawer kick")
		}
	}
}

func contains(b, sub []byte) bool {
	for k := 0; k+len(sub) <= len(b); k++ {
		if string(b[k:k+len(sub)]) == string(sub) {
			return true
		}
	}
	return false
}
