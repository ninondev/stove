# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
"""48-column ESC/POS builder with GB18030 text and control-character filtering."""
from __future__ import annotations

import unicodedata

from ..config import PRINT_COLUMNS, PRINT_ENCODING

# --- raw command constants ------------------------------------------------- #
ESC = b"\x1b"
GS = b"\x1d"
FS = b"\x1c"
DLE = b"\x10"

CMD_INIT = ESC + b"\x40"               # ESC @  initialise
CMD_KANJI_ON = FS + b"\x26"            # FS &   enter Chinese (kanji) mode
CMD_KANJI_OFF = FS + b"\x2e"           # FS .   exit Chinese mode
CMD_SELECT_GBK = FS + b"\x43\x00"      # FS C 0 select GBK double-byte table
# feed 3 lines then partial cut (clears the blade past the last line)
CMD_CUT = GS + b"\x56\x42\x03"         # GS V 66 3
CMD_FEED1 = ESC + b"\x64\x01"
# cash drawer kick: ESC p m=0 t1=25 t2=250  (pin 2, ~50ms on / ~500ms off)
CMD_KICK_PIN2 = ESC + b"\x70\x00\x19\xfa"
CMD_KICK_PIN5 = ESC + b"\x70\x01\x19\xfa"

def display_width(s: str) -> int:
    """Printed column width: full/wide (CJK) = 2, others = 1."""
    return sum(2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1 for ch in s)

def truncate_to_width(s: str, width: int) -> str:
    out, w = [], 0
    for ch in s:
        cw = 2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1
        if w + cw > width:
            break
        out.append(ch)
        w += cw
    return "".join(out)

def status_query(n: int) -> bytes:
    """DLE EOT n — real-time status request (n=1 printer, 2 offline, 4 paper)."""
    return DLE + b"\x04" + bytes([n])

_CTRL_KEEP = ("\n", "\t")

def _strip_ctrl(s: str) -> str:
    # Text cannot supply ESC/POS commands. Legitimate commands use raw().

    return "".join(c for c in s if c in _CTRL_KEEP or (c >= "\x20" and not ("\x7f" <= c <= "\x9f")))

class Builder:
    """Accumulates an ESC/POS byte stream for one ticket."""

    def __init__(self, columns: int = PRINT_COLUMNS, encoding: str = PRINT_ENCODING):
        self.columns = columns
        self.encoding = encoding
        self._buf = bytearray()

    # -- low level ------------------------------------------------------- #
    def raw(self, b: bytes) -> "Builder":
        self._buf += b
        return self

    def init(self) -> "Builder":
        return self.raw(CMD_INIT).raw(CMD_KANJI_ON).raw(CMD_SELECT_GBK)

    def text(self, s: str) -> "Builder":
        return self.raw(_strip_ctrl(s).encode(self.encoding, errors="replace"))

    def line(self, s: str = "") -> "Builder":
        return self.text(s).raw(b"\n")

    def align(self, mode: int) -> "Builder":      # 0 left, 1 center, 2 right
        return self.raw(ESC + b"\x61" + bytes([mode]))

    def bold(self, on: bool) -> "Builder":
        return self.raw(ESC + b"\x45" + bytes([1 if on else 0]))

    def size(self, w: int = 1, h: int = 1) -> "Builder":
        """GS ! n — width/height multipliers 1..8."""
        w = max(1, min(8, w)); h = max(1, min(8, h))
        return self.raw(GS + b"\x21" + bytes([((w - 1) << 4) | (h - 1)]))

    def feed(self, n: int = 1) -> "Builder":
        return self.raw(ESC + b"\x64" + bytes([max(0, min(255, n))]))

    # -- layout helpers -------------------------------------------------- #
    def hr(self, char: str = "-") -> "Builder":
        return self.line(char * self.columns)

    def center(self, s: str) -> "Builder":
        return self.align(1).line(s).align(0)

    def two_col(self, left: str, right: str) -> "Builder":

        rw = display_width(right)
        avail = self.columns - rw - 1
        if avail < 1:
            # right side too wide; just stack
            return self.line(left).align(2).line(right).align(0)
        left = truncate_to_width(left, avail)
        gap = self.columns - display_width(left) - rw
        return self.line(left + (" " * max(1, gap)) + right)

    def three_col(self, left: str, mid: str, right: str, mid_w: int = 10, right_w: int = 10) -> "Builder":

        name_w = self.columns - mid_w - right_w
        left = truncate_to_width(left, name_w)
        left_pad = left + " " * (name_w - display_width(left))
        mid_pad = " " * (mid_w - display_width(mid)) + mid if display_width(mid) <= mid_w else truncate_to_width(mid, mid_w)
        right_pad = " " * (right_w - display_width(right)) + right if display_width(right) <= right_w else truncate_to_width(right, right_w)
        return self.line(left_pad + mid_pad + right_pad)

    # -- finishing ------------------------------------------------------- #
    def cut(self) -> "Builder":
        return self.feed(1).raw(CMD_CUT)

    def kick(self, pin5: bool = False) -> "Builder":
        return self.raw(CMD_KICK_PIN5 if pin5 else CMD_KICK_PIN2)

    def build(self) -> bytes:
        return bytes(self._buf)

# --------------------------------------------------------------------------- #

# --------------------------------------------------------------------------- #

def serial_footer_bytes(serial: int, encoding: str = PRINT_ENCODING) -> bytes:

    return (f"流水号 No.{serial % 100000:05d}").encode(encoding, errors="replace") + b"\n"

def inject_serial(payload: bytes, serial: int) -> bytes:

    line = serial_footer_bytes(serial)
    idx = payload.rfind(CMD_FEED1 + CMD_CUT)
    if idx < 0:
        idx = payload.rfind(CMD_CUT)
    if idx < 0:
        return payload + line
    return payload[:idx] + line + payload[idx:]
