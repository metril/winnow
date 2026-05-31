"""Shared data layer: schema, rtlamr field extraction, and consumption math.

The ``Consumption`` value rtlamr reports is an *odometer* — monotonically
increasing — so a meter's usage over any span is just
``MAX(consumption) - MIN(consumption)`` for the packets in that span. Every
query here builds on that single idea (see :func:`correlation`). No unit
calibration is needed for identification; only relative magnitude and timing
matter.

Reused from the ``meterhunt.py`` prototype: the priority field lists for
extraction and the ``MAX-MIN`` / timestamp-prefix bucketing approach.
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime, timedelta, timezone
from typing import Any, Iterable, Optional

# --- field-name candidates per rtlamr message type, in priority order ---------
# Extended from the prototype with EndpointType (commodity) capture.
ID_FIELDS = ("EndpointID", "ID", "ERTSerialNumber")
CONS_FIELDS = ("Consumption", "LastConsumptionCount", "LastConsumption")
# SCM+ puts the commodity in EndpointType; plain SCM puts it in the numeric
# Message.Type. (The message-type string SCM/SCM+/IDM comes from the outer obj.)
TYPE_FIELDS = ("EndpointType", "Type")

EPSILON = 1e-9  # guards divide-by-zero when a meter has no baseline movement.

# ERT commodity codes are not perfectly standardized across utilities, but these
# ranges are the common convention. Used for the electric-only filter toggle.
# Electric SCM endpoint types are typically 4..7; gas/water occupy other codes.
ELECTRIC_ENDPOINT_TYPES = frozenset({4, 5, 7, 8, 12, 13})


def commodity_name(endpoint_type: Optional[int]) -> str:
    """Best-effort commodity label from an ERT endpoint type code."""
    if endpoint_type is None:
        return "unknown"
    if endpoint_type in ELECTRIC_ENDPOINT_TYPES:
        return "electric"
    if endpoint_type in (2, 9, 156, 157, 158, 160):
        return "gas"
    if endpoint_type in (11, 171, 172):
        return "water"
    return "other"


# --- schema -------------------------------------------------------------------
SCHEMA = """
CREATE TABLE IF NOT EXISTS readings (
    ts            TEXT NOT NULL,   -- ISO8601 UTC, normalized from rtlamr Time
    msg_type      TEXT,            -- SCM / SCM+ / IDM / NetIDM
    endpoint_id   INTEGER,
    endpoint_type INTEGER,         -- commodity code, when present
    consumption   REAL,            -- cumulative odometer value
    raw           TEXT,            -- full JSON line, never discarded
    source        TEXT             -- which SDR/dongle heard it (multi-SDR)
);
CREATE INDEX IF NOT EXISTS idx_meter_ts ON readings(endpoint_id, ts);
CREATE INDEX IF NOT EXISTS idx_ts ON readings(ts);

CREATE TABLE IF NOT EXISTS meters (
    endpoint_id  INTEGER PRIMARY KEY,
    label        TEXT,
    is_candidate INTEGER DEFAULT 0,
    is_mine      INTEGER DEFAULT 0,
    notes        TEXT
);

CREATE TABLE IF NOT EXISTS test_windows (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    label     TEXT,
    start_ts  TEXT NOT NULL,
    end_ts    TEXT              -- NULL while a test is running
);

