//go:build darwin

// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package transport

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPTY opens a pseudo-terminal pair: the master end (to read what was
// "printed") and the path of the slave end (the "serial port").
func openPTY(t *testing.T) (*os.File, string) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	fd := int(m.Fd())
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		m.Close()
		t.Skipf("grantpt: %v", err)
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		m.Close()
		t.Skipf("unlockpt: %v", err)
	}
	buf := make([]byte, 128)
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME),
		uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
		m.Close()
		t.Skipf("ptsname: %v", e)
	}
	return m, string(buf[:bytes.IndexByte(buf, 0)])
}

// A real tty: raw mode must pass every byte through untouched (no CR/LF
// translation, no control-character processing).
func TestSerialSendThroughPTY(t *testing.T) {
	m, slave := openPTY(t)
	defer m.Close()
	payload := []byte("\x1b@\x1b\x39\x00测试\n\r\x03\x11\x13\x7f\x1bd\x01\x1dVB\x03\x1bp\x00\x19\xfa")
	got := make(chan []byte, 1)
	go func() {
		var all []byte
		buf := make([]byte, 256)
		for len(all) < len(payload) {
			n, err := m.Read(buf)
			all = append(all, buf[:n]...)
			if err != nil {
				break
			}
		}
		got <- all
	}()
	s := NewSerial(slave, 9600)
	s.WriteTimeout = 2 * time.Second
	if err := s.Send(payload); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-got:
		if !bytes.Equal(b, payload) {
			t.Fatalf("pty got %q\nwant    %q", b, payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing arrived on the pty")
	}
	if st := s.Probe(); !st.Reachable {
		t.Fatalf("probe = %+v", st)
	}
}
