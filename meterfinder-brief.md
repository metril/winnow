# Project Brief: Dockerized Electric-Meter Finder ("meterfinder")

## Prompt (paste this as the task)

Build me a Dockerized application that helps me identify **which electric meter
in my apartment building is mine** by passively receiving the 900 MHz ISM-band
ERT broadcasts from all nearby utility meters, storing every reading in a
database, and presenting a web dashboard for analysis and meter identification.

Deliver a `docker compose` stack with: (1) a capture service that drives an
RTL-SDR via `rtl_tcp` + `rtlamr` and ingests decoded JSON into a database, and
(2) a web app (backend API + dashboard frontend) for browsing observed meters,
charting their consumption over time, and — most importantly — running a
**load-test correlation tool** that ranks meters by how strongly their usage
tracks a deliberate load I switch on and off. Include a README with host setup,
USB passthrough, and run instructions. Iterate with me; start with a working
end-to-end skeleton, then flesh out the dashboard.

A reference prototype (`meterhunt.py`, attached) already implements the core
ingestion and delta logic in stdlib Python — reuse its data model and field
handling.

---

## Background

### The problem
In an apartment building, an RTL-SDR receiving ERT broadcasts will pick up
**dozens of meter IDs** (mine plus every neighbor's, often electric + gas +
water). I cannot physically access my meter and don't know where the meter bank
is. I need to figure out which transmitted meter ID is mine, purely from the
RF data plus controlled experiments on my own load.

### How meter reading works (technical facts the implementation depends on)
- US utility meters broadcast unencrypted consumption over the 900 MHz ISM band
  using Itron's **ERT** protocol. `rtlamr` decodes these with a cheap RTL-SDR.
  Reception is **fully passive** — no meter access required.
- Relevant `rtlamr` message types:
  - **SCM** — Standard Consumption Message. Reports a **cumulative** total
    (`Message.Consumption`); meter ID in `Message.ID`.
  - **SCM+** — like SCM, longer IDs / more precision. Cumulative total in
    `Message.Consumption`; meter ID in `Message.EndpointID`;
    commodity in `Message.EndpointType`.
  - **IDM / NetIDM** — interval data; cumulative-ish total in
    `Message.LastConsumptionCount`; meter ID in `Message.ERTSerialNumber`.
    NetIDM (net meters) also reports production.
- **Key insight:** the `Consumption` value is an **odometer** (monotonically
  increasing), NOT an instantaneous reading. So a meter's usage over any time
  span is simply `max(consumption) - min(consumption)` over the packets in that
  span. No unit calibration is needed for identification — only relative
  magnitude and timing matter. (Units differ across electric/gas/water, so do
  NOT identify the meter by picking the largest raw number.)

### Identification strategy (the dashboard must support this)
1. Run capture for a day or two to enumerate all meter IDs and baseline them.
2. **Load test:** switch on a large, known load I control (e.g. space heater or
   oven) for a defined 1–2 hour block at an odd hour, then switch it off.
3. The meter whose consumption delta **spikes during that window and returns to
   baseline after** is mine. A single clean on/off step is far more distinctive
   than comparing daily totals across neighbors.
4. Lock onto that meter ID; the app should emit the `rtlamr -filterid=<id>`
   command for a permanent downstream pipeline.

---

## Runtime environment & hardware constraints

- Runs on a **Linux homelab host** (likely a Raspberry Pi or x86 server) with an
  **RTL-SDR dongle** (RTL2832U + R820T2) plugged in. NOT on my Mac.
- **USB passthrough is required.** The capture container needs access to the
  USB device — mount `/dev/bus/usb` and pass `--device` / appropriate cgroup
  rules, or run that single container privileged. Document this clearly.
- **Host kernel module blacklist (critical gotcha):** the DVB-T driver claims
  the dongle and must be blacklisted on the host, or `rtl_tcp` fails with the
  device busy. README must instruct blacklisting `dvb_usb_rtl28xxu` (and
  `rtl2832`, `rtl2830`) and reloading.
- `rtlamr` connects to an `rtl_tcp` server (default `127.0.0.1:1234`). Run
  `rtl_tcp` and `rtlamr` together in the capture container (a supervisor or a
  small wrapper script), or as two services sharing a network namespace.
- The packaging pattern used by the `rtlamr2mqtt` project (bundling `rtl_tcp` +
  `rtlamr` in one container) is a good reference for the **capture half** — but
  the dashboard here is bespoke, so do not just reuse that project wholesale.

---

## Functional requirements

### Capture service
- Launch `rtl_tcp`, then `rtlamr -msgtype=scm,scm+,idm -format=json`, and ingest
  each JSON line into the database.
- Robust field extraction with fallbacks across message types (see prototype):
  - id: `EndpointID` → `ID` → `ERTSerialNumber`
  - consumption: `Consumption` → `LastConsumptionCount` → `LastConsumption`
  - also capture `EndpointType` when present (for commodity filtering)
- **Store the full raw JSON line** for every reading so nothing is lost if a
  field name differs on my meter.
- Skip/ignore unparseable lines without crashing.
- Auto-restart `rtl_tcp`/`rtlamr` if they die; surface capture health to the API.
- Configurable SDR params (see Config).

### Storage
- Default to **SQLite** on a Docker volume (simple, portable, single file).
  Structure the data layer so it can be swapped to Postgres/TimescaleDB later,
  but do not require an extra DB container by default.
- Append-only `readings` time series + small tables for meter annotations and
  test windows (see Data model).

