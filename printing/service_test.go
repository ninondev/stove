// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package printing

import (
	"bytes"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/ninondev/stove/db"
	"github.com/ninondev/stove/perr"
	"github.com/ninondev/stove/print/escpos"
	"github.com/ninondev/stove/print/render"
	"github.com/ninondev/stove/print/routing"
	"github.com/ninondev/stove/print/transport"
)

// seededDB has four null stations, three dishes and their bindings:
// hot dishes go to expo (one summary ticket) and kitchen (one per dish),
// the cold dish goes to cold.
func seededDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	d.InitSchema()
	for _, c := range []string{"Hot", "Drinks", "Combo"} {
		d.Exec("INSERT INTO categories(name) VALUES(?)", c)
	}
	for _, s := range [][4]string{
		{"expo", "Expo", "aggregate", "hot"}, {"kitchen", "Kitchen", "per_dish", "hot"},
		{"cold", "Cold", "per_dish", "cold"}, {"front", "Drinks", "aggregate", "drink"},
	} {
		d.Exec("INSERT INTO stations(code,name,transport,aggregation,dish_temp) VALUES(?,?,?,?,?)",
			s[0], s[1], "null", s[2], s[3])
	}
	hotCat := d.QueryOne("SELECT id FROM categories WHERE name='Hot'").Int("id")
	for _, dd := range []struct {
		name  string
		price int
		temp  string
	}{{"宫保鸡丁", 237, "hot"}, {"麻婆豆腐", 143, "hot"}, {"凉拌黄瓜", 117, "cold"}} {
		id := d.Exec("INSERT INTO dishes(name,category_id,price_jiao,unit,temp) VALUES(?,?,?,?,?)",
			dd.name, hotCat, dd.price, "份", dd.temp).LastInsertID
		binds := [][2]string{{"expo", "total"}, {"kitchen", "split"}}
		if dd.temp == "cold" {
			binds = [][2]string{{"cold", "split"}}
		}
		for _, b := range binds {
			sid := d.QueryOne("SELECT id FROM stations WHERE code=?", b[0]).Int("id")
			d.Exec("INSERT INTO dish_station_bindings(dish_id,station_id,print_rule) VALUES(?,?,?)", id, sid, b[1])
		}
	}
	return d
}

func dishID(d *db.DB, name string) int64 {
	return d.QueryOne("SELECT id FROM dishes WHERE name=?", name).Int("id")
}

func addDrink(d *db.DB) int64 {
	cat := d.QueryOne("SELECT id FROM categories WHERE name='Drinks'").Int("id")
	front := d.QueryOne("SELECT id FROM stations WHERE code='front'").Int("id")
	id := d.Exec("INSERT INTO dishes(name,category_id,price_jiao,unit,temp,is_active) VALUES('柠檬茶',?,73,'瓶','drink',1)", cat).LastInsertID
	d.Exec("INSERT INTO dish_station_bindings(dish_id,station_id,print_rule) VALUES(?,?,'total')", id, front)
	return id
}

func demoRequests(d *db.DB) []ItemRequest {
	return []ItemRequest{
		{DishID: dishID(d, "宫保鸡丁"), Qty: 1, CreatedSeq: 1},
		{DishID: dishID(d, "麻婆豆腐"), Qty: 1, CreatedSeq: 2},
		{DishID: dishID(d, "凉拌黄瓜"), Qty: 1, CreatedSeq: 3},
		{DishID: addDrink(d), Qty: 2, CreatedSeq: 4},
	}
}

func demoCtx() render.Context {
	c := render.NewContext()
	c.TableLabel = "Table 1"
	two := int64(4)
	c.PartySize = &two
	op := "Demo Operator"
	c.Operator = &op
	now := db.NowMs()
	c.PrintTimeMs = &now
	return c
}

func stationCounts(d *db.DB) map[string]int {
	out := map[string]int{}
	for _, r := range d.Query("SELECT s.code AS code FROM print_jobs pj JOIN stations s ON s.id=pj.station_id") {
		out[r.Str("code")]++
	}
	return out
}

func jobPayloads(d *db.DB, code string) [][]byte {
	var out [][]byte
	for _, r := range d.Query("SELECT payload FROM print_jobs pj JOIN stations s ON s.id=pj.station_id WHERE s.code=?", code) {
		out = append(out, r.Bytes("payload"))
	}
	return out
}

func gb(s string) []byte { return escpos.EncodeGB18030(s) }

func fakes(m *Manager) map[string]*transport.Fake {
	out := map[string]*transport.Fake{}
	for _, code := range []string{"expo", "kitchen", "cold", "front"} {
		f := transport.NewFake(code, true)
		out[code] = f
		m.SetTransport(code, f)
	}
	return out
}

