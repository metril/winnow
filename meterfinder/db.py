"""Shared data layer: schema, rtlamr field extraction, and consumption math.

Backed by **PostgreSQL** (psycopg2). MVCC means the multiple SDR ingesters
(concurrent writers) never block each other, and the polling dashboard's reads
never block writers — so the "database is locked" class of bug is impossible.

The ``Consumption`` value rtlamr reports is an *odometer* — monotonically
increasing — so a meter's usage over any span is just
``MAX(consumption) - MIN(consumption)`` for the packets in that span. Every
query here builds on that single idea (see :func:`correlation`). No unit
calibration is needed for identification; only relative magnitude and timing
matter.

``ts`` is stored as a real ``timestamptz``; for a stable JSON/API contract the
read functions convert datetimes back to ISO-8601 strings on the way out, so the
frontend is unaffected by the storage type.
"""

from __future__ import annotations

import os
from datetime import datetime, timedelta, timezone
from typing import Any, Optional

import psycopg2
import psycopg2.extras
import psycopg2.pool

# --- field-name candidates per rtlamr message type, in priority order ---------
ID_FIELDS = ("EndpointID", "ID", "ERTSerialNumber")
CONS_FIELDS = ("Consumption", "LastConsumptionCount", "LastConsumption")
# SCM+ puts the commodity in EndpointType; plain SCM puts it in the numeric
# Message.Type. (The message-type string SCM/SCM+/IDM comes from the outer obj.)
TYPE_FIELDS = ("EndpointType", "Type")

EPSILON = 1e-9  # guards divide-by-zero when a meter has no baseline movement.

# ERT commodity codes are not perfectly standardized across utilities, but these
# ranges are the common convention. Used for the electric-only filter toggle.
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


DATABASE_URL = os.environ.get(
    "DATABASE_URL", "postgresql://meterfinder:meterfinder@localhost:5432/meterfinder"
)

# --- schema -------------------------------------------------------------------
SCHEMA = """
CREATE TABLE IF NOT EXISTS readings (
    ts            TIMESTAMPTZ NOT NULL,  -- normalized from rtlamr Time
    msg_type      TEXT,                  -- SCM / SCM+ / IDM / NetIDM
    endpoint_id   BIGINT,
    endpoint_type INTEGER,               -- commodity code, when present
    consumption   DOUBLE PRECISION,      -- cumulative odometer value
    raw           TEXT,                  -- full JSON line, never discarded
    source        TEXT                   -- which SDR/dongle heard it (multi-SDR)
);
CREATE INDEX IF NOT EXISTS idx_meter_ts ON readings(endpoint_id, ts);
CREATE INDEX IF NOT EXISTS idx_ts ON readings(ts);

CREATE TABLE IF NOT EXISTS meters (
    endpoint_id  BIGINT PRIMARY KEY,
    label        TEXT,
    is_candidate BOOLEAN DEFAULT FALSE,
    is_mine      BOOLEAN DEFAULT FALSE,
    notes        TEXT
);

CREATE TABLE IF NOT EXISTS test_windows (
    id        SERIAL PRIMARY KEY,
    label     TEXT,
    start_ts  TIMESTAMPTZ NOT NULL,
    end_ts    TIMESTAMPTZ            -- NULL while a test is running
);

CREATE TABLE IF NOT EXISTS capture_heartbeat (
    source       TEXT PRIMARY KEY,     -- one row per SDR source
    last_ts      TIMESTAMPTZ,          -- ts of the most recent reading from it
    total_count  BIGINT DEFAULT 0,
    updated_at   TIMESTAMPTZ           -- wall clock of last heartbeat write
);
"""


def connect(dsn: Optional[str] = None, autocommit: bool = False):
    """Open a single PostgreSQL connection (used by the capture ingesters)."""
    con = psycopg2.connect(dsn or DATABASE_URL)
    con.autocommit = autocommit
    return con


_POOL: Optional["psycopg2.pool.ThreadedConnectionPool"] = None


def get_pool(dsn: Optional[str] = None) -> "psycopg2.pool.ThreadedConnectionPool":
    """Lazily-created connection pool for the API (many short read txns)."""
    global _POOL
    if _POOL is None:
        _POOL = psycopg2.pool.ThreadedConnectionPool(1, 10, dsn or DATABASE_URL)
    return _POOL


