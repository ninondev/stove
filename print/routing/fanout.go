// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package routing

import (
	"sort"

	"github.com/ninondev/stove/internal/pyfmt"
)

// notice-style ops: one aggregate ticket per affected station, with a marker.
var noticeOps = map[Op][2]string{
	OpUrge:   {"催菜单", "【催】"},
	OpCallUp: {"起菜单", "【起】"},
	OpModify: {"改菜单", "【改】"},
	OpVoid:   {"作废单", "【作废】"},
	OpMerge:  {"合台单", "【合台】"},
	OpMove:   {"移菜单", "【移】"},
}

const unconfiguredNote = "以下菜品未配置出单口，请配置菜品的打印站点"

func strP(s string) *string { return &s }

func copyAddons(a []Addon) []Addon {
	if a == nil {
		return []Addon{}
	}
	return append([]Addon{}, a...)
}

func maxI(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// lineFrom is _line_from. qty < 0 is never passed, so qtyP nil = item.Qty.
func lineFrom(item PrintItem, ret, transfer, allowHeld bool, qtyP *int64) TicketLine {
	qty := item.Qty
	if qtyP != nil {
		qty = *qtyP
	}
	var retReason *string
	if ret {
		retReason = item.RetReason
	}
	return TicketLine{
		Name:        item.Name,
		Qty:         qty,
		Unit:        item.Unit,
		SpecName:    item.SpecName,
		Notes:       item.Notes,
		Addons:      copyAddons(item.Addons),
		Ret:         ret,
		RetReason:   retReason,
		Transfer:    transfer,
		Held:        item.Held && !ret && !transfer && allowHeld,
		ComboParent: item.ComboParent,
		ComboUID:    item.ComboUID,
	}
}

// comboHeaderLine is _combo_header_line.
func comboHeaderLine(name string, qty int64, sample PrintItem) TicketLine {
	return TicketLine{Name: name, Qty: qty, Unit: "份", Held: sample.Held,
		ComboParent: strP(name), ComboUID: sample.ComboUID, IsComboHeader: true, Addons: []Addon{}}
}

// stationAggregation is _station_aggregation: an invalid per-binding value
// falls back to the station's own aggregation.
func stationAggregation(st StationDef, b ItemBinding) Aggregation {
	if b.Aggregation != nil && *b.Aggregation != "" {
		switch *b.Aggregation {
		case Aggregate, PerDish:
			return *b.Aggregation
		}
		return st.Aggregation
	}
	return st.Aggregation
}

type bound struct {
	st StationDef
	b  ItemBinding
}

func boundStations(item PrintItem, stations *Stations) []bound {
	var out []bound
	for _, b := range item.Bindings {
		st, ok := stations.Get(b.StationCode)
		if ok && st.Enabled {
			out = append(out, bound{st, b})
		}
	}
	return out
}

func ticketType(st StationDef, op Op) string {
	if op == OpRetreat {
		return "退菜单"
	}
	if op == OpTransfer {
		return "转台单"
	}
	if st.Aggregation == Aggregate && st.Code == "expo" {
		return "厨总单"
	}
	return "堂口单"
}

func marker(op Op) *string {
	switch op {
	case OpAdd:
		return strP("【加菜】")
	case OpRetreat:
		return strP("【退】")
	}
	return nil
}

func sortedByCreated(items []PrintItem) []PrintItem {
	out := append([]PrintItem(nil), items...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedSeq < out[j].CreatedSeq })
	return out
}

func sortTickets(ts []*PlannedTicket) {
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].Seq < ts[j].Seq })
}

func deref(ts []*PlannedTicket) []PlannedTicket {
	out := make([]PlannedTicket, len(ts))
	for i, t := range ts {
		out[i] = *t
	}
	return out
}

func frontFooter(stations *Stations) string {
	if front, ok := stations.Get("front"); ok {
		return front.Footer
	}
	return "前台"
}

func newTicket(code, footer, ttype string, mk *string, whole *string, seq, copies int64) *PlannedTicket {
	return &PlannedTicket{StationCode: code, Footer: footer, TicketType: ttype, Marker: mk,
		Lines: []TicketLine{}, WholeNote: whole, Seq: seq, Copies: copies, Extra: pyfmt.NewOMap()}
}

