//go:build windows

// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package transport

import (
	"testing"
	"unsafe"
)

func TestOpenName(t *testing.T) {
	cases := map[string]string{
		"COM3":       "COM3",
		"com8":       "com8",
		"COM9":       `\\.\COM9`,
		"COM10":      `\\.\COM10`,
		`\\.\COM12`:  `\\.\COM12`,
		"COMx":       "COMx",
		`C:\dev\lpt`: `C:\dev\lpt`,
	}
	for in, want := range cases {
		if got := openName(in); got != want {
			t.Errorf("openName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSpoolerReason(t *testing.T) {
	cases := []struct {
		status, attrs uint32
		want          string
	}{
		{0, 0, ""},
		{0x400, 0, ""}, // printing
		{psOffline, 0, "离线/未插好"},
		{0, paWorkOffline, "离线/未插好"},
		{psNotAvailable, 0, "离线/未插好"},
		{psPaperOut, 0, "缺纸"},
		{psPaperProblem, 0, "缺纸"},
		{psDoorOpen, 0, "开盖"},
		{psPaperJam, 0, "卡纸"},
		{psPaused, 0, "已暂停"},
		{psError, 0, "故障/需人工处理"},
		{psUserIntervention, 0, "故障/需人工处理"},
		{psOffline | psPaperOut, 0, "离线/未插好"},
	}
	for _, c := range cases {
		if got := spoolerReason(c.status, c.attrs); got != c.want {
			t.Errorf("spoolerReason(%#x, %#x) = %q, want %q", c.status, c.attrs, got, c.want)
		}
	}
}

// PRINTER_INFO_2W: 13 pointers then 8 DWORDs.
func TestPrinterInfo2Layout(t *testing.T) {
	var p printerInfo2
	ptr := unsafe.Sizeof(uintptr(0))
	if off := unsafe.Offsetof(p.Attributes); off != 13*ptr {
		t.Fatalf("Attributes at %d, want %d", off, 13*ptr)
	}
	if off := unsafe.Offsetof(p.Status); off != 13*ptr+5*4 {
		t.Fatalf("Status at %d, want %d", off, 13*ptr+5*4)
	}
}
