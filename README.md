# meterfinder

Find **which electric meter in your building is yours** by passively receiving
the 900 MHz ISM-band ERT broadcasts from every nearby utility meter, storing the
readings, and running a **load-test correlation tool**: switch a big known load
on and off, and the dashboard ranks meters by how strongly their consumption
tracks your window. The meter whose usage spikes during the window and returns to
baseline after is almost certainly yours.

Reception is **fully passive** — no meter access required. US utility meters
broadcast unencrypted consumption (Itron ERT), decoded with a cheap RTL-SDR via
`rtl_tcp` + [`rtlamr`](https://github.com/bemasher/rtlamr).

```
┌─────────── capture container ───────────┐   ┌──── app container ────┐
│ rtl_tcp ─► rtlamr ─► ingest.py ─┐        │   │ FastAPI  +  React SPA │
│  (one pair per dongle)          ▼        │   │   ▼                   │
└─────────────────────────────────┼────────┘   └───┼───────────────────┘
                                   ▼                ▼
                          ┌────────────────────────────┐
                          │  PostgreSQL (db container)   │
                          └────────────────────────────┘
```

The database is **PostgreSQL**: with multiple SDR ingesters writing concurrently
and a dashboard polling constantly, MVCC means writers never block each other and
reads never block writers. (The data layer in `meterfinder/db.py` is the single
place that talks to it.)

## TL;DR

```bash
cp .env.example .env

# Try it with NO hardware (synthetic meters, one with a built-in spike):
CAPTURE_MOCK=1 docker compose up --build
# open http://localhost:8000  → Load Test tab → add a window over the spike

# Real capture (dongle attached, host modules blacklisted — see below):
docker compose up --build -d
```

## Architecture support (x86 / ARM / Raspberry Pi)

Runs on **x86-64 and ARM** — just `docker compose build` on the target host and
the images build natively for its architecture. All base images are multi-arch,
`rtlamr` is compiled from Go source (so it's always the right arch), and the SPA
builds with the matching Node. Verified on x86-64; arm64 and armv7 use the same
multi-arch bases.

- **64-bit ARM (arm64 — Pi 3/4/5 on 64-bit Pi OS, most SBCs):** works as-is.
- **32-bit ARM (armv7 — older/32-bit Pi OS):** also works — `requirements.txt`
  intentionally uses plain `uvicorn` (not `uvicorn[standard]`), because that
  extra's `uvloop`/`httptools` have no 32-bit-ARM wheels and would need a
  compiler. They're performance-only and unused here (the dashboard polls).
- **Apple Silicon Mac:** the images build, but USB/RTL-SDR passthrough doesn't
  work under Docker Desktop — run capture on the Linux homelab host, as intended.

## Host prerequisites (real capture)

### 1. Blacklist the DVB-T kernel driver (critical)
The kernel's DVB-T driver grabs the dongle, so `rtl_tcp` fails with *device
busy*. Blacklist it on the **host**:

```bash
sudo tee /etc/modprobe.d/blacklist-rtlsdr.conf >/dev/null <<'EOF'
blacklist dvb_usb_rtl28xxu
blacklist rtl2832
blacklist rtl2830
EOF
sudo modprobe -r dvb_usb_rtl28xxu rtl2832 rtl2830 2>/dev/null || true
# or just reboot
```

Verify nothing holds the device: `lsusb` shows it (Realtek RTL2838), and
`dmesg | grep -i rtl` no longer shows the DVB driver claiming it.

### 2. USB passthrough
`docker-compose.yml` already mounts `/dev/bus/usb` and adds a USB cgroup rule, so
any plugged dongle is visible inside the capture container. If your host is fussy
about device rules, comment those two keys out and uncomment `privileged: true`
on the `capture` service instead.

## Multiple RTL-SDRs

More dongles = more coverage and more packets/hour (better odds of cleanly
hearing your meter). One capture container drives them all — one
`rtl_tcp`+`rtlamr` pair per device, each supervised and restarted independently,
all writing to the same DB. Every reading is tagged with its `source`.

Set `RTL_DEVICES` in `.env` to a comma-separated list of **device indices or
serials**:

```ini
RTL_DEVICES=0,1,2          # by index
RTL_DEVICES=heater,roof    # by serial
```

Indices can shuffle when you replug dongles, so for a permanent multi-dongle
setup assign stable serials once (on the host or in a shell inside the
container):

```bash
rtl_eeprom -d 0 -s heater
rtl_eeprom -d 1 -s roof
```

A meter heard by several dongles produces duplicate rows — harmless, because all
consumption math is `MAX(consumption) − MIN(consumption)` over a span (the value
is a monotonic odometer). The meter list shows a `srcs` column = how many
dongles heard each meter. The health bar reports each dongle independently.

## SDR tuning

| var    | meaning                                  | default     |
|--------|------------------------------------------|-------------|
| `FREQ` | center frequency (Hz)                    | `912600155` |
| `GAIN` | tuner gain (dB); empty = auto            | *(auto)*    |
| `PPM`  | frequency-correction in ppm              | `0`         |

If you see few/no packets: confirm the modules are blacklisted, try `GAIN=` set
to a fixed value (e.g. `40`), and set `PPM` from a known-good calibration.

## Using the dashboard — finding your meter

1. **Enumerate.** Let capture run for a day or two. The **Meters** tab lists
   every observed meter: commodity (electric/gas/water when known), packet count,
   packets/hour (reliability), sources, first/last seen, latest consumption, and
   total movement. Sort and filter; flip on **electric only** to hide gas/water.
2. **Baseline + load test (the good part).** Go to **Load Test**, minimize other
   usage, switch on a large load you control (space heater, oven), press
   **Start test**, wait 1–2 hours, switch it off, press **Stop test**. (Odd hours
   work best — less neighbor noise.) You can also add a window **after the fact**
   if you already noted the times.
3. **Read the ranking.** The correlation table ranks meters by
   `in-window rate ÷ baseline rate`. A single clean winner standing well above
   the rest is your meter. The top candidates' delta charts draw your window as a
   shaded band — eyeball the spike lining up with your on/off.
4. **Confirm.** Run a second test at a different time. The **Best candidate
   across all tests** panel surfaces the meter that wins every time.
5. **Lock it.** Click **Lock as mine** (or 🔒). The app shows a copy-ready
   command for your downstream MQTT/Home Assistant pipeline:
   ```
   rtlamr -filterid=<id> -msgtype=<type> -format=json
   ```
   Put that id in `RTLAMR_FILTERID` in `.env` to have capture record only your
   meter going forward.

## How the correlation score works

For a window `[start, end]`, per meter:

```
window_delta  = MAX(consumption) − MIN(consumption)  within the window
window_rate   = window_delta / window_hours
baseline_rate = (total movement outside the window) / (hours outside)
score         = window_rate / max(baseline_rate, ε)
```

Ranked by `score` (desc), tie-broken by `window_delta`. Packets-in-window is
shown as a confidence signal. Because consumption is an odometer, this is robust
to out-of-order packet arrival and needs **no unit calibration** — only relative
magnitude and timing matter, so it works identically across electric/gas/water.

## Data & export

Data lives in **PostgreSQL** on the `pgdata` Docker volume (full raw JSON of every
packet is kept in the `raw` column, so nothing is lost if a field name differs on
your meter). Per-meter history exports as CSV from the meter detail view.
Connection is via `DATABASE_URL` (see `.env`); the data layer
(`meterfinder/db.py`) is the only module that touches the DB.

## Development

```bash
python3 -m venv .venv
.venv/bin/pip install pytest fastapi uvicorn psycopg2-binary

# Tests run against a real Postgres (the correlation math lives in SQL):
docker run -d --name mf-pg -e POSTGRES_USER=test -e POSTGRES_PASSWORD=test \
  -e POSTGRES_DB=test -p 55432:5432 postgres:16
TEST_DATABASE_URL=postgresql://test:test@localhost:55432/test \
  .venv/bin/python -m pytest tests/

# backend (serves API; build the SPA first or run vite dev separately).
# PYTHONPATH=. so the repo-root meterfinder/ package is importable alongside
# meterfinder_api/ (in the container both live under /app, so it's automatic).
PYTHONPATH=. DATABASE_URL=postgresql://test:test@localhost:55432/test \
  .venv/bin/uvicorn meterfinder_api.main:app --reload --app-dir app

# frontend dev server (proxies /api to :8000)
cd app/frontend && npm install && npm run dev
```

## Layout

```
meterfinder/db.py     shared data layer: schema, extraction, delta/correlation SQL
capture/              rtl_tcp+rtlamr supervisor, ingester, mock generator, Dockerfile
app/meterfinder_api/  FastAPI JSON API + static SPA host
app/frontend/         Vite + React + Recharts dashboard
tests/                pytest for the math
meterhunt.py          original stdlib prototype (reference)
```

> Single-user diagnostic tool for a trusted LAN — no auth or multi-tenant
> scaling by design.
