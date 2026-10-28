//go:build linux || darwin

// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package transport

import (
	"os"
	"path/filepath"
	"testing"
)

// The texts pyserial puts in SerialException, which end up in last_error.
func TestSerialErrorTexts(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "ttyNOPE")
	err := serialWrite(missing, 9600, 1e9, []byte("x"))
	want := "[Errno 2] could not open port " + missing + ": [Errno 2] No such file or directory: '" + missing + "'"
	if err == nil || err.Error() != want {
		t.Fatalf("missing port: %v\nwant %s", err, want)
	}

	plain := filepath.Join(dir, "not-a-tty")
	if err := os.WriteFile(plain, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err = serialWrite(plain, 9600, 1e9, []byte("x"))
	want = "Could not configure port: (25, 'Inappropriate ioctl for device')"
	if err == nil || err.Error() != want {
		t.Fatalf("regular file: %v\nwant %s", err, want)
	}
	// A regular writable file counts as present for the probe, like
	// os.path.exists + os.access(W_OK).
	if st := serialProbe(plain); !st.Reachable {
		t.Fatalf("probe of a writable file = %+v", st)
	}
}

func TestSerialBadBaud(t *testing.T) {
	m, slave := openPTY(t)
	defer m.Close()
	err := serialWrite(slave, 12345, 1e9, []byte("x"))
	if err == nil || err.Error() != "Invalid baud rate: 12345" {
		t.Fatalf("err = %v", err)
	}
}