def init_schema(con) -> None:
    with con.cursor() as cur:
        cur.execute(SCHEMA)
    con.commit()


# --- extraction ---------------------------------------------------------------
def normalize_ts(value) -> Optional[datetime]:
    """Normalize an rtlamr Time value to an aware UTC ``datetime``.

    Accepts ISO strings (with ``Z`` or offset) or an existing ``datetime``.
    """
    if value is None:
        return None
    if isinstance(value, datetime):
        dt = value
    else:
        s = str(value).strip()
        if not s:
            return None
        if s.endswith("Z"):
            s = s[:-1] + "+00:00"
        try:
            dt = datetime.fromisoformat(s)
        except ValueError:
            return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.astimezone(timezone.utc)


def now_ts() -> datetime:
    return datetime.now(timezone.utc)


def _iso(dt) -> Optional[str]:
    """datetime -> ISO string for the JSON/API contract (None-safe)."""
    return dt.isoformat() if isinstance(dt, datetime) else dt


def extract(obj: dict) -> dict:
    """Pull normalized fields from one rtlamr JSON object."""
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


def insert_reading(con, rec: dict, raw: str, source: str) -> None:
    with con.cursor() as cur:
        cur.execute(
            "INSERT INTO readings (ts, msg_type, endpoint_id, endpoint_type, "
            "consumption, raw, source) VALUES (%s,%s,%s,%s,%s,%s,%s)",
            (rec["ts"], rec["msg_type"], rec["endpoint_id"], rec["endpoint_type"],
             rec["consumption"], raw, source),
        )


def update_heartbeat(con, source: str, last_ts, total_count: int) -> None:
    with con.cursor() as cur:
        cur.execute(
            "INSERT INTO capture_heartbeat (source, last_ts, total_count, updated_at) "
            "VALUES (%s,%s,%s,%s) ON CONFLICT (source) DO UPDATE SET "
            "last_ts=EXCLUDED.last_ts, total_count=EXCLUDED.total_count, "
            "updated_at=EXCLUDED.updated_at",
            (source, last_ts, total_count, now_ts()),
        )
    if not con.autocommit:
        con.commit()


# --- read helpers -------------------------------------------------------------
def _dictcur(con):
    return con.cursor(cursor_factory=psycopg2.extras.RealDictCursor)


def _hours_between(start: datetime, end: datetime) -> float:
    return max((end - start).total_seconds() / 3600.0, 0.0)


def data_span(con) -> tuple[Optional[datetime], Optional[datetime]]:
    with _dictcur(con) as cur:
        cur.execute(
            "SELECT MIN(ts) AS lo, MAX(ts) AS hi FROM readings "
            "WHERE consumption IS NOT NULL AND endpoint_id IS NOT NULL"
        )
        row = cur.fetchone()
    return (row["lo"], row["hi"]) if row else (None, None)


def _range_clause(since, until) -> tuple[str, list]:
    clause = "WHERE consumption IS NOT NULL AND endpoint_id IS NOT NULL"
    params: list = []
    if since:
        clause += " AND ts >= %s"
        params.append(since)
    if until:
        clause += " AND ts <= %s"
        params.append(until)
    return clause, params


