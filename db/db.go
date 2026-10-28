// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

// Package db is a small SQLite layer: one connection does all reads and
// writes, serialized by a lock that is re-entrant per goroutine. Tx is
// BEGIN IMMEDIATE/COMMIT at the outermost level and a SAVEPOINT when nested,
// so code can call Query or Tx from inside a transaction and see its own
// uncommitted writes.
//
// Errors are raised with panic, so service code does not repeat
// `if err != nil` on every statement. Callers recover at their boundary.
package db

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ninondev/stove/internal/pyfmt"
)

// schemaSQL is applied on every start; every statement is idempotent.
//
//go:embed schema.sql
var schemaSQL string

// NowMs is the current time as integer epoch milliseconds (UTC).
func NowMs() int64 { return time.Now().UnixMilli() }

// Result is what a write reports: cursor.lastrowid and cursor.rowcount.
type Result struct {
	LastInsertID int64
	RowsAffected int64
}

// Q is anything that runs statements: *DB (each Exec is its own transaction)
// or *Tx (raw statements inside the current transaction). Python code that
// takes `conn` or `db` and only runs statements should take Q.
type Q interface {
	Exec(query string, args ...any) Result
	Query(query string, args ...any) []Row
	QueryOne(query string, args ...any) *Row
}

// Error wraps a SQLite error with the statement that failed.
type Error struct {
	Err error
	SQL string
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// IsIntegrity reports a constraint violation.
func IsIntegrity(err error) bool {
	return err != nil && strings.Contains(err.Error(), "constraint failed")
}

// DB owns the single connection.
type DB struct {
	Path    string
	sqldb   *sql.DB
	conn    *sql.Conn
	mu      sync.Mutex
	owner   int64
	held    int
	txDepth int
}

// Open connects and applies the connection PRAGMAs (writer settings).
func Open(path string) (*DB, error) {
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "" {
			_ = os.MkdirAll(dir, 0o755)
		}
	}
	sqldb, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	sqldb.SetMaxOpenConns(1)
	sqldb.SetMaxIdleConns(1)
	sqldb.SetConnMaxLifetime(0)
	sqldb.SetConnMaxIdleTime(0)
	conn, err := sqldb.Conn(context.Background())
	if err != nil {
		sqldb.Close()
		return nil, err
	}
	d := &DB{Path: path, sqldb: sqldb, conn: conn}
	err = func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
			}
		}()
		d.rawQuery("PRAGMA journal_mode=WAL;")
		d.rawExec("PRAGMA foreign_keys=ON;")
		d.rawExec("PRAGMA synchronous=FULL;")
		d.rawExec("PRAGMA busy_timeout=5000;")
		d.rawExec("PRAGMA temp_store=MEMORY;")
		d.rawQuery("PRAGMA wal_autocheckpoint=1000;")
		return nil
	}()
	if err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// Close closes the connection.
func (d *DB) Close() {
	d.lock()
	defer d.unlock()
	if d.conn != nil {
		d.conn.Close()
		d.conn = nil
	}
	if d.sqldb != nil {
		d.sqldb.Close()
		d.sqldb = nil
	}
}

// --- re-entrant lock ---------------------------------------------------------

func goid() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	s := bytes.TrimPrefix(buf[:n], []byte("goroutine "))
	if i := bytes.IndexByte(s, ' '); i > 0 {
		id, _ := strconv.ParseInt(string(s[:i]), 10, 64)
		return id
	}
	return -1
}

func (d *DB) lock() {
	g := goid()
	if atomic.LoadInt64(&d.owner) == g {
		d.held++
		return
	}
	d.mu.Lock()
	atomic.StoreInt64(&d.owner, g)
	d.held = 1
}

func (d *DB) unlock() {
	d.held--
	if d.held == 0 {
		atomic.StoreInt64(&d.owner, 0)
		d.mu.Unlock()
	}
}

// --- raw statements (caller holds the lock) ---------------------------------

func convArg(a any) any {
	switch v := a.(type) {
	case nil:
		return nil
	case bool:
		if v {
			return int64(1)
		}
		return int64(0)
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	case uint8:
		return int64(v)
	case float64:
		return v
	case pyfmt.F:
		return float64(v)
	case string:
		return v
	case []byte:
		return v
	case *int64:
		if v == nil {
			return nil
		}
		return *v
	case *int:
		if v == nil {
			return nil
		}
		return int64(*v)
	case *string:
		if v == nil {
			return nil
		}
		return *v
	case *float64:
		if v == nil {
			return nil
		}
		return *v
	case *bool:
		if v == nil {
			return nil
		}
		return convArg(*v)
	}
	return a
}

func convArgs(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = convArg(a)
	}
	return out
}

