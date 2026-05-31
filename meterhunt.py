#!/usr/bin/env python3
"""
meterhunt - capture rtlamr readings and figure out which meter is yours.

Workflow:
  # 1. SDR server (separate terminal, leave running):
  rtl_tcp

  # 2. Pipe rtlamr JSON into the logger (leave running for a day+):
  rtlamr -msgtype=scm,scm+,idm -format=json | python3 meterhunt.py log

  # 3. Anytime, see per-meter daily consumption deltas:
  python3 meterhunt.py report
  python3 meterhunt.py report --since 2026-05-29     # restrict to recent days
  python3 meterhunt.py report --window 15m           # short-window deltas (load test)

SCM/SCM+ Consumption is a cumulative odometer, so a meter's usage over any
span is just max(consumption) - min(consumption) for packets in that span.
The 'raw' column stores the full JSON line, so nothing is lost if a field
name differs on your meter and you need to re-extract later.
"""

import argparse
import json
import sqlite3
import sys
from datetime import datetime, timezone

DB = "meters.db"

# Field-name candidates per rtlamr message type, in priority order.
ID_FIELDS = ("EndpointID", "ID", "ERTSerialNumber")
CONS_FIELDS = ("Consumption", "LastConsumptionCount", "LastConsumption")


def open_db(path):
    con = sqlite3.connect(path)
    con.execute(
        """
        CREATE TABLE IF NOT EXISTS readings (
            ts          TEXT NOT NULL,      -- ISO8601, from rtlamr's Time field
            msg_type    TEXT,
            endpoint_id INTEGER,
            consumption REAL,
            raw         TEXT
        )
        """
    )
    con.execute("CREATE INDEX IF NOT EXISTS idx_meter_ts ON readings(endpoint_id, ts)")
    con.commit()
    return con


def extract(obj):
    """Pull (msg_type, endpoint_id, consumption) from one rtlamr JSON object."""
    msg = obj.get("Message", {})
    msg_type = obj.get("Type")
    endpoint_id = next((msg[f] for f in ID_FIELDS if f in msg), None)
    consumption = next((msg[f] for f in CONS_FIELDS if f in msg), None)
    return msg_type, endpoint_id, consumption


def cmd_log(args):
    con = open_db(args.db)
    n = 0
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        ts = obj.get("Time") or datetime.now(timezone.utc).isoformat()
        msg_type, endpoint_id, consumption = extract(obj)
        con.execute(
            "INSERT INTO readings (ts, msg_type, endpoint_id, consumption, raw)"
            " VALUES (?,?,?,?,?)",
            (ts, msg_type, endpoint_id, consumption, line),
        )
        n += 1
        if n % 50 == 0:
            con.commit()
            print(f"\rlogged {n} readings", end="", file=sys.stderr, flush=True)
    con.commit()
    print(f"\nstored {n} readings in {args.db}", file=sys.stderr)


def cmd_report(args):
    con = open_db(args.db)
    where = "WHERE consumption IS NOT NULL AND endpoint_id IS NOT NULL"
    params = []
    if args.since:
        where += " AND substr(ts,1,10) >= ?"
        params.append(args.since)

    # Group by day, or by a coarser timestamp prefix for short-window deltas.
    # 'day' -> YYYY-MM-DD (10 chars); '1h' -> YYYY-MM-DDTHH (13 chars).
    bucket_len = {"day": 10, "1h": 13}.get(args.window, 10)
    bucket = f"substr(ts,1,{bucket_len})"

    rows = con.execute(
        f"""
        SELECT endpoint_id,
               {bucket}                          AS bucket,
               MAX(consumption)-MIN(consumption) AS delta,
               COUNT(*)                          AS packets
        FROM readings
        {where}
        GROUP BY endpoint_id, bucket
        ORDER BY bucket, delta DESC
        """,
        params,
    ).fetchall()

    print(f"{'meter_id':>12} {'bucket':>16} {'delta':>14} {'packets':>8}")
    print("-" * 54)
    for endpoint_id, bucket, delta, packets in rows:
        print(f"{endpoint_id:>12} {bucket:>16} {delta:>14.0f} {packets:>8}")

    # Also print a leaderboard: total movement per meter across the whole window.
    print("\n=== total consumption movement per meter (whole window) ===")
    lb = con.execute(
        f"""
        SELECT endpoint_id,
               MAX(consumption)-MIN(consumption) AS total_delta,
               COUNT(*)                          AS packets,
               COUNT(DISTINCT substr(ts,1,10))   AS days_seen
        FROM readings
        {where}
        GROUP BY endpoint_id
        ORDER BY total_delta DESC
        """,
        params,
    ).fetchall()
    print(f"{'meter_id':>12} {'total_delta':>14} {'packets':>8} {'days':>6}")
    print("-" * 44)
    for endpoint_id, total_delta, packets, days in lb:
        print(f"{endpoint_id:>12} {total_delta:>14.0f} {packets:>8} {days:>6}")


def main():
    p = argparse.ArgumentParser(description="Capture rtlamr readings and find your meter.")
    p.add_argument("--db", default=DB, help="sqlite file (default: meters.db)")
    sub = p.add_subparsers(dest="cmd", required=True)
    sub.add_parser("log", help="read rtlamr JSON from stdin and store it")
    rp = sub.add_parser("report", help="per-meter consumption deltas")
    rp.add_argument("--since", help="only buckets on/after YYYY-MM-DD")
    rp.add_argument("--window", choices=["day", "1h"], default="day",
                    help="bucket size for deltas (default: day)")
    args = p.parse_args()
    {"log": cmd_log, "report": cmd_report}[args.cmd](args)


if __name__ == "__main__":
    main()
