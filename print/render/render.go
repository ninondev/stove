// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package render

// Kitchen tickets as 80mm GB18030 ESC/POS payloads.

import (
	"fmt"
	"strings"

	"github.com/ninondev/stove/internal/config"
	"github.com/ninondev/stove/internal/pyfmt"
	"github.com/ninondev/stove/print/escpos"
	"github.com/ninondev/stove/print/routing"
)

// FmtDt is fmt_dt: "%Y/%m/%d %H:%M:%S" in the store timezone, "" for 0/None.
func FmtDt(ms *int64) string {
	if ms == nil || *ms == 0 {
		return ""
	}
	return pyfmt.Strftime(pyfmt.FromMs(*ms, config.TZ), "%Y/%m/%d %H:%M:%S")
}

func storeName(ctx Context) string {
	if ctx.StoreName != nil && *ctx.StoreName != "" {
		return *ctx.StoreName
	}
	return config.ShopName()
}

// turnSuffix is _turn_suffix: "·当日第N单" when turn_no is set.
func turnSuffix(ctx Context) string {
	if ctx.TurnNo != nil && *ctx.TurnNo != 0 {
		return fmt.Sprintf("·当日第%d单", *ctx.TurnNo)
	}
	return ""
}

func truthyS(s *string) bool { return s != nil && *s != "" }

// brandLine is the 来源品牌 line (for example "Demo-堂食"). The brand is empty only in a counter shop that never
// set one (Settings.GetTextSetting), and then the line is left out.
func brandLine(b *escpos.Builder, ctx Context) {
	if ctx.Brand != "" {
		b.Line(ctx.Brand + "-" + ctx.OrderTypeLabel)
	}
}

// dishName is _dish_name.
func dishName(ln routing.TicketLine) string {
	name := ln.Name
	if truthyS(ln.SpecName) {
		name = name + "（" + *ln.SpecName + "）"
	}
	switch {
	case ln.Ret:
		name = "[退]" + name
	case ln.Transfer:
		name = "[转]" + name
	case ln.Held:
		name = "[等叫]" + name
	}
	return name
}

func qtyUnit(ln routing.TicketLine) string { return fmt.Sprintf("%d/%s", ln.Qty, ln.Unit) }

// emitLines is _emit_lines: dish lines in double height, names bold, combo
// components grouped under their parent.
func emitLines(b *escpos.Builder, lines []routing.TicketLine) {
	hasHeader := false
	for _, ln := range lines {
		if ln.IsComboHeader {
			hasHeader = true
			break
		}
	}
	var current *string
	for _, ln := range lines {
		if ln.IsComboHeader {
			b.Size(1, 2).Bold(true).TwoCol(dishName(ln), qtyUnit(ln)).Bold(false).Size(1, 1)
			p := ln.Name
			if truthyS(ln.ComboParent) {
				p = *ln.ComboParent
			}
			current = &p
			continue
		}
		prefix := ""
		if truthyS(ln.ComboParent) {
			if !hasHeader && (current == nil || *ln.ComboParent != *current) {
				b.Size(1, 2).Bold(true).Line(escpos.TruncateToWidth(*ln.ComboParent, b.Columns)).Bold(false).Size(1, 1)
				p := *ln.ComboParent
				current = &p
			}
			prefix = "-"
		} else {
			current = nil
		}
		b.Size(1, 2).Bold(true).TwoCol(prefix+dishName(ln), qtyUnit(ln)).Bold(false).Size(1, 1)
		if truthyS(ln.Notes) {
			b.Size(1, 2).Line("-备注:"+*ln.Notes).Size(1, 1)
		}
		for _, a := range ln.Addons {
			b.Size(1, 2).Line(fmt.Sprintf("  +%s x%d", a.Name, a.Qty)).Size(1, 1)
		}
	}
}

func serialLine(b *escpos.Builder, ctx Context) {
	if ctx.SerialNo != 0 {
		b.Line(escpos.SerialFooterLine(ctx.SerialNo))
	}
}

