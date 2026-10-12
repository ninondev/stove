# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
"""Print policy storage, extracted from the printing dependency."""
import json
from ..db.connection import now_ms

_PRINT_CATS = ('kitchen', 'prebill', 'final', 'shift', 'day', 'merge', 'move')
_PRINT_VALUES = ('auto', 'manual', 'off')
_PRINT_DEFAULT = {c: 'auto' for c in _PRINT_CATS}


def _parse_policy(row):
    out = dict(_PRINT_DEFAULT)
    if row and row['value']:
        try:
            value = json.loads(row['value'])
            if isinstance(value, dict):
                for category in _PRINT_CATS:
                    if value.get(category) in _PRINT_VALUES:
                        out[category] = value[category]
        except (ValueError, TypeError):
            pass
    return out


def get_print_policy_conn(conn):
    return _parse_policy(conn.execute(
        "SELECT value FROM app_settings WHERE key='print_policy'").fetchone())


def get_print_policy(db):
    return _parse_policy(db.query_one(
        "SELECT value FROM app_settings WHERE key='print_policy'"))


def set_print_policy(db, policy):
    clean = dict(_PRINT_DEFAULT)
    for category in _PRINT_CATS:
        if isinstance(policy, dict) and policy.get(category) in _PRINT_VALUES:
            clean[category] = policy[category]
    value = json.dumps(clean)
    with db.transaction() as conn:
        conn.execute("INSERT INTO app_settings(key,value) VALUES('print_policy',?) "
                     "ON CONFLICT(key) DO UPDATE SET value=excluded.value", (value,))
        conn.execute("INSERT INTO audit_log(kind,ref_table,detail,created_at) "
                     "VALUES('print_policy','app_settings',?,?)", (value, now_ms()))
    return clean
