// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package db

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ninondev/stove/internal/pyfmt"
)

// Row is one result row (Python sqlite3.Row). Values are what SQLite gives:
// int64, pyfmt.F (REAL), string, []byte or nil.
type Row struct {
	Cols []string
	Vals []any
}

func (r *Row) idx(name string) int {
	for i, c := range r.Cols {
		if c == name {
			return i
		}
	}
	return -1
}

// Has reports whether the row has a column called name.
func (r *Row) Has(name string) bool { return r.idx(name) >= 0 }

// Get returns the value of the first column called name. A missing column
// panics, like row["missing"] raising IndexError in Python.
func (r *Row) Get(name string) any {
	i := r.idx(name)
	if i < 0 {
		panic(fmt.Errorf("no such column in row: %q", name))
	}
	return r.Vals[i]
}

// At returns the value at position i (row[0]).
func (r *Row) At(i int) any { return r.Vals[i] }

// IsNull reports whether the column is NULL.
func (r *Row) IsNull(name string) bool { return r.Get(name) == nil }

// Int returns an integer column; NULL is 0. REAL is truncated like int().
func (r *Row) Int(name string) int64 { return ToInt(r.Get(name)) }

// IntP returns an integer column, nil for NULL.
func (r *Row) IntP(name string) *int64 {
	v := r.Get(name)
	if v == nil {
		return nil
	}
	n := ToInt(v)
	return &n
}

// Float returns a numeric column as float64; NULL is 0.
func (r *Row) Float(name string) float64 { return ToFloat(r.Get(name)) }

// Str returns a text column; NULL is "".
func (r *Row) Str(name string) string {
	switch v := r.Get(name).(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return pyfmt.Str(v)
	}
}

// StrP returns a text column, nil for NULL.
func (r *Row) StrP(name string) *string {
	if r.Get(name) == nil {
		return nil
	}
	s := r.Str(name)
	return &s
}

// Bytes returns a BLOB column.
func (r *Row) Bytes(name string) []byte {
	switch v := r.Get(name).(type) {
	case []byte:
		return v
	case string:
		return []byte(v)
	}
	return nil
}

// Truthy is Python truthiness of the column value (`if row["x"]:`).
func (r *Row) Truthy(name string) bool { return pyfmt.Truthy(r.Get(name)) }

// Map is dict(row): every column, first occurrence wins on duplicate names.
func (r *Row) Map() map[string]any {
	m := make(map[string]any, len(r.Cols))
	for i, c := range r.Cols {
		if _, seen := m[c]; !seen {
			m[c] = r.Vals[i]
		}
	}
	return m
}

// Maps converts rows with Map.
func Maps(rows []Row) []map[string]any {
	out := make([]map[string]any, len(rows))
	for i := range rows {
		out[i] = rows[i].Map()
	}
	return out
}

// ToInt converts a SQLite/JSON value to int64 (NULL -> 0).
func ToInt(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case int64:
		return x
	case int:
		return int64(x)
	case pyfmt.F:
		return int64(x)
	case float64:
		return int64(x)
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			panic(fmt.Errorf("invalid literal for int(): %q", x))
		}
		return n
	case json.Number:
		n, err := strconv.ParseInt(string(x), 10, 64)
		if err != nil {
			panic(fmt.Errorf("invalid literal for int(): %q", string(x)))
		}
		return n
	}
	panic(fmt.Errorf("cannot convert %T to int", v))
}

// ToFloat converts a SQLite/JSON value to float64 (NULL -> 0).
func ToFloat(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case pyfmt.F:
		return float64(x)
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			panic(fmt.Errorf("could not convert string to float: %q", x))
		}
		return f
	}
	panic(fmt.Errorf("cannot convert %T to float", v))
}
