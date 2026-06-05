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

// MonitoredEnergy returns the total monitored consumption over [start,end] in kWh:
// per-minute total power (W), gap-filled, integrated over time (sum/60 → Wh) and
// scaled to kWh. Same integration the correlation's reference side uses, so the
// reconciliation ("meter X kWh vs monitored Y kWh") is consistent with calibration.
func (d *DB) MonitoredEnergy(ctx context.Context, entities []string, start, end time.Time) float64 {
	if len(entities) == 0 {
		return 0
	}
	var kwh *float64
	_ = d.pool.QueryRow(ctx, `
WITH per_entity AS (
  SELECT time_bucket_gapfill('1 minute', ts) AS mt, entity_id, locf(avg(power_w)) AS w
  FROM reference_samples
  WHERE entity_id = ANY($1) AND ts >= $2 AND ts <= $3
  GROUP BY mt, entity_id),
per_min AS (SELECT mt, sum(coalesce(w,0)) AS w FROM per_entity GROUP BY mt)
SELECT sum(w)/60.0/1000.0 FROM per_min`, entities, start, end).Scan(&kwh)
	if kwh == nil {
		return 0
	}
	return round(*kwh, 3)
}

// EntityEnergy returns one HA sensor's energy over [start,end] in kWh: its
// per-minute power (W), gap-filled, integrated over time. Used by the known-load
// calibration anchor when the toggled load is itself a measured HA sensor.
func (d *DB) EntityEnergy(ctx context.Context, entity string, start, end time.Time) float64 {
	if entity == "" {
		return 0
	}
	var kwh *float64
	_ = d.pool.QueryRow(ctx, `
WITH per_min AS (
  SELECT time_bucket_gapfill('1 minute', ts) AS mt, locf(avg(power_w)) AS w
  FROM reference_samples
  WHERE entity_id = $1 AND ts >= $2 AND ts <= $3
  GROUP BY mt)
SELECT sum(coalesce(w,0))/60.0/1000.0 FROM per_min`, entity, start, end).Scan(&kwh)
	if kwh == nil {
		return 0
	}
	return round(*kwh, 3)
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
