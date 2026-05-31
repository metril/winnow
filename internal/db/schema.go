package db

import (
	"context"
	"log"
)

// baseDDL is plain-Postgres schema (works with or without TimescaleDB).
const baseDDL = `
CREATE TABLE IF NOT EXISTS readings (
    ts            TIMESTAMPTZ NOT NULL,
    msg_type      TEXT,
    endpoint_id   BIGINT,
    endpoint_type INTEGER,
    consumption   DOUBLE PRECISION,
    raw           TEXT,
    source        TEXT
);
CREATE INDEX IF NOT EXISTS idx_meter_ts ON readings(endpoint_id, ts);
CREATE INDEX IF NOT EXISTS idx_ts ON readings(ts);

CREATE TABLE IF NOT EXISTS meters (
    endpoint_id    BIGINT PRIMARY KEY,
    label          TEXT,
    notes          TEXT,
    is_candidate   BOOLEAN DEFAULT FALSE,
    is_mine        BOOLEAN DEFAULT FALSE,
    ignored        BOOLEAN DEFAULT FALSE,
    publish        BOOLEAN DEFAULT FALSE,
    pub_name       TEXT,
    pub_multiplier DOUBLE PRECISION DEFAULT 1,
    pub_unit       TEXT
);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT
);

CREATE TABLE IF NOT EXISTS reference_samples (
    ts        TIMESTAMPTZ NOT NULL,
    entity_id TEXT,
    power_w   DOUBLE PRECISION
);
CREATE INDEX IF NOT EXISTS idx_ref_entity_ts ON reference_samples(entity_id, ts);

CREATE TABLE IF NOT EXISTS test_windows (
    id       SERIAL PRIMARY KEY,
    label    TEXT,
    start_ts TIMESTAMPTZ NOT NULL,
    end_ts   TIMESTAMPTZ,
    source   TEXT DEFAULT 'manual'
);

CREATE TABLE IF NOT EXISTS capture_heartbeat (
    source      TEXT PRIMARY KEY,
    last_ts     TIMESTAMPTZ,
    total_count BIGINT DEFAULT 0,
    updated_at  TIMESTAMPTZ
);
`

// timescaleSteps are run one-by-one; failures are logged but non-fatal so the
// app still runs on plain Postgres (e.g. a minimal dev DB). On the real
// timescaledb image these enable hypertables, the continuous aggregate, and
// compression/retention.
var timescaleSteps = []string{
	`CREATE EXTENSION IF NOT EXISTS timescaledb`,
	`SELECT create_hypertable('readings', 'ts', if_not_exists => TRUE, migrate_data => TRUE)`,
	`SELECT create_hypertable('reference_samples', 'ts', if_not_exists => TRUE, migrate_data => TRUE)`,
	`CREATE MATERIALIZED VIEW IF NOT EXISTS readings_1m
	   WITH (timescaledb.continuous) AS
	   SELECT endpoint_id,
	          time_bucket('1 minute', ts) AS bucket,
	          min(consumption) AS min_c,
	          max(consumption) AS max_c,
	          count(*)         AS n
	   FROM readings
	   WHERE consumption IS NOT NULL AND endpoint_id IS NOT NULL
	   GROUP BY endpoint_id, bucket
	   WITH NO DATA`,
	`SELECT add_continuous_aggregate_policy('readings_1m',
	     start_offset => INTERVAL '3 hours',
	     end_offset   => INTERVAL '1 minute',
	     schedule_interval => INTERVAL '1 minute')`,
	`ALTER TABLE readings SET (timescaledb.compress, timescaledb.compress_segmentby = 'endpoint_id')`,
	`SELECT add_compression_policy('readings', INTERVAL '7 days')`,
	`SELECT add_retention_policy('readings', INTERVAL '180 days')`,
}

// InitSchema creates the base schema (idempotent) and best-effort TimescaleDB
// features. Safe to call from every service at startup: a session advisory lock
// serializes concurrent initializers (capture/worker/api start together), which
// avoids Postgres concurrent-DDL races (e.g. duplicate pg_type).
func (d *DB) InitSchema(ctx context.Context) error {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(739104)"); err != nil {
		return err
	}
	defer conn.Exec(ctx, "SELECT pg_advisory_unlock(739104)")

	if _, err := conn.Exec(ctx, baseDDL); err != nil {
		return err
	}
	for _, step := range timescaleSteps {
		if _, err := conn.Exec(ctx, step); err != nil {
			// Already-exists / policy-exists / not-timescale are all tolerable.
			log.Printf("[db] timescale step skipped: %v", err)
		}
	}
	return nil
}
