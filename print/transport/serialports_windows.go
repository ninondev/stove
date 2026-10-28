//go:build windows

// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package transport

import "golang.org/x/sys/windows/registry"

// LocalSerialPorts lists the COM ports Windows has registered (a USB-serial
// receipt printer shows up as one once its cable's driver is in). It only
// reads HARDWARE\DEVICEMAP\SERIALCOMM and opens no port, so a port another
// program holds is left alone. No key means no COM port at all.
func LocalSerialPorts() ([]string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DEVICEMAP\SERIALCOMM`, registry.QUERY_VALUE)
	if err == registry.ErrNotExist {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer k.Close()
	names, err := k.ReadValueNames(0)
	if err != nil {
		return nil, err
	}
	ports := make([]string, 0, len(names))
	for _, n := range names {
		if v, _, err := k.GetStringValue(n); err == nil {
			ports = append(ports, v)
		}
	}
	return sortPorts(ports), nil
}
