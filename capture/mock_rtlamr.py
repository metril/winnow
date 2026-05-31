#!/usr/bin/env python3
"""Synthetic rtlamr JSON generator — lets the whole stack run with no RTL-SDR.

Emits one JSON line per "received packet" to stdout, exactly like
``rtlamr -format=json``, so it can be piped straight into ``ingest.py``.

It first *backfills* a few hours of history (so the dashboard and the
correlation tool have data immediately), then keeps emitting live packets at a
realistic cadence. Consumption is a monotonic odometer per meter; one meter's
rate spikes during a configurable window so the load-test correlation tool has
a clear winner to find.

Stdlib only.
"""

import argparse
import json
import random
import sys
import time
from datetime import datetime, timedelta, timezone

# (meter_id, outer msg Type, endpoint_type code, base_consumption, units/min)
METERS = [
    (18273645, "SCM",  4, 100000.0, 0.8),   # electric — the spike target
    (27384956, "SCM+", 7, 200000.0, 1.5),   # electric — steady moderate
    (39485012, "SCM",  4, 300000.0, 0.4),   # electric — light
    (48576123, "IDM",  4, 400000.0, 2.2),   # electric — heavy steady
    (55512345, "SCM",  2,  50000.0, 0.2),   # gas
    (66698765, "SCM", 11,  80000.0, 0.1),   # water
]

SPIKE_METER = 18273645
SPIKE_RATE = 30.0  # units/min while the simulated load is on (vs 0.8 baseline)


def consumption(meter_id, base, rate, elapsed_min, spike_lo, spike_hi):
    """Monotonic odometer value for a meter at elapsed_min since backfill start."""
    val = base + rate * elapsed_min
    if meter_id == SPIKE_METER:
        # add the extra load accumulated during the on-window up to now
        on = max(0.0, min(elapsed_min, spike_hi) - spike_lo)
        val += (SPIKE_RATE - rate) * on
    return round(val, 2)


def make_line(meter_id, msg_type, etype, value, ts):
    """Build a JSON object shaped like the relevant rtlamr message type."""
    if msg_type == "SCM+":
        msg = {"EndpointID": meter_id, "EndpointType": etype, "Consumption": value}
    elif msg_type == "IDM":
        msg = {"ERTSerialNumber": meter_id, "LastConsumptionCount": value}
    else:  # SCM
        msg = {"ID": meter_id, "Type": etype, "Consumption": value}
    obj = {"Time": ts.isoformat().replace("+00:00", "Z"), "Type": msg_type,
           "Message": msg}
    return json.dumps(obj)


def emit(ts, start, spike_lo, spike_hi, rng, drop_prob):
    elapsed = (ts - start).total_seconds() / 60.0
    for meter_id, msg_type, etype, base, rate in METERS:
        if rng.random() < drop_prob:
            continue  # this source missed the packet (coverage realism)
        val = consumption(meter_id, base, rate, elapsed, spike_lo, spike_hi)
        print(make_line(meter_id, msg_type, etype, val, ts), flush=True)


def main():
    p = argparse.ArgumentParser(description="Emit synthetic rtlamr JSON.")
    p.add_argument("--rate", type=float, default=2.0, help="live packets/sec (per cycle)")
    p.add_argument("--backfill-hours", type=float, default=4.0)
    p.add_argument("--spike-start-min", type=float, default=120.0,
                   help="load-on, minutes after backfill start")
    p.add_argument("--spike-end-min", type=float, default=210.0,
                   help="load-off, minutes after backfill start")
    p.add_argument("--drop-prob", type=float, default=0.05,
                   help="per-packet drop probability (per-source coverage)")
    p.add_argument("--seed", type=int, default=1)
    args = p.parse_args()

    rng = random.Random(args.seed)
    now = datetime.now(timezone.utc)
    start = now - timedelta(hours=args.backfill_hours)
    spike_lo, spike_hi = args.spike_start_min, args.spike_end_min

    # --- backfill: one cycle every 30 sim-seconds, emitted as fast as possible -
    step = timedelta(seconds=30)
    ts = start
    while ts < now:
        emit(ts, start, spike_lo, spike_hi, rng, args.drop_prob)
        ts += step
    print(f"[mock] backfilled {args.backfill_hours}h up to {now.isoformat()}",
          file=sys.stderr)

    # --- live: keep advancing real time ---------------------------------------
    interval = 1.0 / max(args.rate, 0.1)
    while True:
        now = datetime.now(timezone.utc)
        emit(now, start, spike_lo, spike_hi, rng, args.drop_prob)
        time.sleep(interval)


if __name__ == "__main__":
    main()
