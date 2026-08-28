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
	// first bucket (lag NULL) yields a NULL delta. A negative step is a counter
	// rollover (2^24/2^32 wrap) when it wraps forward by less than half the range —
	// corrected to the real rise — otherwise a genuine reset → NULL gap.
	msgType, _ := d.LatestMsgType(ctx, id)
	mod := counterModulus(msgType)
	bq := fmt.Sprintf(`WITH b AS (
	                     SELECT time_bucket('%s', ts) AS bkt,
	                            max(consumption) AS cmax,
	                            count(*) AS n
	                     FROM readings %s GROUP BY bkt),
	                   stepped AS (
	                     SELECT bkt, n, cmax - lag(cmax) OVER (ORDER BY bkt) AS raw
	                     FROM b)
	                   SELECT bkt, %s AS delta, n
	                   FROM stepped ORDER BY bkt`,
		bucketInterval(bucket), where, rolloverDeltaSQL("raw", fmt.Sprintf("%f", mod)))
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
		// rollover-aware cross-bucket delta: the modulus is per-meter (by message
		// type), so join meter_index. A wrap forward by <½ range is a rollover; a
		// larger drop is a reset → NULL (dropped below).
		q = fmt.Sprintf(`WITH b AS (
		                   SELECT r.endpoint_id, time_bucket('%s', r.ts) AS bkt, max(r.consumption) AS cmax,
		                          CASE WHEN mi.msg_type = 'SCM' THEN 16777216.0 ELSE 4294967296.0 END AS modulus
		                   FROM readings r JOIN meter_index mi USING (endpoint_id) %s GROUP BY r.endpoint_id, bkt, modulus),
		                 stepped AS (
		                   SELECT endpoint_id, bkt, modulus,
		                          cmax - lag(cmax) OVER (PARTITION BY endpoint_id ORDER BY bkt) AS raw
		                   FROM b)
		                 SELECT endpoint_id, bkt, %s AS v
		                 FROM stepped ORDER BY bkt`,
			bucketInterval(bucket), where, rolloverDeltaSQL("raw", "modulus"))
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
// carried forward within the staleness bound on a per-minute grid, summed
// across the configured set (plus the estimated HVAC branch), then averaged to
// the requested bucket. A bucket with no in-bound REAL monitored data is
// OMITTED even if the HVAC estimate alone covers it — the chart draws a gap
// where the feed was dead, never a fabricated flat line or a fake 0 W painted
// over a dead monitored feed by the estimate.
func (d *DB) AggregateSeries(ctx context.Context, entities []string, start, end time.Time, bucket string) ([]MultiSeriesPoint, error) {
	out := []MultiSeriesPoint{}
	if len(entities) == 0 {
		return out, nil
	}
	q := `
WITH ` + refBoundedCTEs("entity_id = ANY($1)", "ts >= $2 AND ts <= $3") + `,
per_min AS (
  SELECT mt, CASE WHEN count(w) FILTER (WHERE NOT is_hvac) > 0 THEN coalesce(sum(w), 0) END AS w
  FROM per_entity GROUP BY mt)
SELECT time_bucket('` + bucketInterval(bucket) + `', mt) AS b, avg(w) AS power
FROM per_min GROUP BY b ORDER BY b`
	rows, err := d.pool.Query(ctx, q, entities, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b time.Time
		var p *float64
		if err := rows.Scan(&b, &p); err != nil {
			return nil, err
		}
		if p == nil {
			continue // feed dead for this whole bucket → gap
		}
		out = append(out, MultiSeriesPoint{Bucket: b.UTC().Format(time.RFC3339Nano), Value: round(*p, 1)})
	}
	return out, rows.Err()
}
