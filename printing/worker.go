// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package printing

// Per-station print workers and the health registry.
//
//   - Per station, jobs go out strictly in enqueue_seq order, one at a time;
//     on the first failure the station stops for this tick (never skip ahead).
//   - The send happens outside any DB transaction: claim (->sending) in one
//     short transaction, send, record the result in another.
//   - Failures back off (exponential with jitter); after RetryMaxAttempts a
//     job is dead (kept for reprint) and raises an alarm.
//   - Health comes from the transport probe. Sending is never gated on it.

import (
	"fmt"
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ninondev/stove/db"
	"github.com/ninondev/stove/internal/config"
	"github.com/ninondev/stove/internal/pyfmt"
	"github.com/ninondev/stove/perr"
	"github.com/ninondev/stove/print/transport"
)

const (
	// A reachable station whose oldest queued job is older than this is
	// "stuck" (poison job at the head, or a printer that accepts TCP but
	// never prints) and raises an alert.
	stuckBacklogMs = 180_000
	// Claim lease: a job stuck in 'sending' past this is put back by the sweep.
	sendingLeaseMs = 60_000
	// After a failed send the station stays offline this long even if the
	// probe connects (half-dead printers that accept TCP but swallow writes).
	sendFailCooldownMs = 30_000

	pmInterval    = time.Second
	pmHealthEvery = 5
	pmSweepEvery  = 30
	pmMaxJobs     = 50
	pmStopWait    = 3 * time.Second
)

