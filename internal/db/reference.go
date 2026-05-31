package db

import (
	"context"
	"time"
)

// InsertReferenceSample stores one normalized power sample (from the HA
// WebSocket; energy sensors are differentiated to power upstream).
func (d *DB) InsertReferenceSample(ctx context.Context, entity string, ts time.Time, powerW float64) error {
	_, err := d.pool.Exec(ctx,
		`INSERT INTO reference_samples (ts, entity_id, power_w) VALUES ($1,$2,$3)`,
		ts, entity, powerW)
	return err
}

// MonitoredFloor returns the user's baseline draw: a low percentile (5th) of the
// total monitored power over [start,end]. This is the floor a real meter can
// never go below.
func (d *DB) MonitoredFloor(ctx context.Context, entities []string, start, end time.Time) float64 {
	if len(entities) == 0 {
		return 0
	}
	var floor *float64
	_ = d.pool.QueryRow(ctx, `
WITH per_entity AS (
  SELECT time_bucket_gapfill('1 minute', ts) AS b, entity_id, locf(avg(power_w)) AS w
  FROM reference_samples
  WHERE entity_id = ANY($1) AND ts >= $2 AND ts <= $3
  GROUP BY b, entity_id),
agg AS (SELECT b, sum(coalesce(w,0)) AS power FROM per_entity GROUP BY b)
SELECT percentile_cont(0.05) WITHIN GROUP (ORDER BY power) FROM agg WHERE power > 0`,
		entities, start, end).Scan(&floor)
	if floor == nil {
		return 0
	}
	return round(*floor, 1)
}