def leaderboard(con, since=None, until=None, msg_type=None, endpoint_type=None,
                electric_only: bool = False) -> list[dict]:
    """Per-meter summary over a time range, joined with annotations."""
    clause, params = _range_clause(since, until)
    if msg_type:
        clause += " AND msg_type = %s"
        params.append(msg_type)
    if endpoint_type is not None:
        clause += " AND endpoint_type = %s"
        params.append(endpoint_type)

    with _dictcur(con) as cur:
        cur.execute(
            f"""
            SELECT r.endpoint_id                            AS endpoint_id,
                   MAX(r.msg_type)                          AS msg_type,
                   MAX(r.endpoint_type)                     AS endpoint_type,
                   COUNT(*)                                 AS packets,
                   COUNT(DISTINCT r.source)                 AS sources,
                   MIN(r.ts)                                AS first_seen,
                   MAX(r.ts)                                AS last_seen,
                   MAX(r.consumption)-MIN(r.consumption)    AS total_movement,
                   (SELECT consumption FROM readings r2
                      WHERE r2.endpoint_id = r.endpoint_id
                        AND r2.consumption IS NOT NULL
                      ORDER BY r2.ts DESC LIMIT 1)           AS latest_consumption
            FROM readings r
            {clause}
            GROUP BY r.endpoint_id
            """,
            params,
        )
        rows = cur.fetchall()
        ann_rows = {}
        cur.execute("SELECT * FROM meters")
        for a in cur.fetchall():
            ann_rows[a["endpoint_id"]] = a

    out = []
    for r in rows:
        et = r["endpoint_type"]
        commodity = commodity_name(et)
        if electric_only and commodity != "electric":
            continue
        hours = _hours_between(r["first_seen"], r["last_seen"]) or EPSILON
        ann = ann_rows.get(r["endpoint_id"])
        out.append({
            "endpoint_id": r["endpoint_id"],
            "msg_type": r["msg_type"],
            "endpoint_type": et,
            "commodity": commodity,
            "packets": r["packets"],
            "packets_per_hour": round(r["packets"] / hours, 2),
            "sources": r["sources"],
            "first_seen": _iso(r["first_seen"]),
            "last_seen": _iso(r["last_seen"]),
            "latest_consumption": r["latest_consumption"],
            "total_movement": r["total_movement"],
            "label": ann["label"] if ann else None,
            "is_candidate": bool(ann["is_candidate"]) if ann else False,
            "is_mine": bool(ann["is_mine"]) if ann else False,
            "notes": ann["notes"] if ann else None,
        })
    out.sort(key=lambda m: (m["total_movement"] or 0), reverse=True)
    return out


def _bucket_expr(bucket: str) -> str:
    """SQL expression producing a bucket timestamp from the ts column."""
    if bucket == "1d":
        return "date_trunc('day', ts)"
    if bucket == "1h":
        return "date_trunc('hour', ts)"
    # default 5m: floor epoch seconds to the nearest 300
    return "to_timestamp(floor(extract(epoch FROM ts)/300)*300)"


def meter_series(con, endpoint_id: int, since=None, until=None,
                 bucket: str = "1h") -> dict:
    """Cumulative points + per-bucket consumption deltas for one meter."""
    clause, params = _range_clause(since, until)
    clause += " AND endpoint_id = %s"
    params.append(endpoint_id)
    bexpr = _bucket_expr(bucket)

    with _dictcur(con) as cur:
        cur.execute(
            f"SELECT ts, consumption FROM readings {clause} ORDER BY ts", params
        )
        points = [{"ts": _iso(r["ts"]), "consumption": r["consumption"]}
                  for r in cur.fetchall()]
        cur.execute(
            f"""
            SELECT {bexpr}                            AS bucket,
                   MAX(consumption)-MIN(consumption)  AS delta,
                   COUNT(*)                           AS packets
            FROM readings {clause}
            GROUP BY bucket ORDER BY bucket
            """,
            params,
        )
        deltas = [{"bucket": _iso(r["bucket"]), "delta": r["delta"],
                   "packets": r["packets"]} for r in cur.fetchall()]
    return {"endpoint_id": endpoint_id, "bucket": bucket,
            "points": points, "deltas": deltas}


