# SPDX-License-Identifier: GPL-3.0-only
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from app.db.connection import Database


@pytest.fixture
def db(tmp_path):
    database = Database(tmp_path / 'test.db')
    database.connect()
    database.init_schema()
    yield database
    database.close()


@pytest.fixture
def seeded_db(db):
    with db.transaction() as conn:
        for category in ('Hot', 'Drinks', 'Combo'):
            conn.execute('INSERT INTO categories(name) VALUES(?)', (category,))
        stations = [
            ('expo', 'Expo', 'aggregate', 'hot'),
            ('kitchen', 'Kitchen', 'per_dish', 'hot'),
            ('cold', 'Cold', 'per_dish', 'cold'),
            ('front', 'Drinks', 'aggregate', 'drink'),
        ]
        for code, name, aggregation, temperature in stations:
            conn.execute(
                'INSERT INTO stations(code,name,transport,aggregation,dish_temp) VALUES(?,?,?,?,?)',
                (code, name, 'null', aggregation, temperature),
            )
        category_id = conn.execute("SELECT id FROM categories WHERE name='Hot'").fetchone()['id']
        for name, price, temperature in (
            ('宫保鸡丁', 237, 'hot'), ('麻婆豆腐', 143, 'hot'), ('凉拌黄瓜', 117, 'cold'),
        ):
            dish_id = conn.execute(
                'INSERT INTO dishes(name,category_id,price_jiao,unit,temp) '
                'VALUES(?,?,?,?,?)',
                (name, category_id, price, '份', temperature),
            ).lastrowid
            bindings = [('expo', 'total'), ('kitchen', 'split')] if temperature == 'hot' else [('cold', 'split')]
            for code, rule in bindings:
                station_id = conn.execute('SELECT id FROM stations WHERE code=?', (code,)).fetchone()['id']
                conn.execute(
                    'INSERT INTO dish_station_bindings(dish_id,station_id,print_rule) VALUES(?,?,?)',
                    (dish_id, station_id, rule),
                )
    return db
