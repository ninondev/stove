//go:build linux

// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package transport

import (
	"os"
	"strconv"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// openPTY opens a pseudo-terminal pair: the master end and the slave path.
func openPTY(t *testing.T) (*os.File, string) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	fd := int(m.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		t.Skipf("unlockpt: %v", err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		m.Close()
		t.Skipf("ptsname: %v", err)
	}
	return m, "/dev/pts/" + strconv.Itoa(n)
}
