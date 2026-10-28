//go:build linux || darwin

// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package transport

// Serial port I/O the way pyserial 3.5 (serialposix.py) does it: open
// O_RDWR|O_NOCTTY|O_NONBLOCK, raw 8N1 termios at the station baud, DTR and
// RTS on, write with the write timeout, drain, close.

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// pyOSError is str(OSError) for a failed call on path: "[Errno 2] No such
// file or directory: '/dev/x'".
func pyOSError(err error, path string) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if path != "" {
			return fmt.Sprintf("%s: '%s'", errnoText(errno), path)
		}
		return errnoText(errno)
	}
	return err.Error()
}

func serialWrite(path string, baud int64, wt time.Duration, payload []byte) error {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		var errno syscall.Errno
		if errors.As(err, &errno) {
			return fmt.Errorf("[Errno %d] could not open port %s: %s", uintptr(errno), path, pyOSError(err, path))
		}
		return fmt.Errorf("could not open port %s: %v", path, err)
	}
	defer unix.Close(fd)
	if err := configurePort(fd, baud); err != nil {
		return err
	}
	for _, bit := range []int{unix.TIOCM_DTR, unix.TIOCM_RTS} {
		if err := unix.IoctlSetPointerInt(fd, unix.TIOCMBIS, bit); err != nil {
			if err != unix.EINVAL && err != unix.ENOTTY {
				return errors.New(pyOSError(err, ""))
			}
		}
	}
	flushInput(fd)
	if err := writeAll(fd, payload, wt); err != nil {
		return err
	}
	return drain(fd)
}

func configurePort(fd int, baud int64) error {
	t, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return termiosError(err)
	}
	t.Cflag |= unix.CLOCAL | unix.CREAD
	t.Lflag &^= unix.ICANON | unix.ECHO | unix.ECHOE | unix.ECHOK | unix.ECHONL | unix.ISIG | unix.IEXTEN |
		unix.ECHOCTL | unix.ECHOKE
	t.Oflag &^= unix.OPOST | unix.ONLCR | unix.OCRNL
	t.Iflag &^= unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IGNBRK | unix.PARMRK
	clearIUCLC(t)
	if !setSpeed(t, baud) {
		return fmt.Errorf("Invalid baud rate: %d", baud)
	}
	t.Cflag &^= unix.CSIZE
	t.Cflag |= unix.CS8
	t.Cflag &^= unix.CSTOPB
	t.Iflag &^= unix.INPCK | unix.ISTRIP
	t.Cflag &^= unix.PARENB | unix.PARODD
	t.Iflag &^= unix.IXON | unix.IXOFF | unix.IXANY
	t.Cflag &^= unix.CRTSCTS
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, t); err != nil {
		return termiosError(err)
	}
	return nil
}

// termiosError is SerialException("Could not configure port: (25, '...')").
func termiosError(err error) error {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return fmt.Errorf("Could not configure port: (%d, '%s')", uintptr(errno), capitalize(errno.Error()))
	}
	return fmt.Errorf("Could not configure port: %v", err)
}

func writeAll(fd int, d []byte, wt time.Duration) error {
	deadline := time.Now().Add(wt)
	for len(d) > 0 {
		n, err := unix.Write(fd, d)
		if err != nil {
			if err != unix.EAGAIN && err != unix.EALREADY && err != unix.EINPROGRESS && err != unix.EINTR {
				return fmt.Errorf("write failed: %s", pyOSError(err, ""))
			}
			n = 0
		}
		left := time.Until(deadline)
		if left <= 0 {
			return errors.New("Write timeout")
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
		ready, perr := unix.Poll(fds, int(left/time.Millisecond)+1)
		if perr != nil && perr != unix.EINTR {
			return fmt.Errorf("write failed: %s", pyOSError(perr, ""))
		}
		if ready == 0 && perr == nil {
			return errors.New("Write timeout")
		}
		if n > 0 {
			d = d[n:]
		}
		// pyserial checks the deadline once more after every chunk, even the
		// last one.
		if time.Until(deadline) <= 0 {
			return errors.New("Write timeout")
		}
	}
	return nil
}

// serialProbe: the device node exists and is writable (os.path.exists +
// os.access(W_OK)).
func serialProbe(path string) StatusResult {
	if _, err := os.Stat(path); err != nil {
		return StatusResult{Reachable: false, Reason: reason("离线/未插好")}
	}
	if err := unix.Access(path, unix.W_OK); err != nil {
		return StatusResult{Reachable: false, Reason: reason("无权限/未就绪")}
	}
	return StatusResult{Reachable: true}
}
