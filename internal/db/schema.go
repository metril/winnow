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

-- utility_energy holds the user's billed energy pulled from HA long-term
-- statistics (e.g. the Opower/Eversource integration), one row per period bucket
-- normalized to consumed kWh. This is the user's REAL whole-home meter, used as
-- an independent magnitude anchor + reconciliation for identification. ts is in
-- the PK so the upsert is idempotent (the backfill re-fetches a trailing window
-- to absorb late utility revisions) and to satisfy Timescale's unique-key rule.
CREATE TABLE IF NOT EXISTS utility_energy (
    ts           TIMESTAMPTZ NOT NULL,
    statistic_id TEXT        NOT NULL,
    period       TEXT        NOT NULL,
    kwh          DOUBLE PRECISION,
    PRIMARY KEY (statistic_id, period, ts)
);
CREATE INDEX IF NOT EXISTS idx_utility_stat_ts ON utility_energy(statistic_id, period, ts);

CREATE TABLE IF NOT EXISTS test_windows (
    id              SERIAL PRIMARY KEY,
    label           TEXT,
    start_ts        TIMESTAMPTZ NOT NULL,
    end_ts          TIMESTAMPTZ,
    source          TEXT DEFAULT 'manual',
    known_load_w    DOUBLE PRECISION,
    known_entity_id TEXT
);
-- known-load calibration anchor (added after initial release): the wattage (or HA
-- sensor) of a load the user toggles during the test, so a meter that moves by the
-- known energy is directly calibrated and strongly confirmed.
ALTER TABLE test_windows ADD COLUMN IF NOT EXISTS known_load_w DOUBLE PRECISION;
ALTER TABLE test_windows ADD COLUMN IF NOT EXISTS known_entity_id TEXT;
-- snoop_k (added with the daily physics screen): the screened candidate-pool size
-- frozen when the window closes, so the data-snooping correction for this window
-- stays stable as more meters are overheard later.
ALTER TABLE test_windows ADD COLUMN IF NOT EXISTS snoop_k INTEGER;

CREATE TABLE IF NOT EXISTS capture_heartbeat (
    source      TEXT PRIMARY KEY,
    last_ts     TIMESTAMPTZ,
    total_count BIGINT DEFAULT 0,
    updated_at  TIMESTAMPTZ
);

-- meter_index is a tiny per-endpoint registry maintained on ingest. It carries
-- the metadata (msg/endpoint type) and all-time rollups (packets, min/max/last
-- consumption) the continuous aggregate can't, so reads never scan raw readings
-- just to learn a meter's type or latest value.
CREATE TABLE IF NOT EXISTS meter_index (
    endpoint_id         BIGINT PRIMARY KEY,
    msg_type            TEXT,
    endpoint_type       INTEGER,
    first_seen          TIMESTAMPTZ,
    last_seen           TIMESTAMPTZ,
    packets             BIGINT DEFAULT 0,
    min_consumption     DOUBLE PRECISION,
    max_consumption     DOUBLE PRECISION,
    last_consumption    DOUBLE PRECISION,
    last_consumption_ts TIMESTAMPTZ
);

-- meter_source counts which dongle (source) heard which meter — the coverage
-- matrix, maintained on ingest.
CREATE TABLE IF NOT EXISTS meter_source (
    endpoint_id BIGINT,
    source      TEXT,
    packets     BIGINT DEFAULT 0,
    last_seen   TIMESTAMPTZ,
    PRIMARY KEY (endpoint_id, source)
);

