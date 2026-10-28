//go:build !linux && !darwin && !windows

// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package transport

import (
	"os"
	"time"
)

// serialWrite on other systems is the "pyserial unavailable" fallback: a raw
// write to the device node, which works when the tty is already configured.
func serialWrite(path string, baud int64, wt time.Duration, payload []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(payload)
	return err
}

func serialProbe(path string) StatusResult {
	st, err := os.Stat(path)
	if err != nil {
		return StatusResult{Reachable: false, Reason: reason("离线/未插好")}
	}
	if st.Mode().IsRegular() && st.Mode().Perm()&0o222 == 0 {
		return StatusResult{Reachable: false, Reason: reason("无权限/未就绪")}
	}
	return StatusResult{Reachable: true}
}
