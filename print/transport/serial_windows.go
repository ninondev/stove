//go:build windows

// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package transport

// COM ports the way pyserial 3.5 (serialwin32.py) opens them: \\.\ prefix
// for COM9 and up, 4096-byte queues, write timeout as the total write
// timeout constant, 8N1 binary with DTR and RTS on, purge, write, wait for
// the output queue to empty, close.

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DCB.Flags bits.
const (
	dcbBinary         = 0x0001
	dcbParity         = 0x0002
	dcbOutxCtsFlow    = 0x0004
	dcbOutxDsrFlow    = 0x0008
	dcbDtrControlMask = 0x0030
	dcbDsrSensitivity = 0x0040
	dcbOutX           = 0x0100
	dcbInX            = 0x0200
	dcbErrorChar      = 0x0400
	dcbNull           = 0x0800
	dcbRtsControlMask = 0x3000
	dcbAbortOnError   = 0x4000
	maxDword          = 0xFFFFFFFF
	commQueueSize     = 4096
	flushPollInterval = 50 * time.Millisecond
	xonChar, xoffChar = 0x11, 0x13
)

// winErrText is repr-free str(WinError): "[WinError 2] 系统找不到指定的文件。".
func winErrText(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errnoText(errno)
	}
	return err.Error()
}

// comNumber returns n for "COMn" or "\\.\COMn" (case-insensitive).
func comNumber(path string) (int, bool) {
	p := strings.ToUpper(strings.TrimPrefix(path, `\\.\`))
	if !strings.HasPrefix(p, "COM") {
		return 0, false
	}
	n, err := strconv.Atoi(p[3:])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// openName adds the \\.\ prefix pyserial adds (COM numbers above 8); COM10
// and up cannot be opened without it.
func openName(path string) string {
	if strings.HasPrefix(path, `\\.\`) {
		return path
	}
	if n, ok := comNumber(path); ok && n > 8 {
		return `\\.\` + path
	}
	return path
}

func serialWrite(path string, baud int64, wt time.Duration, payload []byte) error {
	name, err := windows.UTF16PtrFromString(openName(path))
	if err != nil {
		return fmt.Errorf("could not open port '%s': %v", path, err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return fmt.Errorf("could not open port '%s': %s", path, winErrText(err))
	}
	defer windows.CloseHandle(h)

	if err := windows.SetupComm(h, commQueueSize, commQueueSize); err != nil {
		return configErr(err)
	}
	ms := uint32(wt / time.Millisecond)
	if ms == 0 {
		ms = 1
	}
	// Reads are never used: return immediately. Writes give up after ms.
	timeouts := windows.CommTimeouts{ReadIntervalTimeout: maxDword, WriteTotalTimeoutConstant: ms}
	if err := windows.SetCommTimeouts(h, &timeouts); err != nil {
		return configErr(err)
	}
	var dcb windows.DCB
	dcb.DCBlength = uint32(unsafe.Sizeof(dcb))
	if err := windows.GetCommState(h, &dcb); err != nil {
		return configErr(err)
	}
	dcb.BaudRate = uint32(baud)
	dcb.ByteSize = 8
	dcb.Parity = windows.NOPARITY
	dcb.StopBits = windows.ONESTOPBIT
	dcb.Flags &^= dcbParity | dcbOutxCtsFlow | dcbOutxDsrFlow | dcbDtrControlMask | dcbDsrSensitivity |
		dcbOutX | dcbInX | dcbErrorChar | dcbNull | dcbRtsControlMask | dcbAbortOnError
	dcb.Flags |= dcbBinary | windows.DTR_CONTROL_ENABLE | windows.RTS_CONTROL_ENABLE
	dcb.XonChar = xonChar
	dcb.XoffChar = xoffChar
	if err := windows.SetCommState(h, &dcb); err != nil {
		return configErr(err)
	}
	_ = windows.PurgeComm(h, windows.PURGE_TXCLEAR|windows.PURGE_TXABORT|windows.PURGE_RXCLEAR|windows.PURGE_RXABORT)

	var n uint32
	if err := windows.WriteFile(h, payload, &n, nil); err != nil && err != windows.ERROR_IO_PENDING {
		return fmt.Errorf("WriteFile failed (%s)", winErrText(err))
	}
	if int(n) != len(payload) {
		return errors.New("Write timeout")
	}
	// flush(): wait for the driver's output queue to empty. pyserial waits
	// without limit; this waits at most the write timeout, then closes (the
	// bytes were already accepted, so reporting failure would print twice).
	deadline := time.Now().Add(wt)
	for time.Now().Before(deadline) {
		var errs uint32
		var st windows.ComStat
		if windows.ClearCommError(h, &errs, &st) != nil || st.CBOutQue == 0 {
			break
		}
		time.Sleep(flushPollInterval)
	}
	return nil
}

func configErr(err error) error {
	return fmt.Errorf("Cannot configure port, something went wrong. Original message: %s", winErrText(err))
}

// serialProbe: for a COM port, ask the object manager whether the device
// exists (QueryDosDevice) instead of opening it, so a probe never steals the
// port from a print in progress. Other paths: the file exists.
func serialProbe(path string) StatusResult {
	if n, ok := comNumber(path); ok {
		dev, err := windows.UTF16PtrFromString("COM" + strconv.Itoa(n))
		if err != nil {
			return StatusResult{Reachable: false, Reason: reason("离线/未插好")}
		}
		buf := make([]uint16, 1024)
		if _, err := windows.QueryDosDevice(dev, &buf[0], uint32(len(buf))); err != nil {
			return StatusResult{Reachable: false, Reason: reason("离线/未插好")}
		}
		return StatusResult{Reachable: true}
	}
	st, err := os.Stat(path)
	if err != nil {
		return StatusResult{Reachable: false, Reason: reason("离线/未插好")}
	}
	if st.Mode().IsRegular() && st.Mode().Perm()&0o200 == 0 {
		return StatusResult{Reachable: false, Reason: reason("无权限/未就绪")}
	}
	return StatusResult{Reachable: true}
}
