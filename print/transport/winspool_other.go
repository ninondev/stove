//go:build !windows

// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package transport

// newWinspool: winspool printers exist only on Windows; elsewhere the
// station is reported as misconfigured.
func newWinspool(name string) (Transport, error) {
	return nil, &ConfigError{"本机不是 Windows，不能用 winspool 打印机"}
}

// LocalPrinters: only Windows has an installed-printer list.
func LocalPrinters() ([]LocalPrinter, error) { return nil, ErrNoSpooler }
