package db

import (
	"context"
	"strings"
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

// kwhFactor converts a statistic's native energy unit to kWh. utility_energy
// stores values in the statistic's own unit, so display/cost must normalize.
func kwhFactor(unit string) float64 {
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case "WH":
		return 0.001
	case "MWH":
		return 1000
	default: // kWh or unknown
		return 1
	}
}

type reconBucket struct{ kwh, coverage float64 }

// utilityReconciliation returns, per billing bucket (keyed by bucket-start Unix
// seconds), the energy winnow's OWN published (else is-mine) electric meter(s)
// recorded — counter delta × pub_multiplier → kWh — plus that bucket's capture
// coverage. This is the "is winnow tracking the bill?" overlay. Also returns the
// eligible meter ids backing the line.
func (d *DB) utilityReconciliation(ctx context.Context, statID, period string) (map[int64]reconBucket, []int64, error) {
	out := map[int64]reconBucket{}
	meters := []int64{}

	mrows, err := d.pool.Query(ctx, `
SELECT mi.endpoint_id
FROM meter_index mi JOIN meters m ON m.endpoint_id = mi.endpoint_id
WHERE (m.publish OR m.is_mine) AND mi.endpoint_type IN (4,5,7,8,12,13)
ORDER BY mi.endpoint_id`)
	if err != nil {
		return out, meters, err
	}
	for mrows.Next() {
		var id int64
		if err := mrows.Scan(&id); err != nil {
			mrows.Close()
			return out, meters, err
		}
		meters = append(meters, id)
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return out, meters, err
	}
	if len(meters) == 0 {
		return out, meters, nil // no published/mine electric meter to reconcile against
	}

	rows, err := d.pool.Query(ctx, `
WITH ub AS (
  SELECT ts AS bstart, lead(ts) OVER (ORDER BY ts) AS bend
  FROM utility_energy WHERE statistic_id=$1 AND period=$2),
span AS (SELECT min(bstart) AS lo, max(coalesce(bend,bstart)) AS hi FROM ub),
pm AS (
  SELECT mi.endpoint_id,
         coalesce(m.pub_multiplier,1) AS mult,
         CASE WHEN mi.msg_type='SCM' THEN 16777216.0 ELSE 4294967296.0 END AS modulus
  FROM meter_index mi JOIN meters m ON m.endpoint_id = mi.endpoint_id
  WHERE (m.publish OR m.is_mine) AND mi.endpoint_type IN (4,5,7,8,12,13)),
hourly AS (
  SELECT r.endpoint_id, time_bucket('1 hour', r.bucket) AS h,
         max(r.max_c) AS cmax, pm.modulus, pm.mult
  FROM readings_1m r JOIN pm ON pm.endpoint_id = r.endpoint_id
  WHERE r.bucket >= (SELECT lo FROM span) AND r.bucket < (SELECT hi FROM span)
  GROUP BY r.endpoint_id, h, pm.modulus, pm.mult),
stepped AS (
  SELECT endpoint_id, h, modulus, mult,
         cmax - lag(cmax) OVER (PARTITION BY endpoint_id ORDER BY h) AS raw
  FROM hourly),
mdelta AS (
  SELECT endpoint_id, h, mult, `+rolloverDeltaSQL("raw", "modulus")+` AS delta
  FROM stepped),
joined AS (
  SELECT b.bstart, b.bend, m.h, m.delta * m.mult AS kwh
  FROM ub b JOIN mdelta m ON m.h >= b.bstart AND m.h < b.bend
  WHERE b.bend IS NOT NULL AND m.delta IS NOT NULL)
SELECT bstart,
       sum(kwh)                                    AS meter_kwh,
       count(DISTINCT h)                           AS covered_hours,
       EXTRACT(EPOCH FROM (bend - bstart))/3600.0  AS bucket_hours
FROM joined
GROUP BY bstart, bend
ORDER BY bstart`, statID, period)
	if err != nil {
		return out, meters, err
	}
	defer rows.Close()
	for rows.Next() {
		var bstart time.Time
		var kwh, coveredHours, bucketHours float64
		if err := rows.Scan(&bstart, &kwh, &coveredHours, &bucketHours); err != nil {
			return out, meters, err
		}
		cov := 1.0
		if bucketHours > 0 {
			cov = clamp01(coveredHours / bucketHours)
		}
		out[bstart.Unix()] = reconBucket{kwh: kwh, coverage: cov}
	}
	return out, meters, rows.Err()
}

