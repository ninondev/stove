# Stove

Stove prints kitchen tickets and runs a take-a-number queue. It is written in Go. The code comes from a restaurant ordering system. Hired staff at the restaurant used that system from August to early October 2026. It ran there as Python code; this repository first held that Python code and now holds a Go port of the same two parts.

```text
桌台号：Demo 1
堂口单
Stove Demo Kitchen-堂食
菜品                                   3人  数量
------------------------------------------------
宫保鸡丁                                    1/份
麻婆豆腐                                    1/份
鱼香茄子                                    1/份
------------------------------------------------
操作员：Demo Operator
打印时间：2026/10/01 18:30:00
厨房
流水号 No.00001
```

`go run ./examples/ticketpreview` prints that: a kitchen ticket rendered as ESC/POS bytes and shown as text, with made-up dishes and no printer attached.

This is a partial open-source release. It has no payment, cashier, or order service, and the rest of that system stays private. The menu here is made up and the database starts empty.

## Parts

- Kitchen printing: route dishes to stations, render Chinese tickets, and store print jobs in SQLite. Tickets use 48 columns and GB18030 text. Each station has its own serial numbers, so a gap or a repeat shows a lost or doubled ticket. A worker sends each station's jobs in order and retries failures with a growing delay; after 12 attempts a job is marked dead and kept for a reprint. A print policy can hold a ticket type for release by hand or switch it off.
- Queue: take numbers for small, medium, or large parties. Each group starts at 1 on each business day. The counter and ticket are saved in one transaction. You can call, pass, return, or cancel a ticket.

Seating a ticket at a table, phone numbers, and pre-orders from the queue are not part of this release; they belong to the ordering system.

Queue wait times are a fixed estimate of 10 minutes per party ahead, not a forecast. Pass a business day in `YYYY-MM-DD` form to keep a shift on the same day after midnight. Without it, the queue uses the local date. Ticket times use UTC.

## Quick start

Use Go 1.20 or later. Run these commands from the repository folder:

```sh
go run ./examples/ticketpreview
go run ./examples/queuedemo
go test ./...
```

The ticket program renders a real ESC/POS payload and shows its text. It does not open a printer. The queue program uses an in-memory database.

The code stays within Go 1.20, the last release that runs on Windows 7. It needs no cgo: SQLite comes from `modernc.org/sqlite`, and the other two modules are `golang.org/x/sys` and `golang.org/x/text`.

## Layout

| Package | What it holds |
|---|---|
| `print/escpos` | ESC/POS byte builder, GB18030 encoding, column widths for wide characters, serial footer |
| `print/routing` | which station prints which dish, per-dish and summary tickets, combos |
| `print/render` | a planned ticket as bytes |
| `print/transport` | raw TCP (port 9100), serial and COM ports, Windows printers by name, a fake printer |
| `printing` | menu rows to print jobs, serial numbers, print policy, reprints, the sending worker |
| `queue` | the take-a-number queue |
| `db` | the SQLite layer and the schema |

## Tests

The tests use fake printers, mocked ports, loopback sockets, and temporary files. They check ticket bytes, Chinese column widths, routing, queue counters, and retries. The worker and queue tests also pass under Go's race detector.

Two recorded files, `print/escpos/testdata/escpos_cases.json` and `print/render/testdata/kitchen_cases.json`, hold output from the Python version of this kit: 34 cases with 99 tickets, plus text widths and the GB18030 table. The Go code gives the same planned tickets and the same bytes. The dishes in them are made up.

All 108 test runs (subtests included) pass on macOS with Go 1.20.14 and with the current Go. The Linux and Windows code (serial ports, the Windows printer list) compiles for those systems, but its platform tests were not run, and none of this Go code has run on a Windows PC. It has not been tested with a physical printer either, so the tests do not prove that paper comes out.

A job marked `done` means the send returned, nothing more. A send may be repeated after a crash; serial numbers help find missing or repeated tickets.

## What changed from the Python version

The earlier release of this repository was Python. That code is still in the git history; its last commit is `d4a7e89`. The Go code replaces it and gives the same ticket bytes for the cases above. Error messages in the queue are in English, as before; ticket text and printer status reasons are Chinese.

The earlier release also had a USB installer that put Ubuntu Desktop on a PC. It was not tried on real hardware, and it is not part of this version, because the Go version has no such installer.

## License and contributions

Copyright (c) 2026 Ninon. Licensed under GPL-3.0-only. See [LICENSE](LICENSE).

Pull requests are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) and [CLA.md](CLA.md) before sending one.
