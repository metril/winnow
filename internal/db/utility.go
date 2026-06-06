package db

import (
	"context"
	"time"

	"winnow/internal/model"
)

// This file uses the user's billed utility energy (pulled from HA long-term
// statistics into utility_energy) as an INDEPENDENT identification signal. The
// utility's metered energy IS the user's real whole-home electric meter, so the
// candidate radio meter whose counter delta over each billing bucket matches the
// bill — at a stable multiplier — is almost certainly theirs. Unlike the
// monitored-subset reference (an inequality), this is a whole-home EQUALITY
// match, and it doubles as an absolute calibration source.

// UpsertUtilityEnergy idempotently writes per-bucket consumed kWh for a statistic.
// The backfill re-fetches a trailing window each run; ON CONFLICT lets late
// utility revisions overwrite prior values.
func (d *DB) UpsertUtilityEnergy(ctx context.Context, statID, period string, ts []time.Time, kwh []float64) error {
	if len(ts) == 0 {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
INSERT INTO utility_energy (ts, statistic_id, period, kwh)
SELECT u.ts, $1, $2, u.kwh
FROM unnest($3::timestamptz[], $4::float8[]) AS u(ts, kwh)
ON CONFLICT (statistic_id, period, ts) DO UPDATE SET kwh = EXCLUDED.kwh`,
		statID, period, ts, kwh)
	return err
}

// KeepOnlyUtility drops utility rows that don't match the current statistic and
// period, so a switch of statistic or granularity doesn't leave stale buckets
// that would confuse the evidence query.
func (d *DB) KeepOnlyUtility(ctx context.Context, statID, period string) error {
	_, err := d.pool.Exec(ctx,
		`DELETE FROM utility_energy WHERE statistic_id <> $1 OR period <> $2`, statID, period)
	return err
}

// UtilityEnergy returns the billed energy (kWh) over [start,end] for a statistic.
func (d *DB) UtilityEnergy(ctx context.Context, statID string, start, end time.Time) float64 {
	if statID == "" {
		return 0
	}
	var kwh *float64
	_ = d.pool.QueryRow(ctx,
		`SELECT sum(kwh) FROM utility_energy WHERE statistic_id=$1 AND ts >= $2 AND ts < $3`,
		statID, start, end).Scan(&kwh)
	return deref(kwh)
}

// UtilityCoverage returns the stored span for a statistic/period and bucket count.
func (d *DB) UtilityCoverage(ctx context.Context, statID, period string) (lo, hi time.Time, n int) {
	var a, b *time.Time
	_ = d.pool.QueryRow(ctx,
		`SELECT min(ts), max(ts), count(*) FROM utility_energy WHERE statistic_id=$1 AND period=$2`,
		statID, period).Scan(&a, &b, &n)
	if a != nil {
		lo = *a
	}
	if b != nil {
		hi = *b
	}
	return
}

// utilBucket is one billing bucket aligned to a candidate meter: the billed kWh,
// the meter's rollover-aware counter delta over the part of the bucket winnow
// covered, and that coverage fraction (covered hours ÷ bucket hours).
type utilBucket struct {
	start, end time.Time
	utilityKwh float64
	meterDelta float64
	coverage   float64 // 0..1
}

// utilEvidence is a candidate meter's aggregated standing against the bill.
type utilEvidence struct {
	multiplier     *float64 // coverage-weighted mean kWh per meter-unit
	cov            *float64 // coefficient of variation of the per-bucket multiplier
	r              *float64 // per-bucket correlation (only meaningful for hour/day periods)
	bucketsCovered int
	buckets        []utilBucket
}

// utilityMeterEvidence computes, per candidate meter, its per-bucket alignment to
// the bill over the statistic's covered span. Hourly meter deltas are computed
// once across the whole span (so rollovers and bucket boundaries are handled
// continuously) then assigned to billing buckets and summed; coverage is the
// fraction of each bucket's hours winnow actually has data for, which prorates
// the bill for partially-captured buckets.
func (d *DB) utilityMeterEvidence(ctx context.Context, statID, period string) (map[int64]*utilEvidence, error) {
	out := map[int64]*utilEvidence{}
	if statID == "" {
		return out, nil
	}
	rows, err := d.pool.Query(ctx, `
WITH ub AS (
  SELECT ts AS bstart,
         lead(ts) OVER (ORDER BY ts) AS bend,
         kwh
  FROM utility_energy
  WHERE statistic_id=$1 AND period=$2),
span AS (SELECT min(bstart) AS lo, max(coalesce(bend,bstart)) AS hi FROM ub),
hourly AS (
  SELECT r.endpoint_id,
         time_bucket('1 hour', r.bucket) AS h,
         max(r.max_c) AS cmax,
         CASE WHEN mi.msg_type='SCM' THEN 16777216.0 ELSE 4294967296.0 END AS modulus
  FROM readings_1m r
  JOIN meter_index mi USING (endpoint_id)
  WHERE r.bucket >= (SELECT lo FROM span) AND r.bucket < (SELECT hi FROM span)
  GROUP BY r.endpoint_id, h, modulus),
stepped AS (
  SELECT endpoint_id, h, modulus,
         cmax - lag(cmax) OVER (PARTITION BY endpoint_id ORDER BY h) AS raw
  FROM hourly),
mdelta AS (
  SELECT endpoint_id, h, `+rolloverDeltaSQL("raw", "modulus")+` AS delta
  FROM stepped),
joined AS (
  SELECT b.bstart, b.bend, b.kwh, m.endpoint_id, m.delta
  FROM ub b JOIN mdelta m ON m.h >= b.bstart AND m.h < b.bend
  WHERE b.bend IS NOT NULL AND m.delta IS NOT NULL)
SELECT endpoint_id, bstart, bend, kwh,
       sum(delta)                                        AS meter_delta,
       count(*)                                          AS covered_hours,
       EXTRACT(EPOCH FROM (bend - bstart))/3600.0        AS bucket_hours
FROM joined
GROUP BY endpoint_id, bstart, bend, kwh
HAVING sum(delta) > 0
ORDER BY endpoint_id, bstart`, statID, period)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var bstart, bend time.Time
		var kwh, meterDelta, coveredHours, bucketHours float64
		if err := rows.Scan(&id, &bstart, &bend, &kwh, &meterDelta, &coveredHours, &bucketHours); err != nil {
			return out, err
		}
		cov := 1.0
		if bucketHours > 0 {
			cov = coveredHours / bucketHours
		}
		cov = clamp01(cov)
		e := out[id]
		if e == nil {
			e = &utilEvidence{}
			out[id] = e
		}
		e.buckets = append(e.buckets, utilBucket{start: bstart, end: bend, utilityKwh: kwh, meterDelta: meterDelta, coverage: cov})
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, e := range out {
		summarizeUtilEvidence(e)
	}
	return out, nil
}

// summarizeUtilEvidence derives the coverage-weighted multiplier, its stability,
// and (when there are enough buckets) the per-bucket correlation.
func summarizeUtilEvidence(e *utilEvidence) {
	var mults, weights, meterX, utilY []float64
	for _, b := range e.buckets {
		if b.meterDelta <= 0 || b.coverage <= 0 {
			continue
		}
		prorated := b.utilityKwh * b.coverage
		mults = append(mults, prorated/b.meterDelta)
		weights = append(weights, b.coverage)
		meterX = append(meterX, b.meterDelta)
		utilY = append(utilY, prorated)
		e.bucketsCovered++
	}
	if len(mults) == 0 {
		return
	}
	m := weightedMean(mults, weights)
	if m > 0 {
		mr := round(m, 8)
		e.multiplier = &mr
		if len(mults) >= 2 {
			cv := round(stddev(mults)/m, 3)
			e.cov = &cv
		}
	}
	// per-bucket correlation only carries signal when there are many native
	// buckets (hour/day periods) — monthly data has too few points.
	if len(meterX) >= 24 {
		r := round(pearson(meterX, utilY), 3)
		e.r = &r
	}
}

func weightedMean(xs, ws []float64) float64 {
	var sx, sw float64
	for i := range xs {
		sx += xs[i] * ws[i]
		sw += ws[i]
	}
	if sw == 0 {
		return 0
	}
	return sx / sw
}

// UtilityCompare builds the per-meter "compare vs utility bill" panel: each
// billing bucket's billed kWh vs the candidate's metered energy (at the utility
// multiplier), plus — for coarse (monthly) bills — an estimated-daily breakdown.
func (d *DB) UtilityCompare(ctx context.Context, meterID int64) (*model.UtilityCompareResult, error) {
	cfg, err := d.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	statID := cfg.UtilityStatisticID
	if statID == "" {
		return &model.UtilityCompareResult{}, nil
	}
	period := d.resolvedPeriod(ctx, statID)
	ev, err := d.utilityMeterEvidence(ctx, statID, period)
	if err != nil {
		return nil, err
	}
	res := &model.UtilityCompareResult{StatisticID: statID, Period: period}
	e := ev[meterID]
	if e == nil {
		return res, nil
	}
	res.UtilityMultiplier = e.multiplier
	res.BucketsCovered = e.bucketsCovered
	mult := deref(e.multiplier)
	for _, b := range e.buckets {
		pt := model.UtilityComparePoint{
			TS:          b.start.UTC().Format(time.RFC3339),
			UtilityKwh:  round(b.utilityKwh, 3),
			CoveragePct: round(b.coverage, 3),
		}
		if mult > 0 {
			mk := round(b.meterDelta*mult, 3)
			pt.MeterKwh = &mk
		}
		res.Buckets = append(res.Buckets, pt)
	}
	if period == "month" {
		res.DailyEstimate = d.utilityDailyEstimate(ctx, meterID, statID, cfg.MonitoredEntities, mult, cfg.HATimeZone)
	}
	return res, nil
}

// resolvedPeriod returns the period actually stored for a statistic (the backfill
// records whichever period it fetched); falls back to the configured value.
func (d *DB) resolvedPeriod(ctx context.Context, statID string) string {
	var p *string
	_ = d.pool.QueryRow(ctx,
		`SELECT period FROM utility_energy WHERE statistic_id=$1 ORDER BY ts DESC LIMIT 1`, statID).Scan(&p)
	if p != nil && *p != "" {
		return *p
	}
	return "month"
}

// utilityDailyEstimate spreads each monthly bill across its days: a flat level
// (bill ÷ days) always, and a profile-shaped estimate (bill × monitored_day /
// monitored_month) when monitored sensors exist, alongside the candidate's actual
// metered energy per day. The flat line is for eyeballing/reconciliation; the
// shaped curve is the only one with day-to-day variance to correlate against.
//
// All day/month boundaries are taken in `tz` (HA's timezone) so the breakdown
// aligns to the user's local calendar day, not UTC: utility-bucket starts and
// meter readings are bucketed by their LOCAL calendar date (`AT TIME ZONE`,
// DST-safe via the date type), and the monitored-energy spans are built at local
// midnight via a time.Location.
func (d *DB) utilityDailyEstimate(ctx context.Context, meterID int64, statID string, entities []string, mult float64, tz string) []model.UtilityDayEstimate {
	out := []model.UtilityDayEstimate{}
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc, tz = time.UTC, "UTC"
	}
	rows, err := d.pool.Query(ctx, `
WITH ub AS (
  SELECT ts AS bstart, lead(ts) OVER (ORDER BY ts) AS bend, kwh
  FROM utility_energy WHERE statistic_id=$1 AND period='month'),
days AS (
  SELECT b.kwh,
         generate_series((b.bstart AT TIME ZONE $3)::date,
                         (b.bend   AT TIME ZONE $3)::date - 1,
                         interval '1 day')::date AS day,
         ((b.bend AT TIME ZONE $3)::date - (b.bstart AT TIME ZONE $3)::date) AS ndays
  FROM ub b WHERE b.bend IS NOT NULL),
mday AS (
  SELECT (r.bucket AT TIME ZONE $3)::date AS day,
         max(r.max_c) AS cmax,
         CASE WHEN mi.msg_type='SCM' THEN 16777216.0 ELSE 4294967296.0 END AS modulus
  FROM readings_1m r JOIN meter_index mi USING (endpoint_id)
  WHERE r.endpoint_id=$2
  GROUP BY day, modulus),
mstep AS (
  SELECT day, modulus, cmax - lag(cmax) OVER (ORDER BY day) AS raw FROM mday),
mdelta AS (
  SELECT day, `+rolloverDeltaSQL("raw", "modulus")+` AS delta FROM mstep)
SELECT d.day, d.kwh, d.kwh / nullif(d.ndays,0) AS flat, md.delta AS meter_delta
FROM days d
LEFT JOIN mdelta md ON md.day = d.day
ORDER BY d.day`, statID, meterID, tz)
	if err != nil {
		return out
	}
	defer rows.Close()
	type row struct {
		day        time.Time
		monthKwh   float64
		flat       float64
		meterDelta *float64
	}
	var drows []row
	for rows.Next() {
		var r row
		var flat *float64
		if err := rows.Scan(&r.day, &r.monthKwh, &flat, &r.meterDelta); err != nil {
			return out
		}
		r.flat = deref(flat)
		drows = append(drows, r)
	}
	for _, r := range drows {
		// r.day is a calendar date; rebuild local-midnight bounds in HA's tz.
		y, m, dd := r.day.Date()
		dayStart := time.Date(y, m, dd, 0, 0, 0, 0, loc)
		dayEnd := dayStart.AddDate(0, 0, 1) // DST-safe next local midnight
		de := model.UtilityDayEstimate{Day: dayStart.Format("2006-01-02"), FlatKwh: round(r.flat, 3)}
		if r.meterDelta != nil && mult > 0 {
			mk := round(*r.meterDelta*mult, 3)
			de.MeterKwh = &mk
		}
		// profile-shaped estimate: bill × (monitored energy this local day ÷ this
		// local month). Needs monitored sensors.
		if len(entities) > 0 && r.monthKwh > 0 {
			monStart := time.Date(y, m, 1, 0, 0, 0, 0, loc)
			monEnd := monStart.AddDate(0, 1, 0)
			dayE := d.MonitoredEnergy(ctx, entities, dayStart, dayEnd)
			monE := d.MonitoredEnergy(ctx, entities, monStart, monEnd)
			if monE > 0 {
				shaped := round(r.monthKwh*(dayE/monE), 3)
				de.ShapedKwh = &shaped
			}
		}
		out = append(out, de)
	}
	return out
}
