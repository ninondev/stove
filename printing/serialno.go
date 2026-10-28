// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package printing

import "github.com/ninondev/stove/db"

// NextSerial atomically allocates the next serial number for a station. Each
// station counts on its own, so a gap or a repeat in the printed numbers
// shows a lost or doubled ticket. The caller is inside a transaction.
func NextSerial(q db.Q, stationID int64) int64 {
	q.Exec("INSERT INTO station_serials(station_id, next_serial) VALUES(?, 1) "+
		"ON CONFLICT(station_id) DO NOTHING", stationID)
	row := q.QueryOne("SELECT next_serial FROM station_serials WHERE station_id=?", stationID)
	serial := row.Int("next_serial")
	q.Exec("UPDATE station_serials SET next_serial = next_serial + 1 WHERE station_id=?", stationID)
	return serial
}

// ReconcileSerials makes next_serial larger than the highest printed serial
// of every station. Call it at start-up, in case the counters fell behind.
func ReconcileSerials(q db.Q) {
	rows := q.Query("SELECT station_id, MAX(serial_no) AS m FROM print_jobs GROUP BY station_id")
	for i := range rows {
		r := &rows[i]
		m := r.Int("m") + 1
		q.Exec("INSERT INTO station_serials(station_id, next_serial) VALUES(?, ?) "+
			"ON CONFLICT(station_id) DO UPDATE SET next_serial = MAX(next_serial, ?)",
			r.Get("station_id"), m, m)
	}
}
