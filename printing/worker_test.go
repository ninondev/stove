// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package printing

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ninondev/stove/db"
	"github.com/ninondev/stove/internal/config"
	"github.com/ninondev/stove/print/transport"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// testDB is a fresh schema with two null-transport stations (front, kitchen).
func testDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "stove.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	d.InitSchema()
	d.Exec("INSERT INTO stations(id, code, name, transport, sort) VALUES(1,'front','Front','null',1)")
	d.Exec("INSERT INTO stations(id, code, name, transport, sort) VALUES(2,'kitchen','Kitchen','null',2)")
	return d
}

type jobSpec struct {
	station    int64
	ticketType string
	payload    []byte
	state      string
	serial     int64
	attempts   int64
	createdAt  int64 // 0 = now
	next       int64
}

func addJob(t *testing.T, d *db.DB, j jobSpec) int64 {
	t.Helper()
	if j.state == "" {
		j.state = "pending"
	}
	if j.createdAt == 0 {
		j.createdAt = db.NowMs()
	}
	if j.payload == nil {
		j.payload = []byte{}
	}
	seq := d.QueryOne("SELECT COALESCE(MAX(enqueue_seq),0)+1 AS s FROM print_jobs").Int("s")
	return d.Exec(`INSERT INTO print_jobs(station_id, ticket_type, serial_no, payload, state, attempts,
		enqueue_seq, created_at, next_attempt_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		j.station, j.ticketType, j.serial, j.payload, j.state, j.attempts, seq, j.createdAt, j.next).LastInsertID
}

func jobRow(t *testing.T, d *db.DB, id int64) *db.Row {
	t.Helper()
	r := d.QueryOne("SELECT * FROM print_jobs WHERE id=?", id)
	if r == nil {
		t.Fatalf("job %d missing", id)
	}
	return r
}

func managerWithFakes(t *testing.T, d *db.DB) (*Manager, *transport.Fake, *transport.Fake) {
	t.Helper()
	m := NewManager(d)
	m.ReloadStations()
	front, kitchen := transport.NewFake("front", true), transport.NewFake("kitchen", true)
	m.SetTransport("front", front)
	m.SetTransport("kitchen", kitchen)
	return m, front, kitchen
}

func TestBackoffSeconds(t *testing.T) {
	for attempts, base := range map[int64]float64{0: 1, 1: 2, 2: 5, 3: 15, 4: 30, 5: 30, 12: 30} {
		for i := 0; i < 200; i++ {
			v := backoffSeconds(attempts)
			if v < base*0.5 || v > base {
				t.Fatalf("backoffSeconds(%d) = %v, want in [%v, %v]", attempts, v, base*0.5, base)
			}
		}
	}
}

func TestTickSendsInOrder(t *testing.T) {
	d := testDB(t)
	m, front, kitchen := managerWithFakes(t, d)
	a := addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", payload: []byte("A"), serial: 1})
	b := addJob(t, d, jobSpec{station: 2, ticketType: "厨总单", payload: []byte("B"), serial: 1})
	c := addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", payload: []byte("C"), serial: 2})
	later := addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", payload: []byte("L"), serial: 3,
		state: "failed", next: db.NowMs() + 60_000})
	held := addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", payload: []byte("H"), serial: 4, state: "hold"})
	if n := m.tick(); n != 3 {
		t.Fatalf("tick sent %d, want 3", n)
	}
	if got := front.Sent(); len(got) != 2 || string(got[0]) != "A" || string(got[1]) != "C" {
		t.Fatalf("front got %q", got)
	}
	if got := kitchen.Sent(); len(got) != 1 || string(got[0]) != "B" {
		t.Fatalf("kitchen got %q", got)
	}
	for _, id := range []int64{a, b, c} {
		r := jobRow(t, d, id)
		if r.Str("state") != "done" || r.IsNull("sent_at") || !r.IsNull("last_error") {
			t.Fatalf("job %d: state %s sent_at %v last_error %v", id, r.Str("state"), r.Get("sent_at"), r.Get("last_error"))
		}
	}
	if s := jobRow(t, d, later).Str("state"); s != "failed" {
		t.Fatalf("job in backoff: state %s", s)
	}
	if s := jobRow(t, d, held).Str("state"); s != "hold" {
		t.Fatalf("held job: state %s", s)
	}
}

func TestFailureStopsStationAndBacksOff(t *testing.T) {
	d := testDB(t)
	m, front, kitchen := managerWithFakes(t, d)
	m.RefreshHealth(true)
	front.SetOnline(false)
	a := addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", payload: []byte("A"), serial: 1})
	b := addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", payload: []byte("B"), serial: 2})
	k := addJob(t, d, jobSpec{station: 2, ticketType: "厨总单", payload: []byte("K"), serial: 1})
	before := db.NowMs()
	if n := m.tick(); n != 1 {
		t.Fatalf("tick sent %d, want 1 (kitchen only)", n)
	}
	ra := jobRow(t, d, a)
	if ra.Str("state") != "failed" || ra.Int("attempts") != 1 || ra.Str("last_error") != "假打印机离线 front" {
		t.Fatalf("failed job: %v", ra.Map())
	}
	if na := ra.Int("next_attempt_at"); na < before+1000 || na > db.NowMs()+2000 {
		t.Fatalf("next_attempt_at %d not in the attempt-1 backoff window", na)
	}
	if s := jobRow(t, d, b).Str("state"); s != "pending" {
		t.Fatalf("job behind the failure: state %s, want pending (order kept)", s)
	}
	if s := jobRow(t, d, k).Str("state"); s != "done" || len(kitchen.Sent()) != 1 {
		t.Fatalf("other station blocked: %s", s)
	}
	// The station is marked offline at once and the breaker skips it.
	snap := m.HealthSnapshot()
	if snap[0].Code != "front" || snap[0].Reachable || *snap[0].Reason != "发送失败/无响应" {
		t.Fatalf("health after failure: %+v", snap[0])
	}
	front.SetOnline(true)
	if n := m.tick(); n != 0 || len(front.Sent()) != 0 {
		t.Fatalf("breaker did not hold: sent %d", n)
	}
	// The probe reaches the printer, but the cooldown keeps it offline.
	h := m.RefreshHealth(true)
	if h[0].Reachable || h[0].Reason == nil || *h[0].Reason != "发送失败冷却中" || h[0].Pending != 2 {
		t.Fatalf("cooldown health: %+v", h[0])
	}
	if h[0].LastError == nil || *h[0].LastError != "假打印机离线 front" {
		t.Fatalf("last_error = %v", h[0].LastError)
	}
	// After the cooldown and the backoff, the failed job goes first.
	atomic.StoreInt64(&m.stations[1].failUntil, 0)
	d.Exec("UPDATE print_jobs SET next_attempt_at=0 WHERE id=?", a)
	m.RefreshHealth(true)
	if n := m.tick(); n != 2 {
		t.Fatalf("recovered tick sent %d, want 2", n)
	}
	if got := front.Sent(); string(got[0]) != "A" || string(got[1]) != "B" {
		t.Fatalf("order after recovery: %q", got)
	}
}

func TestDeadAfterMaxAttempts(t *testing.T) {
	d := testDB(t)
	m, front, _ := managerWithFakes(t, d)
	front.SetOnline(false)
	id := addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", payload: []byte("A"), serial: 1,
		attempts: int64(config.RetryMaxAttempts - 1)})
	m.tick()
	r := jobRow(t, d, id)
	if r.Str("state") != "dead" || r.Int("attempts") != int64(config.RetryMaxAttempts) {
		t.Fatalf("job: %v", r.Map())
	}
	a := d.QueryOne("SELECT * FROM audit_log WHERE kind='print_dead'")
	if a == nil || a.Int("ref_id") != id || a.Str("ref_table") != "print_jobs" || a.Str("detail") != "假打印机离线 front" {
		t.Fatalf("audit: %v", a)
	}
}

// stubTransport is always reachable; types embedding it override Send.
type stubTransport struct{}

func (stubTransport) Send([]byte) error             { return nil }
func (stubTransport) Probe() transport.StatusResult { return transport.StatusResult{Reachable: true} }
func (stubTransport) Describe() string              { return "stub" }

type longErrTransport struct{ stubTransport }

func (longErrTransport) Send([]byte) error {
	return &transport.Error{Msg: strings.Repeat("错", 600)}
}

func TestLastErrorTruncatedTo500Chars(t *testing.T) {
	d := testDB(t)
	m, _, _ := managerWithFakes(t, d)
	m.SetTransport("front", longErrTransport{})
	id := addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", payload: []byte("A"), serial: 1})
	m.tick()
	if got := jobRow(t, d, id).Str("last_error"); got != strings.Repeat("错", 500) {
		t.Fatalf("last_error has %d runes", len([]rune(got)))
	}
}

func TestRefreshHealth(t *testing.T) {
	d := testDB(t)
	d.Exec("INSERT INTO stations(id, code, name, transport, net_host, sort) VALUES(3,'cold','Cold','net',NULL,3)")
	d.Exec("INSERT INTO stations(id, code, name, transport, serial_path, sort) VALUES(4,'bar','Bar','winspool','POS-80',4)")
	d.Exec("INSERT INTO stations(id, code, name, transport, enabled, sort) VALUES(5,'off','停用','net',0,5)")
	m, _, kitchen := managerWithFakes(t, d)
	var alerts [][]StationHealth
	m.SetAlertCallback(func(bad []StationHealth) { alerts = append(alerts, bad) })

	old := db.NowMs() - stuckBacklogMs - 1000
	addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", serial: 1, createdAt: old})
	addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", serial: 2, state: "failed", next: db.NowMs() + 60_000})
	addJob(t, d, jobSpec{station: 2, ticketType: "厨总单", serial: 1, state: "dead"})
	d.Exec("UPDATE print_jobs SET last_error='b 错' WHERE serial_no=2 AND station_id=1")
	d.Exec("UPDATE print_jobs SET last_error='a 错' WHERE station_id=2")
	addJob(t, d, jobSpec{station: 3, ticketType: "凉菜单", serial: 1, createdAt: old})
	kitchen.SetOnline(false)

	h := m.RefreshHealth(true)
	if len(h) != 4 {
		t.Fatalf("got %d entries: %+v", len(h), h)
	}
	codes := []string{h[0].Code, h[1].Code, h[2].Code, h[3].Code}
	if strings.Join(codes, ",") != "front,kitchen,cold,bar" {
		t.Fatalf("order %v", codes)
	}
	f := h[0]
	if !f.Reachable || f.Pending != 2 || f.Dead != 0 || f.OldestPendingMs == nil || *f.OldestPendingMs != old ||
		f.Reason == nil || *f.Reason != "积压 2 张未出" || f.LastError == nil || *f.LastError != "b 错" {
		t.Fatalf("front: %+v reason %v", f, f.Reason)
	}
	k := h[1]
	if k.Reachable || *k.Reason != "离线/连接失败" || k.Dead != 1 || *k.LastError != "a 错" || k.OldestPendingMs != nil {
		t.Fatalf("kitchen: %+v", k)
	}
	c := h[2]
	if c.Reachable || *c.Reason != "配置错误：网口出单口缺少 IP 地址" || c.Pending != 1 || c.LastError != nil {
		t.Fatalf("cold: %+v", c)
	}
	b := h[3]
	if b.Reachable || *b.Reason != "配置错误：本机不是 Windows，不能用 winspool 打印机" {
		t.Fatalf("bar: %+v", b)
	}
	if len(alerts) != 1 || len(alerts[0]) != 4 {
		t.Fatalf("alerts %+v", alerts)
	}
	snap := m.HealthSnapshot()
	if len(snap) != 4 || snap[0].Reason == nil || *snap[0].Reason != "积压 2 张未出" {
		t.Fatalf("snapshot %+v", snap)
	}

	// Without probing, reachability is not looked at (and nothing is in trouble
	// once the queue is empty).
	d.Exec("DELETE FROM print_jobs")
	alerts = nil
	d.Exec("UPDATE stations SET enabled=0 WHERE id IN (3,4)")
	m.ReloadStations()
	m.SetTransport("front", transport.NewFake("front", true))
	m.SetTransport("kitchen", transport.NewFake("kitchen", false))
	h = m.RefreshHealth(false)
	if len(h) != 2 || !h[0].Reachable || !h[1].Reachable || h[1].Reason != nil || alerts != nil {
		t.Fatalf("probe=false: %+v alerts %v", h, alerts)
	}
}

func TestAlertCallbackPanicIsSwallowed(t *testing.T) {
	d := testDB(t)
	m, front, _ := managerWithFakes(t, d)
	front.SetOnline(false)
	m.SetAlertCallback(func([]StationHealth) { panic("ws gone") })
	if h := m.RefreshHealth(true); len(h) != 2 {
		t.Fatalf("health %+v", h)
	}
}

func TestSweepStuckSending(t *testing.T) {
	d := testDB(t)
	m, _, _ := managerWithFakes(t, d)
	stale := addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", serial: 1, state: "sending", next: db.NowMs() - 1})
	leased := addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", serial: 2, state: "sending", next: db.NowMs() + 60_000})
	if n := m.sweepStuckSending(); n != 1 {
		t.Fatalf("swept %d", n)
	}
	if r := jobRow(t, d, stale); r.Str("state") != "pending" || r.Int("next_attempt_at") != 0 {
		t.Fatalf("stale: %v", r.Map())
	}
	if s := jobRow(t, d, leased).Str("state"); s != "sending" {
		t.Fatalf("leased: %s", s)
	}
}

func TestWakeWithoutLoopSendsNow(t *testing.T) {
	d := testDB(t)
	m, front, _ := managerWithFakes(t, d)
	addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", payload: []byte("A"), serial: 1})
	m.Wake()
	if len(front.Sent()) != 1 {
		t.Fatal("Wake with no loop did not send")
	}
}

// sendGate lets the test hold a send in progress.
type sendGate struct {
	stubTransport
	entered chan struct{}
	release chan struct{}
}

func (g *sendGate) Send(p []byte) error {
	g.entered <- struct{}{}
	<-g.release
	return nil
}

func TestOneSenderPerStation(t *testing.T) {
	d := testDB(t)
	m, _, _ := managerWithFakes(t, d)
	g := &sendGate{entered: make(chan struct{}, 1), release: make(chan struct{})}
	m.SetTransport("front", g)
	addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", payload: []byte("A"), serial: 1})
	addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", payload: []byte("B"), serial: 2})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); m.processStationOnce(1, pmMaxJobs) }()
	<-g.entered
	if n := m.processStationOnce(1, pmMaxJobs); n != 0 {
		t.Fatalf("second sender sent %d", n)
	}
	close(g.release)
	go func() {
		for range g.entered {
		}
	}()
	wg.Wait()
	if n := d.QueryOne("SELECT COUNT(*) AS n FROM print_jobs WHERE state='done'").Int("n"); n != 2 {
		t.Fatalf("done %d", n)
	}
}

type panicTransport struct{ stubTransport }

func (panicTransport) Send([]byte) error { panic("driver bug") }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestLoopSurvivesPanicsAndStops(t *testing.T) {
	d := testDB(t)
	m := NewManager(d)
	m.Start()
	kitchen := transport.NewFake("kitchen", true)
	m.SetTransport("front", panicTransport{})
	m.SetTransport("kitchen", kitchen)
	stuck := addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", payload: []byte("P"), serial: 1})
	addJob(t, d, jobSpec{station: 2, ticketType: "厨总单", payload: []byte("K1"), serial: 1})
	m.Wake()
	// The panic aborts the round; the loop goes on and the next rounds send.
	addJob(t, d, jobSpec{station: 2, ticketType: "厨总单", payload: []byte("K2"), serial: 2})
	waitFor(t, "kitchen jobs", func() bool { return len(kitchen.Sent()) == 2 })
	if !m.alive() {
		t.Fatal("loop died")
	}
	if s := jobRow(t, d, stuck).Str("state"); s != "sending" {
		t.Fatalf("panicked job: %s (left in sending for the sweep)", s)
	}
	start := time.Now()
	m.Stop()
	if time.Since(start) > pmStopWait+time.Second || m.alive() {
		t.Fatalf("Stop took %v, alive %v", time.Since(start), m.alive())
	}
	m.EnsureAlive() // stopped on purpose: stays stopped
	if m.alive() {
		t.Fatal("EnsureAlive restarted a stopped manager")
	}
}

func TestEnsureAliveRestarts(t *testing.T) {
	d := testDB(t)
	m := NewManager(d)
	m.Start()
	// Simulate a loop that ended without Stop (the watchdog's case).
	m.Stop()
	atomic.StoreInt32(&m.stopped, 0)
	if m.alive() {
		t.Fatal("still alive after Stop")
	}
	m.EnsureAlive()
	if !m.alive() {
		t.Fatal("EnsureAlive did not restart")
	}
	front := transport.NewFake("front", true)
	m.SetTransport("front", front)
	addJob(t, d, jobSpec{station: 1, ticketType: "堂口单", payload: []byte("A"), serial: 1})
	m.Wake()
	waitFor(t, "send after restart", func() bool { return len(front.Sent()) == 1 })
	m.Stop()
}
