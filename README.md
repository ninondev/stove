# Stove

Stove prints kitchen tickets, runs a take-a-number queue. The code comes from a restaurant ordering system that hired staff used regularly from August to early October 2026.

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

That is what `python3 examples/ticket_preview.py` prints: a kitchen ticket rendered as ESC/POS bytes and shown as text, with made-up dishes and no printer attached.

This is a partial open-source release. It has no payment or full order service, and the rest of that system stays private. The menu here is made up and the database starts empty.

## Parts

- Kitchen printing: route dishes to stations, render Chinese tickets, and store print jobs in SQLite. Tickets use 48 columns and GB18030 text. Each station has its own serial numbers. The worker retries failed jobs.
- Queue: take numbers for small, medium, or large parties. Each group starts at 1 on each business day. The counter and ticket are saved in one transaction. You can call, pass, return, or cancel a ticket.
- USB installer: install Ubuntu Desktop 24.04 and start this kit's local demo service on first boot.

Money on tickets uses integer jiao (one yuan is ten jiao). Queue wait times are a fixed estimate, not a forecast. Pass a business day in `YYYY-MM-DD` form to keep a shift on the same day after midnight. Without it, the queue uses the local date. Ticket times use UTC.

## Quick start

Use Python 3.12 or later. Run these commands from the repository folder:

```sh
python3 examples/ticket_preview.py
python3 examples/queue_demo.py
```

The ticket script renders a real ESC/POS payload and shows its text. It does not open a printer. The queue script uses an in-memory database. All menu prices are made up, in jiao.

Run the tests if pytest is already installed:

```sh
python3 -m pytest
```

No third-party code is bundled. The demo and core use the Python standard library. Tests need pytest. The optional serial transport can use pyserial if it is installed. Without pyserial, it writes to a device node that you must configure first.

## Hardware and tests

The code supports ESC/POS thermal printers over serial or USB device nodes. It also has a raw TCP transport. The tests use fake printers, mocked ports, and temporary files. They check ticket bytes, Chinese column widths, routing, queue counters, and retries. They do not prove that a physical printer works. I have not tested this extracted kit with a physical printer or a real USB install.

A job marked `done` means the transport send returned. It does not prove that paper came out. A send may be repeated after a crash. Serial numbers help find missing or repeated tickets.

## USB installer

**Warning: the installer erases the selected target disk. Back up that disk first.**

Read [the installer guide](ops/autoinstall/README.md) before building or booting the ISO. It needs a local Ubuntu Desktop 24.04 ISO and build tools. It installs the demo service, with an in-memory queue and no printer connection. The service listens on `127.0.0.1:8859`. The ISO builder and VM helpers are supplied for review and testing; a full install still needs to be tested.

You can run the same service locally:

```sh
python3 -m app.demo_service
```

The service has `/health`, `/ticket`, and `/queue` paths. It is a demo. Its queue is cleared when it stops.

## License and contributions

Copyright (c) 2026 Ninon. Licensed under GPL-3.0-only. See [LICENSE](LICENSE).

Pull requests are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) and [CLA.md](CLA.md) before sending one.
