"""Correctness tests for the consumption-delta and correlation math.

These are the highest-stakes part of the project: if the ranking is wrong, the
user identifies the wrong meter. We seed synthetic readings where exactly one
meter spikes during a window and assert it ranks first.
"""

import json
import os
import sys
from datetime import datetime, timedelta, timezone

import pytest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from meterfinder import db  # noqa: E402

BASE = datetime(2026, 5, 30, 0, 0, 0, tzinfo=timezone.utc)


@pytest.fixture
def con():
    c = db.connect(":memory:")
    db.init_schema(c)
    return c


def add(con, endpoint_id, minute, consumption, *, msg_type="SCM",
        endpoint_type=4, source="0"):
    ts = (BASE + timedelta(minutes=minute)).isoformat()
    rec = {
        "ts": ts,
        "msg_type": msg_type,
        "endpoint_id": endpoint_id,
        "endpoint_type": endpoint_type,
        "consumption": consumption,
    }
    db.insert_reading(con, rec, raw=json.dumps(rec), source=source)
    con.commit()


def seed_load_test(con):
    """Three meters over 8 hours. Meter 1001 spikes hard during hours 3-5.

    Window = [BASE+3h, BASE+5h]. 1001 climbs 1000 units in the window vs ~slow
    baseline; 1002 is steady; 1003 climbs steadily the whole time.
    """
    minutes = list(range(0, 8 * 60 + 1, 10))  # every 10 min for 8h
    win_lo, win_hi = 3 * 60, 5 * 60
    for m in minutes:
        # 1001: flat baseline, big ramp inside the window
        if m < win_lo:
            c1 = 10000 + m * 0.1
        elif m <= win_hi:
            c1 = 10000 + win_lo * 0.1 + (m - win_lo) * 10.0   # steep ramp
        else:
            c1 = 10000 + win_lo * 0.1 + (win_hi - win_lo) * 10.0 + (m - win_hi) * 0.1
        add(con, 1001, m, round(c1, 2))

        # 1002: essentially idle the whole time
        add(con, 1002, m, round(20000 + m * 0.05, 2))

        # 1003: steady moderate consumer all day (no on/off step)
        add(con, 1003, m, round(30000 + m * 2.0, 2))


def test_extract_uses_priority_fields():
    obj = {"Time": "2026-05-30T12:00:00Z", "Type": "SCM+",
           "Message": {"EndpointID": 555, "Consumption": 42, "EndpointType": 4}}
    rec = db.extract(obj)
    assert rec["endpoint_id"] == 555
    assert rec["consumption"] == 42
    assert rec["endpoint_type"] == 4
    assert rec["msg_type"] == "SCM+"
    assert rec["ts"].endswith("+00:00")


def test_extract_idm_fallbacks():
    obj = {"Type": "IDM",
           "Message": {"ERTSerialNumber": 777, "LastConsumptionCount": 9}}
    rec = db.extract(obj)
    assert rec["endpoint_id"] == 777
    assert rec["consumption"] == 9


def test_normalize_ts_handles_z_and_offset():
    assert db.normalize_ts("2026-05-30T12:00:00Z") == "2026-05-30T12:00:00+00:00"
    # a +02:00 local time converts to UTC
    assert db.normalize_ts("2026-05-30T14:00:00+02:00") == "2026-05-30T12:00:00+00:00"
    assert db.normalize_ts(None) is None
    assert db.normalize_ts("garbage") is None


def test_leaderboard_movement_and_dedup(con):
    seed_load_test(con)
    # same meter heard by a second dongle -> duplicate rows, must not inflate movement
    add(con, 1001, 0, 10000.0, source="1")
    add(con, 1001, 300, 13000.0, source="1")
    board = db.leaderboard(con)
    by_id = {m["endpoint_id"]: m for m in board}
    assert set(by_id) == {1001, 1002, 1003}
    # 1001 total movement is max-min, unaffected by the duplicate source rows
    assert by_id[1001]["sources"] == 2
    # 1003 (steady all-day) has the largest *total* movement; 1001 spike is local
    assert by_id[1003]["total_movement"] > by_id[1002]["total_movement"]


def test_correlation_ranks_spiking_meter_first(con):
    seed_load_test(con)
    start = (BASE + timedelta(hours=3)).isoformat()
    end = (BASE + timedelta(hours=5)).isoformat()
    ranked = db.correlation(con, start, end)
    assert ranked[0]["endpoint_id"] == 1001
    # the spike should stand well above the steady consumer's score
    assert ranked[0]["score"] > 5 * ranked[1]["score"]
    assert ranked[0]["window_delta"] > 0
    assert ranked[0]["window_packets"] > 0


def test_correlation_empty_db_returns_empty(con):
    assert db.correlation(con, BASE.isoformat(),
                          (BASE + timedelta(hours=1)).isoformat()) == []


def test_combined_ranking_winner_across_tests(con):
    seed_load_test(con)
    # two non-overlapping windows, both covering parts of 1001's spike
    db.create_test(con, "t1", (BASE + timedelta(hours=3)).isoformat(),
                   (BASE + timedelta(hours=4)).isoformat())
    db.create_test(con, "t2", (BASE + timedelta(hours=4)).isoformat(),
                   (BASE + timedelta(hours=5)).isoformat())
    result = db.combined_ranking(con)
    assert result["ranking"][0]["endpoint_id"] == 1001
    assert result["ranking"][0]["wins"] == 2
    assert result["ranking"][0]["tests_total"] == 2


def test_meter_series_buckets(con):
    seed_load_test(con)
    s = db.meter_series(con, 1003, bucket="1h")
    assert s["points"]  # cumulative points present
    assert len(s["deltas"]) >= 7  # ~8 hourly buckets
    # 1003 climbs steadily -> any bucket with >1 packet has a positive delta
    # (a bucket with a single packet legitimately has delta 0: max == min)
    assert all(d["delta"] > 0 for d in s["deltas"] if d["packets"] > 1)
    assert any(d["delta"] > 0 for d in s["deltas"])


def test_update_meter_and_lock(con):
    seed_load_test(con)
    db.update_meter(con, 1001, is_mine=1, label="my heater test")
    m = db.get_meter(con, 1001)
    assert m["is_mine"] == 1
    assert m["label"] == "my heater test"
