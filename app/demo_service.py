# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
"""Local demo service for the USB installer. No printer is opened."""
import argparse
from http.server import BaseHTTPRequestHandler, HTTPServer
import json

from .db.connection import Database
from .domain.errors import KitError
from .services import queue
from examples.ticket_preview import sample_payload, text_preview


def make_handler(db):
    class Handler(BaseHTTPRequestHandler):
        def respond(self, status, body):
            data = json.dumps(body, ensure_ascii=False).encode('utf-8')
            self.send_response(status)
            self.send_header('Content-Type', 'application/json; charset=utf-8')
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def do_GET(self):
            if self.path == '/health':
                self.respond(200, {'status': 'ok'})
            elif self.path == '/ticket':
                self.respond(200, {'preview': text_preview(sample_payload())})
            elif self.path == '/queue':
                self.respond(200, queue.summary(db))
            else:
                self.respond(404, {'error': 'Use /health, /ticket, or /queue.'})

        def do_POST(self):
            if self.path != '/queue':
                self.respond(404, {'error': 'Use /queue to take a number.'})
                return
            try:
                size = int(self.headers.get('Content-Length', '0'))
                if not 0 < size <= 4096:
                    raise ValueError('Send a JSON body of 1 to 4096 bytes.')
                body = json.loads(self.rfile.read(size))
                if not isinstance(body, dict):
                    raise ValueError('Send a JSON object.')
                ticket = queue.take_number(db, size_group=body.get('size_group', ''),
                                           party_size=body.get('party_size'),
                                           business_day=body.get('business_day'))
            except (ValueError, TypeError, KitError) as exc:
                self.respond(400, {'error': str(exc)})
                return
            self.respond(201, ticket)

    return Handler


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--host', choices=['127.0.0.1'], default='127.0.0.1')
    parser.add_argument('--port', type=int, default=8859)
    args = parser.parse_args()
    db = Database(':memory:')
    db.init_schema()
    server = HTTPServer((args.host, args.port), make_handler(db))
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
        db.close()


if __name__ == '__main__':
    main()
