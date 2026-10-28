// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package printing

import (
	"encoding/json"

	"github.com/ninondev/stove/db"
)

// Ticket categories that a print policy can name.
var policyCategories = []string{"kitchen", "merge", "move"}

// Policy values: "auto" prints at once, "manual" holds the job until
// ReleaseHolds, "off" makes no job at all.
const (
	PolicyAuto   = "auto"
	PolicyManual = "manual"
	PolicyOff    = "off"
)

func validPolicy(v string) bool { return v == PolicyAuto || v == PolicyManual || v == PolicyOff }

func defaultPolicy() map[string]string {
	out := map[string]string{}
	for _, c := range policyCategories {
		out[c] = PolicyAuto
	}
	return out
}

// GetPolicy reads the stored print policy; a missing or damaged value falls
// back to "auto" for every category.
func GetPolicy(q db.Q) map[string]string {
	out := defaultPolicy()
	row := q.QueryOne("SELECT value FROM app_settings WHERE key='print_policy'")
	if row == nil {
		return out
	}
	var stored map[string]any
	if json.Unmarshal([]byte(row.Str("value")), &stored) != nil {
		return out
	}
	for _, c := range policyCategories {
		if v, ok := stored[c].(string); ok && validPolicy(v) {
			out[c] = v
		}
	}
	return out
}

// SetPolicy stores the policy (unknown categories and values are dropped)
// and writes an audit row. It returns what was stored.
func SetPolicy(d *db.DB, policy map[string]string) map[string]string {
	clean := defaultPolicy()
	for _, c := range policyCategories {
		if v, ok := policy[c]; ok && validPolicy(v) {
			clean[c] = v
		}
	}
	raw, _ := json.Marshal(clean)
	d.Tx(func(tx *db.Tx) {
		tx.Exec("INSERT INTO app_settings(key,value) VALUES('print_policy',?) "+
			"ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(raw))
		tx.Exec("INSERT INTO audit_log(kind,ref_table,detail,created_at) "+
			"VALUES('print_policy','app_settings',?,?)", string(raw), db.NowMs())
	})
	return clean
}
