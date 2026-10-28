// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package pyfmt

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PyInt is Python int(v) on JSON/SQLite values. ok is false where Python
// raises TypeError or ValueError (callers usually fall back to a default).
func PyInt(v any) (int64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, false
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case int:
		return int64(x), true
	case int64:
		return x, true
	case int32:
		return int64(x), true
	case F:
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return int64(f), true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return int64(x), true
	case json.Number:
		if n, err := strconv.ParseInt(x.String(), 10, 64); err == nil {
			return n, true
		}
		f, err := strconv.ParseFloat(x.String(), 64)
		if err != nil {
			return 0, false
		}
		return int64(f), true
	case string:
		s := strings.TrimSpace(x)
		s = strings.ReplaceAll(s, "_", "") // int("1_000") == 1000
		if s == "" {
			return 0, false
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// RuneLen is len(s) in Python (code points).
func RuneLen(s string) int { return utf8.RuneCountInString(s) }
