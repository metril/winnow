#!/usr/bin/env python3
"""Read rtlamr JSON lines from stdin and store them via the shared data layer.

One ingester runs per SDR source (``--source``), each with its own autocommit
PostgreSQL connection — MVCC lets the N ingesters write concurrently without
blocking. Unparseable lines are skipped so a single malformed packet never
crashes the capture; the full raw JSON is always stored so nothing is lost if a
field name differs on a particular meter. A transient DB error never kills the
process (that would break the pipe and take down the upstream rtlamr) — the
connection is reset and we carry on.
"""

import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import psycopg2  # noqa: E402

from meterfinder import db  # noqa: E402

COMMIT_EVERY = 25


def main() -> None:
    p = argparse.ArgumentParser(description="Ingest rtlamr JSON into the DB.")
    p.add_argument("--dsn", default=os.environ.get("DATABASE_URL"))
    p.add_argument("--source", default="0", help="SDR source id (device index/serial)")
    args = p.parse_args()

    def new_con():
        # Each insert is its own short autocommit transaction.
        return db.connect(args.dsn, autocommit=True)

    con = new_con()
    db.init_schema(con)  # idempotent; run.sh normally created it already

    n = 0
    last_ts = None

    def reset():
        nonlocal con
        try:
            con.close()
        except Exception:
            pass
        con = new_con()

    def heartbeat() -> None:
        nonlocal con
        try:
            db.update_heartbeat(con, args.source, last_ts, n)
        except psycopg2.Error as exc:
            print(f"[ingest:{args.source}] heartbeat error, resetting: {exc}",
                  file=sys.stderr)
            reset()

    # Seed a heartbeat immediately so health shows the source as alive on startup.
    heartbeat()

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        try:
            rec = db.extract(obj)
            db.insert_reading(con, rec, raw=line, source=args.source)
            last_ts = rec["ts"]
            n += 1
        except psycopg2.Error as exc:  # DB hiccup: reset the connection, continue
            print(f"[ingest:{args.source}] db error, resetting: {exc}", file=sys.stderr)
            reset()
            continue
        except Exception as exc:  # never let one bad packet kill the stream
            print(f"[ingest:{args.source}] skip: {exc}", file=sys.stderr)
            continue

        if n % COMMIT_EVERY == 0:
            heartbeat()
            print(f"[ingest:{args.source}] stored {n}", file=sys.stderr, flush=True)

    heartbeat()
    print(f"\n[ingest:{args.source}] final: stored {n} readings", file=sys.stderr)


if __name__ == "__main__":
    main()
