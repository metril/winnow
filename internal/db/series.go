package db

import (
	"context"
	"fmt"
	"time"

	"winnow/internal/model"
)

// MeterSeries returns cumulative points + per-bucket deltas for one meter.
func (d *DB) MeterSeries(ctx context.Context, id int64, since, until *time.Time, bucket string) (model.Series, error) {
	s := model.Series{EndpointID: id, Bucket: bucket, Points: []model.Point{}, Deltas: []model.Bucket{}}
	where := "WHERE consumption IS NOT NULL AND endpoint_id = $1"
	args := []any{id}
	if since != nil {
		args = append(args, *since)
		where += fmt.Sprintf(" AND ts >= $%d", len(args))
	}
	if until != nil {
		args = append(args, *until)
		where += fmt.Sprintf(" AND ts <= $%d", len(args))
	}

	prows, err := d.pool.Query(ctx, "SELECT ts, consumption FROM readings "+where+" ORDER BY ts", args...)
	if err != nil {
		return s, err
	}
	for prows.Next() {
		var t time.Time
		var c *float64
		if err := prows.Scan(&t, &c); err != nil {
			prows.Close()
			return s, err
		}
		s.Points = append(s.Points, model.Point{TS: t.UTC().Format(time.RFC3339Nano), Consumption: c})
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return s, err
	}

	// Per-bucket usage = the rise in the cumulative counter SINCE the previous
	// bucket (lag), not max-min WITHIN a bucket — the latter is 0 whenever a bucket
	// holds a single packet, which collapsed sparse meters' charts to zero. The
	// first bucket (lag NULL) and any negative step (counter reset) yield a NULL
	// delta, which the chart renders as a gap rather than a dive to 0.
	bq := fmt.Sprintf(`WITH b AS (
	                     SELECT time_bucket('%s', ts) AS bkt,
	                            max(consumption) AS cmax,
	                            count(*) AS n
	                     FROM readings %s GROUP BY bkt)
	                   SELECT bkt,
	                          cmax - lag(cmax) OVER (ORDER BY bkt) AS delta,
	                          n
	                   FROM b ORDER BY bkt`, bucketInterval(bucket), where)
	drows, err := d.pool.Query(ctx, bq, args...)
	if err != nil {
		return s, err
	}
	defer drows.Close()
	for drows.Next() {
		var b time.Time
		var delta *float64
		var n int64
		if err := drows.Scan(&b, &delta, &n); err != nil {
			return s, err
		}
		if delta != nil && *delta < 0 {
			delta = nil
		}
		s.Deltas = append(s.Deltas, model.Bucket{Bucket: b.UTC().Format(time.RFC3339Nano), Delta: delta, Packets: n})
	}
	return s, drows.Err()
}

// MultiSeriesPoint is one meter's value at a bucket (for overlay plotting).
type MultiSeriesPoint struct {
	Bucket string  `json:"bucket"`
	Value  float64 `json:"value"`
}

// MultiSeries returns aligned series for several meters at once.
// mode: "cumulative" (max consumption per bucket) or "delta" (max-min per bucket).
func (d *DB) MultiSeries(ctx context.Context, ids []int64, since, until *time.Time, bucket, mode string) (map[string][]MultiSeriesPoint, error) {
	out := map[string][]MultiSeriesPoint{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{ids}
	where := "WHERE consumption IS NOT NULL AND endpoint_id = ANY($1)"
	if since != nil {
		args = append(args, *since)
		where += fmt.Sprintf(" AND ts >= $%d", len(args))
	}
	if until != nil {
		args = append(args, *until)
		where += fmt.Sprintf(" AND ts <= $%d", len(args))
	}
	// cumulative: the bucket's max counter value. delta: the rise in that counter
	// SINCE the previous bucket (lag), so a single-packet bucket still shows real
	// usage instead of 0. A NULL/negative step (first bucket, or counter reset) is
	// dropped so the chart shows a gap rather than diving to 0. See MeterSeries.
	var q string
	if mode == "cumulative" {
		q = fmt.Sprintf(`SELECT endpoint_id, time_bucket('%s', ts) AS b, max(consumption) AS v
		                  FROM readings %s GROUP BY endpoint_id, b ORDER BY b`,
			bucketInterval(bucket), where)
	} else {
		q = fmt.Sprintf(`WITH b AS (
		                   SELECT endpoint_id, time_bucket('%s', ts) AS bkt, max(consumption) AS cmax
		                   FROM readings %s GROUP BY endpoint_id, bkt)
		                 SELECT endpoint_id, bkt,
		                        cmax - lag(cmax) OVER (PARTITION BY endpoint_id ORDER BY bkt) AS v
		                 FROM b ORDER BY bkt`, bucketInterval(bucket), where)
	}
	rows, err := d.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var b time.Time
		var v *float64
		if err := rows.Scan(&id, &b, &v); err != nil {
			return nil, err
		}
		if v == nil || *v <= 0 {
			// Omit no-data, counter-reset, AND no-movement buckets (delta 0) — a
			// slow-ticking counter at fine buckets yields many 0s that would draw the
			// line diving to zero. Charts connectNulls across these gaps instead.
			continue
		}
		key := fmt.Sprintf("%d", id)
		out[key] = append(out[key], MultiSeriesPoint{Bucket: b.UTC().Format(time.RFC3339Nano), Value: *v})
	}
	return out, rows.Err()
}

// AggregateSeries returns the TOTAL monitored power over a window: per entity
// last-value-carried-forward on a bucket grid (TimescaleDB gapfill+locf), summed
// across the configured set. One entity = that single sensor; many = the sum.
func (d *DB) AggregateSeries(ctx context.Context, entities []string, start, end time.Time, bucket string) ([]MultiSeriesPoint, error) {
	out := []MultiSeriesPoint{}
	if len(entities) == 0 {
		return out, nil
	}
	q := fmt.Sprintf(`
WITH per_entity AS (
  SELECT time_bucket_gapfill('%s', ts) AS b, entity_id, locf(avg(power_w)) AS w
  FROM reference_samples
  WHERE entity_id = ANY($1) AND ts >= $2 AND ts <= $3
  GROUP BY b, entity_id)
SELECT b, sum(coalesce(w,0)) AS power FROM per_entity GROUP BY b ORDER BY b`,
		bucketInterval(bucket))
	rows, err := d.pool.Query(ctx, q, entities, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b time.Time
		var p float64
		if err := rows.Scan(&b, &p); err != nil {
			return nil, err
		}
		out = append(out, MultiSeriesPoint{Bucket: b.UTC().Format(time.RFC3339Nano), Value: round(p, 1)})
	}
	return out, rows.Err()
}