func TestSeedSummary(t *testing.T) {
	d := seededDB(t)
	if n := d.QueryOne("SELECT COUNT(*) c FROM dishes").Int("c"); n != 3 {
		t.Fatalf("%d dishes", n)
	}
	if n := d.QueryOne("SELECT COUNT(*) c FROM stations").Int("c"); n != 4 {
		t.Fatalf("%d stations", n)
	}
}

func TestPlanAndEnqueueRoutesToStations(t *testing.T) {
	d := seededDB(t)
	ids := PlanAndEnqueue(d, routing.OpNew, demoRequests(d), demoCtx(), PlanOpts{})
	if len(ids) != 5 {
		t.Fatalf("%d jobs", len(ids))
	}
	c := stationCounts(d)
	if c["expo"] != 1 || c["kitchen"] != 2 || c["cold"] != 1 || c["front"] != 1 {
		t.Fatalf("counts %v", c)
	}
	for _, r := range d.Query("SELECT payload FROM print_jobs") {
		if len(r.Bytes("payload")) == 0 {
			t.Fatal("empty payload")
		}
	}
	if tt := d.QueryOne("SELECT ticket_type FROM print_jobs pj JOIN stations s ON s.id=pj.station_id WHERE s.code='expo'").Str("ticket_type"); tt != "厨总单" {
		t.Fatalf("expo ticket type %q", tt)
	}
}

func TestWorkerDeliversWithFakes(t *testing.T) {
	d := seededDB(t)
	PlanAndEnqueue(d, routing.OpNew, demoRequests(d), demoCtx(), PlanOpts{})
	m := NewManager(d)
	m.ReloadStations()
	fs := fakes(m)
	m.Tick()
	if n := d.QueryOne("SELECT COUNT(*) c FROM print_jobs WHERE state!='done'").Int("c"); n != 0 {
		t.Fatalf("%d jobs not done", n)
	}
	if len(fs["kitchen"].Sent()) != 2 || len(fs["expo"].Sent()) != 1 || len(fs["cold"].Sent()) != 1 || len(fs["front"].Sent()) != 1 {
		t.Fatal("wrong number of tickets sent")
	}
}

func TestOfflineQueuesThenFlushesOnReconnect(t *testing.T) {
	d := seededDB(t)
	PlanAndEnqueue(d, routing.OpNew, demoRequests(d), demoCtx(), PlanOpts{})
	m := NewManager(d)
	m.ReloadStations()
	fs := fakes(m)
	fs["expo"].SetOnline(false)
	m.Tick()
	state := d.QueryOne("SELECT state FROM print_jobs pj JOIN stations s ON s.id=pj.station_id WHERE s.code='expo'").Str("state")
	if state != "failed" || len(fs["expo"].Sent()) != 0 || len(fs["kitchen"].Sent()) != 2 {
		t.Fatalf("expo %s, expo sent %d, kitchen sent %d", state, len(fs["expo"].Sent()), len(fs["kitchen"].Sent()))
	}
	fs["expo"].SetOnline(true)
	d.Exec("UPDATE print_jobs SET next_attempt_at=0 WHERE state='failed'")
	// A failed send keeps the station offline for a cooldown, even when it
	// answers again. Skip the wait, then probe it.
	for _, st := range m.stations {
		atomic.StoreInt64(&st.failUntil, 0)
	}
	m.RefreshHealth(true)
	m.Tick()
	if n := d.QueryOne("SELECT COUNT(*) c FROM print_jobs WHERE state!='done'").Int("c"); n != 0 || len(fs["expo"].Sent()) != 1 {
		t.Fatalf("%d jobs left, expo sent %d", n, len(fs["expo"].Sent()))
	}
}

func TestHealthReportsOfflineWithReason(t *testing.T) {
	d := seededDB(t)
	m := NewManager(d)
	m.ReloadStations()
	fs := fakes(m)
	fs["expo"].SetOnline(false)
	for _, h := range m.RefreshHealth(true) {
		if h.Code == "expo" {
			if h.Reachable || h.Reason == nil || *h.Reason == "" {
				t.Fatalf("expo health %+v", h)
			}
			return
		}
	}
	t.Fatal("expo missing from health")
}

