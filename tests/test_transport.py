# SPDX-License-Identifier: GPL-3.0-only
import sys
import threading
import time
from pathlib import Path
from types import SimpleNamespace

import pytest

from app.db.connection import now_ms
from app.print import escpos, serial_no, transport as T, worker


class MockSocket:
    def __init__(self, responses=()):
        self.sent = []
        self.timeouts = []
        self.responses = iter(responses)

    def __enter__(self):
        return self

    def __exit__(self, *args):
        pass

    def settimeout(self, timeout):
        self.timeouts.append(timeout)

    def sendall(self, payload):
        self.sent.append(payload)

    def recv(self, count):
        response = next(self.responses)
        if isinstance(response, Exception):
            raise response
        return response


def test_transport_connects_outbound_without_source_binding(monkeypatch):
    calls = []
    socket = MockSocket()
    def connect(address, *, timeout):
        calls.append((address, timeout))
        return socket
    monkeypatch.setattr(T.socket, 'create_connection', connect)
    printer = T.NetworkTransport('printer.invalid', 9100, connect_timeout=0.5, write_timeout=0.75)
    printer.send(b'demo')
    assert calls == [(('printer.invalid', 9100), 0.5)]
    assert socket.sent == [b'demo']
    assert socket.timeouts == [0.75]


def test_network_failure_wrapped_and_probe_offline(monkeypatch):
    def fail(*args, **kwargs):
        raise OSError('mock disconnected')
    monkeypatch.setattr(T.socket, 'create_connection', fail)
    printer = T.NetworkTransport('printer.invalid')
    with pytest.raises(T.TransportError):
        printer.send(b'demo')
    assert printer.probe().reachable is False


def test_status_reports_paper_and_cover_without_gating():
    socket = MockSocket([b'\x60', b'\x04'])
    result = T.NetworkTransport('printer.invalid')._read_status(socket)
    assert result.reachable is True
    assert result.paper_ok is False and result.cover_ok is False
    assert result.reason
    assert socket.sent == [escpos.status_query(4), escpos.status_query(2)]


def test_status_timeout_keeps_reachable():
    result = T.NetworkTransport('printer.invalid')._read_status(MockSocket([OSError('no status')]))
    assert result.reachable is True
    assert result.paper_ok is None and result.cover_ok is None


def test_serial_open_failure_becomes_transport_error(monkeypatch):
    def fail(*args, **kwargs):
        raise OSError('mock unplugged')
    monkeypatch.setitem(sys.modules, 'serial', SimpleNamespace(Serial=fail))
    with pytest.raises(T.TransportError):
        T.SerialTransport('mock-device').send(b'demo')


def test_serial_pyserial_write_flush_close(monkeypatch):
    events = []
    class MockPort:
        def write(self, data):
            events.append(('write', data))
        def flush(self):
            events.append(('flush',))
        def close(self):
            events.append(('close',))
    def open_serial(path, baud, **kwargs):
        assert path == 'mock-device' and baud == 9600
        assert kwargs == {'timeout': 2, 'write_timeout': 2}
        return MockPort()
    monkeypatch.setitem(sys.modules, 'serial', SimpleNamespace(Serial=open_serial))
    T.SerialTransport('mock-device', baud=9600, write_timeout=2).send(b'demo')
    assert events == [('write', b'demo'), ('flush',), ('close',)]


def test_serial_closes_port_after_write_failure(monkeypatch):
    closed = []
    class MockPort:
        def write(self, data):
            raise OSError('mock write failed')
        def close(self):
            closed.append(True)
    monkeypatch.setitem(sys.modules, 'serial', SimpleNamespace(Serial=lambda *args, **kwargs: MockPort()))
    with pytest.raises(T.TransportError):
        T.SerialTransport('mock-device').send(b'demo')
    assert closed == [True]


def test_serial_raw_fallback_writes_only_temporary_file(monkeypatch, tmp_path):
    monkeypatch.setitem(sys.modules, 'serial', None)
    path = tmp_path / 'mock-device'
    path.touch()
    printer = T.SerialTransport(str(path))
    printer.send(b'\x1b@demo')
    assert path.read_bytes() == b'\x1b@demo'
    assert printer.probe().reachable is True
    assert T.SerialTransport(str(tmp_path / 'missing')).probe().reachable is False