-- sdr_devices is the detected-dongle inventory written by capture so the UI can
-- show every dongle (even disabled ones) with its serial/tuner/index.
CREATE TABLE IF NOT EXISTS sdr_devices (
    serial     TEXT PRIMARY KEY,
    dev_index  INTEGER,
    name       TEXT,
    tuner      TEXT,
    first_seen TIMESTAMPTZ,
    last_seen  TIMESTAMPTZ
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
	`SELECT create_hypertable('utility_energy', 'ts', if_not_exists => TRUE, migrate_data => TRUE)`,
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
	     schedule_interval => INTERVAL '1 minute',
	     if_not_exists => true)`,
	// real-time aggregation: union the materialized buckets with the freshest raw
	// rows so analysis over a recent window isn't missing the last minute.
	`ALTER MATERIALIZED VIEW readings_1m SET (timescaledb.materialized_only = false)`,
	`ALTER TABLE readings SET (timescaledb.compress, timescaledb.compress_segmentby = 'endpoint_id')`,
	`SELECT add_compression_policy('readings', INTERVAL '7 days', if_not_exists => true)`,
	`SELECT add_retention_policy('readings', INTERVAL '180 days', if_not_exists => true)`,
	// readings_1h backs everything calendar-shaped (usage browser, daily screens,
	// leaderboards): hourly counter maxima are the smallest unit all of those
	// need, and reading them from a cagg keeps those queries off compressed raw
	// chunks. Built directly on readings (not on readings_1m) — hierarchical
	// caggs exclude the parent's real-time region during materialization, and the
	// direct form is the same proven pattern as readings_1m.
	`CREATE MATERIALIZED VIEW IF NOT EXISTS readings_1h
	   WITH (timescaledb.continuous) AS
	   SELECT endpoint_id,
	          time_bucket('1 hour', ts) AS bucket,
	          min(consumption) AS min_c,
	          max(consumption) AS max_c,
	          count(*)         AS n
	   FROM readings
	   WHERE consumption IS NOT NULL AND endpoint_id IS NOT NULL
	   GROUP BY endpoint_id, bucket
	   WITH NO DATA`,
	`SELECT add_continuous_aggregate_policy('readings_1h',
	     start_offset => INTERVAL '48 hours',
	     end_offset   => INTERVAL '1 hour',
	     schedule_interval => INTERVAL '30 minutes',
	     if_not_exists => true)`,
	`ALTER MATERIALIZED VIEW readings_1h SET (timescaledb.materialized_only = false)`,
	`SELECT add_retention_policy('readings_1h', INTERVAL '180 days', if_not_exists => true)`,
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
	// One-time backfill of the registries from existing readings (a fresh deploy
	// already has data on disk). Guarded so it only runs when empty.
	for _, step := range backfillSteps {
		if _, err := conn.Exec(ctx, step); err != nil {
			log.Printf("[db] backfill step skipped: %v", err)
		}
	}
	return nil
}

// backfillSteps populate the ingest-maintained registries from history exactly
// once (each guarded by NOT EXISTS), so analysis is fast on day one.
var backfillSteps = []string{
	`INSERT INTO meter_index
	   (endpoint_id, msg_type, endpoint_type, first_seen, last_seen, packets,
	    min_consumption, max_consumption, last_consumption, last_consumption_ts)
	 SELECT r.endpoint_id, max(r.msg_type), max(r.endpoint_type),
	        min(r.ts), max(r.ts), count(*),
	        min(r.consumption), max(r.consumption),
	        (SELECT consumption FROM readings r2
	           WHERE r2.endpoint_id = r.endpoint_id AND r2.consumption IS NOT NULL
	           ORDER BY r2.ts DESC LIMIT 1),
	        max(r.ts)
	 FROM readings r
	 WHERE r.endpoint_id IS NOT NULL
	   AND NOT EXISTS (SELECT 1 FROM meter_index LIMIT 1)
	 GROUP BY r.endpoint_id`,
	`INSERT INTO meter_source (endpoint_id, source, packets, last_seen)
	 SELECT endpoint_id, source, count(*), max(ts)
	 FROM readings
	 WHERE endpoint_id IS NOT NULL AND source IS NOT NULL
	   AND NOT EXISTS (SELECT 1 FROM meter_source LIMIT 1)
	 GROUP BY endpoint_id, source`,
}
