#!/usr/bin/env bash
# Capture supervisor: drives N RTL-SDR dongles, one rtl_tcp+rtlamr pair each,
# piping decoded JSON into ingest.py. Each pair is supervised independently so
# one dongle dying never takes the others down.
#
# Mock mode (CAPTURE_MOCK=1) skips the SDRs entirely and runs two synthetic
# sources, so the full stack works on a host with no hardware.
set -u

RTLAMR_MSGTYPE="${RTLAMR_MSGTYPE:-scm,scm+,idm}"
RTLAMR_FILTERID="${RTLAMR_FILTERID:-}"
FREQ="${FREQ:-912600155}"
GAIN="${GAIN:-}"
PPM="${PPM:-0}"
RTL_DEVICES="${RTL_DEVICES:-0}"
HERE="$(cd "$(dirname "$0")" && pwd)"

ingest() { python3 "$HERE/ingest.py" --source "$1"; }

# Wait for PostgreSQL to be ready, then create the schema once up front so the
# N per-device ingesters don't each race to initialize it.
echo "[run] waiting for PostgreSQL and initializing schema" >&2
for attempt in $(seq 1 30); do
  if PYTHONPATH="$HERE/.." python3 -c \
       "from meterfinder import db; db.init_schema(db.connect())" 2>/dev/null; then
    echo "[run] schema ready" >&2
    break
  fi
  echo "[run] postgres not ready yet (attempt $attempt); retrying in 2s" >&2
  sleep 2
done

# Supervise one real SDR: device id $1 on tcp port $2.
supervise_sdr() {
  local dev="$1" port="$2"
  local filter=""
  [ -n "$RTLAMR_FILTERID" ] && filter="-filterid=$RTLAMR_FILTERID"
  local gainarg=""
  [ -n "$GAIN" ] && gainarg="-g $GAIN"
  while true; do
    echo "[run] starting rtl_tcp dev=$dev port=$port" >&2
    # shellcheck disable=SC2086
    rtl_tcp -d "$dev" -a 127.0.0.1 -p "$port" -P "$PPM" $gainarg &
    local rtl_pid=$!
    sleep 2  # give rtl_tcp time to claim the device and listen
    echo "[run] starting rtlamr dev=$dev -> ingest source=$dev" >&2
    # shellcheck disable=SC2086
    rtlamr -server="127.0.0.1:$port" -msgtype="$RTLAMR_MSGTYPE" \
           -format=json -centerfreq="$FREQ" $filter | ingest "$dev"
    echo "[run] dev=$dev pipeline exited; restarting in 5s" >&2
    kill "$rtl_pid" 2>/dev/null
    wait "$rtl_pid" 2>/dev/null
    sleep 5
  done
}

# Supervise one synthetic source.
supervise_mock() {
  local name="$1" seed="$2"
  while true; do
    echo "[run] starting mock source=$name" >&2
    python3 "$HERE/mock_rtlamr.py" --seed "$seed" | ingest "$name"
    echo "[run] mock $name exited; restarting in 5s" >&2
    sleep 5
  done
}

pids=()
if [ "${CAPTURE_MOCK:-0}" = "1" ]; then
  echo "[run] CAPTURE_MOCK=1 — using synthetic data, no SDR" >&2
  supervise_mock "mock-0" 1 & pids+=($!)
  supervise_mock "mock-1" 2 & pids+=($!)
else
  i=0
  IFS=',' read -ra devs <<< "$RTL_DEVICES"
  for dev in "${devs[@]}"; do
    dev="$(echo "$dev" | tr -d '[:space:]')"
    [ -z "$dev" ] && continue
    supervise_sdr "$dev" "$((1234 + i))" & pids+=($!)
    i=$((i + 1))
  done
fi

# Exit (and let Docker restart the container) if every supervisor dies.
trap 'kill "${pids[@]}" 2>/dev/null' TERM INT
wait -n "${pids[@]}"
echo "[run] a supervisor exited unexpectedly; shutting down for restart" >&2
kill "${pids[@]}" 2>/dev/null
exit 1
