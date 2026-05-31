# --- build rtlamr (Go) -------------------------------------------------------
FROM golang:1.22-bookworm AS rtlamr
RUN go install github.com/bemasher/rtlamr@latest

# --- capture runtime ---------------------------------------------------------
FROM debian:bookworm-slim

# rtl-sdr provides rtl_tcp / rtl_eeprom; python3 + python3-psycopg2 run the
# ingester and talk to PostgreSQL (apt package, so no pip needed in this image).
RUN apt-get update \
    && apt-get install -y --no-install-recommends rtl-sdr python3 python3-psycopg2 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=rtlamr /go/bin/rtlamr /usr/local/bin/rtlamr

WORKDIR /app
# Shared data layer + capture scripts.
COPY meterfinder /app/meterfinder
COPY capture/ingest.py capture/mock_rtlamr.py capture/run.sh /app/capture/
RUN chmod +x /app/capture/run.sh

ENV PYTHONUNBUFFERED=1

CMD ["/app/capture/run.sh"]
