// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

// Ticketpreview renders a kitchen ticket as real ESC/POS bytes, then shows
// its text. No printer is used. The dishes are made up.
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"

	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/ninondev/stove/print/render"
	"github.com/ninondev/stove/print/routing"
)

//go:embed demo_menu.json
var menuJSON []byte

type dish struct {
	Name string `json:"name"`
	Unit string `json:"unit"`
}

func samplePayload() []byte {
	var menu []dish
	if err := json.Unmarshal(menuJSON, &menu); err != nil {
		panic(err)
	}
	var lines []routing.TicketLine
	for _, d := range menu[:3] {
		lines = append(lines, routing.TicketLine{Name: d.Name, Qty: 1, Unit: d.Unit})
	}
	ticket := routing.PlannedTicket{StationCode: "kitchen", Footer: "厨房", TicketType: "堂口单", Lines: lines}
	ctx := render.NewContext()
	ctx.TableLabel = "Demo 1"
	party, printTime, operator := int64(3), int64(1790879400000), "Demo Operator"
	ctx.PartySize, ctx.PrintTimeMs, ctx.Operator, ctx.SerialNo = &party, &printTime, &operator, 1
	return render.StationTicket(ticket, ctx)
}

// textPreview removes the printer commands and keeps the GB18030 text.
func textPreview(payload []byte) string {
	var out []byte
	for i := 0; i < len(payload); {
		switch payload[i] {
		case 0x1b: // ESC
			switch payload[i+1] {
			case 0x40:
				i += 2
			case 0x70:
				i += 5
			default:
				i += 3
			}
		case 0x1c: // FS
			if payload[i+1] == 0x43 {
				i += 3
			} else {
				i += 2
			}
		case 0x1d: // GS
			if payload[i+1] == 0x56 {
				i += 4
			} else {
				i += 3
			}
		default:
			out = append(out, payload[i])
			i++
		}
	}
	text, err := simplifiedchinese.GB18030.NewDecoder().Bytes(out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return string(text)
}

func main() {
	fmt.Print(textPreview(samplePayload()))
}