def _job(db, station, payload, seq=1, state='pending', due=0):
    db.execute(
        'INSERT INTO print_jobs(station_id,ticket_type,serial_no,payload,state,enqueue_seq,created_at,next_attempt_at) '
        'VALUES(?,?,1,?,?,?,?,?)', (station, '厨总单', payload, state, seq, now_ms(), due),
    )


def test_print_sweep_recovers_stuck_sending(seeded_db):
    sid = seeded_db.query_one('SELECT id FROM stations LIMIT 1')['id']
    _job(seeded_db, sid, b'expired', state='sending', due=now_ms()-1000)
    _job(seeded_db, sid, b'active', seq=2, state='sending', due=now_ms()+60000)
    manager = worker.PrintManager(seeded_db)
    assert manager._sweep_stuck_sending() == 1
    states = [row['state'] for row in seeded_db.query('SELECT state FROM print_jobs ORDER BY enqueue_seq')]
    assert states == ['pending', 'sending']


def test_worker_retry_does_not_skip_older_job_in_backoff(seeded_db):
    station = seeded_db.query_one("SELECT id,code FROM stations WHERE code='kitchen'")
    _job(seeded_db, station['id'], b'older', state='failed', due=now_ms()+60000)
    _job(seeded_db, station['id'], b'newer', seq=2)
    manager = worker.PrintManager(seeded_db)
    manager.reload_stations()
    fake = T.FakeTransport()
    manager.set_transport(station['code'], fake)
    assert manager.tick() == 0
    assert fake.sent == []
    seeded_db.execute("UPDATE print_jobs SET next_attempt_at=0 WHERE state='failed'")
    assert manager.tick() == 2
    assert fake.sent == [b'older', b'newer']


def test_worker_first_failure_stops_station_until_retry(seeded_db):
    station = seeded_db.query_one("SELECT id,code FROM stations WHERE code='kitchen'")
    _job(seeded_db, station['id'], b'older')
    _job(seeded_db, station['id'], b'newer', seq=2)
    manager = worker.PrintManager(seeded_db)
    manager.reload_stations()
    fake = T.FakeTransport(online=False)
    manager.set_transport(station['code'], fake)
    assert manager.tick() == 0
    rows = seeded_db.query('SELECT state,attempts FROM print_jobs ORDER BY enqueue_seq')
    assert [(row['state'],row['attempts']) for row in rows] == [('failed',1),('pending',0)]
    fake.online = True
    seeded_db.execute("UPDATE print_jobs SET next_attempt_at=0 WHERE state='failed'")
    assert manager.tick() == 2
    assert fake.sent == [b'older', b'newer']


def test_wake_does_not_block_on_slow_printer(seeded_db, monkeypatch):
    entered = threading.Event()
    release = threading.Event()
    class SlowTransport:
        def send(self, payload):
            entered.set()
            assert release.wait(3)
        def probe(self):
            return T.StatusResult(reachable=True)
    monkeypatch.setattr(T, 'build_transport', lambda **kwargs: SlowTransport())
    station = seeded_db.query_one('SELECT id FROM stations LIMIT 1')['id']
    _job(seeded_db, station, b'demo')
    manager = worker.PrintManager(seeded_db)
    manager.start(interval_s=0.05)
    try:
        assert entered.wait(2)
        started = time.monotonic()
        manager.wake()
        assert time.monotonic() - started < 0.5
    finally:
        release.set()
        manager.stop()


def test_wake_falls_back_to_sync_when_no_worker(seeded_db):
    station = seeded_db.query_one("SELECT id,code FROM stations WHERE code='kitchen'")
    _job(seeded_db, station['id'], b'demo')
    manager = worker.PrintManager(seeded_db)
    manager.reload_stations()
    fake = T.FakeTransport()
    manager.set_transport(station['code'], fake)
    manager.wake()
    assert fake.sent == [b'demo']
    assert seeded_db.query_one('SELECT state FROM print_jobs')['state'] == 'done'


def test_serial_reconcile_after_counter_corruption(seeded_db):
    station = seeded_db.query_one('SELECT id FROM stations LIMIT 1')['id']
    with seeded_db.transaction() as conn:
        assert serial_no.next_serial(conn, station) == 1
        assert serial_no.next_serial(conn, station) == 2
        _job(seeded_db, station, b'demo')
        conn.execute('UPDATE print_jobs SET serial_no=9')
        conn.execute('UPDATE station_serials SET next_serial=1')
        serial_no.reconcile(conn)
        assert serial_no.next_serial(conn, station) == 10
