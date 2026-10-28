// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

// Queuedemo runs the take-a-number queue in memory. No database file and no
// printer are used.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ninondev/stove/db"
	"github.com/ninondev/stove/queue"
)

func main() {
	d, err := db.Open(":memory:")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer d.Close()
	d.InitSchema()

	two, one, four := int64(2), int64(1), int64(4)
	first := queue.TakeNumber(d, "small", &two, nil)
	second := queue.TakeNumber(d, "small", &one, nil)
	queue.TakeNumber(d, "medium", &four, nil)
	queue.Call(d, first["id"].(int64))

	fmt.Println("Called ticket:", label(d, first))
	fmt.Println("Next small ticket:", label(d, second))
	out, _ := json.MarshalIndent(queue.Summary(d, nil), "", "  ")
	fmt.Println(string(out))
	fmt.Println("The wait estimate is a simple rule: 10 minutes per waiting table ahead.")
}

func label(d *db.DB, t map[string]any) string {
	return queue.TicketLabel(d.QueryOne("SELECT * FROM queue_tickets WHERE id=?", t["id"]))
}