func TestMisconfiguredStationSurfaces(t *testing.T) {
	d := seededDB(t)
	d.Exec("INSERT INTO stations(code,name,transport,net_host,net_port,aggregation,sort,enabled) VALUES('badnet','Bad net','net',NULL,9100,'per_dish',99,1)")
	sid := d.QueryOne("SELECT id FROM stations WHERE code='badnet'").Int("id")
	d.Exec("INSERT INTO print_jobs(station_id,ticket_type,serial_no,payload,state,enqueue_seq,created_at,next_attempt_at,attempts) VALUES(?,'堂口单',0,X'01','pending',999999,1,0,0)", sid)
	m := NewManager(d)
	m.ReloadStations()
	for _, h := range m.RefreshHealth(false) {
		if h.Code == "badnet" {
			if h.Reachable || h.Reason == nil || h.Pending < 1 {
				t.Fatalf("badnet health %+v", h)
			}
			return
		}
	}
	t.Fatal("a misconfigured station was silently dropped")
}

func TestReprintClonesPayloadWithFreshSerial(t *testing.T) {
	d := seededDB(t)
	PlanAndEnqueue(d, routing.OpNew, demoRequests(d), demoCtx(), PlanOpts{})
	src := d.QueryOne("SELECT * FROM print_jobs ORDER BY id LIMIT 1")
	id := ReprintJob(d, src.Int("id"))
	if id == nil {
		t.Fatal("no reprint")
	}
	n := d.QueryOne("SELECT * FROM print_jobs WHERE id=?", *id)
	if !bytes.Equal(n.Bytes("payload"), src.Bytes("payload")) || n.Int("serial_no") == src.Int("serial_no") || n.Int("is_reprint") != 1 {
		t.Fatal("reprint is not a copy with a new serial")
	}
	if c := d.QueryOne("SELECT COUNT(*) c FROM audit_log WHERE kind='reprint'").Int("c"); c != 1 {
		t.Fatalf("%d reprint audit rows", c)
	}
}

func TestSerialsAreUniquePerStation(t *testing.T) {
	d := seededDB(t)
	PlanAndEnqueue(d, routing.OpNew, demoRequests(d), demoCtx(), PlanOpts{})
	PlanAndEnqueue(d, routing.OpAdd, demoRequests(d), demoCtx(), PlanOpts{})
	seen := map[[2]any]bool{}
	for _, r := range d.Query("SELECT s.code AS code, pj.serial_no AS sn FROM print_jobs pj JOIN stations s ON s.id=pj.station_id") {
		k := [2]any{r.Str("code"), r.Int("sn")}
		if seen[k] {
			t.Fatalf("duplicate serial %v", k)
		}
		seen[k] = true
	}
}

func addCombo(t *testing.T, d *db.DB, name string, parts ...string) int64 {
	t.Helper()
	cat := d.QueryOne("SELECT id FROM categories WHERE name='Combo'").Int("id")
	id := d.Exec("INSERT INTO dishes(name,category_id,price_jiao,unit,temp,is_combo,is_active) VALUES(?,?,917,'份','special',1,1)", name, cat).LastInsertID
	for _, p := range parts {
		d.Exec("INSERT INTO combo_components(combo_dish_id,component_dish_id,qty) VALUES(?,?,1)", id, dishID(d, p))
	}
	return id
}

func TestComboExpandsIntoItsComponents(t *testing.T) {
	d := seededDB(t)
	id := addCombo(t, d, "Demo Combo", "宫保鸡丁", "凉拌黄瓜")
	PlanAndEnqueue(d, routing.OpNew, []ItemRequest{{DishID: id, Qty: 1, CreatedSeq: 1}}, demoCtx(), PlanOpts{})
	c := stationCounts(d)
	if c["expo"] != 1 || c["kitchen"] != 1 || c["cold"] != 1 || c["front"] != 0 {
		t.Fatalf("counts %v", c)
	}
	cold := jobPayloads(d, "cold")[0]
	if !bytes.Contains(cold, gb("Demo Combo")) || !bytes.Contains(cold, gb("-凉拌黄瓜")) {
		t.Fatal("the cold ticket must name the combo and its component")
	}
}

func TestComboTicketLayout(t *testing.T) {
	d := seededDB(t)
	id := addCombo(t, d, "Demo Layout Combo", "宫保鸡丁", "麻婆豆腐", "凉拌黄瓜")
	PlanAndEnqueue(d, routing.OpNew, []ItemRequest{{DishID: id, Qty: 1, CreatedSeq: 1}}, demoCtx(), PlanOpts{})
	c := stationCounts(d)
	if c["expo"] != 1 || c["kitchen"] != 2 || c["cold"] != 1 || c["front"] != 0 {
		t.Fatalf("expo must sum the hot dishes on 1 ticket, kitchen and cold print one per dish: %v", c)
	}
	name := gb("Demo Layout Combo")
	var joined []byte
	for _, p := range jobPayloads(d, "kitchen") {
		if !bytes.Contains(p, name) {
			t.Fatal("every kitchen ticket must carry the combo name")
		}
		joined = append(joined, p...)
	}
	if !bytes.Contains(jobPayloads(d, "cold")[0], name) {
		t.Fatal("the cold ticket must carry the combo name")
	}
	if !bytes.Contains(joined, gb("宫保鸡丁")) || !bytes.Contains(joined, gb("麻婆豆腐")) {
		t.Fatal("the kitchen tickets must list both hot dishes")
	}
}

