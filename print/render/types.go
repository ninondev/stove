// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

// Package render turns planned kitchen tickets into ESC/POS bytes.
package render

// Context is what a ticket needs besides its lines. Use NewContext for the
// defaults (OrderTypeLabel "堂食", Brand "Stove Demo Kitchen").
type Context struct {
	TableLabel     string
	PartySize      *int64
	Operator       *string
	PrintTimeMs    *int64
	OpenTimeMs     *int64
	SettleTimeMs   *int64
	DailySeq       *string // 5-digit 流水号
	OrderNo        *string
	OrderTypeLabel string
	Brand          string
	StoreName      *string // nil -> config.ShopName()
	SourceLabel    *string
	SerialNo       int64
	TurnNo         *int64
}

func NewContext() Context { return Context{OrderTypeLabel: "堂食", Brand: "Stove Demo Kitchen"} }
