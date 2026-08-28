package db

import (
	"context"
	"time"

	"winnow/internal/config"
)

// refCarryLimit bounds how long a reference sample's value may be carried
// forward by gap-fill. HA sensors report on change, so the worker re-inserts
// each entity's last value every 5 minutes while its subscription is healthy —
// which makes "no samples for >15 minutes" mean exactly one thing: the feed is
// dead. Unbounded locf once fabricated 23 days of constant power from the last
// sample before an outage, silently poisoning every daily analysis built on it.
const refCarryLimit = "15 minutes"

// refBoundedCTEs emits the shared reference gap-fill ladder ending in a CTE
// named per_entity (mt, entity_id, w, is_hvac) whose carried values are NULL
// once the source sample is older than refCarryLimit. per_entity is the UNION
// ALL of the real reference_samples branch (is_hvac = FALSE) and an estimated
// HVAC branch (is_hvac = TRUE): thermostat hvac_action x the configured
// heating/cooling kW, read from settings at query time so retuning either kW
// is retroactive with no re-ingest. entityWhere filters reference_samples with
// the caller's own placeholders, e.g. "entity_id = ANY($1)"; tsWhere MUST
// constrain ts on both branches (gapfill needs a finite window), e.g.
// "ts >= $2 AND ts <= $3". The HVAC branch is unconditional on entityWhere —
// callers that must exclude it (the known-load anchor) filter per_entity by
// "WHERE NOT is_hvac" themselves.
func refBoundedCTEs(entityWhere, tsWhere string) string {
	return `
per_entity_raw AS (
  SELECT time_bucket_gapfill('1 minute', ts) AS mt, entity_id,
         locf(avg(power_w)) AS w0,
         locf(max(ts))      AS src_ts
  FROM reference_samples
  WHERE ` + entityWhere + ` AND ` + tsWhere + `
  GROUP BY mt, entity_id),
hvac_raw AS (
  SELECT time_bucket_gapfill('1 minute', ts) AS mt, entity_id,
         locf(last(action, ts)) AS act,
         locf(max(ts))          AS src_ts
  FROM hvac_samples
  WHERE entity_id = (SELECT value FROM settings WHERE key = '` + config.KeyHVACEntityID + `') AND ` + tsWhere + `
  GROUP BY mt, entity_id),
per_entity AS (
  SELECT mt, entity_id,
         CASE WHEN mt <= src_ts + interval '` + refCarryLimit + `' THEN w0 END AS w,
         FALSE AS is_hvac
  FROM per_entity_raw
  UNION ALL
  SELECT mt, entity_id,
         CASE WHEN mt <= src_ts + interval '` + refCarryLimit + `' THEN 1000 * CASE act
              WHEN 'heating' THEN ` + settingKW(config.KeyHVACHeatingKW) + `
              WHEN 'cooling' THEN ` + settingKW(config.KeyHVACCoolingKW) + `
              ELSE 0 END END AS w,
         TRUE AS is_hvac
  FROM hvac_raw)`
}

// InsertReferenceSample stores one normalized power sample (from the HA
// WebSocket; energy sensors are differentiated to power upstream).
func (d *DB) InsertReferenceSample(ctx context.Context, entity string, ts time.Time, powerW float64) error {
	_, err := d.pool.Exec(ctx,
		`INSERT INTO reference_samples (ts, entity_id, power_w) VALUES ($1,$2,$3)`,
		ts, entity, powerW)
	return err
}

// MonitoredEnergy returns the total monitored consumption over [start,end] in kWh:
// per-minute total power (W), gap-filled within the carry bound, integrated over
// time (sum/60 → Wh) and scaled to kWh. Same integration the correlation's
// reference side uses, so the reconciliation ("meter X kWh vs monitored Y kWh")
// is consistent with calibration. Minutes beyond the bound contribute 0 energy.
func (d *DB) MonitoredEnergy(ctx context.Context, entities []string, start, end time.Time) float64 {
	if len(entities) == 0 {
		return 0
	}
	var kwh *float64
	_ = d.pool.QueryRow(ctx, `
WITH `+refBoundedCTEs("entity_id = ANY($1)", "ts >= $2 AND ts <= $3")+`,
per_min AS (SELECT mt, sum(coalesce(w,0)) AS w FROM per_entity GROUP BY mt)
SELECT sum(w)/60.0/1000.0 FROM per_min`, entities, start, end).Scan(&kwh)
	if kwh == nil {
		return 0
	}
	return round(*kwh, 3)
}

// EntityEnergy returns one HA sensor's energy over [start,end] in kWh: its
// per-minute power (W), gap-filled within the carry bound, integrated over
// time. Used by the known-load calibration anchor when the toggled load is
// itself a measured HA sensor — excludes the HVAC branch (NOT is_hvac): the
// anchor is one real sensor, never the estimate.
func (d *DB) EntityEnergy(ctx context.Context, entity string, start, end time.Time) float64 {
	if entity == "" {
		return 0
	}
	var kwh *float64
	_ = d.pool.QueryRow(ctx, `
WITH `+refBoundedCTEs("entity_id = $1", "ts >= $2 AND ts <= $3")+`
SELECT sum(coalesce(w,0))/60.0/1000.0 FROM per_entity WHERE NOT is_hvac`, entity, start, end).Scan(&kwh)
	if kwh == nil {
		return 0
	}
	return round(*kwh, 3)
}

