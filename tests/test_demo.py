# SPDX-License-Identifier: GPL-3.0-only
"""Exercise the HTTP handler through memory streams, with no socket."""
from email.message import Message
from io import BytesIO
import json

from app.db.connection import Database
from app.demo_service import make_handler
from app.print.escpos import display_width, CMD_CUT, CMD_KICK_PIN2
from examples.ticket_preview import sample_payload, text_preview


def invoke(db, method, path, body=None):
    handler = object.__new__(make_handler(db))
    handler.path = path
    handler.headers = Message()
    raw = json.dumps(body).encode() if body is not None else b''
    handler.headers['Content-Length'] = str(len(raw))
    handler.rfile = BytesIO(raw)
    handler.wfile = BytesIO()
    codes = []
    handler.send_response = codes.append
    handler.send_header = lambda *args: None
    handler.end_headers = lambda: None
    getattr(handler, 'do_' + method)()
    return codes[0], json.loads(handler.wfile.getvalue())


def test_preview_keeps_chinese_layout_and_neutral_header():
    payload = sample_payload()
    preview = text_preview(payload)
    assert 'Stove Demo Kitchen' in preview
    assert '宫保鸡丁' in preview
    assert 'No.00001' in preview
    assert all(display_width(line) <= 48 for line in preview.splitlines())
    assert CMD_CUT in payload and CMD_KICK_PIN2 not in payload
    assert not any(ord(c) < 32 for c in preview if c != '\n')


def test_http_demo_health_ticket_and_queue_in_memory():
    db = Database(':memory:')
    db.init_schema()
    try:
        assert invoke(db, 'GET', '/health') == (200, {'status': 'ok'})
        assert '宫保鸡丁' in invoke(db, 'GET', '/ticket')[1]['preview']
        first = invoke(db, 'POST', '/queue', {'size_group': 'small', 'party_size': 2})
        assert first[0] == 201 and first[1]['number'] == 1
        assert invoke(db, 'GET', '/queue')[1]['total_waiting'] == 1
        assert invoke(db, 'POST', '/queue', {'size_group': 'bad'})[0] == 400
        assert invoke(db, 'POST', '/queue', [1])[0] == 400
        assert invoke(db, 'GET', '/missing')[0] == 404
    finally:
        db.close()
