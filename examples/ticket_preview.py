# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
"""Render the real ESC/POS payload, then show its text without a printer."""
import json
from pathlib import Path
import sys

if __package__ in (None, ''):
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from app.domain import routing
from app.print import escpos, render


def sample_payload():
    menu = json.loads(Path(__file__).with_name('demo_menu.json').read_text(encoding='utf-8'))
    lines = [routing.TicketLine(d['name'], 1, d['unit']) for d in menu[:3]]
    ticket = routing.PlannedTicket('kitchen', '厨房', '堂口单', None, lines)
    ctx = render.RenderContext(table_label='Demo 1', party_size=3,
                               operator='Demo Operator', serial_no=1,
                               print_time_ms=1790879400000)
    return render.render_station_ticket(ticket, ctx)


def text_preview(payload):
    """Remove commands produced by Builder, leaving GB18030 text intact."""
    out = bytearray()
    i = 0
    while i < len(payload):
        c = payload[i]
        if c == 0x1b:
            code = payload[i + 1]
            i += 2 if code == 0x40 else (5 if code == 0x70 else 3)
        elif c == 0x1c:
            i += 3 if payload[i + 1] == 0x43 else 2
        elif c == 0x1d:
            i += 4 if payload[i + 1] == 0x56 else 3
        else:
            out.append(c)
            i += 1
    return out.decode('gb18030')


def main():
    print(text_preview(sample_payload()), end='')


if __name__ == '__main__':
    main()
