// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package transport

import (
	"strings"
	"testing"
)

func TestSortPortsCountsLikeAPerson(t *testing.T) {
	got := sortPorts([]string{"COM10", "COM3", " COM2 ", "", "com3", "COM1"})
	if strings.Join(got, ",") != "COM1,COM2,COM3,COM10" {
		t.Fatalf("got %q", got)
	}
	if got := sortPorts(nil); got == nil || len(got) != 0 {
		t.Fatalf("no ports: %#v", got)
	}
}
