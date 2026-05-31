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
func (d *DB) InsertReading(ctx context.Context, r model.Reading, raw string) error {
	_, err := d.pool.Exec(ctx,
		`INSERT INTO readings (ts, msg_type, endpoint_id, endpoint_type, consumption, raw, source)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		r.TS, r.MsgType, r.EndpointID, r.EndpointType, r.Consumption, raw, r.Source)
	if err != nil {
		return err
	}
	// pg_notify payload is the endpoint id; cheap, broadcast to all listeners.
	_, _ = d.pool.Exec(ctx, `SELECT pg_notify('winnow', $1)`, strconv.FormatInt(r.EndpointID, 10))
	return nil
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
