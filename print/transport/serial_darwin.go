//go:build darwin

// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package transport

import "golang.org/x/sys/unix"

const (
	ioctlGetTermios = unix.TIOCGETA
	ioctlSetTermios = unix.TIOCSETA
)

var darwinSpeeds = map[int64]bool{
	50: true, 75: true, 110: true, 134: true, 150: true, 200: true, 300: true, 600: true,
	1200: true, 1800: true, 2400: true, 4800: true, 7200: true, 9600: true, 14400: true,
	19200: true, 28800: true, 38400: true, 57600: true, 76800: true, 115200: true, 230400: true,
}

// setSpeed: on macOS the B constants are the numeric rates.
func setSpeed(t *unix.Termios, baud int64) bool {
	if !darwinSpeeds[baud] {
		return false
	}
	t.Ispeed = uint64(baud)
	t.Ospeed = uint64(baud)
	return true
}

func clearIUCLC(t *unix.Termios) {}

func flushInput(fd int) { _ = unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, 1) } // FREAD

// drain is termios.tcdrain.
func drain(fd int) error {
	if err := unix.IoctlSetInt(fd, unix.TIOCDRAIN, 0); err != nil {
		return termiosError(err)
	}
	return nil
}
