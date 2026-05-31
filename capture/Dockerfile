# --- build rtlamr (Go) -------------------------------------------------------
FROM golang:1.22-bookworm AS rtlamr
RUN go install github.com/bemasher/rtlamr@latest

# --- capture runtime ---------------------------------------------------------
FROM debian:bookworm-slim

# rtl-sdr provides rtl_tcp / rtl_eeprom; python3 runs the stdlib ingester.
RUN apt-get update \
    && apt-get install -y --no-install-recommends rtl-sdr python3 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=rtlamr /go/bin/rtlamr /usr/local/bin/rtlamr

WORKDIR /app
# Shared data layer (stdlib only) + capture scripts.
COPY meterfinder /app/meterfinder
COPY capture/ingest.py capture/mock_rtlamr.py capture/run.sh /app/capture/
RUN chmod +x /app/capture/run.sh

ENV DB_PATH=/data/meters.db \
    PYTHONUNBUFFERED=1
VOLUME ["/data"]

CMD ["/app/capture/run.sh"]
