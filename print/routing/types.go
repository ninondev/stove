// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

// Package routing decides which station prints what.
// The types here are the contract other packages code against; FanOut and
// the planners live in fanout.go.
package routing

import "github.com/ninondev/stove/internal/pyfmt"

// Aggregation mirrors routing.Aggregation.
type Aggregation string

const (
	Aggregate Aggregation = "aggregate" // 多菜一纸 / 厨总单
	PerDish   Aggregation = "per_dish"  // 一菜一纸
)

// StationDef mirrors routing.StationDef. Use NewStationDef for the Python
// defaults (MoveTableTicket and Enabled default to true).
type StationDef struct {
	Code            string
	Name            string
	Aggregation     Aggregation
	Footer          string
	PrintsBills     bool
	MoveTableTicket bool
	Enabled         bool
}

func NewStationDef(code, name string, agg Aggregation, footer string) StationDef {
	return StationDef{Code: code, Name: name, Aggregation: agg, Footer: footer,
		MoveTableTicket: true, Enabled: true}
}

// Stations is dict[str, StationDef]: lookups by code, iteration in
// insertion order (Python dicts keep it, and fan-out order depends on it).
type Stations struct {
	Codes  []string
	ByCode map[string]StationDef
}

func NewStations() *Stations { return &Stations{ByCode: map[string]StationDef{}} }

// Put sets d under d.Code, keeping the first insertion position.
func (s *Stations) Put(d StationDef) {
	if _, ok := s.ByCode[d.Code]; !ok {
		s.Codes = append(s.Codes, d.Code)
	}
	s.ByCode[d.Code] = d
}

func (s *Stations) Get(code string) (StationDef, bool) {
	d, ok := s.ByCode[code]
	return d, ok
}

// ItemBinding mirrors routing.ItemBinding. Aggregation nil = station default.
type ItemBinding struct {
	StationCode string
	PrintRule   string // "total" | "split"
	Aggregation *Aggregation
	Copies      int64
}

// Addon is one (name, qty) tuple.
type Addon struct {
	Name string
	Qty  int64
}

// PrintItem mirrors routing.PrintItem (Unit defaults to "份").
type PrintItem struct {
	Name        string
	Qty         int64
	Bindings    []ItemBinding
	Unit        string
	SpecName    *string
	Notes       *string
	Addons      []Addon
	CreatedSeq  int64
	ComboParent *string
	ComboUID    *int64
	ComboQty    int64
	RetReason   *string
	Held        bool
}

// TicketLine mirrors routing.TicketLine (Unit defaults to "份").
type TicketLine struct {
	Name          string
	Qty           int64
	Unit          string
	SpecName      *string
	Notes         *string
	Addons        []Addon
	Ret           bool
	RetReason     *string
	Transfer      bool
	Held          bool
	ComboParent   *string
	ComboUID      *int64
	IsComboHeader bool
}

// PlannedTicket mirrors routing.PlannedTicket. Extra is the `extra` dict
// (insertion-ordered); callers may Set keys on it (orders sets ret_reason).
type PlannedTicket struct {
	StationCode string
	Footer      string
	TicketType  string
	Marker      *string
	Lines       []TicketLine
	WholeNote   *string
	Seq         int64
	Copies      int64
	Extra       *pyfmt.OMap
}

// Op mirrors routing.Op.
type Op string

const (
	OpNew      Op = "new"
	OpAdd      Op = "add"
	OpRetreat  Op = "retreat"
	OpTransfer Op = "transfer"
	OpUrge     Op = "urge"
	OpCallUp   Op = "call_up"
	OpModify   Op = "modify"
	OpVoid     Op = "void"
	OpMerge    Op = "merge"
	OpMove     Op = "move"
)

// FanOutOpts are fan_out's keyword arguments.
type FanOutOpts struct {
	WholeNote   *string
	OrigTable   *string
	TargetTable *string
	Extra       *pyfmt.OMap
}