func extraGet(t routing.PlannedTicket, k string) (any, bool) {
	if t.Extra == nil {
		return nil, false
	}
	return t.Extra.Get(k)
}

// StationTicket is render_station_ticket (后厨/出单/退菜/转台 tickets).
func StationTicket(t routing.PlannedTicket, ctx Context) []byte {
	b := escpos.NewBuilder()
	b.Init()
	if t.TicketType == "转台单" {
		return renderTransfer(b, t, ctx)
	}
	if truthyS(t.Marker) {
		b.Align(1).Bold(true).Size(2, 2).Line(*t.Marker).Size(1, 1).Bold(false).Align(0)
	}
	b.Align(1).Bold(true).Size(1, 2).Line("桌台号："+ctx.TableLabel+turnSuffix(ctx)).Size(1, 1)
	b.Line(t.TicketType).Bold(false).Align(0)

	brandLine(b, ctx)
	party := ""
	if ctx.PartySize != nil && *ctx.PartySize != 0 {
		party = fmt.Sprintf("%d人", *ctx.PartySize)
	}
	b.TwoCol("菜品", pyStrip(party+"  数量"))
	b.HR()

	emitLines(b, t.Lines)

	seen := map[string]bool{}
	for _, ln := range t.Lines {
		if ln.Ret && truthyS(ln.RetReason) && !seen[*ln.RetReason] {
			seen[*ln.RetReason] = true
			b.Line("退菜原因：" + *ln.RetReason)
		}
	}
	if v, ok := extraGet(t, "ret_reason"); ok && pyfmt.Truthy(v) && !seen[pyfmt.Str(v)] {
		b.Line("退菜原因：" + pyfmt.Str(v))
	}

	b.HR()
	if truthyS(t.WholeNote) {
		b.Line("整单备注 " + *t.WholeNote)
		b.HR()
	}
	if truthyS(ctx.Operator) {
		b.Line("操作员：" + *ctx.Operator)
	}
	b.Line("打印时间：" + FmtDt(ctx.PrintTimeMs))
	b.Line(t.Footer)
	serialLine(b, ctx)
	b.Cut()
	return b.Build()
}

// pyStrip is str.strip() (Unicode whitespace).
func pyStrip(s string) string { return strings.TrimSpace(s) }

func renderTransfer(b *escpos.Builder, t routing.PlannedTicket, ctx Context) []byte {
	b.Align(1).Bold(true).Size(1, 2).Line("转台单").Size(1, 1).Bold(false).Align(0)
	orig, _ := extraGetOr(t, "orig_table", "")
	target, _ := extraGetOr(t, "target_table", "")
	b.Line("原桌台：" + pyfmt.Str(orig))
	b.Line("目标桌台：" + pyfmt.Str(target))
	if truthyS(ctx.DailySeq) {
		b.Line("流水号：" + *ctx.DailySeq)
	}
	if ctx.PartySize != nil && *ctx.PartySize != 0 {
		b.Line(fmt.Sprintf("人数：%d", *ctx.PartySize))
	}
	if truthyS(ctx.OrderNo) {
		b.Line("订单号：" + *ctx.OrderNo)
	}
	b.HR()
	b.TwoCol("菜品", "数量")
	emitLines(b, t.Lines)
	b.HR()
	if truthyS(ctx.Operator) {
		b.Line("操作员：" + *ctx.Operator)
	}
	b.Line("开单时间：" + FmtDt(ctx.OpenTimeMs))
	b.Line("打印时间：" + FmtDt(ctx.PrintTimeMs))
	b.Line(t.Footer)
	serialLine(b, ctx)
	b.Cut()
	return b.Build()
}

func extraGetOr(t routing.PlannedTicket, k string, def any) (any, bool) {
	if v, ok := extraGet(t, k); ok {
		return v, true
	}
	return def, false
}
