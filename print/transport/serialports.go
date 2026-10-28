// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package transport

import (
	"sort"
	"strconv"
	"strings"
)

// sortPorts puts COM port names in the order a person counts them (COM2
// before COM10) and drops repeats and blanks.
func sortPorts(names []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[strings.ToUpper(n)] {
			continue
		}
		seen[strings.ToUpper(n)] = true
		out = append(out, n)
	}
	num := func(s string) (string, int) {
		i := len(s)
		for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
			i--
		}
		v, err := strconv.Atoi(s[i:])
		if err != nil {
			return strings.ToUpper(s), -1
		}
		return strings.ToUpper(s[:i]), v
	}
	sort.SliceStable(out, func(a, b int) bool {
		pa, na := num(out[a])
		pb, nb := num(out[b])
		if pa != pb {
			return pa < pb
		}
		return na < nb
	})
	return out
}