def correlation(con, start, end) -> list[dict]:
    """Rank meters by how strongly their usage tracks the window [start, end].

    For each meter:
      window_delta  = MAX-MIN of consumption within the window
      window_rate   = window_delta / window_hours
      baseline_rate = (total movement outside window) / (hours outside window)
      score         = window_rate / max(baseline_rate, EPSILON)
    Ranked desc by score, tie-broken by window_delta.
    """
    start = normalize_ts(start)
    end = normalize_ts(end)
    window_hours = _hours_between(start, end) or EPSILON

    lo, hi = data_span(con)
    if lo is None:
        return []
    span_hours = _hours_between(lo, hi) or EPSILON
    outside_hours = max(span_hours - window_hours, EPSILON)

    with _dictcur(con) as cur:
        cur.execute(
            """
            SELECT endpoint_id,
                   MAX(msg_type)                          AS msg_type,
                   MAX(endpoint_type)                     AS endpoint_type,
                   MAX(consumption)-MIN(consumption)      AS total_delta,
                   MAX(consumption) FILTER (WHERE ts BETWEEN %s AND %s) -
                   MIN(consumption) FILTER (WHERE ts BETWEEN %s AND %s) AS window_delta,
                   COUNT(*) FILTER (WHERE ts BETWEEN %s AND %s)         AS window_packets
            FROM readings
            WHERE consumption IS NOT NULL AND endpoint_id IS NOT NULL
            GROUP BY endpoint_id
            """,
            (start, end, start, end, start, end),
        )
        rows = cur.fetchall()

    out = []
    for r in rows:
        window_delta = r["window_delta"] or 0.0
        total_delta = r["total_delta"] or 0.0
        outside_movement = max(total_delta - window_delta, 0.0)
        window_rate = window_delta / window_hours
        baseline_rate = outside_movement / outside_hours
        score = window_rate / max(baseline_rate, EPSILON)
        out.append({
            "endpoint_id": r["endpoint_id"],
            "msg_type": r["msg_type"],
            "endpoint_type": r["endpoint_type"],
            "commodity": commodity_name(r["endpoint_type"]),
            "window_delta": window_delta,
            "window_rate": round(window_rate, 4),
            "baseline_rate": round(baseline_rate, 4),
            "score": round(score, 3),
            "window_packets": r["window_packets"] or 0,
        })
    out.sort(key=lambda m: (m["score"], m["window_delta"]), reverse=True)
    return out


def combined_ranking(con) -> dict:
    """Aggregate correlation across all *closed* test windows."""
    with _dictcur(con) as cur:
        cur.execute(
            "SELECT id, label, start_ts, end_ts FROM test_windows "
            "WHERE end_ts IS NOT NULL ORDER BY start_ts"
        )
        windows = cur.fetchall()

    per_meter: dict[int, dict] = {}
    used = []
    for w in windows:
        ranked = correlation(con, w["start_ts"], w["end_ts"])
        if not ranked:
            continue
        used.append({"id": w["id"], "label": w["label"]})
        for rank, m in enumerate(ranked):
            agg = per_meter.setdefault(m["endpoint_id"], {
                "endpoint_id": m["endpoint_id"], "commodity": m["commodity"],
                "scores": [], "wins": 0, "tests_present": 0,
            })
            agg["scores"].append(m["score"])
            agg["tests_present"] += 1
            if rank == 0:
                agg["wins"] += 1

    n_tests = len(used)
    results = []
    for agg in per_meter.values():
        scores = agg["scores"]
        avg = sum(scores) / len(scores) if scores else 0.0
        results.append({
            "endpoint_id": agg["endpoint_id"], "commodity": agg["commodity"],
            "avg_score": round(avg, 3),
            "min_score": round(min(scores), 3) if scores else 0.0,
            "wins": agg["wins"], "tests_present": agg["tests_present"],
            "tests_total": n_tests,
        })
    results.sort(key=lambda m: (m["wins"], m["avg_score"], m["min_score"]), reverse=True)
    return {"tests": used, "ranking": results}


def meter_readings(con, endpoint_id: int, since=None, until=None) -> list[dict]:
    clause, params = _range_clause(since, until)
    clause += " AND endpoint_id = %s"
    params.append(endpoint_id)
    with _dictcur(con) as cur:
        cur.execute(
            f"SELECT ts, msg_type, endpoint_id, endpoint_type, consumption, source "
            f"FROM readings {clause} ORDER BY ts",
            params,
        )
        return [dict(r) for r in cur.fetchall()]


# --- annotations & test windows ----------------------------------------------
def latest_msg_type(con, endpoint_id: int) -> Optional[str]:
    """Most recent rtlamr message type seen for a meter (for the filter cmd)."""
    with con.cursor() as cur:
        cur.execute(
            "SELECT msg_type FROM readings WHERE endpoint_id = %s "
            "AND msg_type IS NOT NULL ORDER BY ts DESC LIMIT 1",
            (endpoint_id,),
        )
        row = cur.fetchone()
    return row[0] if row else None


def get_meter(con, endpoint_id: int) -> Optional[dict]:
    with _dictcur(con) as cur:
        cur.execute("SELECT * FROM meters WHERE endpoint_id = %s", (endpoint_id,))
        row = cur.fetchone()
    return dict(row) if row else None


