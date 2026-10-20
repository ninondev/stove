# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
"""Integer jiao conversion and ticket formatting; one yuan is ten jiao."""
from __future__ import annotations

from ..config import JIAO_PER_YUAN

def yuan_to_jiao(yuan: float | int | str) -> int:

    # Work in tenths to stay exact.
    from decimal import Decimal, ROUND_HALF_UP

    d = (Decimal(str(yuan)) * JIAO_PER_YUAN).quantize(Decimal("1"), rounding=ROUND_HALF_UP)
    return int(d)

def format_yuan(jiao: int) -> str:
    """Format jiao as a yuan string for tickets/UI.

    2580 -> "258"; 125 -> "12.5"; -90 -> "-9".
    """
    neg = jiao < 0
    j = abs(jiao)
    whole, frac = divmod(j, JIAO_PER_YUAN)
    s = str(whole) if frac == 0 else f"{whole}.{frac}"
    return f"-{s}" if neg else s

def format_yuan_sign(jiao: int) -> str:
    """Like format_yuan but always shows a leading sign (for adjustment lines)."""
    s = format_yuan(abs(jiao))
    return f"-{s}" if jiao < 0 else f"+{s}"