CREATE TABLE IF NOT EXISTS capture_heartbeat (
    source       TEXT PRIMARY KEY,  -- one row per SDR source
    last_ts      TEXT,              -- ts of the most recent reading from it
    total_count  INTEGER DEFAULT 0,
    updated_at   TEXT               -- wall clock of last heartbeat write
);
"""


def connect(path: str) -> sqlite3.Connection:
    """Open the DB with settings safe for one writer + many readers."""
    con = sqlite3.connect(path, timeout=30.0)
    con.row_factory = sqlite3.Row
    con.execute("PRAGMA journal_mode=WAL")
    con.execute("PRAGMA synchronous=NORMAL")
    con.execute("PRAGMA busy_timeout=30000")
    return con


def init_schema(con: sqlite3.Connection) -> None:
    con.executescript(SCHEMA)
    con.commit()


# --- extraction ---------------------------------------------------------------
def normalize_ts(value: Optional[str]) -> Optional[str]:
    """Normalize an rtlamr Time string to canonical UTC ISO8601.

    Storing a single canonical format means lexical string comparison on ``ts``
    matches chronological order, which the range/window SQL relies on.
    """
    if not value:
        return None
    s = value.strip()
    if s.endswith("Z"):
        s = s[:-1] + "+00:00"
    try:
        dt = datetime.fromisoformat(s)
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.astimezone(timezone.utc).isoformat()


def now_ts() -> str:
    return datetime.now(timezone.utc).isoformat()


def extract(obj: dict) -> dict:
    """Pull normalized fields from one rtlamr JSON object.

    Returns a dict with ts, msg_type, endpoint_id, endpoint_type, consumption.
    Mirrors the prototype's priority field lists, with EndpointType added.
    """
    msg = obj.get("Message", {}) or {}
    endpoint_id = next((msg[f] for f in ID_FIELDS if msg.get(f) is not None), None)
    consumption = next((msg[f] for f in CONS_FIELDS if msg.get(f) is not None), None)
    endpoint_type = next((msg[f] for f in TYPE_FIELDS if msg.get(f) is not None), None)
    ts = normalize_ts(obj.get("Time")) or now_ts()
    return {
        "ts": ts,
        "msg_type": obj.get("Type"),
        "endpoint_id": endpoint_id,
        "endpoint_type": endpoint_type,
        "consumption": consumption,
    }


def insert_reading(con: sqlite3.Connection, rec: dict, raw: str, source: str) -> None:
    con.execute(
        "INSERT INTO readings (ts, msg_type, endpoint_id, endpoint_type, "
        "consumption, raw, source) VALUES (?,?,?,?,?,?,?)",
        (
            rec["ts"],
            rec["msg_type"],
            rec["endpoint_id"],
            rec["endpoint_type"],
            rec["consumption"],
            raw,
            source,
        ),
    )


def update_heartbeat(
    con: sqlite3.Connection, source: str, last_ts: Optional[str], total_count: int
) -> None:
    con.execute(
        "INSERT INTO capture_heartbeat (source, last_ts, total_count, updated_at) "
        "VALUES (?,?,?,?) ON CONFLICT(source) DO UPDATE SET "
        "last_ts=excluded.last_ts, total_count=excluded.total_count, "
        "updated_at=excluded.updated_at",
        (source, last_ts, total_count, now_ts()),
    )
    con.commit()


# --- read helpers -------------------------------------------------------------
def _hours_between(start: str, end: str) -> float:
    a = datetime.fromisoformat(start)
    b = datetime.fromisoformat(end)
    return max((b - a).total_seconds() / 3600.0, 0.0)


def _ensure_meter_row(con: sqlite3.Connection, endpoint_id: int) -> None:
    con.execute(
        "INSERT OR IGNORE INTO meters (endpoint_id) VALUES (?)", (endpoint_id,)
    )


def data_span(con: sqlite3.Connection) -> tuple[Optional[str], Optional[str]]:
    row = con.execute(
        "SELECT MIN(ts) AS lo, MAX(ts) AS hi FROM readings "
        "WHERE consumption IS NOT NULL AND endpoint_id IS NOT NULL"
    ).fetchone()
    return (row["lo"], row["hi"]) if row else (None, None)


def _range_clause(since: Optional[str], until: Optional[str]) -> tuple[str, list]:
    clause = "WHERE consumption IS NOT NULL AND endpoint_id IS NOT NULL"
    params: list = []
    if since:
        clause += " AND ts >= ?"
        params.append(since)
    if until:
        clause += " AND ts <= ?"
        params.append(until)
    return clause, params


def leaderboard(
    con: sqlite3.Connection,
    since: Optional[str] = None,
    until: Optional[str] = None,
    msg_type: Optional[str] = None,
    endpoint_type: Optional[int] = None,
    electric_only: bool = False,
) -> list[dict]:
    """Per-meter summary over a time range, joined with annotations."""
    clause, params = _range_clause(since, until)
    if msg_type:
        clause += " AND msg_type = ?"
        params.append(msg_type)
    if endpoint_type is not None:
        clause += " AND endpoint_type = ?"
        params.append(endpoint_type)

    rows = con.execute(
        f"""
        SELECT r.endpoint_id                              AS endpoint_id,
               MAX(r.msg_type)                            AS msg_type,
               MAX(r.endpoint_type)                       AS endpoint_type,
               COUNT(*)                                   AS packets,
               COUNT(DISTINCT r.source)                   AS sources,
               MIN(r.ts)                                  AS first_seen,
               MAX(r.ts)                                  AS last_seen,
               MAX(r.consumption)-MIN(r.consumption)      AS total_movement,
               (SELECT consumption FROM readings r2
                  WHERE r2.endpoint_id = r.endpoint_id
                    AND r2.consumption IS NOT NULL
                  ORDER BY r2.ts DESC LIMIT 1)             AS latest_consumption
        FROM readings r
        {clause}
        GROUP BY r.endpoint_id
        """,
        params,
    ).fetchall()

    out = []
    for r in rows:
        et = r["endpoint_type"]
        commodity = commodity_name(et)
        if electric_only and commodity != "electric":
            continue
        hours = _hours_between(r["first_seen"], r["last_seen"]) or EPSILON
        ann = con.execute(
            "SELECT label, is_candidate, is_mine, notes FROM meters "
            "WHERE endpoint_id = ?",
            (r["endpoint_id"],),
        ).fetchone()
        out.append(
            {
                "endpoint_id": r["endpoint_id"],
                "msg_type": r["msg_type"],
                "endpoint_type": et,
                "commodity": commodity,
                "packets": r["packets"],
                "packets_per_hour": round(r["packets"] / hours, 2),
                "sources": r["sources"],
                "first_seen": r["first_seen"],
                "last_seen": r["last_seen"],
                "latest_consumption": r["latest_consumption"],
                "total_movement": r["total_movement"],
                "label": ann["label"] if ann else None,
                "is_candidate": bool(ann["is_candidate"]) if ann else False,
                "is_mine": bool(ann["is_mine"]) if ann else False,
                "notes": ann["notes"] if ann else None,
            }
        )
    out.sort(key=lambda m: (m["total_movement"] or 0), reverse=True)
    return out


def _bucket_expr(bucket: str) -> str:
    """SQL expression producing a bucket key from the UTC ts column."""
    if bucket == "1d":
        return "strftime('%Y-%m-%d', ts)"
    if bucket == "1h":
        return "strftime('%Y-%m-%dT%H:00', ts)"
    # default 5m: floor minute to nearest 5
    return (
        "strftime('%Y-%m-%dT%H:', ts) || "
        "printf('%02d', (CAST(strftime('%M', ts) AS INTEGER)/5)*5)"
    )


def meter_series(
    con: sqlite3.Connection,
    endpoint_id: int,
    since: Optional[str] = None,
    until: Optional[str] = None,
    bucket: str = "1h",
) -> dict:
    """Cumulative points + per-bucket consumption deltas for one meter."""
    clause, params = _range_clause(since, until)
    clause += " AND endpoint_id = ?"
    params.append(endpoint_id)

    points = [
        {"ts": r["ts"], "consumption": r["consumption"]}
        for r in con.execute(
            f"SELECT ts, consumption FROM readings {clause} ORDER BY ts", params
        ).fetchall()
    ]

    bexpr = _bucket_expr(bucket)
    deltas = [
        {"bucket": r["bucket"], "delta": r["delta"], "packets": r["packets"]}
        for r in con.execute(
            f"""
            SELECT {bexpr}                            AS bucket,
                   MAX(consumption)-MIN(consumption)  AS delta,
                   COUNT(*)                           AS packets
            FROM readings {clause}
            GROUP BY bucket
            ORDER BY bucket
            """,
            params,
        ).fetchall()
    ]
    return {"endpoint_id": endpoint_id, "bucket": bucket, "points": points, "deltas": deltas}


def correlation(con: sqlite3.Connection, start: str, end: str) -> list[dict]:
    """Rank meters by how strongly their usage tracks the window [start, end].

    For each meter:
      window_delta  = MAX-MIN of consumption within the window
      window_rate   = window_delta / window_hours
      baseline_rate = (total movement outside window) / (hours outside window)
      score         = window_rate / max(baseline_rate, EPSILON)
    Ranked desc by score, tie-broken by window_delta. The clean on/off step a
    real load produces makes the true meter's score stand far above neighbors'.
    """
    start = normalize_ts(start) or start
    end = normalize_ts(end) or end
    window_hours = _hours_between(start, end) or EPSILON

    lo, hi = data_span(con)
    if lo is None:
        return []
    span_hours = _hours_between(lo, hi) or EPSILON
    outside_hours = max(span_hours - window_hours, EPSILON)

    rows = con.execute(
        """
        SELECT endpoint_id,
               MAX(msg_type)                          AS msg_type,
               MAX(endpoint_type)                     AS endpoint_type,
               MAX(consumption)-MIN(consumption)      AS total_delta,
               MAX(CASE WHEN ts>=? AND ts<=? THEN consumption END) -
               MIN(CASE WHEN ts>=? AND ts<=? THEN consumption END) AS window_delta,
               SUM(CASE WHEN ts>=? AND ts<=? THEN 1 ELSE 0 END)    AS window_packets
        FROM readings
        WHERE consumption IS NOT NULL AND endpoint_id IS NOT NULL
        GROUP BY endpoint_id
        """,
        (start, end, start, end, start, end),
    ).fetchall()

    out = []
    for r in rows:
        window_delta = r["window_delta"] or 0.0
        total_delta = r["total_delta"] or 0.0
        outside_movement = max(total_delta - window_delta, 0.0)
        window_rate = window_delta / window_hours
        baseline_rate = outside_movement / outside_hours
        score = window_rate / max(baseline_rate, EPSILON)
        out.append(
            {
                "endpoint_id": r["endpoint_id"],
                "msg_type": r["msg_type"],
                "endpoint_type": r["endpoint_type"],
                "commodity": commodity_name(r["endpoint_type"]),
                "window_delta": window_delta,
                "window_rate": round(window_rate, 4),
                "baseline_rate": round(baseline_rate, 4),
                "score": round(score, 3),
                "window_packets": r["window_packets"] or 0,
            }
        )
    out.sort(key=lambda m: (m["score"], m["window_delta"]), reverse=True)
    return out


def combined_ranking(con: sqlite3.Connection) -> dict:
    """Aggregate correlation across all *closed* test windows.

    The meter that ranks high in every test is almost certainly the user's. We
    score each meter by its average per-test score and how often it lands in the
    top spot.
    """
    windows = con.execute(
        "SELECT id, label, start_ts, end_ts FROM test_windows "
        "WHERE end_ts IS NOT NULL ORDER BY start_ts"
    ).fetchall()

    per_meter: dict[int, dict] = {}
    used = []
    for w in windows:
        ranked = correlation(con, w["start_ts"], w["end_ts"])
        if not ranked:
            continue
        used.append({"id": w["id"], "label": w["label"]})
        for rank, m in enumerate(ranked):
            agg = per_meter.setdefault(
                m["endpoint_id"],
                {
                    "endpoint_id": m["endpoint_id"],
                    "commodity": m["commodity"],
                    "scores": [],
                    "wins": 0,
                    "tests_present": 0,
                },
            )
            agg["scores"].append(m["score"])
            agg["tests_present"] += 1
            if rank == 0:
                agg["wins"] += 1

    n_tests = len(used)
    results = []
    for agg in per_meter.values():
        scores = agg["scores"]
        avg = sum(scores) / len(scores) if scores else 0.0
        results.append(
            {
                "endpoint_id": agg["endpoint_id"],
                "commodity": agg["commodity"],
                "avg_score": round(avg, 3),
                "min_score": round(min(scores), 3) if scores else 0.0,
                "wins": agg["wins"],
                "tests_present": agg["tests_present"],
                "tests_total": n_tests,
            }
        )
    # A meter that wins every test and never drops out ranks first.
    results.sort(key=lambda m: (m["wins"], m["avg_score"], m["min_score"]), reverse=True)
    return {"tests": used, "ranking": results}


def meter_readings(
    con: sqlite3.Connection,
    endpoint_id: int,
    since: Optional[str] = None,
    until: Optional[str] = None,
) -> Iterable[sqlite3.Row]:
    clause, params = _range_clause(since, until)
    clause += " AND endpoint_id = ?"
    params.append(endpoint_id)
    return con.execute(
        f"SELECT ts, msg_type, endpoint_id, endpoint_type, consumption, source "
        f"FROM readings {clause} ORDER BY ts",
        params,
    )


# --- annotations & test windows ----------------------------------------------
def get_meter(con: sqlite3.Connection, endpoint_id: int) -> Optional[dict]:
    row = con.execute(
        "SELECT * FROM meters WHERE endpoint_id = ?", (endpoint_id,)
    ).fetchone()
    return dict(row) if row else None


def update_meter(con: sqlite3.Connection, endpoint_id: int, **fields: Any) -> dict:
    _ensure_meter_row(con, endpoint_id)
    allowed = {"label", "is_candidate", "is_mine", "notes"}
    sets = {k: v for k, v in fields.items() if k in allowed and v is not None}
    if sets:
        assignments = ", ".join(f"{k} = ?" for k in sets)
        con.execute(
            f"UPDATE meters SET {assignments} WHERE endpoint_id = ?",
            (*sets.values(), endpoint_id),
        )
        con.commit()
    return get_meter(con, endpoint_id) or {"endpoint_id": endpoint_id}


def create_test(
    con: sqlite3.Connection, label: str, start_ts: str, end_ts: Optional[str] = None
) -> dict:
    cur = con.execute(
        "INSERT INTO test_windows (label, start_ts, end_ts) VALUES (?,?,?)",
        (label, normalize_ts(start_ts) or start_ts,
         normalize_ts(end_ts) if end_ts else None),
    )
    con.commit()
    return get_test(con, cur.lastrowid)


def stop_test(con: sqlite3.Connection, test_id: int, end_ts: str) -> Optional[dict]:
    con.execute(
        "UPDATE test_windows SET end_ts = ? WHERE id = ? AND end_ts IS NULL",
        (normalize_ts(end_ts) or end_ts, test_id),
    )
    con.commit()
    return get_test(con, test_id)


def get_test(con: sqlite3.Connection, test_id: int) -> Optional[dict]:
    row = con.execute(
        "SELECT * FROM test_windows WHERE id = ?", (test_id,)
    ).fetchone()
    return dict(row) if row else None


def list_tests(con: sqlite3.Connection) -> list[dict]:
    return [
        dict(r)
        for r in con.execute(
            "SELECT * FROM test_windows ORDER BY start_ts DESC"
        ).fetchall()
    ]


def delete_test(con: sqlite3.Connection, test_id: int) -> None:
    con.execute("DELETE FROM test_windows WHERE id = ?", (test_id,))
    con.commit()


def health(con: sqlite3.Connection, stale_after_s: int = 60) -> dict:
    """Capture health: per-source heartbeat freshness + recent packet rate."""
    now = datetime.now(timezone.utc)
    one_min_ago = (now - timedelta(seconds=60)).isoformat()
    sources = []
    for r in con.execute("SELECT * FROM capture_heartbeat ORDER BY source").fetchall():
        updated = r["updated_at"]
        alive = False
        age_s = None
        if updated:
            try:
                age_s = (now - datetime.fromisoformat(updated)).total_seconds()
                alive = age_s <= stale_after_s
            except ValueError:
                pass
        # packets in the last 60s from this source = rough live rate
        recent = con.execute(
            "SELECT COUNT(*) AS c FROM readings WHERE source = ? AND ts >= ?",
            (r["source"], one_min_ago),
        ).fetchone()["c"]
        sources.append(
            {
                "source": r["source"],
                "alive": alive,
                "age_seconds": round(age_s, 1) if age_s is not None else None,
                "last_ts": r["last_ts"],
                "total_count": r["total_count"],
                "packets_last_min": recent,
            }
        )
    unique_meters = con.execute(
        "SELECT COUNT(DISTINCT endpoint_id) AS c FROM readings "
        "WHERE endpoint_id IS NOT NULL"
    ).fetchone()["c"]
    return {
        "alive": any(s["alive"] for s in sources),
        "sources": sources,
        "unique_meters": unique_meters,
        "packets_last_min": sum(s["packets_last_min"] for s in sources),
    }
