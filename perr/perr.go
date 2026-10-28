// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

// Package perr holds the errors that service code raises with panic(...).
// Callers turn them into a message at their own boundary with Catch or Try.
package perr

import "fmt"

// Error kinds.
const (
	KindValidation = "ValidationError"
	KindNotFound   = "NotFoundError"
)

// Error is a failure the caller can show to the user.
type Error struct {
	Kind    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// Validation is a request the code refuses because of its content or state.
func Validation(msg string) *Error { return &Error{Kind: KindValidation, Message: msg} }

// NotFound is a request for a row that does not exist.
func NotFound(msg string) *Error { return &Error{Kind: KindNotFound, Message: msg} }

// Catch runs fn and returns the *Error it panicked with, or nil. Any other
// panic propagates.
func Catch(fn func()) (caught *Error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(*Error); ok {
				caught = e
				return
			}
			panic(r)
		}
	}()
	fn()
	return nil
}

// Try runs fn and returns whatever it panicked with as an error (nil if it
// returned normally).
func Try(fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			switch v := r.(type) {
			case error:
				err = v
			default:
				err = fmt.Errorf("%v", v)
			}
		}
	}()
	fn()
	return nil
}

// Swallow runs fn and ignores any panic.
func Swallow(fn func()) { _ = Try(fn) }
