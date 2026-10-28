//go:build linux

// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package transport

import "golang.org/x/sys/unix"

const (
	ioctlGetTermios = unix.TCGETS
	ioctlSetTermios = unix.TCSETS
)

var linuxSpeeds = map[int64]uint32{
	50: unix.B50, 75: unix.B75, 110: unix.B110, 134: unix.B134, 150: unix.B150, 200: unix.B200,
	300: unix.B300, 600: unix.B600, 1200: unix.B1200, 1800: unix.B1800, 2400: unix.B2400,
	4800: unix.B4800, 9600: unix.B9600, 19200: unix.B19200, 38400: unix.B38400, 57600: unix.B57600,
	115200: unix.B115200, 230400: unix.B230400, 460800: unix.B460800, 500000: unix.B500000,
	576000: unix.B576000, 921600: unix.B921600, 1000000: unix.B1000000, 1152000: unix.B1152000,
	1500000: unix.B1500000, 2000000: unix.B2000000, 2500000: unix.B2500000, 3000000: unix.B3000000,
	3500000: unix.B3500000, 4000000: unix.B4000000,
}

func setSpeed(t *unix.Termios, baud int64) bool {
	sp, ok := linuxSpeeds[baud]
	if !ok {
		return false
	}
	t.Cflag &^= unix.CBAUD
	t.Cflag |= sp
	t.Ispeed = sp
	t.Ospeed = sp
	return true
}

func clearIUCLC(t *unix.Termios) { t.Iflag &^= unix.IUCLC }

func flushInput(fd int) { _ = unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH) }

// drain is termios.tcdrain (TCSBRK with a non-zero argument).
func drain(fd int) error {
	if err := unix.IoctlSetInt(fd, unix.TCSBRK, 1); err != nil {
		return termiosError(err)
	}
	return nil
}
