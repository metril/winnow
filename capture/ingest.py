#!/usr/bin/env python3
"""Read rtlamr JSON lines from stdin and store them via the shared data layer.

One ingester runs per SDR source (``--source``). Unparseable lines are skipped
so a single malformed packet never crashes the capture. The full raw JSON is
always stored so nothing is lost if a field name differs on a particular meter.

Stdlib only — the capture container stays slim (no pip).
"""

import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from meterfinder import db  # noqa: E402

COMMIT_EVERY = 25


def main() -> None:
    p = argparse.ArgumentParser(description="Ingest rtlamr JSON into the DB.")
    p.add_argument("--db", default=os.environ.get("DB_PATH", "meters.db"))
    p.add_argument("--source", default="0", help="SDR source id (device index/serial)")
    args = p.parse_args()

    con = db.connect(args.db)
    db.init_schema(con)

    n = 0
    last_ts = None
    # Seed a heartbeat immediately so health shows the source as alive on startup.
    db.update_heartbeat(con, args.source, last_ts, n)

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
        except Exception as exc:  # never let one bad packet kill the stream
            print(f"[ingest:{args.source}] skip: {exc}", file=sys.stderr)
            continue

        # Commit + refresh heartbeat together so health's last_ts/count stay
        # accurate (update_heartbeat commits too).
        if n % COMMIT_EVERY == 0:
            db.update_heartbeat(con, args.source, last_ts, n)
            print(f"\r[ingest:{args.source}] stored {n}", end="", file=sys.stderr, flush=True)

    con.commit()
    db.update_heartbeat(con, args.source, last_ts, n)
    print(f"\n[ingest:{args.source}] final: stored {n} readings", file=sys.stderr)


if __name__ == "__main__":
    main()
