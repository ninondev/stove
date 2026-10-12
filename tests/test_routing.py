# SPDX-License-Identifier: GPL-3.0-only
# Copyright (c) 2026 Ninon
import sys, pathlib
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))

from app.domain import routing as R

def stations(kitchen_move=False):
    return {
        "expo": R.StationDef("expo", "传菜口", R.Aggregation.AGGREGATE, "传菜"),
        "kitchen": R.StationDef("kitchen", "厨房", R.Aggregation.PER_DISH, "厨房",
                                move_table_ticket=kitchen_move),
        "cold": R.StationDef("cold", "凉菜房", R.Aggregation.PER_DISH, "凉菜"),
        "front": R.StationDef("front", "前台", R.Aggregation.PER_DISH, "酒水", prints_bills=True),
    }

HOT = [R.ItemBinding("expo", "total"), R.ItemBinding("kitchen", "split")]
COLD = [R.ItemBinding("cold", "split")]
DRINK = [R.ItemBinding("front", "total")]

def by_station(tickets):
    out = {}
    for t in tickets:
        out.setdefault(t.station_code, []).append(t)
    return out

def test_new_order_routes_2hot_1cold_1drink():
    items = [
        R.PrintItem("宫保鸡丁", 1, HOT, created_seq=1),
        R.PrintItem("麻婆豆腐", 1, HOT, created_seq=2),
        R.PrintItem("凉拌黄瓜", 1, COLD, created_seq=3),
        R.PrintItem("柠檬茶", 2, DRINK, unit="瓶", created_seq=4),
    ]
    tickets = R.fan_out(R.Op.NEW, items, stations())
    grp = by_station(tickets)

    assert len(grp["expo"]) == 1
    assert grp["expo"][0].ticket_type == "厨总单"
    assert [l.name for l in grp["expo"][0].lines] == ["宫保鸡丁", "麻婆豆腐"]

    assert len(grp["kitchen"]) == 2
    assert all(t.ticket_type == "堂口单" for t in grp["kitchen"])
    assert [t.lines[0].name for t in grp["kitchen"]] == ["宫保鸡丁", "麻婆豆腐"]

    assert len(grp["cold"]) == 1
    assert len(grp["front"]) == 1
    assert grp["front"][0].lines[0].name == "柠檬茶"
    assert grp["front"][0].lines[0].qty == 2

def test_front_per_dish_two_drinks_two_tickets():

    items = [
        R.PrintItem("柠檬茶", 2, DRINK, unit="瓶", created_seq=1),
        R.PrintItem("热红茶", 1, DRINK, unit="瓶", created_seq=2),
    ]
    grp = by_station(R.fan_out(R.Op.NEW, items, stations()))
    assert len(grp["front"]) == 2
    assert [t.lines[0].name for t in grp["front"]] == ["柠檬茶", "热红茶"]
    assert all(t.ticket_type == "堂口单" for t in grp["front"])

def test_kitchen_per_dish_preserves_time_order():
    items = [
        R.PrintItem("A菜", 1, HOT, created_seq=5),
        R.PrintItem("B菜", 1, HOT, created_seq=1),
        R.PrintItem("C菜", 1, HOT, created_seq=3),
    ]
    tickets = R.fan_out(R.Op.NEW, items, stations())
    kit = [t for t in tickets if t.station_code == "kitchen"]
    assert [t.lines[0].name for t in kit] == ["B菜", "C菜", "A菜"]  # by created_seq
    # the global ticket order also respects seq (worker enqueues in this order)
    assert tickets == sorted(tickets, key=lambda t: t.seq)

def test_add_marks_jiacai():
    items = [R.PrintItem("宫保鸡丁", 1, HOT, created_seq=1)]
    tickets = R.fan_out(R.Op.ADD, items, stations())
    assert all(t.marker == "【加菜】" for t in tickets)

def test_retreat_hot_hits_expo_and_kitchen_only():
    items = [R.PrintItem("宫保鸡丁", 1, HOT, created_seq=1)]
    tickets = R.fan_out(R.Op.RETREAT, items, stations())
    grp = by_station(tickets)
    assert set(grp) == {"expo", "kitchen"}
    for t in tickets:
        assert t.ticket_type == "退菜单"
        assert t.marker == "【退】"
        assert all(l.ret for l in t.lines)

def test_retreat_drink_hits_front_only():
    items = [R.PrintItem("酸梅汤", 1, DRINK, unit="瓶", created_seq=1)]
    tickets = R.fan_out(R.Op.RETREAT, items, stations())
    assert {t.station_code for t in tickets} == {"front"}

def test_transfer_splits_per_station_kitchen_off_by_default():
    items = [
        R.PrintItem("宫保鸡丁", 1, HOT, created_seq=1),
        R.PrintItem("凉拌黄瓜", 1, COLD, created_seq=2),
        R.PrintItem("冰绿茶", 4, DRINK, unit="瓶", created_seq=3),
    ]
    tickets = R.fan_out(R.Op.TRANSFER, items, stations(kitchen_move=False),
                        orig_table="B5", target_table="外1")
    grp = by_station(tickets)
    # kitchen excluded (move_table_ticket off); expo+cold+front each one aggregate ticket
    assert set(grp) == {"expo", "cold", "front"}
    for t in tickets:
        assert t.ticket_type == "转台单"
        assert all(l.transfer for l in t.lines)
        assert t.extra["orig_table"] == "B5"
        assert t.extra["target_table"] == "外1"

def test_transfer_includes_kitchen_when_switch_on():
    items = [R.PrintItem("宫保鸡丁", 1, HOT, created_seq=1)]
    tickets = R.fan_out(R.Op.TRANSFER, items, stations(kitchen_move=True),
                        orig_table="A", target_table="B")
    assert "kitchen" in by_station(tickets)

def test_unconfigured_dish_goes_to_front_notice_never_silent():
    items = [R.PrintItem("神秘新菜", 1, [], created_seq=1)]  # no bindings
    tickets = R.fan_out(R.Op.NEW, items, stations())
    assert len(tickets) == 1
    assert tickets[0].station_code == "front"
    assert tickets[0].ticket_type == "未配置单"
    assert tickets[0].lines[0].name == "神秘新菜"