func TestCopiesMakeSeveralJobs(t *testing.T) {
	d := seededDB(t)
	dish := dishID(d, "凉拌黄瓜")
	d.Exec("UPDATE dish_station_bindings SET copies=2 WHERE dish_id=?", dish)
	prefix := "demo"
	ids := PlanAndEnqueue(d, routing.OpNew, []ItemRequest{NewItemRequest(dish)}, demoCtx(), PlanOpts{DedupePrefix: &prefix})
	jobs := d.Query("SELECT serial_no,enqueue_seq,dedupe_key FROM print_jobs ORDER BY id")
	if len(ids) != 2 || len(jobs) != 2 {
		t.Fatalf("%d ids, %d jobs", len(ids), len(jobs))
	}
	for _, f := range []string{"serial_no", "enqueue_seq", "dedupe_key"} {
		if jobs[0].Get(f) == jobs[1].Get(f) {
			t.Errorf("%s repeats between copies", f)
		}
	}
}

// --- print policy ---------------------------------------------------------------

func enqueueOne(d *db.DB) []int64 {
	return PlanAndEnqueue(d, routing.OpNew, []ItemRequest{NewItemRequest(dishID(d, "宫保鸡丁"))}, demoCtx(), PlanOpts{})
}

func TestManualPolicyHoldsThenReleases(t *testing.T) {
	d := seededDB(t)
	SetPolicy(d, map[string]string{"kitchen": PolicyManual})
	enqueueOne(d)
	jobs := d.Query("SELECT state,serial_no,payload FROM print_jobs")
	if len(jobs) == 0 {
		t.Fatal("no jobs")
	}
	for _, r := range jobs {
		if r.Str("state") != "hold" || r.Int("serial_no") != 0 || bytes.Contains(r.Bytes("payload"), gb("流水号 No.")) {
			t.Fatalf("job is not a clean hold: %v", r.Map())
		}
	}
	if n := ReleaseHolds(d, nil); n != int64(len(jobs)) {
		t.Fatalf("released %d of %d", n, len(jobs))
	}
	if d.QueryOne("SELECT COUNT(*) c FROM print_jobs WHERE state='hold'").Int("c") != 0 {
		t.Fatal("holds remain")
	}
	if ReleaseHolds(d, nil) != 0 {
		t.Fatal("a second release found more")
	}
}

func TestAutoIsTheDefault(t *testing.T) {
	d := seededDB(t)
	enqueueOne(d)
	if d.QueryOne("SELECT COUNT(*) c FROM print_jobs WHERE state='hold'").Int("c") != 0 ||
		d.QueryOne("SELECT COUNT(*) c FROM print_jobs WHERE state='pending'").Int("c") < 1 {
		t.Fatal("default policy must print at once")
	}
}

func TestOffMakesNoJobsOrSerials(t *testing.T) {
	d := seededDB(t)
	SetPolicy(d, map[string]string{"kitchen": PolicyOff})
	if len(enqueueOne(d)) != 0 ||
		d.QueryOne("SELECT COUNT(*) c FROM print_jobs").Int("c") != 0 ||
		d.QueryOne("SELECT COUNT(*) c FROM station_serials").Int("c") != 0 {
		t.Fatal("policy off must leave no trace")
	}
}

