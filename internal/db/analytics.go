package db

import (
	"context"
	"time"

	"winnow/internal/ert"
)

// --- usage profiles (per meter, over readings_1m) ---------------------------

// ProfilePoint is one bucketed average per-minute consumption delta.
type ProfilePoint struct {
	Key   int     `json:"key"`   // hour-of-day (0-23) or day-of-week (0-6)
	Value float64 `json:"value"` // avg per-minute delta
}

// HourlyProfile returns the average per-minute consumption by hour-of-day.
func (d *DB) HourlyProfile(ctx context.Context, id int64, days int) ([]ProfilePoint, error) {
	return d.profile(ctx, `extract(hour from bucket)::int`, id, days)
}

// DowProfile returns the average per-minute consumption by day-of-week (0=Sun).
func (d *DB) DowProfile(ctx context.Context, id int64, days int) ([]ProfilePoint, error) {
	return d.profile(ctx, `extract(dow from bucket)::int`, id, days)
}

func (d *DB) profile(ctx context.Context, keyExpr string, id int64, days int) ([]ProfilePoint, error) {
	if days <= 0 {
		days = 7
	}
	rows, err := d.pool.Query(ctx, `
SELECT `+keyExpr+` AS k, avg(max_c - min_c) AS v
FROM readings_1m
WHERE endpoint_id = $1 AND bucket >= now() - make_interval(days => $2)
GROUP BY k ORDER BY k`, id, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProfilePoint{}
	for rows.Next() {
		var p ProfilePoint
		var v *float64
		if err := rows.Scan(&p.Key, &v); err != nil {
			return nil, err
		}
		p.Value = round(deref(v), 3)
		out = append(out, p)
	}
	return out, rows.Err()
}

// HeatCell is one hour-of-day × day-of-week average.
type HeatCell struct {
	Dow   int     `json:"dow"`
	Hour  int     `json:"hour"`
	Value float64 `json:"value"`
}

// Heatmap returns the hour×day-of-week consumption grid for one meter.
func (d *DB) Heatmap(ctx context.Context, id int64, days int) ([]HeatCell, error) {
	if days <= 0 {
		days = 14
	}
	rows, err := d.pool.Query(ctx, `
SELECT extract(dow from bucket)::int AS dow,
       extract(hour from bucket)::int AS hour,
       avg(max_c - min_c) AS v
FROM readings_1m
WHERE endpoint_id = $1 AND bucket >= now() - make_interval(days => $2)
GROUP BY dow, hour`, id, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HeatCell{}
	for rows.Next() {
		var c HeatCell
		var v *float64
		if err := rows.Scan(&c.Dow, &c.Hour, &v); err != nil {
			return nil, err
		}
		c.Value = round(deref(v), 3)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DailyPoint is one day's total consumption.
type DailyPoint struct {
	Day   string  `json:"day"`
	Value float64 `json:"value"`
}

// DailyRollup returns per-day consumption (max-min) for one meter.
func (d *DB) DailyRollup(ctx context.Context, id int64, days int) ([]DailyPoint, error) {
	if days <= 0 {
		days = 30
	}
	rows, err := d.pool.Query(ctx, `
SELECT time_bucket('1 day', bucket) AS day, max(max_c) - min(min_c) AS v
FROM readings_1m
WHERE endpoint_id = $1 AND bucket >= now() - make_interval(days => $2)
GROUP BY day ORDER BY day`, id, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DailyPoint{}
	for rows.Next() {
		var t time.Time
		var v *float64
		if err := rows.Scan(&t, &v); err != nil {
			return nil, err
		}
		out = append(out, DailyPoint{Day: t.UTC().Format("2006-01-02"), Value: round(deref(v), 2)})
	}
	return out, rows.Err()
}

// MovementSince returns a meter's consumption movement (max-min) since a time.
func (d *DB) MovementSince(ctx context.Context, id int64, since time.Time) float64 {
	var v *float64
	_ = d.pool.QueryRow(ctx,
		`SELECT max(max_c) - min(min_c) FROM readings_1m WHERE endpoint_id=$1 AND bucket >= $2`,
		id, since).Scan(&v)
	return deref(v)
}

// --- benchmarking (vs neighbours) -------------------------------------------

// Benchmark compares a meter's consumption over the window to its peers of the
// same commodity (anonymized): the percentile rank and the median.
type Benchmark struct {
	EndpointID int64   `json:"endpoint_id"`
	Commodity  string  `json:"commodity"`
	Days       int     `json:"days"`
	Yours      float64 `json:"yours"`      // your total movement over the window
	Median     float64 `json:"median"`     // peer median
	Percentile float64 `json:"percentile"` // 0-100, your rank among peers
	Peers      int     `json:"peers"`
}

// BenchmarkMeter ranks one meter against same-commodity neighbours by total
// movement over the window.
func (d *DB) BenchmarkMeter(ctx context.Context, id int64, days int) (Benchmark, error) {
	if days <= 0 {
		days = 7
	}
	b := Benchmark{EndpointID: id, Days: days}
	rows, err := d.pool.Query(ctx, `
SELECT i.endpoint_id, i.endpoint_type, COALESCE(w.movement, 0) AS movement
FROM meter_index i
LEFT JOIN (
  SELECT endpoint_id, max(max_c) - min(min_c) AS movement
  FROM readings_1m WHERE bucket >= now() - make_interval(days => $1)
  GROUP BY endpoint_id
) w ON w.endpoint_id = i.endpoint_id`, days)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	type row struct {
		id       int64
		movement float64
	}
	var target *row
	var targetType *int
	all := []row{}
	byType := map[int64]*int{}
	for rows.Next() {
		var rid int64
		var et *int
		var mv float64
		if err := rows.Scan(&rid, &et, &mv); err != nil {
			return b, err
		}
		all = append(all, row{rid, mv})
		byType[rid] = et
		if rid == id {
			r := row{rid, mv}
			target = &r
			targetType = et
		}
	}
	if target == nil {
		return b, nil
	}
	b.Commodity = ert.Commodity(targetType)
	b.Yours = round(target.movement, 2)
	// peers = same commodity, with some movement
	peers := []float64{}
	below := 0
	for _, r := range all {
		if ert.Commodity(byType[r.id]) != b.Commodity || r.movement <= 0 {
			continue
		}
		peers = append(peers, r.movement)
		if r.movement < target.movement {
			below++
		}
	}
	b.Peers = len(peers)
	if len(peers) > 0 {
		b.Percentile = round(100*float64(below)/float64(len(peers)), 1)
		b.Median = round(medianOf(peers), 2)
	}
	return b, nil
}

func medianOf(xs []float64) float64 {
	n := len(xs)
	if n == 0 {
		return 0
	}
	// simple insertion sort (peer counts are small)
	cp := append([]float64(nil), xs...)
	for i := 1; i < n; i++ {
		for j := i; j > 0 && cp[j-1] > cp[j]; j-- {
			cp[j-1], cp[j] = cp[j], cp[j-1]
		}
	}
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}

// --- coverage & signal diagnostics ------------------------------------------

// CoverageCell is how many packets one source heard from one meter.
type CoverageCell struct {
	Source     string `json:"source"`
	EndpointID int64  `json:"endpoint_id"`
	Packets    int64  `json:"packets"`
	LastSeen   string `json:"last_seen"`
}

// CoverageMatrix returns which dongle heard which meter (all-time, from the
// ingest-maintained meter_source registry).
func (d *DB) CoverageMatrix(ctx context.Context) ([]CoverageCell, error) {
	rows, err := d.pool.Query(ctx, `
SELECT source, endpoint_id, packets, last_seen
FROM meter_source ORDER BY source, packets DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CoverageCell{}
	for rows.Next() {
		var c CoverageCell
		var ls *time.Time
		if err := rows.Scan(&c.Source, &c.EndpointID, &c.Packets, &ls); err != nil {
			return nil, err
		}
		if ls != nil {
			c.LastSeen = ls.UTC().Format(time.RFC3339)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SourcePoint is a source's packet count in a time bucket.
type SourcePoint struct {
	Bucket  string `json:"bucket"`
	Packets int64  `json:"packets"`
}

// SourceTimeline returns per-source packet counts over time (reception health).
func (d *DB) SourceTimeline(ctx context.Context, since time.Time, bucket string) (map[string][]SourcePoint, error) {
	rows, err := d.pool.Query(ctx, `
SELECT source, time_bucket($2, ts) AS b, count(*)
FROM readings WHERE ts >= $1 AND source IS NOT NULL
GROUP BY source, b ORDER BY b`, since, bucketInterval(bucket))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]SourcePoint{}
	for rows.Next() {
		var src string
		var b time.Time
		var n int64
		if err := rows.Scan(&src, &b, &n); err != nil {
			return nil, err
		}
		out[src] = append(out[src], SourcePoint{Bucket: b.UTC().Format(time.RFC3339), Packets: n})
	}
	return out, rows.Err()
}

// --- anomalies --------------------------------------------------------------

// Anomaly is a flagged issue worth the user's attention.
type Anomaly struct {
	Kind       string `json:"kind"` // "dropout" | "stuck" | "source_down"
	EndpointID int64  `json:"endpoint_id,omitempty"`
	Source     string `json:"source,omitempty"`
	Detail     string `json:"detail"`
}

// Anomalies scans for tracked-meter dropouts/stalls and dead sources. liveSources
// is the set of capture sources that are currently expected to be producing data
// (a dongle that is plugged in AND enabled); a source_down anomaly is only raised
// for one of those — so a removed or disabled dongle can't trigger a phantom alert.
func (d *DB) Anomalies(ctx context.Context, liveSources []string) ([]Anomaly, error) {
	out := []Anomaly{}
	// tracked meters that haven't been heard recently (dropout)
	rows, err := d.pool.Query(ctx, `
SELECT i.endpoint_id, i.last_seen
FROM meter_index i JOIN meters m ON m.endpoint_id = i.endpoint_id
WHERE (m.is_mine OR m.publish) AND i.last_seen < now() - interval '15 minutes'`)
	if err == nil {
		for rows.Next() {
			var id int64
			var ls *time.Time
			if err := rows.Scan(&id, &ls); err == nil {
				det := "no packets in 15+ min"
				if ls != nil {
					det = "last heard " + ls.UTC().Format(time.RFC3339)
				}
				out = append(out, Anomaly{Kind: "dropout", EndpointID: id, Detail: det})
			}
		}
		rows.Close()
	}
	// tracked meters with zero movement in the last hour but movement before
	srows, err := d.pool.Query(ctx, `
WITH recent AS (
  SELECT endpoint_id, max(max_c) - min(min_c) AS mv
  FROM readings_1m WHERE bucket >= now() - interval '1 hour' GROUP BY endpoint_id),
prior AS (
  SELECT endpoint_id, max(max_c) - min(min_c) AS mv
  FROM readings_1m WHERE bucket >= now() - interval '25 hours'
    AND bucket < now() - interval '1 hour' GROUP BY endpoint_id)
SELECT m.endpoint_id
FROM meters m
JOIN prior  ON prior.endpoint_id = m.endpoint_id
LEFT JOIN recent ON recent.endpoint_id = m.endpoint_id
WHERE (m.is_mine OR m.publish) AND prior.mv > 0 AND COALESCE(recent.mv, 0) = 0`)
	if err == nil {
		for srows.Next() {
			var id int64
			if err := srows.Scan(&id); err == nil {
				out = append(out, Anomaly{Kind: "stuck", EndpointID: id, Detail: "odometer flat for 1h while active in prior 24h"})
			}
		}
		srows.Close()
	}
	// dead capture sources — only flag dongles we actually expect to be running
	// (present + enabled). A departed/disabled dongle whose heartbeat row lingers
	// is not an anomaly. The 10-minute window keeps a marginal dongle that hears
	// meters only sporadically from flapping the alert; a genuine outage still trips.
	hrows, err := d.pool.Query(ctx, `
SELECT source, updated_at FROM capture_heartbeat
WHERE updated_at < now() - interval '10 minutes'
  AND source = ANY($1)`, liveSources)
	if err == nil {
		for hrows.Next() {
			var src string
			var up *time.Time
			if err := hrows.Scan(&src, &up); err == nil {
				out = append(out, Anomaly{Kind: "source_down", Source: src, Detail: "no data in 10+ min"})
			}
		}
		hrows.Close()
	}
	return out, nil
}
