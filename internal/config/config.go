// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

// Package config holds the defaults of a 48-column demo kitchen. Ticket times
// are shown in UTC. The values are plain variables so tests can change them.
package config

import "time"

const (
	JiaoPerYuan   = 10
	PrintEncoding = "gb18030"
)

var (
	// StoreName is printed in the ticket header when a ticket has none.
	StoreName = "Stove Demo Kitchen"

	TZ = time.UTC

	PrintColumns = 48

	PrinterConnectTimeout = 4 * time.Second
	PrinterWriteTimeout   = 8 * time.Second

	RetryBackoffS    = []int{1, 2, 5, 15, 30}
	RetryBackoffCapS = 60
	RetryMaxAttempts = 12
)

// ShopName is the name printed on tickets.
func ShopName() string { return StoreName }

// Load is a no-op kept so callers have one place to reset settings in tests.
func Load() {}

// Now is the current time in TZ.
func Now() time.Time { return time.Now().In(TZ) }