func TestHoldDefersTheSerialSoNumbersHaveNoGap(t *testing.T) {
	d := seededDB(t)
	for _, p := range []string{PolicyAuto, PolicyManual, PolicyAuto} {
		SetPolicy(d, map[string]string{"kitchen": p})
		enqueueOne(d)
	}
	autos, holds := map[int64][]int64{}, map[int64][]int64{}
	for _, r := range d.Query("SELECT station_id,serial_no,state FROM print_jobs ORDER BY station_id,enqueue_seq") {
		if r.Str("state") == "hold" {
			holds[r.Int("station_id")] = append(holds[r.Int("station_id")], r.Int("serial_no"))
		} else {
			autos[r.Int("station_id")] = append(autos[r.Int("station_id")], r.Int("serial_no"))
		}
	}
	if len(holds) == 0 {
		t.Fatal("nothing was held")
	}
	total := 0
	for _, s := range autos {
		for i, n := range s {
			if n != int64(i+1) {
				t.Fatalf("auto serials %v have a gap", s)
			}
		}
	}
	for _, s := range holds {
		for _, n := range s {
			if n != 0 {
				t.Fatalf("a held job already has serial %d", n)
			}
		}
		total += len(s)
	}
	if ReleaseHolds(d, nil) != int64(total) {
		t.Fatal("released the wrong number")
	}
	for station, held := range holds {
		rows := d.Query("SELECT serial_no,payload FROM print_jobs WHERE station_id=? ORDER BY serial_no", station)
		if len(rows) != len(autos[station])+len(held) {
			t.Fatalf("station %d: %d jobs", station, len(rows))
		}
		for i, r := range rows {
			if r.Int("serial_no") != int64(i+1) || !bytes.Contains(r.Bytes("payload"), escpos.SerialFooterBytes(r.Int("serial_no"))) {
				t.Fatalf("station %d job %d: serial %d or its footer is wrong", station, i, r.Int("serial_no"))
			}
		}
	}
}

func TestInjectSerialIsByteIdenticalToRenderingWithIt(t *testing.T) {
	with := escpos.NewBuilder().Init().Line("宫保鸡丁").Line("流水号 No.00007").Cut().Build()
	without := escpos.NewBuilder().Init().Line("宫保鸡丁").Cut().Build()
	if !bytes.Equal(escpos.InjectSerial(without, 7), with) {
		t.Fatal("injected serial differs from a rendered one")
	}
}

func TestReprintOfAHeldJobGetsASerial(t *testing.T) {
	d := seededDB(t)
	SetPolicy(d, map[string]string{"kitchen": PolicyManual})
	orig := enqueueOne(d)[0]
	if d.QueryOne("SELECT serial_no FROM print_jobs WHERE id=?", orig).Int("serial_no") != 0 {
		t.Fatal("held job has a serial")
	}
	n := d.QueryOne("SELECT serial_no,payload,state FROM print_jobs WHERE id=?", *ReprintJob(d, orig))
	if n.Str("state") != "pending" || n.Int("serial_no") <= 0 || !bytes.Contains(n.Bytes("payload"), escpos.SerialFooterBytes(n.Int("serial_no"))) {
		t.Fatalf("reprint %v", n.Map())
	}
}

func TestReprintRefusesAClearedPayload(t *testing.T) {
	d := seededDB(t)
	id := enqueueOne(d)[0]
	d.Exec("UPDATE print_jobs SET payload=X'' WHERE id=?", id)
	if e := perr.Catch(func() { ReprintJob(d, id) }); e == nil || e.Kind != perr.KindValidation {
		t.Fatalf("got %v", e)
	}
}

func TestReprintStripsTheDrawerKick(t *testing.T) {
	for _, kick := range [][]byte{escpos.CmdKickPin2, escpos.CmdKickPin5} {
		d := seededDB(t)
		payload := escpos.NewBuilder().Init().Line("Demo").Cut().Build()
		var id *int64
		d.Tx(func(tx *db.Tx) {
			id = EnqueuePayload(tx, "front", "堂口单", append(append([]byte(nil), payload...), kick...), PayloadOpts{})
		})
		got := d.QueryOne("SELECT payload FROM print_jobs WHERE id=?", *ReprintJob(d, *id)).Bytes("payload")
		if !bytes.Equal(got, payload) || bytes.Contains(got, escpos.CmdKickPin2) || bytes.Contains(got, escpos.CmdKickPin5) {
			t.Fatalf("reprint kept the drawer kick")
		}
	}
}

func TestPolicyDefaultsAndBadValues(t *testing.T) {
	d := seededDB(t)
	if p := GetPolicy(d); p["kitchen"] != PolicyAuto || p["move"] != PolicyAuto || p["merge"] != PolicyAuto {
		t.Fatalf("defaults %v", p)
	}
	got := SetPolicy(d, map[string]string{"kitchen": "sometimes", "move": PolicyOff, "other": PolicyOff})
	if got["kitchen"] != PolicyAuto || got["move"] != PolicyOff || len(got) != 3 {
		t.Fatalf("stored %v", got)
	}
	d.Exec("UPDATE app_settings SET value='not json' WHERE key='print_policy'")
	if GetPolicy(d)["move"] != PolicyAuto {
		t.Fatal("damaged policy must fall back to auto")
	}
}