// backoffSeconds picks schedule[min(attempts, last)], capped, with jitter in
// [base/2, base).
func backoffSeconds(attempts int64) float64 {
	sched := config.RetryBackoffS
	i := attempts
	if i > int64(len(sched)-1) {
		i = int64(len(sched) - 1)
	}
	base := float64(sched[i])
	if c := float64(config.RetryBackoffCapS); c < base {
		base = c
	}
	lo := base * 0.5
	return lo + float64((base-lo)*rand.Float64())
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

type pmStation struct {
	id        int64
	code      string
	name      string
	transport transport.Transport // guarded by Manager.mu
	sendMu    sync.Mutex          // one claim->send sequence per station at a time
	failUntil int64               // atomic: end of the send-failure cooldown (ms)
}

type pmMisconfigured struct {
	id         int64
	code, name string
	err        string
}

type pmRun struct{ done chan struct{} }

// Manager sends the print jobs of every enabled station and keeps their
// health.
type Manager struct {
	d *db.DB

	mu            sync.Mutex
	order         []int64 // station ids in row order
	stations      map[int64]*pmStation
	misconfigured []pmMisconfigured
	healthOrder   []string
	health        map[string]*StationHealth
	alertCb       func([]StationHealth)
	run           *pmRun

	stopped int32 // atomic
	wake    chan struct{}
}

// NewManager builds a Manager for d. Start loads the stations and runs the
// background loop.
func NewManager(d *db.DB) *Manager {
	return &Manager{
		d:        d,
		stations: map[int64]*pmStation{},
		health:   map[string]*StationHealth{},
		wake:     make(chan struct{}, 1),
	}
}

// --- setup -------------------------------------------------------------------

// ReloadStations rebuilds the transports from the enabled stations. A station
// whose transport cannot be built is kept as misconfigured so it shows up as
// unreachable instead of silently disappearing with its queue.
func (m *Manager) ReloadStations() {
	rows := m.d.Query("SELECT * FROM stations WHERE enabled=1")
	order := []int64{}
	stations := map[int64]*pmStation{}
	var misconf []pmMisconfigured
	for i := range rows {
		r := &rows[i]
		tr, err := transport.Build(r.Str("transport"), r.StrP("net_host"), r.Int("net_port"),
			r.StrP("serial_path"), r.Int("serial_baud"))
		if err != nil {
			log.Printf("WARNING print: station %q (id=%s) cannot be enabled: %s. Its jobs wait until the settings are fixed.",
				pyfmt.Str(r.Get("name")), pyfmt.Str(r.Get("id")), err.Error())
			misconf = append(misconf, pmMisconfigured{r.Int("id"), r.Str("code"), r.Str("name"), err.Error()})
			continue
		}
		id := r.Int("id")
		if _, seen := stations[id]; !seen {
			order = append(order, id)
		}
		stations[id] = &pmStation{id: id, code: r.Str("code"), name: r.Str("name"), transport: tr}
	}
	m.mu.Lock()
	m.order = order
	m.stations = stations
	m.misconfigured = misconf
	m.mu.Unlock()
}

// SetAlertCallback sets the function given the stations in trouble after
// every health refresh that finds some.
func (m *Manager) SetAlertCallback(cb func(bad []StationHealth)) {
	m.mu.Lock()
	m.alertCb = cb
	m.mu.Unlock()
}

// SetTransport overrides a station's transport (tests, or a runtime swap).
func (m *Manager) SetTransport(stationCode string, tr transport.Transport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.order {
		if st := m.stations[id]; st.code == stationCode {
			st.transport = tr
		}
	}
}

// --- claiming & recording ------------------------------------------------------

func (m *Manager) claimNext(stationID int64) *db.Row {
	ts := db.NowMs()
	var job *db.Row
	m.d.Tx(func(tx *db.Tx) {
		row := tx.QueryOne(`SELECT * FROM print_jobs
                   WHERE station_id=? AND state IN ('pending','failed')
                     AND next_attempt_at <= ?
                   ORDER BY enqueue_seq ASC LIMIT 1`, stationID, ts)
		if row == nil {
			return
		}
		tx.Exec("UPDATE print_jobs SET state='sending', next_attempt_at=? WHERE id=?",
			ts+sendingLeaseMs, row.Get("id"))
		job = row
	})
	return job
}

func (m *Manager) recordSuccess(jobID any) {
	m.d.Tx(func(tx *db.Tx) {
		tx.Exec("UPDATE print_jobs SET state='done', sent_at=?, last_error=NULL WHERE id=?", db.NowMs(), jobID)
	})
}

func (m *Manager) recordFailure(job *db.Row, errText string) {
	attempts := job.Int("attempts") + 1
	dead := attempts >= int64(config.RetryMaxAttempts)
	msg := truncRunes(errText, 500)
	m.d.Tx(func(tx *db.Tx) {
		state := "failed"
		if dead {
			state = "dead"
		}
		tx.Exec(`UPDATE print_jobs
                   SET state=?, attempts=?, last_error=?, next_attempt_at=?
                   WHERE id=?`,
			state, attempts, msg, db.NowMs()+int64(backoffSeconds(attempts)*1000), job.Get("id"))
		if dead {
			tx.Exec("INSERT INTO audit_log(kind, ref_table, ref_id, detail, created_at) "+
				"VALUES('print_dead','print_jobs',?,?,?)", job.Get("id"), msg, db.NowMs())
		}
	})
}

// --- processing -------------------------------------------------------------------

// processStationOnce sends due jobs for one station, in order, until one
// fails or none are left.
func (m *Manager) processStationOnce(stationID int64, maxJobs int) int {
	m.mu.Lock()
	st := m.stations[stationID]
	var h *StationHealth
	reachable := true
	if st != nil {
		h = m.health[st.code]
		if h != nil {
			reachable = h.Reachable
		}
	}
	m.mu.Unlock()
	if st == nil {
		return 0
	}
	// Circuit breaker: a station last probed offline is skipped, so a dead
	// printer never holds up the tick (its jobs stay queued).
	if h != nil && !reachable {
		return 0
	}
	// Another goroutine is sending for this station: skip, it keeps order.
	if !st.sendMu.TryLock() {
		return 0
	}
	defer st.sendMu.Unlock()
	sent := 0
	for i := 0; i < maxJobs; i++ {
		job := m.claimNext(stationID)
		if job == nil {
			break
		}
		m.mu.Lock()
		tr := st.transport
		m.mu.Unlock()
		payload := job.Bytes("payload")
		if err := tr.Send(payload); err != nil {
			atomic.StoreInt64(&st.failUntil, db.NowMs()+sendFailCooldownMs)
			if h != nil {
				m.mu.Lock()
				h.Reachable = false
				why := "发送失败/无响应"
				h.Reason = &why
				m.mu.Unlock()
			}
			// A failed write-back leaves the job in 'sending'; the sweep puts
			// it back after the lease.
			perr.Swallow(func() { m.recordFailure(job, err.Error()) })
			break // preserve order: never skip past a stuck job
		}
		// Printed; if recording fails the sweep re-sends (a duplicate beats a
		// missing ticket).
		perr.Swallow(func() { m.recordSuccess(job.Get("id")) })
		sent++
	}
	return sent
}

func (m *Manager) tick() int {
	m.mu.Lock()
	ids := append([]int64(nil), m.order...)
	m.mu.Unlock()
	total := 0
	for _, id := range ids {
		total += m.processStationOnce(id, pmMaxJobs)
	}
	return total
}

// Tick sends one round for every station.
func (m *Manager) Tick() { m.tick() }

func (m *Manager) isStopped() bool { return atomic.LoadInt32(&m.stopped) != 0 }

func (m *Manager) alive() bool {
	m.mu.Lock()
	r := m.run
	m.mu.Unlock()
	if r == nil {
		return false
	}
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Wake makes the background loop run a round now without blocking the
// caller. With no loop running (tests, or between watchdog restarts) it
// runs the round synchronously so nothing is left unsent.
func (m *Manager) Wake() {
	if m.alive() && !m.isStopped() {
		m.signal()
		return
	}
	m.tick()
}

// sweepStuckSending puts jobs stuck in 'sending' past their lease back to
// pending so they re-send without a restart.
func (m *Manager) sweepStuckSending() int64 {
	return m.d.Exec("UPDATE print_jobs SET state='pending', next_attempt_at=0 "+
		"WHERE state='sending' AND next_attempt_at < ?", db.NowMs()).RowsAffected
}

// --- health -------------------------------------------------------------------------

// RefreshHealth recomputes every station's health (probing the printers
// when probe is set), publishes it, and calls the alert callback with the
// stations that are unreachable, have dead jobs, or have a stuck backlog.
func (m *Manager) RefreshHealth(probe bool) []StationHealth {
	m.mu.Lock()
	type stRef struct {
		st *pmStation
		tr transport.Transport
	}
	sts := make([]stRef, 0, len(m.order))
	for _, id := range m.order {
		st := m.stations[id]
		sts = append(sts, stRef{st, st.transport})
	}
	misconf := append([]pmMisconfigured(nil), m.misconfigured...)
	m.mu.Unlock()

	var result []*StationHealth
	for _, s := range sts {
		counts := m.d.QueryOne(`SELECT
                     SUM(CASE WHEN state IN ('pending','failed') THEN 1 ELSE 0 END) AS pending,
                     SUM(CASE WHEN state='dead' THEN 1 ELSE 0 END) AS dead,
                     MIN(CASE WHEN state IN ('pending','failed') THEN created_at END) AS oldest,
                     MAX(CASE WHEN state IN ('failed','dead') THEN last_error END) AS err
                   FROM print_jobs WHERE station_id=?`, s.st.id)
		h := &StationHealth{Code: s.st.code, Name: s.st.name, Reachable: true}
		if counts != nil {
			h.Pending = counts.Int("pending")
			h.Dead = counts.Int("dead")
			h.OldestPendingMs = counts.IntP("oldest")
			h.LastError = counts.StrP("err")
		}
		if probe {
			status := s.tr.Probe()
			h.Reachable = status.Reachable
			h.Reason = status.Reason
		}
		if atomic.LoadInt64(&s.st.failUntil) > db.NowMs() {
			h.Reachable = false
			if h.Reason == nil || *h.Reason == "" {
				why := "发送失败冷却中"
				h.Reason = &why
			}
		}
		result = append(result, h)
	}
	for _, mc := range misconf {
		counts := m.d.QueryOne("SELECT SUM(CASE WHEN state IN ('pending','failed') THEN 1 ELSE 0 END) AS pending, "+
			"MIN(CASE WHEN state IN ('pending','failed') THEN created_at END) AS oldest "+
			"FROM print_jobs WHERE station_id=?", mc.id)
		why := "配置错误：" + mc.err
		h := &StationHealth{Code: mc.code, Name: mc.name, Reachable: false, Reason: &why}
		if counts != nil {
			h.Pending = counts.Int("pending")
			h.OldestPendingMs = counts.IntP("oldest")
		}
		result = append(result, h)
	}

	m.mu.Lock()
	order := []string{}
	byCode := map[string]*StationHealth{}
	for _, h := range result {
		if _, seen := byCode[h.Code]; !seen {
			order = append(order, h.Code)
		}
		byCode[h.Code] = h
	}
	m.healthOrder = order
	m.health = byCode
	now := db.NowMs()
	var bad []StationHealth
	for _, h := range result {
		stuck := h.OldestPendingMs != nil && now-*h.OldestPendingMs > stuckBacklogMs
		if stuck && h.Reachable && (h.Reason == nil || *h.Reason == "") {
			why := fmt.Sprintf("积压 %d 张未出", h.Pending)
			h.Reason = &why
		}
		if !h.Reachable || h.Dead > 0 || stuck {
			bad = append(bad, *h)
		}
	}
	out := make([]StationHealth, len(result))
	for i, h := range result {
		out[i] = *h
	}
	cb := m.alertCb
	m.mu.Unlock()

	if len(bad) > 0 && cb != nil {
		perr.Swallow(func() { cb(bad) })
	}
	return out
}

// HealthSnapshot returns the last published health, in station order.
func (m *Manager) HealthSnapshot() []StationHealth {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]StationHealth, 0, len(m.healthOrder))
	for _, c := range m.healthOrder {
		out = append(out, *m.health[c])
	}
	return out
}

