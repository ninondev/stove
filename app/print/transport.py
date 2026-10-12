# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
from __future__ import annotations

import socket
from dataclasses import dataclass

from .. import config
from . import escpos

class TransportError(Exception):
    """Raised when a payload could not be delivered to the printer."""

@dataclass
class StatusResult:
    reachable: bool
    reason: str | None = None
    paper_ok: bool | None = None    # None = unknown (clone did not answer)
    cover_ok: bool | None = None

class Transport:
    def send(self, payload: bytes) -> None:               # pragma: no cover - interface
        raise NotImplementedError

    def probe(self) -> StatusResult:                      # pragma: no cover - interface
        raise NotImplementedError

    def describe(self) -> str:
        return self.__class__.__name__

# --------------------------------------------------------------------------- #
# Network (raw TCP 9100)                                                      #
# --------------------------------------------------------------------------- #

class NetworkTransport(Transport):
    def __init__(self, host: str, port: int = 9100,
                 connect_timeout: float | None = None,
                 write_timeout: float | None = None):
        self.host = host
        self.port = port
        self.connect_timeout = connect_timeout or config.PRINTER_CONNECT_TIMEOUT_S
        self.write_timeout = write_timeout or config.PRINTER_WRITE_TIMEOUT_S

    def describe(self) -> str:
        return f"{self.host}:{self.port}"

    def send(self, payload: bytes) -> None:
        try:
            with socket.create_connection((self.host, self.port), timeout=self.connect_timeout) as s:
                s.settimeout(self.write_timeout)
                s.sendall(payload)
        except OSError as e:
            raise TransportError(f"网络打印失败 {self.host}:{self.port}: {e}") from e

    def probe(self) -> StatusResult:
        # Connect = the trustworthy liveness check.
        try:
            with socket.create_connection((self.host, self.port), timeout=self.connect_timeout) as s:
                return self._read_status(s)
        except OSError:
            return StatusResult(reachable=False, reason="离线/连接失败")

    def _read_status(self, s: socket.socket) -> StatusResult:
        """Best-effort DLE EOT read. No answer => unknown (still reachable)."""
        reason = None
        paper_ok: bool | None = None
        cover_ok: bool | None = None
        try:
            s.settimeout(1.0)
            # n=4 paper sensor
            s.sendall(escpos.status_query(4))
            data = s.recv(1)
            if data:
                byte = data[0]
                if byte & 0x60:          # bits 5&6 -> paper end
                    paper_ok = False
                    reason = "缺纸"
                else:
                    paper_ok = True
            # n=2 offline status (cover)
            s.sendall(escpos.status_query(2))
            data = s.recv(1)
            if data:
                byte = data[0]
                if byte & 0x04:          # bit 2 -> cover open
                    cover_ok = False
                    reason = "开盖或缺纸" if reason else "开盖"
                else:
                    cover_ok = True
        except OSError:
            # reachable (we connected) but status unreadable — leave reason None
            pass
        return StatusResult(reachable=True, reason=reason, paper_ok=paper_ok, cover_ok=cover_ok)

# --------------------------------------------------------------------------- #
# Serial (local USB-serial, e.g. /dev/escpos-frontdesk)                       #
# --------------------------------------------------------------------------- #

class SerialTransport(Transport):
    def __init__(self, path: str, baud: int = 115200, write_timeout: float | None = None):
        self.path = path
        self.baud = baud
        self.write_timeout = write_timeout or config.PRINTER_WRITE_TIMEOUT_S

    def describe(self) -> str:
        return self.path

    def _open(self):
        try:
            import serial  # optional pyserial
        except ImportError:
            return None
        return serial.Serial(self.path, self.baud, timeout=self.write_timeout,
                             write_timeout=self.write_timeout)

    def send(self, payload: bytes) -> None:
        try:

            port = self._open()
            if port is not None:
                try:
                    port.write(payload)
                    port.flush()
                finally:
                    port.close()
            else:
                # pyserial unavailable — raw write to the device node as a fallback
                # (works when the tty is already configured; udev gives a stable path).
                with open(self.path, "wb", buffering=0) as f:
                    f.write(payload)
        except OSError as e:
            raise TransportError(f"串口打印失败 {self.path}: {e}") from e

    def probe(self) -> StatusResult:
        import os
        if not os.path.exists(self.path):
            return StatusResult(reachable=False, reason="离线/未插好")
        if not os.access(self.path, os.W_OK):
            return StatusResult(reachable=False, reason="无权限/未就绪")
        return StatusResult(reachable=True)

# --------------------------------------------------------------------------- #
# Fake (tests + the no-hardware simulator)                                    #
# --------------------------------------------------------------------------- #

class FakeTransport(Transport):
    """In-memory printer. `online` toggles failure; `sent` records payloads."""

    def __init__(self, name: str = "fake", online: bool = True):
        self.name = name
        self.online = online
        self.sent: list[bytes] = []

    def describe(self) -> str:
        return f"fake:{self.name}"

    def send(self, payload: bytes) -> None:
        if not self.online:
            raise TransportError(f"假打印机离线 {self.name}")
        self.sent.append(payload)

    def probe(self) -> StatusResult:
        return StatusResult(reachable=self.online, reason=None if self.online else "离线/连接失败")

def build_transport(*, transport: str, net_host: str | None, net_port: int,
                    serial_path: str | None, serial_baud: int) -> Transport:
    """Factory from a stations-table row's transport config."""
    if transport == "net":
        if not net_host:
            raise ValueError("网口出单口缺少 IP 地址")
        return NetworkTransport(net_host, net_port)
    if transport == "serial":
        if not serial_path:
            raise ValueError("串口出单口缺少设备路径")
        return SerialTransport(serial_path, serial_baud)
    if transport == "null":
        return FakeTransport(name="null", online=True)
    raise ValueError(f"未知传输方式: {transport}")