// UtilitySeries returns the configured statistic's billed-energy series (all
// buckets, converted to kWh) annotated with cost and — for bill reconciliation —
// what winnow's published meter recorded for the same periods. Backs the
// standalone "Utility bill" dashboard view.
func (d *DB) UtilitySeries(ctx context.Context) (*model.UtilitySeriesResult, error) {
	cfg, err := d.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	res := &model.UtilitySeriesResult{
		StatisticID:     cfg.UtilityStatisticID,
		Unit:            "kWh",
		Currency:        cfg.Currency,
		CostPerKwh:      cfg.CostPerKwh,
		Points:          []model.UtilitySeriesPoint{},
		ReconcileMeters: []int64{},
	}
	if cfg.UtilityStatisticID == "" {
		return res, nil
	}
	period := d.resolvedPeriod(ctx, cfg.UtilityStatisticID)
	res.Period = period
	factor := kwhFactor(cfg.UtilityUnit)

	recon, meters, rerr := d.utilityReconciliation(ctx, cfg.UtilityStatisticID, period)
	if rerr == nil {
		res.ReconcileMeters = meters
	} else {
		recon = nil // reconciliation is a bonus; the bill still renders without it
	}

	// For a coarse (monthly) bill, spread each bill across its days so the user can
	// eyeball an estimated daily-usage curve on the dashboard — a weak but useful
	// identification data point. Overlay what the published meter(s) recorded per day.
	if period == "month" {
		res.DailyEstimate = d.utilityDailyEstimateSeries(ctx, cfg.UtilityStatisticID, cfg.MonitoredEntities, cfg.HATimeZone, factor, meters)
	}

	rows, err := d.pool.Query(ctx,
		`SELECT ts, kwh FROM utility_energy WHERE statistic_id=$1 AND period=$2 ORDER BY ts`,
		cfg.UtilityStatisticID, period)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ts time.Time
		var raw float64
		if err := rows.Scan(&ts, &raw); err != nil {
			return nil, err
		}
		kwh := raw * factor
		pt := model.UtilitySeriesPoint{TS: ts.UTC().Format(time.RFC3339), Kwh: round(kwh, 3)}
		if cfg.CostPerKwh > 0 {
			c := round(kwh*cfg.CostPerKwh, 2)
			pt.Cost = &c
		}
		if recon != nil {
			if rc, ok := recon[ts.Unix()]; ok {
				mk := round(rc.kwh, 3)
				pt.MeterKwh = &mk
				pt.CoveragePct = round(rc.coverage, 3)
			}
		}
		res.TotalKwh += kwh
		res.Points = append(res.Points, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res.TotalKwh = round(res.TotalKwh, 3)
	res.BucketCount = len(res.Points)
	return res, nil
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

// utilityDailyEstimateSeries is the whole-home (meter-agnostic) sibling of
// utilityDailyEstimate, backing the standalone Utility-bill page. It spreads each
// monthly bill across its local-calendar days — a flat level (bill ÷ days) always,
// and a profile-shaped curve (bill × monitored_day ÷ monitored_month) when monitored
// sensors exist — and overlays MeterKwh: the energy the user's published/is-mine
// electric meter(s) recorded that day (Σ delta × pub_multiplier). Bill-derived values
// are converted to kWh via `factor` (the statistic's native unit); the meter overlay
// is already kWh. `meters` is the reconciliation meter set (resolved once by the
// caller). Returns empty when no monthly buckets exist.
func (d *DB) utilityDailyEstimateSeries(ctx context.Context, statID string, entities []string, tz string, factor float64, meters []int64) []model.UtilityDayEstimate {
	out := []model.UtilityDayEstimate{}
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc, tz = time.UTC, "UTC"
	}
	// Per local day: the bill's monthly total (native unit), the flat level, and the
	// published meters' summed recorded energy (already kWh via pub_multiplier).
	rows, err := d.pool.Query(ctx, `
WITH ub AS (
  SELECT ts AS bstart, lead(ts) OVER (ORDER BY ts) AS bend, kwh
  FROM utility_energy WHERE statistic_id=$1 AND period='month'),
days AS (
  SELECT b.kwh,
         generate_series((b.bstart AT TIME ZONE $2)::date,
                         (b.bend   AT TIME ZONE $2)::date - 1,
                         interval '1 day')::date AS day,
         ((b.bend AT TIME ZONE $2)::date - (b.bstart AT TIME ZONE $2)::date) AS ndays
  FROM ub b WHERE b.bend IS NOT NULL),
pm AS (
  SELECT mi.endpoint_id,
         coalesce(m.pub_multiplier,1) AS mult,
         CASE WHEN mi.msg_type='SCM' THEN 16777216.0 ELSE 4294967296.0 END AS modulus
  FROM meter_index mi JOIN meters m ON m.endpoint_id = mi.endpoint_id
  WHERE mi.endpoint_id = ANY($3)),
mday AS (
  SELECT r.endpoint_id, (r.bucket AT TIME ZONE $2)::date AS day,
         max(r.max_c) AS cmax, pm.modulus, pm.mult
  FROM readings_1m r JOIN pm ON pm.endpoint_id = r.endpoint_id
  GROUP BY r.endpoint_id, day, pm.modulus, pm.mult),
mstep AS (
  SELECT endpoint_id, day, modulus, mult,
         cmax - lag(cmax) OVER (PARTITION BY endpoint_id ORDER BY day) AS raw
  FROM mday),
mdelta AS (
  SELECT endpoint_id, day, mult, `+rolloverDeltaSQL("raw", "modulus")+` AS delta
  FROM mstep),
msum AS (SELECT day, sum(delta*mult) AS meter_kwh FROM mdelta GROUP BY day)
SELECT d.day, d.kwh, d.kwh / nullif(d.ndays,0) AS flat, ms.meter_kwh
FROM days d
LEFT JOIN msum ms ON ms.day = d.day
ORDER BY d.day`, statID, tz, meters)
	if err != nil {
		return out
	}
	defer rows.Close()
	type row struct {
		day      time.Time
		monthKwh float64 // native unit
		flat     float64 // native unit
		meterKwh *float64
	}
	var drows []row
	for rows.Next() {
		var r row
		var flat *float64
		if err := rows.Scan(&r.day, &r.monthKwh, &flat, &r.meterKwh); err != nil {
			return out
		}
		r.flat = deref(flat)
		drows = append(drows, r)
	}
	if rows.Err() != nil || len(drows) == 0 {
		return out
	}

	// Monitored per-local-day energy (kWh), fetched once over the day span, for the
	// profile-shaped estimate. Bounded to the reference span to avoid gapfilling an
	// empty multi-year range.
	var dayMon, monthMon map[string]float64
	if len(entities) > 0 {
		y0, m0, d0 := drows[0].day.Date()
		y1, m1, d1 := drows[len(drows)-1].day.Date()
		lo := time.Date(y0, m0, d0, 0, 0, 0, 0, loc)
		hi := time.Date(y1, m1, d1, 0, 0, 0, 0, loc).AddDate(0, 0, 1)
		dayMon = d.monitoredDailyKwh(ctx, entities, tz, lo, hi)
		monthMon = map[string]float64{}
		for day, k := range dayMon {
			monthMon[day[:7]] += k // sum per calendar month (yyyy-mm)
		}
	}

	for _, r := range drows {
		dayStr := r.day.Format("2006-01-02")
		de := model.UtilityDayEstimate{Day: dayStr, FlatKwh: round(r.flat*factor, 3)}
		if r.meterKwh != nil {
			mk := round(*r.meterKwh, 3) // already kWh
			de.MeterKwh = &mk
		}
		if dayMon != nil && r.monthKwh > 0 {
			if monE := monthMon[dayStr[:7]]; monE > 0 {
				shaped := round(r.monthKwh*factor*(dayMon[dayStr]/monE), 3)
				de.ShapedKwh = &shaped
			}
		}
		out = append(out, de)
	}
	return out
}

// UtilityDailyEstimateRange returns the bill's daily estimate (flat + shaped)
// restricted to local days [from, to] (YYYY-MM-DD), without the published-meter
// overlay — the identify daily screen charts it next to monitored + candidates.
func (d *DB) UtilityDailyEstimateRange(ctx context.Context, from, to string) []model.UtilityDayEstimate {
	cfg, err := d.LoadConfig(ctx)
	if err != nil || !cfg.UtilityConfigured() {
		return nil
	}
	all := d.utilityDailyEstimateSeries(ctx, cfg.UtilityStatisticID, cfg.MonitoredEntities, cfg.HATimeZone, kwhFactor(cfg.UtilityUnit), nil)
	out := make([]model.UtilityDayEstimate, 0, 64)
	for _, e := range all {
		if e.Day >= from && e.Day <= to {
			out = append(out, e)
		}
	}
	return out
}

// monitoredDailyKwh returns the monitored consumption per local-calendar day (kWh)
// over [lo,hi), in ONE query — the daily counterpart to MonitoredEnergy. The range
// is clamped to the actual reference-sample span so time_bucket_gapfill never fills
// an empty multi-year window. Keyed by local date "2006-01-02".
func (d *DB) monitoredDailyKwh(ctx context.Context, entities []string, tz string, lo, hi time.Time) map[string]float64 {
	out := map[string]float64{}
	if len(entities) == 0 {
		return out
	}
	var rlo, rhi *time.Time
	_ = d.pool.QueryRow(ctx,
		`SELECT min(ts), max(ts) FROM reference_samples WHERE entity_id = ANY($1)`, entities).Scan(&rlo, &rhi)
	if rlo == nil || rhi == nil {
		return out
	}
	start, end := lo, hi
	if rlo.After(start) {
		start = *rlo
	}
	if rhi.Before(end) {
		end = *rhi
	}
	if !start.Before(end) {
		return out
	}
	rows, err := d.pool.Query(ctx, `
WITH `+refBoundedCTEs("entity_id = ANY($1) AND ts >= $2 AND ts <= $3")+`,
per_min AS (SELECT mt, sum(coalesce(w,0)) AS w FROM per_entity GROUP BY mt)
SELECT (mt AT TIME ZONE $4)::date AS day, sum(w)/60.0/1000.0 AS kwh
FROM per_min GROUP BY day ORDER BY day`, entities, start, end, tz)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var day time.Time
		var kwh *float64
		if err := rows.Scan(&day, &kwh); err != nil {
			return out
		}
		out[day.Format("2006-01-02")] = deref(kwh)
	}
	return out
}
