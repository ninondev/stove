//go:build !windows

// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package transport

// LocalSerialPorts: the COM port list is for Windows hosts, like the
// installed-printer list (LocalPrinters).
func LocalSerialPorts() ([]string, error) { return nil, ErrNoSpooler }