// orderedBuckets is a Python dict of tickets keyed by string.
type orderedBuckets struct {
	keys []string
	m    map[string]*PlannedTicket
}

func newBuckets() *orderedBuckets { return &orderedBuckets{m: map[string]*PlannedTicket{}} }

func (o *orderedBuckets) get(k string) *PlannedTicket { return o.m[k] }

func (o *orderedBuckets) put(k string, t *PlannedTicket) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = t
}

func (o *orderedBuckets) values() []*PlannedTicket {
	out := make([]*PlannedTicket, 0, len(o.keys))
	for _, k := range o.keys {
		out = append(out, o.m[k])
	}
	return out
}

const unconfKey = "__unconfigured__"

// planOrderLike is _plan_order_like (RETREAT; marks lines as 退).
func planOrderLike(items []PrintItem, stations *Stations, op Op, wholeNote *string) []PlannedTicket {
	var tickets []*PlannedTicket
	var seq int64
	isRet := op == OpRetreat
	buckets := newBuckets()
	for _, item := range sortedByCreated(items) {
		bs := boundStations(item, stations)
		if len(bs) == 0 {
			seq++
			notice := buckets.get(unconfKey)
			if notice == nil {
				notice = newTicket("front", frontFooter(stations), "未配置单", marker(op),
					strP(unconfiguredNote), seq, 1)
				buckets.put(unconfKey, notice)
			}
			notice.Lines = append(notice.Lines, lineFrom(item, isRet, false, true, nil))
			continue
		}
		for _, sb := range bs {
			if stationAggregation(sb.st, sb.b) == PerDish {
				seq++
				t := newTicket(sb.st.Code, sb.st.Footer, ticketType(sb.st, op), marker(op),
					wholeNote, seq, maxI(1, sb.b.Copies))
				t.Lines = append(t.Lines, lineFrom(item, isRet, false, true, nil))
				tickets = append(tickets, t)
			} else {
				bucket := buckets.get(sb.st.Code)
				if bucket == nil {
					seq++
					bucket = newTicket(sb.st.Code, sb.st.Footer, ticketType(sb.st, op), marker(op),
						wholeNote, seq, maxI(1, sb.b.Copies))
					buckets.put(sb.st.Code, bucket)
				}
				bucket.Copies = maxI(bucket.Copies, sb.b.Copies)
				bucket.Lines = append(bucket.Lines, lineFrom(item, isRet, false, true, nil))
			}
		}
	}
	tickets = append(tickets, buckets.values()...)
	sortTickets(tickets)
	return deref(tickets)
}

// optKey renders an optional string as a distinct map-key part.
func optKey(s *string) string {
	if s == nil {
		return "\x00N"
	}
	return "\x00S" + *s
}

func boolKey(b bool) string {
	if b {
		return "\x00T"
	}
	return "\x00F"
}

type expoBlock struct {
	solo   bool
	sample PrintItem
	qty    int64
	comps  []PrintItem
}

// expoLines is _expo_lines: combos expanded set by set (header 1/份 + the
// per-set component lines); other dishes merged by (name, spec, notes, held).
func expoLines(items []PrintItem) []TicketLine {
	var blocks []*expoBlock
	soloIdx := map[string]int{}
	comboIdx := map[int64]int{}
	for _, it := range items {
		if it.ComboUID == nil {
			skey := it.Name + optKey(it.SpecName) + optKey(it.Notes) + boolKey(it.Held)
			if _, ok := soloIdx[skey]; !ok {
				soloIdx[skey] = len(blocks)
				blocks = append(blocks, &expoBlock{solo: true, sample: it})
			}
			blocks[soloIdx[skey]].qty += it.Qty
		} else {
			uid := *it.ComboUID
			if _, ok := comboIdx[uid]; !ok {
				comboIdx[uid] = len(blocks)
				blocks = append(blocks, &expoBlock{})
			}
			b := blocks[comboIdx[uid]]
			b.comps = append(b.comps, it)
		}
	}
	lines := []TicketLine{}
	for _, b := range blocks {
		if b.solo {
			q := b.qty
			lines = append(lines, lineFrom(b.sample, false, false, true, &q))
			continue
		}
		comps := b.comps
		sets := maxI(1, comps[0].ComboQty)
		pname := comps[0].Name
		if comps[0].ComboParent != nil && *comps[0].ComboParent != "" {
			pname = *comps[0].ComboParent
		}
		for s := int64(0); s < sets; s++ {
			lines = append(lines, comboHeaderLine(pname, 1, comps[0]))
			for _, c := range comps {
				q := pyfmt.FloorDiv(c.Qty, sets)
				lines = append(lines, lineFrom(c, false, false, true, &q))
			}
		}
	}
	return lines
}

