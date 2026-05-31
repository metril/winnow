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

	bq := fmt.Sprintf(`SELECT time_bucket('%s', ts) AS b,
	                          max(consumption)-min(consumption) AS delta,
	                          count(*) AS n
	                   FROM readings %s GROUP BY b ORDER BY b`, bucketInterval(bucket), where)
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
	val := "max(consumption)-min(consumption)"
	if mode == "cumulative" {
		val = "max(consumption)"
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
	q := fmt.Sprintf(`SELECT endpoint_id, time_bucket('%s', ts) AS b, %s AS v
	                  FROM readings %s GROUP BY endpoint_id, b ORDER BY b`,
		bucketInterval(bucket), val, where)
	rows, err := d.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var b time.Time
		var v float64
		if err := rows.Scan(&id, &b, &v); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%d", id)
		out[key] = append(out[key], MultiSeriesPoint{Bucket: b.UTC().Format(time.RFC3339Nano), Value: v})
	}
	return out, rows.Err()
}

// ReferenceSeries returns the plug power series over a window (1-min buckets).
func (d *DB) ReferenceSeries(ctx context.Context, entity string, start, end time.Time) ([]MultiSeriesPoint, error) {
	q := `SELECT time_bucket('1 minute', ts) AS b, avg(power_w) AS p
	      FROM reference_samples
	      WHERE ts BETWEEN $1 AND $2 AND ($3 = '' OR entity_id = $3)
	      GROUP BY b ORDER BY b`
	rows, err := d.pool.Query(ctx, q, start, end, entity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MultiSeriesPoint{}
	for rows.Next() {
		var b time.Time
		var p float64
		if err := rows.Scan(&b, &p); err != nil {
			return nil, err
		}
		out = append(out, MultiSeriesPoint{Bucket: b.UTC().Format(time.RFC3339Nano), Value: p})
	}
	return out, rows.Err()
}