// --- background loop -------------------------------------------------------------

// Start reloads the stations and starts the background loop: a send round
// every second (or at once on Wake), a probe every 5th round, a sweep every
// 30th. A panic in a round is swallowed and the loop goes on.
func (m *Manager) Start() {
	m.ReloadStations()
	atomic.StoreInt32(&m.stopped, 0)
	r := &pmRun{done: make(chan struct{})}
	m.mu.Lock()
	m.run = r
	m.mu.Unlock()
	go m.loop(r)
}

func (m *Manager) loop(r *pmRun) {
	defer close(r.done)
	timer := time.NewTimer(pmInterval)
	defer timer.Stop()
	for i := 0; !m.isStopped(); i++ {
		n := i
		perr.Swallow(func() {
			m.tick()
			if n%pmHealthEvery == 0 {
				m.RefreshHealth(true)
			}
			if n%pmSweepEvery == 0 {
				m.sweepStuckSending()
			}
		})
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(pmInterval)
		select {
		case <-m.wake:
		case <-timer.C:
		}
	}
}

// Stop ends the loop, waiting at most 3s for the current round.
func (m *Manager) Stop() {
	atomic.StoreInt32(&m.stopped, 1)
	m.signal()
	m.mu.Lock()
	r := m.run
	m.mu.Unlock()
	if r == nil {
		return
	}
	t := time.NewTimer(pmStopWait)
	defer t.Stop()
	select {
	case <-r.done:
	case <-t.C:
	}
}

// EnsureAlive is the watchdog: restart the loop if it ended unexpectedly.
func (m *Manager) EnsureAlive() {
	if m.isStopped() {
		return
	}
	if m.alive() {
		return
	}
	m.Start()
}

// StationHealth is one station's state as last probed.
type StationHealth struct {
	Code            string  `json:"code"`
	Name            string  `json:"name"`
	Reachable       bool    `json:"reachable"`
	Reason          *string `json:"reason"`
	Pending         int64   `json:"pending"`
	Dead            int64   `json:"dead"`
	LastError       *string `json:"last_error"`
	OldestPendingMs *int64  `json:"oldest_pending_ms"`
}