type pdGroup struct {
	sample PrintItem
	qty    int64
	sets   int64
	first  int64
	copies int64
}

// perdishTickets is _perdish_tickets: same dish merged across combos into one
// N/份 ticket (combo header kept); one ticket per distinct dish, ordered by
// the first created_seq.
func perdishTickets(ib []boundItem, st StationDef, op Op, wholeNote *string, nextSeq func() int64) []*PlannedTicket {
	groups := map[string]*pdGroup{}
	var order []string
	for _, x := range ib {
		item := x.it
		key := optKey(item.ComboParent) + "\x01" + item.Name + optKey(item.SpecName) +
			optKey(item.Notes) + boolKey(item.Held)
		g := groups[key]
		if g == nil {
			g = &pdGroup{sample: item, first: item.CreatedSeq, copies: 1}
			groups[key] = g
			order = append(order, key)
		}
		g.qty += item.Qty
		g.sets += item.ComboQty
		g.copies = maxI(g.copies, x.b.Copies)
		if item.CreatedSeq < g.first {
			g.first = item.CreatedSeq
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return groups[order[i]].first < groups[order[j]].first })
	var out []*PlannedTicket
	for _, key := range order {
		g := groups[key]
		s := g.sample
		lines := []TicketLine{}
		if s.ComboParent != nil && *s.ComboParent != "" {
			lines = append(lines, comboHeaderLine(*s.ComboParent, g.sets, s))
		}
		q := g.qty
		lines = append(lines, lineFrom(s, false, false, true, &q))
		t := newTicket(st.Code, st.Footer, ticketType(st, op), marker(op), wholeNote, nextSeq(), maxI(1, g.copies))
		t.Lines = lines
		out = append(out, t)
	}
	return out
}

// boundItem pairs an item with one of its bindings (the (item, binding)
// tuples of _plan_new_add).
type boundItem struct {
	it PrintItem
	b  ItemBinding
}

type stationBucket struct {
	st    StationDef
	items []boundItem
}

type orderedStations struct {
	keys []string
	m    map[string]*stationBucket
}

func (o *orderedStations) add(st StationDef, x boundItem) {
	if o.m == nil {
		o.m = map[string]*stationBucket{}
	}
	b := o.m[st.Code]
	if b == nil {
		b = &stationBucket{st: st}
		o.m[st.Code] = b
		o.keys = append(o.keys, st.Code)
	}
	b.items = append(b.items, x)
}

// planNewAdd is _plan_new_add (下单/加菜).
func planNewAdd(items []PrintItem, stations *Stations, op Op, wholeNote *string) []PlannedTicket {
	var seq int64
	nextSeq := func() int64 { seq++; return seq }
	var agg, pd orderedStations
	var unconfigured []PrintItem
	for _, item := range sortedByCreated(items) {
		bs := boundStations(item, stations)
		if len(bs) == 0 {
			unconfigured = append(unconfigured, item)
			continue
		}
		for _, sb := range bs {
			if stationAggregation(sb.st, sb.b) == Aggregate {
				agg.add(sb.st, boundItem{item, sb.b})
			} else {
				pd.add(sb.st, boundItem{item, sb.b})
			}
		}
	}
	var tickets []*PlannedTicket
	if len(unconfigured) > 0 {
		t := newTicket("front", frontFooter(stations), "未配置单", marker(op), strP(unconfiguredNote), nextSeq(), 1)
		for _, item := range unconfigured {
			t.Lines = append(t.Lines, lineFrom(item, false, false, true, nil))
		}
		tickets = append(tickets, t)
	}
	for _, code := range agg.keys {
		sb := agg.m[code]
		copies := int64(1)
		its := make([]PrintItem, 0, len(sb.items))
		for _, x := range sb.items {
			copies = maxI(copies, x.b.Copies)
			its = append(its, x.it)
		}
		t := newTicket(sb.st.Code, sb.st.Footer, ticketType(sb.st, op), marker(op), wholeNote, 0, copies)
		t.Lines = expoLines(its)
		t.Seq = nextSeq()
		tickets = append(tickets, t)
	}
	for _, code := range pd.keys {
		sb := pd.m[code]
		tickets = append(tickets, perdishTickets(sb.items, sb.st, op, wholeNote, nextSeq)...)
	}
	sortTickets(tickets)
	return deref(tickets)
}

