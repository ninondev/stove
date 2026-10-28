-- SPDX-License-Identifier: GPL-3.0-only
-- Copyright (c) 2026 Ninon
-- Empty kit schema. No restaurant, staff, or customer data is bundled.
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS business_days (
    id INTEGER PRIMARY KEY,
    biz_date TEXT NOT NULL UNIQUE,
    opened_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS queue_tickets (
    id INTEGER PRIMARY KEY,
    business_day_id INTEGER NOT NULL REFERENCES business_days(id),
    size_group TEXT NOT NULL CHECK(size_group IN ('small','medium','large')),
    number INTEGER NOT NULL CHECK(number > 0),
    party_size INTEGER CHECK(party_size BETWEEN 0 AND 999),
    state TEXT NOT NULL DEFAULT 'waiting'
        CHECK(state IN ('waiting','called','passed','cancelled')),
    created_at INTEGER NOT NULL,
    called_at INTEGER,
    call_count INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 0,
    UNIQUE(business_day_id, size_group, number)
);
CREATE INDEX IF NOT EXISTS ix_queue_day ON queue_tickets(business_day_id, state);
CREATE TABLE IF NOT EXISTS queue_counters (
    business_day_id INTEGER NOT NULL REFERENCES business_days(id),
    size_group TEXT NOT NULL CHECK(size_group IN ('small','medium','large')),
    next_number INTEGER NOT NULL DEFAULT 1 CHECK(next_number > 0),
    PRIMARY KEY(business_day_id, size_group)
);

CREATE TABLE IF NOT EXISTS categories (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    sort INTEGER NOT NULL DEFAULT 0,
    kind TEXT NOT NULL DEFAULT 'dish',
    is_active INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS stations (
    id INTEGER PRIMARY KEY,
    code TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    transport TEXT NOT NULL DEFAULT 'null',
    net_host TEXT,
    net_port INTEGER NOT NULL DEFAULT 9100,
    serial_path TEXT,
    serial_baud INTEGER NOT NULL DEFAULT 115200,
    aggregation TEXT NOT NULL DEFAULT 'aggregate',
    dish_temp TEXT,
    has_drawer INTEGER NOT NULL DEFAULT 0,
    prints_bills INTEGER NOT NULL DEFAULT 0,
    move_table_ticket INTEGER NOT NULL DEFAULT 1,
    enabled INTEGER NOT NULL DEFAULT 1,
    sort INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS dishes (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    category_id INTEGER REFERENCES categories(id),
    price_jiao INTEGER NOT NULL DEFAULT 0,
    unit TEXT NOT NULL DEFAULT 'portion',
    temp TEXT NOT NULL DEFAULT 'hot',
    is_combo INTEGER NOT NULL DEFAULT 0,
    is_active INTEGER NOT NULL DEFAULT 1,
    is_sold_out INTEGER NOT NULL DEFAULT 0,
    sold_out_remaining INTEGER,
    sort INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS dish_station_bindings (
    id INTEGER PRIMARY KEY,
    dish_id INTEGER NOT NULL REFERENCES dishes(id) ON DELETE CASCADE,
    station_id INTEGER NOT NULL REFERENCES stations(id) ON DELETE CASCADE,
    print_rule TEXT NOT NULL DEFAULT 'total',
    aggregation TEXT,
    copies INTEGER NOT NULL DEFAULT 1,
    needs_review INTEGER NOT NULL DEFAULT 0,
    guess_reason TEXT,
    UNIQUE(dish_id, station_id)
);
CREATE INDEX IF NOT EXISTS ix_binding_dish ON dish_station_bindings(dish_id);
CREATE TABLE IF NOT EXISTS dish_specs (
    id INTEGER PRIMARY KEY,
    dish_id INTEGER NOT NULL REFERENCES dishes(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    price_delta_jiao INTEGER NOT NULL DEFAULT 0,
    sort INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS combo_components (
    id INTEGER PRIMARY KEY,
    combo_dish_id INTEGER NOT NULL REFERENCES dishes(id) ON DELETE CASCADE,
    component_dish_id INTEGER NOT NULL REFERENCES dishes(id),
    qty INTEGER NOT NULL DEFAULT 1,
    spec_id INTEGER REFERENCES dish_specs(id),
    sort INTEGER NOT NULL DEFAULT 0
);
-- Optional snapshots for build_print_items_from_rows. This is not an order service.
CREATE TABLE IF NOT EXISTS order_items (
    id INTEGER PRIMARY KEY,
    dish_id INTEGER NOT NULL REFERENCES dishes(id),
    name_snapshot TEXT NOT NULL,
    qty INTEGER NOT NULL DEFAULT 1,
    ret_qty INTEGER NOT NULL DEFAULT 0,
    combo_item_id INTEGER REFERENCES order_items(id),
    spec_name TEXT,
    notes TEXT,
    seq_in_order INTEGER NOT NULL DEFAULT 0,
    ret_reason TEXT,
    status TEXT NOT NULL DEFAULT 'ordered'
);
CREATE TABLE IF NOT EXISTS order_item_addons (
    id INTEGER PRIMARY KEY,
    order_item_id INTEGER NOT NULL REFERENCES order_items(id) ON DELETE CASCADE,
    name_snapshot TEXT NOT NULL,
    qty INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS print_jobs (
    id INTEGER PRIMARY KEY,
    station_id INTEGER NOT NULL REFERENCES stations(id),
    order_id INTEGER,
    ticket_type TEXT NOT NULL,
    serial_no INTEGER NOT NULL,
    payload BLOB NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    dedupe_key TEXT,
    enqueue_seq INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    sent_at INTEGER,
    is_reprint INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_pj_station_pending ON print_jobs(station_id, enqueue_seq)
    WHERE state IN ('pending','failed');
CREATE UNIQUE INDEX IF NOT EXISTS ux_pj_dedupe ON print_jobs(dedupe_key)
    WHERE dedupe_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS ix_pj_order ON print_jobs(order_id);
CREATE INDEX IF NOT EXISTS ix_pj_enqueue ON print_jobs(enqueue_seq);
CREATE TABLE IF NOT EXISTS station_serials (
    station_id INTEGER PRIMARY KEY REFERENCES stations(id),
    next_serial INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS audit_log (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,
    ref_table TEXT,
    ref_id INTEGER,
    staff_id INTEGER,
    pin_ok INTEGER,
    detail TEXT,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_audit_kind ON audit_log(kind);
CREATE INDEX IF NOT EXISTS ix_audit_created ON audit_log(created_at);
CREATE TABLE IF NOT EXISTS app_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