### Web dashboard (the core deliverable)
- **Meter list / leaderboard:** every observed meter ID with message type,
  endpoint type (commodity, when known), packet count, packets/hour (reliability
  indicator), first seen, last seen, latest consumption, and total movement over
  the selected time range. Sortable; filterable by message type and endpoint
  type (electric vs gas/water).
- **Per-meter detail:** time-series chart of cumulative consumption and a
  derived per-interval delta chart. Selectable time range and bucket size
  (e.g. 5m / 1h / 1d).
- **Load-test correlation tool (highest priority feature):**
  - Let me define a test window — either by clicking **Start test / Stop test**
    buttons (timestamps now) or by entering a labeled time range after the fact
    ("space heater, 21:00–22:30").
  - For each meter, compute: delta during the window, in-window hourly rate, and
    out-of-window baseline hourly rate. **Rank meters by the ratio of in-window
    rate to baseline rate (and by window-delta magnitude).** The top result with
    a clean, high ratio is the candidate.
  - Visualize the window as a shaded region overlaid on the candidate meters'
    delta charts so I can eyeball the spike lining up with my on/off.
- **Candidate management:** flag/favorite a meter, add notes, and "Lock as mine."
  Locking shows a copy-ready `rtlamr -filterid=<id> -msgtype=<type>` command for
  my downstream MQTT/Home Assistant pipeline.
- **Capture health indicator:** is the SDR alive, are packets flowing, current
  packet rate, total unique meters seen.

---

## Suggested architecture & stack

Use your judgment, but a clean target:

- `docker-compose.yml` with two services sharing a DB volume:
  - **capture** — Debian/Alpine base with `rtl-sdr` + `rtlamr` (Go binary) +
    a small Python ingester. Handles USB. Restart policy `unless-stopped`.
  - **app** — **FastAPI** backend serving a JSON API + a lightweight frontend.
    Frontend can be server-rendered + **Chart.js**, or a small Vite/React SPA —
    keep dependencies minimal; this is a single-user diagnostic tool, not a
    production multi-tenant app.
- The ingester and the API share the same data layer module so extraction and
  delta logic live in one place.
- All consumption-delta math reuses the prototype's `max - min` approach.

---

## Data model (starting point)

```sql
CREATE TABLE readings (
    ts           TEXT NOT NULL,   -- ISO8601 from rtlamr Time field
    msg_type     TEXT,            -- SCM / SCM+ / IDM
    endpoint_id  INTEGER,
    endpoint_type INTEGER,        -- commodity code, when present
    consumption  REAL,            -- cumulative odometer value
    raw          TEXT             -- full JSON line, never discarded
);
CREATE INDEX idx_meter_ts ON readings(endpoint_id, ts);

CREATE TABLE meters (            -- annotations, one row per endpoint_id
    endpoint_id  INTEGER PRIMARY KEY,
    label        TEXT,
    is_candidate INTEGER DEFAULT 0,
    is_mine      INTEGER DEFAULT 0,
    notes        TEXT
);

CREATE TABLE test_windows (
    id        INTEGER PRIMARY KEY,
    label     TEXT,
    start_ts  TEXT NOT NULL,
    end_ts    TEXT             -- null while a test is running
);
```

## Core delta logic (reuse)

- Span usage for a meter: `MAX(consumption) - MIN(consumption)` over packets in
  the span (consumption is monotonic, so robust to out-of-order arrivals).
- Bucketed deltas: group by a timestamp prefix (10 chars = day, 13 = hour) or by
  rounding ts to the chosen bucket size.
- Correlation score for a test window `[start, end]`:
  - `window_delta = MAX-MIN` of consumption within the window
  - `window_rate = window_delta / window_hours`
  - `baseline_rate = (total movement outside window) / (hours outside window)`
  - `score = window_rate / max(baseline_rate, epsilon)`
  - Rank desc by `score`, tie-break by `window_delta`. Display `packets` in
    window as a confidence signal.

---

## Configuration (env vars / compose)

- `RTLAMR_MSGTYPE` (default `scm,scm+,idm`)
- `RTLAMR_FILTERID` (optional; empty during discovery, set after locking)
- SDR tuning: gain, `ppm` frequency-correction, center frequency
  (rtlamr default `912600155` Hz) — pass through to `rtl_tcp`/`rtlamr`.
- DB path / volume location.
- App port.

---

## Definition of done

1. `docker compose up` brings up capture + app; with the dongle attached and the
   host module blacklisted, decoded readings begin landing in the DB.
2. Dashboard lists all observed meters with packet counts and consumption, and
   updates as new data arrives.
3. I can run a load test (start/stop buttons), and the correlation view returns a
   ranked list with my deliberately-loaded meter at or near the top, with the
   spike visibly aligned to my on/off window.
4. I can lock a meter as "mine" and copy the resulting `rtlamr -filterid=...`
   command.
5. README documents: host prerequisites (module blacklist, USB passthrough),
   bringing the stack up, tuning the SDR, and interpreting the correlation tool.

## Stretch / nice-to-have

- Live/auto-refreshing dashboard (websocket or polling).
- Endpoint-type → commodity name mapping with an electric-only filter toggle.
- CSV / SQLite export of a meter's history.
- Multiple saved test windows with a combined "best candidate across all tests"
  ranking (the meter that wins every test is almost certainly mine).
- Optional Postgres/TimescaleDB profile in compose.

## Notes for the build

- Attach `meterhunt.py` as the reference implementation of ingestion + delta SQL.
- Build a minimal end-to-end skeleton first (capture → DB → bare meter list),
  confirm packets flow, then iterate on charts and the correlation tool.
- This is a single-user diagnostic tool on a trusted LAN — don't over-engineer
  auth or scaling; prioritize correctness of the consumption math and the
  clarity of the correlation view.
