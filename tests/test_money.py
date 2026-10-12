# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
import sys, pathlib
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))

from app.domain import money

def test_yuan_to_jiao_exact():
    assert money.yuan_to_jiao(258) == 2580
    assert money.yuan_to_jiao("89.9") == 899
    assert money.yuan_to_jiao(12.5) == 125
    assert money.yuan_to_jiao("0.1") == 1
    assert money.yuan_to_jiao(0) == 0

def test_format_yuan():
    assert money.format_yuan(2580) == "258"
    assert money.format_yuan(125) == "12.5"
    assert money.format_yuan(-90) == "-9"
    assert money.format_yuan(0) == "0"

def test_format_yuan_sign():
    assert money.format_yuan_sign(-90) == "-9"
    assert money.format_yuan_sign(50) == "+5"
