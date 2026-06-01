// Package db is winnow's data layer over TimescaleDB (pgx). It owns the schema,
// all SQL, and the consumption/correlation math (kept in SQL so it runs with
// Postgres parallel query, not a Python/Go loop).
package db

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"winnow/internal/config"
	"winnow/internal/model"
)

// DB wraps a pgx pool.
type DB struct{ pool *pgxpool.Pool }

// New opens a pool against dsn.
func New(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &DB{pool: pool}, nil
}

// Pool exposes the underlying pool (for LISTEN connections).
func (d *DB) Pool() *pgxpool.Pool { return d.pool }
func (d *DB) Close()              { d.pool.Close() }

func iso(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}

// --- readings & heartbeat (writers: capture) --------------------------------

// InsertReading stores one reading and NOTIFYs listeners with the endpoint id.
// It also maintains the small per-meter registries (meter_index, meter_source)
// so reads never scan raw readings just for metadata.
func (d *DB) InsertReading(ctx context.Context, r model.Reading, raw string) error {
	_, err := d.pool.Exec(ctx,
		`INSERT INTO readings (ts, msg_type, endpoint_id, endpoint_type, consumption, raw, source)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		r.TS, r.MsgType, r.EndpointID, r.EndpointType, r.Consumption, raw, r.Source)
	if err != nil {
		return err
	}
	// Maintain registries. LEAST/GREATEST ignore NULLs, so a packet with no
	// consumption (e.g. an IDM differential) won't clobber the min/max.
	_, _ = d.pool.Exec(ctx, `
INSERT INTO meter_index
  (endpoint_id, msg_type, endpoint_type, first_seen, last_seen, packets,
   min_consumption, max_consumption, last_consumption, last_consumption_ts)
VALUES ($1,$2,$3,$4,$4,1,$5,$5,$5,$4)
ON CONFLICT (endpoint_id) DO UPDATE SET
  msg_type        = COALESCE(EXCLUDED.msg_type, meter_index.msg_type),
  endpoint_type   = COALESCE(EXCLUDED.endpoint_type, meter_index.endpoint_type),
  last_seen       = GREATEST(meter_index.last_seen, EXCLUDED.last_seen),
  packets         = meter_index.packets + 1,
  min_consumption = LEAST(meter_index.min_consumption, EXCLUDED.min_consumption),
  max_consumption = GREATEST(meter_index.max_consumption, EXCLUDED.max_consumption),
  last_consumption = CASE WHEN EXCLUDED.last_consumption IS NOT NULL
                          AND EXCLUDED.last_consumption_ts >= meter_index.last_consumption_ts
                          THEN EXCLUDED.last_consumption ELSE meter_index.last_consumption END,
  last_consumption_ts = GREATEST(meter_index.last_consumption_ts, EXCLUDED.last_consumption_ts)`,
		r.EndpointID, r.MsgType, r.EndpointType, r.TS, r.Consumption)
	if r.Source != "" {
		_, _ = d.pool.Exec(ctx, `
INSERT INTO meter_source (endpoint_id, source, packets, last_seen)
VALUES ($1,$2,1,$3)
ON CONFLICT (endpoint_id, source) DO UPDATE SET
  packets = meter_source.packets + 1,
  last_seen = GREATEST(meter_source.last_seen, EXCLUDED.last_seen)`,
			r.EndpointID, r.Source, r.TS)
	}
	// pg_notify payload is "endpoint_id\x1fsource" so SSE clients can tally live
	// per-source capture activity without polling /api/health.
	_, _ = d.pool.Exec(ctx, `SELECT pg_notify('winnow', $1)`,
		strconv.FormatInt(r.EndpointID, 10)+"\x1f"+r.Source)
	return nil
}

// UpsertDevice records a detected SDR dongle in the inventory.
func (d *DB) UpsertDevice(ctx context.Context, serial string, idx int, name, tuner string) error {
	_, err := d.pool.Exec(ctx, `
INSERT INTO sdr_devices (serial, dev_index, name, tuner, first_seen, last_seen)
VALUES ($1,$2,$3,$4, now(), now())
ON CONFLICT (serial) DO UPDATE SET
  dev_index=EXCLUDED.dev_index, name=EXCLUDED.name, tuner=EXCLUDED.tuner, last_seen=now()`,
		serial, idx, name, tuner)
	return err
}

// PruneDevices removes inventory rows for dongles not in the currently-detected
// set, so the Devices list reflects what's actually plugged in (departed dongles
// and flaky-enumeration phantoms don't linger). No-op on an empty set.
func (d *DB) PruneDevices(ctx context.Context, keep []string) error {
	if len(keep) == 0 {
		return nil
	}
	_, err := d.pool.Exec(ctx, `DELETE FROM sdr_devices WHERE serial <> ALL($1)`, keep)
	return err
}

// Device is one detected SDR dongle joined with live capture health.
type Device struct {
	Serial         string   `json:"serial"`
	DevIndex       int      `json:"dev_index"`
	Name           string   `json:"name"`
	Tuner          string   `json:"tuner"`
	LastSeen       *string  `json:"last_seen"`
	Enabled        bool     `json:"enabled"`
	Label          string   `json:"label"`
	// per-dongle scan overrides (empty = inherit the global default)
	Freq           string   `json:"freq"`
	Gain           string   `json:"gain"`
	PPM            string   `json:"ppm"`
	MsgType        string   `json:"msgtype"`
	FilterID       string   `json:"filterid"`
	Alive          bool     `json:"alive"`
	PacketsLastMin int64    `json:"packets_last_min"`
	MetersHeard    int64    `json:"meters_heard"`
	AgeSeconds     *float64 `json:"age_seconds"`
}

// ListDevices returns the SDR inventory joined with heartbeat liveness and the
// number of distinct meters each has heard. Per-device enable/gain/label come
// from caller-supplied config (stored in settings).
func (d *DB) ListDevices(ctx context.Context, staleAfter time.Duration) ([]Device, error) {
	rows, err := d.pool.Query(ctx, `
SELECT s.serial, s.dev_index, s.name, s.tuner, s.last_seen,
       hb.updated_at,
       COALESCE((SELECT count(*) FROM readings r
                 WHERE r.source = s.serial AND r.ts >= now() - interval '1 minute'), 0) AS ppm,
       COALESCE((SELECT count(DISTINCT endpoint_id) FROM meter_source ms
                 WHERE ms.source = s.serial), 0) AS heard
FROM sdr_devices s
LEFT JOIN capture_heartbeat hb ON hb.source = s.serial
ORDER BY s.dev_index`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now().UTC()
	out := []Device{}
	for rows.Next() {
		var dev Device
		var lastSeen, updated *time.Time
		if err := rows.Scan(&dev.Serial, &dev.DevIndex, &dev.Name, &dev.Tuner,
			&lastSeen, &updated, &dev.PacketsLastMin, &dev.MetersHeard); err != nil {
			return nil, err
		}
		dev.LastSeen = iso(lastSeen)
		if updated != nil {
			age := now.Sub(*updated).Seconds()
			dev.AgeSeconds = &age
			dev.Alive = age <= staleAfter.Seconds()
		}
		out = append(out, dev)
	}
	return out, rows.Err()
}

func (d *DB) UpdateHeartbeat(ctx context.Context, source string, lastTS time.Time, total int64) error {
	_, err := d.pool.Exec(ctx,
		`INSERT INTO capture_heartbeat (source, last_ts, total_count, updated_at)
		 VALUES ($1,$2,$3, now())
		 ON CONFLICT (source) DO UPDATE SET
		   last_ts=EXCLUDED.last_ts, total_count=EXCLUDED.total_count, updated_at=now()`,
		source, lastTS, total)
	return err
}

// NotifyConfig signals workers that settings/publish flags changed.
func (d *DB) NotifyConfig(ctx context.Context) error {
	_, err := d.pool.Exec(ctx, `SELECT pg_notify('winnow_config', '')`)
	return err
}

// NotifyReference pushes a live plug power sample to listeners (for the SSE
// overlay). Payload is the power value as a string.
func (d *DB) NotifyReference(ctx context.Context, power float64) {
	_, _ = d.pool.Exec(ctx, `SELECT pg_notify('winnow_ref', $1)`, strconv.FormatFloat(power, 'f', 2, 64))
}

// --- settings & config ------------------------------------------------------

func (d *DB) GetSettings(ctx context.Context) (map[string]string, error) {
	rows, err := d.pool.Query(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, rows.Err()
}

func (d *DB) SetSetting(ctx context.Context, key, value string) error {
	_, err := d.pool.Exec(ctx,
		`INSERT INTO settings (key, value) VALUES ($1,$2)
		 ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`, key, value)
	return err
}

// LoadConfig overlays DB settings on env bootstrap defaults.
func (d *DB) LoadConfig(ctx context.Context) (config.Config, error) {
	m, err := d.GetSettings(ctx)
	if err != nil {
		return config.Config{}, err
	}
	return config.FromMap(m), nil
}

// --- health -----------------------------------------------------------------

func (d *DB) Health(ctx context.Context, staleAfter time.Duration) (model.Health, error) {
	h := model.Health{Sources: []model.SourceHealth{}}
	// Self-clean: drop heartbeats for sources that vanished long ago (e.g. a
	// dongle removed, or a renamed source after switching to serial tagging).
	_, _ = d.pool.Exec(ctx, `DELETE FROM capture_heartbeat WHERE updated_at < now() - interval '1 hour'`)
	rows, err := d.pool.Query(ctx, `SELECT source, last_ts, total_count, updated_at FROM capture_heartbeat ORDER BY source`)
	if err != nil {
		return h, err
	}
	defer rows.Close()
	now := time.Now().UTC()
	oneMinAgo := now.Add(-time.Minute)
	for rows.Next() {
		var s model.SourceHealth
		var lastTS, updated *time.Time
		if err := rows.Scan(&s.Source, &lastTS, &s.TotalCount, &updated); err != nil {
			return h, err
		}
		s.LastTS = iso(lastTS)
		if updated != nil {
			age := now.Sub(*updated).Seconds()
			s.AgeSeconds = &age
			s.Alive = age <= staleAfter.Seconds()
		}
		var recent int64
		_ = d.pool.QueryRow(ctx,
			`SELECT count(*) FROM readings WHERE source=$1 AND ts >= $2`, s.Source, oneMinAgo).Scan(&recent)
		s.PacketsLastMin = recent
		h.Sources = append(h.Sources, s)
		if s.Alive {
			h.Alive = true
		}
		h.PacketsLastMin += recent
	}
	if err := rows.Err(); err != nil {
		return h, err
	}
	_ = d.pool.QueryRow(ctx, `SELECT count(DISTINCT endpoint_id) FROM readings WHERE endpoint_id IS NOT NULL`).Scan(&h.UniqueMeters)
	return h, nil
}