// MonitoredCV returns the coefficient of variation (stddev/mean) of the total
// monitored power over [start,end], from per-minute gap-filled totals. A small
// CV means the reference is nearly constant — correlation against it is noise —
// and the confidence model shifts weight to energy reconciliation instead.
func (d *DB) MonitoredCV(ctx context.Context, entities []string, start, end time.Time) *float64 {
	if len(entities) == 0 {
		return nil
	}
	var mean, sd *float64
	_ = d.pool.QueryRow(ctx, `
WITH `+refBoundedCTEs("entity_id = ANY($1)", "ts >= $2 AND ts <= $3")+`,
per_min AS (SELECT mt, sum(coalesce(w,0)) AS w FROM per_entity GROUP BY mt)
SELECT avg(w), stddev_samp(w) FROM per_min WHERE w > 0`, entities, start, end).Scan(&mean, &sd)
	if mean == nil || sd == nil || *mean <= 0 {
		return nil
	}
	cv := round(*sd / *mean, 3)
	return &cv
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
WITH `+refBoundedCTEs("entity_id = ANY($1)", "ts >= $2 AND ts <= $3")+`,
agg AS (SELECT mt, sum(coalesce(w,0)) AS power FROM per_entity GROUP BY mt)
SELECT percentile_cont(0.05) WITHIN GROUP (ORDER BY power) FROM agg WHERE power > 0`,
		entities, start, end).Scan(&floor)
	if floor == nil {
		return 0
	}
	return round(*floor, 1)
}

// ReferenceGaps returns holes (> minGap with zero samples, any entity) in the
// last `lookback` of the reference feed, including a trailing hole from the
// last sample to `capEnd` — the work-list for statistics backfill. Gap edges
// are the surrounding real samples. Delegates to sampleGaps (hvac.go), shared
// with HVACGaps so the two queries stay identical.
func (d *DB) ReferenceGaps(ctx context.Context, entities []string, lookback time.Duration, minGap time.Duration, capEnd time.Time) [][2]time.Time {
	if len(entities) == 0 {
		return [][2]time.Time{}
	}
	return d.sampleGaps(ctx, "reference_samples", "entity_id = ANY($1)", entities, lookback, minGap, capEnd)
}

// ReplaceBackfillSamples idempotently (re)writes statistics-derived samples for
// one entity over [from,to]: prior backfill rows in the span are deleted first;
// live rows are never touched.
func (d *DB) ReplaceBackfillSamples(ctx context.Context, entity string, from, to time.Time, ts []time.Time, powerW []float64) error {
	if _, err := d.pool.Exec(ctx,
		`DELETE FROM reference_samples WHERE entity_id=$1 AND src='lts_backfill' AND ts >= $2 AND ts <= $3`,
		entity, from, to); err != nil {
		return err
	}
	if len(ts) == 0 {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
INSERT INTO reference_samples (ts, entity_id, power_w, src)
SELECT t, $1, w, 'lts_backfill' FROM unnest($2::timestamptz[], $3::double precision[]) AS u(t, w)`,
		entity, ts, powerW)
	return err
}

// RefGap is a hole in the reference feed (no real samples).
type RefGap struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// ReferenceHealth reports whether the monitored feed is actually flowing —
// separate from "HA is reachable", which stayed green through a 23-day feed
// outage. Stale = no real sample in the last 30 minutes.
type ReferenceHealth struct {
	Configured   bool     `json:"configured"`
	LastSampleTS *string  `json:"last_sample_ts"`
	Stale        bool     `json:"stale"`
	Gaps7d       []RefGap `json:"gaps_7d"`
}

func (d *DB) ReferenceHealth(ctx context.Context, entities []string) ReferenceHealth {
	h := ReferenceHealth{Configured: len(entities) > 0, Gaps7d: []RefGap{}}
	if !h.Configured {
		return h
	}
	var last *time.Time
	_ = d.pool.QueryRow(ctx,
		`SELECT max(ts) FROM reference_samples WHERE entity_id = ANY($1)`, entities).Scan(&last)
	if last != nil {
		ts := last.UTC().Format(time.RFC3339)
		h.LastSampleTS = &ts
		h.Stale = time.Since(*last) > 30*time.Minute
	} else {
		h.Stale = true
	}
	rows, err := d.pool.Query(ctx, `
WITH mins AS (
  SELECT DISTINCT time_bucket('1 minute', ts) AS mt
  FROM reference_samples
  WHERE entity_id = ANY($1) AND ts >= now() - interval '7 days'),
g AS (SELECT mt, lag(mt) OVER (ORDER BY mt) AS prev FROM mins)
SELECT prev, mt FROM g WHERE mt - prev > interval '30 minutes' ORDER BY prev`, entities)
	if err != nil {
		return h
	}
	defer rows.Close()
	for rows.Next() {
		var a, b time.Time
		if err := rows.Scan(&a, &b); err != nil {
			return h
		}
		h.Gaps7d = append(h.Gaps7d, RefGap{Start: a.UTC().Format(time.RFC3339), End: b.UTC().Format(time.RFC3339)})
	}
	return h
}