// planTransfer is _plan_transfer: one aggregate ticket per affected station,
// lines marked [转]; kitchen only when its move_table_ticket switch is on.
func planTransfer(items []PrintItem, stations *Stations, orig, target string, extra *pyfmt.OMap) []PlannedTicket {
	var tickets []*PlannedTicket
	var seq int64
	buckets := newBuckets()
	for _, item := range sortedByCreated(items) {
		for _, sb := range boundStations(item, stations) {
			if sb.st.Code == "kitchen" && !sb.st.MoveTableTicket {
				continue
			}
			bucket := buckets.get(sb.st.Code)
			if bucket == nil {
				seq++
				bucket = newTicket(sb.st.Code, sb.st.Footer, "转台单", nil, nil, seq, 1)
				bucket.Extra = pyfmt.NewOMap("orig_table", orig, "target_table", target)
				for _, k := range extra.Keys() {
					v, _ := extra.Get(k)
					bucket.Extra.Set(k, v)
				}
				buckets.put(sb.st.Code, bucket)
			}
			bucket.Lines = append(bucket.Lines, lineFrom(item, false, true, true, nil))
		}
	}
	tickets = append(tickets, buckets.values()...)
	sortTickets(tickets)
	return deref(tickets)
}

// planNotice is _plan_notice (催/起/改/作废/合台/移菜).
func planNotice(items []PrintItem, stations *Stations, ttype, mk string, allowHeld bool) []PlannedTicket {
	var seq int64
	buckets := newBuckets()
	for _, item := range sortedByCreated(items) {
		bs := boundStations(item, stations)
		if len(bs) == 0 {
			notice := buckets.get(unconfKey)
			if notice == nil {
				seq++
				notice = newTicket("front", frontFooter(stations), ttype, strP(mk), strP(unconfiguredNote), seq, 1)
				buckets.put(unconfKey, notice)
			}
			notice.Lines = append(notice.Lines, lineFrom(item, false, false, allowHeld, nil))
			continue
		}
		for _, sb := range bs {
			bucket := buckets.get(sb.st.Code)
			if bucket == nil {
				seq++
				bucket = newTicket(sb.st.Code, sb.st.Footer, ttype, strP(mk), nil, seq, 1)
				buckets.put(sb.st.Code, bucket)
			}
			bucket.Lines = append(bucket.Lines, lineFrom(item, false, false, allowHeld, nil))
		}
	}
	tickets := buckets.values()
	sortTickets(tickets)
	return deref(tickets)
}

// FanOut is routing.fan_out: PlannedTickets in the order they should be
// enqueued (per-dish kitchen tickets in created order).
func FanOut(op Op, items []PrintItem, stations *Stations, o FanOutOpts) []PlannedTicket {
	if stations == nil {
		stations = NewStations()
	}
	if op == OpTransfer {
		orig, target := "", ""
		if o.OrigTable != nil {
			orig = *o.OrigTable
		}
		if o.TargetTable != nil {
			target = *o.TargetTable
		}
		extra := o.Extra
		if extra == nil {
			extra = pyfmt.NewOMap()
		}
		return planTransfer(items, stations, orig, target, extra)
	}
	if n, ok := noticeOps[op]; ok {
		return planNotice(items, stations, n[0], n[1], op == OpModify)
	}
	if op == OpNew || op == OpAdd {
		return planNewAdd(items, stations, op, o.WholeNote)
	}
	return planOrderLike(items, stations, op, o.WholeNote)
}
