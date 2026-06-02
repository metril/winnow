# winnow

Find **which utility meter in your building is yours** — electric, gas, or water
— by passively receiving the 900 MHz ISM-band ERT broadcasts from every nearby
meter, then **winnowing** yours out of the crowd using a Home Assistant smart
plug as ground truth. Once identified, winnow **feeds the meter back into Home
Assistant** as a real energy/power sensor via MQTT Discovery.

Reception is fully passive (no meter access). Meters broadcast unencrypted
consumption (Itron ERT), decoded with a cheap RTL-SDR via `rtl_tcp` + `rtlamr`.

## How it identifies your meter

winnow uses the **sum of your Home-Assistant-monitored devices** as ground truth.
Your whole-home meter must, at every instant, draw at least that sum — and its
floor is your baseline draw. winnow correlates **and regresses** every meter's
per-minute consumption against the total monitored power (Pearson `corr()` +
`regr_*`, in TimescaleDB):

- the meter that tracks the aggregate with high `r` is yours;
- the regression **calibrates its units** (slope → kWh per meter-unit, suggested
  for the published sensor) and **estimates your unmonitored baseline** (intercept);
- a meter that can't cover your **minimum monitored power** is down-ranked.

You point winnow at one pre-aggregated HA sensor (a **Utility Meter**/energy
sensor, or a **Group "sum"** of device power), or select several devices —
winnow can even create the HA Group-sum helper for you. A deliberate load test
still works (a spike in the aggregate) but isn't required.

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
   a cgroup rule).

**SDR auto-detection:** `RTL_DEVICES=auto` (the default) detects and uses *every*
connected RTL-SDR dongle at startup, tagging each by its **serial** — so USB
index ordering never matters and swapping/adding dongles needs no config (just
restart the capture container). Only RTL2832U-based dongles work (rtlamr
constraint); other SDRs (Airspy/HackRF/SDRplay/…) are not supported. If several
dongles share the generic serial `00000001`, give them unique ones once with
`rtl_eeprom -d <i> -s <name>` for stable identity across reboots. To pin a
specific set instead, set `RTL_DEVICES=0,1` (a comma list of indices).

## Remote SDRs (capture on another host)

Place a dongle on a second machine (a Pi in the garage, a box near the meter) and
stream its readings back. The remote runs the **same capture image in agent mode**:
it decodes locally and pushes only decoded readings over an encrypted,
mutually-authenticated WebSocket — a few packets/min, not raw IQ.

**Security:** each side has a Curve25519 keypair. The session is end-to-end
encrypted and authenticated at the application layer (NaCl `box`/`secretbox`), so
the data is confidential even through a TLS-terminating proxy. The app also exposes
an optional self-signed TLS listener on `:8443` (defense-in-depth).

Pairing (the SSH `authorized_keys` model), all from **System → Remote agents**:

1. On the main host, the app auto-generates its keypair + TLS cert on first boot.
   Copy the **server public key** and **agent URL** from the dashboard.
2. On the remote host, build the image (`docker build -f capture/Dockerfile -t
   winnow-capture .`) and run it in agent mode:
   ```bash
   docker run -d --name winnow-agent --restart unless-stopped \
     --device /dev/bus/usb:/dev/bus/usb -v winnow-agent-key:/data \
     -e AGENT_URL=wss://<main-host>:8443/api/agent/ws \
     -e AGENT_SERVER_KEY=<server public key> \
     -e AGENT_NAME=garage \
     winnow-capture
   ```
   (Same host prerequisites as below — blacklist the DVB-T driver, USB passthrough.)
3. On first start the agent **prints its own public key**. Paste it into
   **Authorized agents** in the dashboard. It connects within seconds and its dongle
   appears in the inventory and coverage like a local one, tunable per-dongle.

Optional: pin the outer TLS with `-e AGENT_SERVER_FINGERPRINT=<TLS fingerprint
shown in the dashboard>`. If you later add a reverse proxy, point `AGENT_URL` at it
and drop the fingerprint — the app-layer auth is unchanged. The agent key persists
in the `winnow-agent-key` volume, so restarts don't need re-authorizing.

## Configure Home Assistant (in the dashboard)

Open **Settings → Integrations** and enter:
- **Home Assistant**: base URL + a long-lived access token (Profile → Long-lived
  tokens). Used to read the plug's power history (REST) and live state (WebSocket).
- **MQTT broker**: your HA broker's host/port/user/pass (the same broker HA
  listens to, so Discovery entities appear). Hit **Test connections**.
- **Monitored consumption**: load your HA sensors and either pick one
  pre-aggregated sensor (Utility Meter / energy, or a Group "sum" of power), or
  check several devices and **Use selected** (winnow sums) or **Create HA sum
  helper** (winnow makes the Group-sum in HA). The **floor** (your minimum draw)
  is shown once samples flow.

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