func (d *DB) rawExec(query string, args ...any) Result {
	res, err := d.conn.ExecContext(context.Background(), query, convArgs(args)...)
	if err != nil {
		panic(&Error{Err: err, SQL: query})
	}
	id, _ := res.LastInsertId()
	n, _ := res.RowsAffected()
	return Result{LastInsertID: id, RowsAffected: n}
}

func (d *DB) rawQuery(query string, args ...any) []Row {
	rows, err := d.conn.QueryContext(context.Background(), query, convArgs(args)...)
	if err != nil {
		panic(&Error{Err: err, SQL: query})
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		panic(&Error{Err: err, SQL: query})
	}
	var out []Row
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			panic(&Error{Err: err, SQL: query})
		}
		for i, v := range vals {
			if f, ok := v.(float64); ok {
				vals[i] = pyfmt.F(f) // REAL values keep their float type
			}
		}
		out = append(out, Row{Cols: cols, Vals: vals})
	}
	if err := rows.Err(); err != nil {
		panic(&Error{Err: err, SQL: query})
	}
	return out
}

// --- public API ---------------------------------------------------------------

// Tx runs fn in a serialized write transaction: commit on return, roll back
// if fn panics (the panic continues). Nested calls use SAVEPOINTs, so a
// caught inner failure undoes only the inner work.
func (d *DB) Tx(fn func(tx *Tx)) {
	d.lock()
	defer d.unlock()
	depth := d.txDepth
	sp := fmt.Sprintf("sp_%d", depth)
	if depth == 0 {
		d.rawExec("BEGIN IMMEDIATE;")
	} else {
		d.rawExec("SAVEPOINT " + sp + ";")
	}
	d.txDepth = depth + 1
	done := false
	defer func() {
		d.txDepth = depth
		if done {
			return
		}
		// fn panicked: undo, never letting the rollback's own error mask the cause.
		func() {
			defer func() { _ = recover() }()
			if depth == 0 {
				d.rawExec("ROLLBACK;")
			} else {
				d.rawExec("ROLLBACK TO " + sp + ";")
				d.rawExec("RELEASE " + sp + ";")
			}
		}()
	}()
	fn(&Tx{d: d})
	if depth == 0 {
		if _, err := d.conn.ExecContext(context.Background(), "COMMIT;"); err != nil {
			func() {
				defer func() { _ = recover() }()
				d.rawExec("ROLLBACK;")
			}()
			done = true
			panic(&Error{Err: err, SQL: "COMMIT;"})
		}
	} else {
		d.rawExec("RELEASE " + sp + ";")
	}
	done = true
}

// Exec runs one statement in its own transaction.
func (d *DB) Exec(query string, args ...any) (r Result) {
	d.Tx(func(tx *Tx) { r = tx.Exec(query, args...) })
	return
}

// ExecMany runs one statement per argument list in one transaction.
func (d *DB) ExecMany(query string, argLists [][]any) {
	d.Tx(func(tx *Tx) {
		for _, a := range argLists {
			tx.Exec(query, a...)
		}
	})
}

// Query returns all rows.
func (d *DB) Query(query string, args ...any) []Row {
	d.lock()
	defer d.unlock()
	return d.rawQuery(query, args...)
}

// QueryOne returns the first row or nil.
func (d *DB) QueryOne(query string, args ...any) *Row {
	rows := d.Query(query, args...)
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

// InTx reports whether the calling goroutine is inside d.Tx.
func (d *DB) InTx() bool {
	return atomic.LoadInt64(&d.owner) == goid() && d.txDepth > 0
}

// Tx is the handle passed to a transaction body.
type Tx struct{ d *DB }

// DB returns the database this transaction belongs to.
func (t *Tx) DB() *DB { return t.d }

func (t *Tx) Exec(query string, args ...any) Result { return t.d.rawExec(query, args...) }
func (t *Tx) Query(query string, args ...any) []Row { return t.d.rawQuery(query, args...) }
func (t *Tx) QueryOne(query string, args ...any) *Row {
	rows := t.d.rawQuery(query, args...)
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

// Nested runs fn inside a SAVEPOINT of this transaction.
func (t *Tx) Nested(fn func(tx *Tx)) { t.d.Tx(fn) }

// --- schema -----------------------------------------------------------------

// InitSchema applies schema.sql. It is safe to call on every start.
func (d *DB) InitSchema() {
	d.lock()
	defer d.unlock()
	d.rawExec(schemaSQL)
}

// IntegrityOK runs PRAGMA integrity_check.
func (d *DB) IntegrityOK() bool {
	r := d.QueryOne("PRAGMA integrity_check;")
	return r != nil && r.At(0) == "ok"
}

// Placeholders returns "?,?,?" for n parameters.
func Placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// Args converts a slice of ids to []any for an IN (...) query.
func Args[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}
