# winnow

Find **which utility meter in your building is yours** — electric, gas, or water
— by passively receiving the 900 MHz ISM-band ERT broadcasts from every nearby
meter, then **winnowing** yours out of the crowd using a Home Assistant smart
plug as ground truth. Once identified, winnow **feeds the meter back into Home
Assistant** as a real energy/power sensor via MQTT Discovery.

Reception is fully passive (no meter access). Meters broadcast unencrypted
consumption (Itron ERT), decoded with a cheap RTL-SDR via `rtl_tcp` + `rtlamr`.

## How it identifies your meter

Plug a known load (space heater, kettle) into a smart plug that Home Assistant
monitors. The plug tells winnow *exactly* when the load is on and how many watts
it draws. winnow correlates every meter's per-minute consumption against the
plug's power profile (Pearson `corr()`, computed in TimescaleDB) — the meter
whose usage tracks the plug is yours. It runs **continuously** (auto-opening a
window whenever the plug turns on) and **on demand** ("analyze last N hours").

## Architecture

```
rtlamr×N ─► capture×N ─INSERT+NOTIFY─► TimescaleDB ─(parallel corr/aggregates)
                                          │  ▲ LISTEN
HA plug ══WebSocket══► worker ◄───────────┘  │
                       └─ MQTT publish ═► EMQX ═► Home Assistant   (singleton)
app (Go net/http) ─LISTEN→ SSE ─► React dashboard   (no polling)
```

- **db** — TimescaleDB (hypertables, `time_bucket`, a continuous aggregate for
  per-minute deltas, compression + retention).
- **capture** — Go supervisor running one `rtl_tcp`+`rtlamr` pair per dongle
  (true multi-process parallelism), storing readings + `NOTIFY`.
- **worker** — singleton: subscribes to the HA WebSocket for live plug power,
  builds auto windows, and on each new reading publishes published meters to HA
  over MQTT. Event-driven via Postgres `LISTEN/NOTIFY`.
- **app** — Go API serving JSON + Server-Sent Events and the embedded React SPA.

Everything is **multi-meter**: track, publish, plot, and ignore any number of
meters independently. Each published meter is its own HA device.

## Quick start

```bash
cp .env.example .env       # set POSTGRES_PASSWORD; RTL_DEVICES=0,1,2 for 3 dongles
docker compose up --build -d
# open http://<host>:8000
```

Try it with **no hardware**: `CAPTURE_MOCK=1 docker compose up --build` (synthetic
meters, one with a built-in spike).

## Host prerequisites (real capture)

1. **Blacklist the DVB-T driver** (it grabs the dongle, so `rtl_tcp` fails):
   ```bash
   echo -e "blacklist dvb_usb_rtl28xxu\nblacklist rtl2832\nblacklist rtl2830" | \
     sudo tee /etc/modprobe.d/blacklist-rtlsdr.conf
   sudo modprobe -r dvb_usb_rtl28xxu rtl2832 rtl2830 2>/dev/null || true   # or reboot
   ```
2. **USB passthrough** is already wired in `docker-compose.yml` (`/dev/bus/usb` +
   a cgroup rule). For multiple dongles, set `RTL_DEVICES=0,1,2`.

## Configure Home Assistant (in the dashboard)

Open **Settings → Integrations** and enter:
- **Home Assistant**: base URL + a long-lived access token (Profile → Long-lived
  tokens). Used to read the plug's power history (REST) and live state (WebSocket).
- **MQTT broker**: your HA broker's host/port/user/pass (the same broker HA
  listens to, so Discovery entities appear). Hit **Test connections**.
- **Reference plug**: pick the smart-plug power sensor; set the on/off threshold.

Config is stored in the database (secrets masked) — no `.env` editing needed.

## Identify & publish

1. **Identify** tab → switch your load on/off → the ranked table puts your meter
   on top with a high correlation `r`, and the overlay chart shows the spike
   lining up with the plug.
2. **Track** it (and any others), then **Publish to HA**. winnow creates
   `sensor.winnow_<id>_energy` (total_increasing → Energy dashboard), `_power`
   (W), and `_signal` (pkt/h) via MQTT Discovery. Tune the per-meter
   multiplier/unit so energy reads real kWh.

## Development

```bash
# backend (Go 1.22+)
go build ./...
docker run -d --name pg -e POSTGRES_USER=test -e POSTGRES_PASSWORD=test \
  -e POSTGRES_DB=test -p 55433:5432 timescale/timescaledb:2.17.2-pg16
TEST_DATABASE_URL=postgres://test:test@localhost:55433/test go test ./...

# frontend
cd app/frontend && npm install && npm run dev   # proxies /api to :8000
```

## Layout

```
cmd/{capture,worker,api}   the three Go services
internal/{db,ha,mqtt,ert,config,model}   data layer + integrations
app/frontend/              React + Vite + Recharts dashboard (embedded into api)
{capture,worker,app}/Dockerfile  multi-stage Go builds
meterhunt.py               original stdlib prototype (reference)
NAMES.md                   the renaming story
```

> Single-user diagnostic + telemetry tool for a trusted LAN — no auth by design.
> Runs on x86-64 and ARM (Raspberry Pi / Rockchip); images build natively per arch.