def update_meter(con, endpoint_id: int, **fields: Any) -> dict:
    allowed = {"label", "is_candidate", "is_mine", "notes"}
    sets = {k: v for k, v in fields.items() if k in allowed and v is not None}
    # boolean columns: accept 1/0/true/false from the API and coerce to bool
    for b in ("is_candidate", "is_mine"):
        if b in sets:
            sets[b] = bool(sets[b])
    with con.cursor() as cur:
        cur.execute(
            "INSERT INTO meters (endpoint_id) VALUES (%s) ON CONFLICT DO NOTHING",
            (endpoint_id,),
        )
        if sets:
            assignments = ", ".join(f"{k} = %s" for k in sets)
            cur.execute(
                f"UPDATE meters SET {assignments} WHERE endpoint_id = %s",
                (*sets.values(), endpoint_id),
            )
    con.commit()
    return get_meter(con, endpoint_id) or {"endpoint_id": endpoint_id}


def create_test(con, label: str, start_ts, end_ts=None) -> dict:
    with con.cursor() as cur:
        cur.execute(
            "INSERT INTO test_windows (label, start_ts, end_ts) VALUES (%s,%s,%s) "
            "RETURNING id",
            (label, normalize_ts(start_ts), normalize_ts(end_ts) if end_ts else None),
        )
        test_id = cur.fetchone()[0]
    con.commit()
    return get_test(con, test_id)


def stop_test(con, test_id: int, end_ts) -> Optional[dict]:
    with con.cursor() as cur:
        cur.execute(
            "UPDATE test_windows SET end_ts = %s WHERE id = %s AND end_ts IS NULL",
            (normalize_ts(end_ts), test_id),
        )
    con.commit()
    return get_test(con, test_id)


def get_test(con, test_id: int) -> Optional[dict]:
    with _dictcur(con) as cur:
        cur.execute("SELECT * FROM test_windows WHERE id = %s", (test_id,))
        row = cur.fetchone()
    if not row:
        return None
    d = dict(row)
    d["start_ts"], d["end_ts"] = _iso(d["start_ts"]), _iso(d["end_ts"])
    return d


def list_tests(con) -> list[dict]:
    with _dictcur(con) as cur:
        cur.execute("SELECT * FROM test_windows ORDER BY start_ts DESC")
        out = []
        for r in cur.fetchall():
            d = dict(r)
            d["start_ts"], d["end_ts"] = _iso(d["start_ts"]), _iso(d["end_ts"])
            out.append(d)
    return out


def delete_test(con, test_id: int) -> None:
    with con.cursor() as cur:
        cur.execute("DELETE FROM test_windows WHERE id = %s", (test_id,))
    con.commit()


def health(con, stale_after_s: int = 60) -> dict:
    """Capture health: per-source heartbeat freshness + recent packet rate."""
    now = datetime.now(timezone.utc)
    one_min_ago = now - timedelta(seconds=60)
    sources = []
    with _dictcur(con) as cur:
        cur.execute("SELECT * FROM capture_heartbeat ORDER BY source")
        hb_rows = cur.fetchall()
        for r in hb_rows:
            updated = r["updated_at"]
            age_s = (now - updated).total_seconds() if updated else None
            alive = age_s is not None and age_s <= stale_after_s
            cur.execute(
                "SELECT COUNT(*) AS c FROM readings WHERE source = %s AND ts >= %s",
                (r["source"], one_min_ago),
            )
            recent = cur.fetchone()["c"]
            sources.append({
                "source": r["source"], "alive": alive,
                "age_seconds": round(age_s, 1) if age_s is not None else None,
                "last_ts": _iso(r["last_ts"]), "total_count": r["total_count"],
                "packets_last_min": recent,
            })
        cur.execute(
            "SELECT COUNT(DISTINCT endpoint_id) AS c FROM readings "
            "WHERE endpoint_id IS NOT NULL"
        )
        unique_meters = cur.fetchone()["c"]
    return {
        "alive": any(s["alive"] for s in sources),
        "sources": sources,
        "unique_meters": unique_meters,
        "packets_last_min": sum(s["packets_last_min"] for s in sources),
    }
