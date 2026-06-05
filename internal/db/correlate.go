package db

import (
	"context"
	"math"
	"sort"
	"time"

	"winnow/internal/ert"
	"winnow/internal/model"
)

const epsilon = 1e-9

func (d *DB) dataSpan(ctx context.Context) (time.Time, time.Time, bool) {
	var lo, hi *time.Time
	err := d.pool.QueryRow(ctx,
		`SELECT min(first_seen), max(last_seen) FROM meter_index`).Scan(&lo, &hi)
	if err != nil || lo == nil || hi == nil {
		return time.Time{}, time.Time{}, false
	}
	return *lo, *hi, true
}

// Correlation ranks meters by in-window rate ÷ baseline rate (rate-ratio score).
func (d *DB) Correlation(ctx context.Context, start, end time.Time) ([]model.CorrRow, error) {
	lo, hi, ok := d.dataSpan(ctx)
	if !ok {
		return []model.CorrRow{}, nil
	}
	windowHours := end.Sub(start).Hours()
	if windowHours <= 0 {
		windowHours = epsilon
	}
	spanHours := hi.Sub(lo).Hours()
	outsideHours := spanHours - windowHours
	if outsideHours < epsilon {
		outsideHours = epsilon
	}

	rows, err := d.pool.Query(ctx, `
SELECT i.endpoint_id, i.msg_type, i.endpoint_type,
       i.max_consumption - i.min_consumption AS total_delta,
       w.window_delta, w.window_packets,
       coalesce(m.is_mine, false), coalesce(m.publish, false),
       coalesce(m.pub_multiplier, 1), m.pub_unit
FROM meter_index i
LEFT JOIN (
  SELECT endpoint_id,
         max(max_c) - min(min_c) AS window_delta,
         sum(n)                  AS window_packets
  FROM readings_1m
  WHERE bucket BETWEEN $1 AND $2
  GROUP BY endpoint_id
) w ON w.endpoint_id = i.endpoint_id
LEFT JOIN meters m ON m.endpoint_id = i.endpoint_id`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.CorrRow{}
	for rows.Next() {
		var r model.CorrRow
		var msgType *string
		var totalDelta, windowDelta *float64
		var windowPackets *int64
		if err := rows.Scan(&r.EndpointID, &msgType, &r.EndpointType, &totalDelta, &windowDelta, &windowPackets, &r.IsMine, &r.Publish, &r.PubMultiplier, &r.PubUnit); err != nil {
			return nil, err
		}
		if msgType != nil {
			r.MsgType = *msgType
		}
		if windowPackets != nil {
			r.WindowPackets = *windowPackets
		}
		r.Commodity = ert.Commodity(r.EndpointType)
		wd := deref(windowDelta)
		outsideMovement := deref(totalDelta) - wd
		if outsideMovement < 0 {
			outsideMovement = 0
		}
		r.WindowDelta = wd
		r.WindowRate = round(wd/windowHours, 4)
		r.BaselineRate = round(outsideMovement/outsideHours, 4)
		r.Score = round((wd/windowHours)/max(outsideMovement/outsideHours, epsilon), 3)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].WindowDelta > out[j].WindowDelta
	})
	return out, nil
}

// regrRow holds the in-DB regression of a meter's per-bucket energy delta vs the
// aggregate monitored energy. sxx/syy/sxy are the regression sums used for the
// Deming (total-least-squares) slope; xbar/ybar recover the Deming intercept.
type regrRow struct {
	r, slope, intercept, r2, p10 *float64
	sxx, syy, sxy, xbar, ybar    *float64
	nbuckets                     *int
}

// CorrelationVsReference ranks meters against the TOTAL monitored power (sum of
// the configured set). For each meter it computes, in-DB: Pearson r and the
// linear regression of per-minute delta vs aggregate power — which yields the
// unit multiplier (slope), the unmonitored baseline (intercept), and a floor
// check. Returns rows + the monitored floor (W). Floor is a soft down-rank.
func (d *DB) CorrelationVsReference(ctx context.Context, entities []string, start, end time.Time, bucketMin int, electricOnly bool) ([]model.CorrRow, float64, error) {
	base, err := d.Correlation(ctx, start, end)
	if err != nil {
		return nil, 0, err
	}
	if electricOnly {
		// The monitored reference is electrical power, so ranking gas/water meters
		// against it is meaningless. Reuse the commodity already set in Correlation().
		kept := base[:0]
		for _, r := range base {
			if r.Commodity == "electric" {
				kept = append(kept, r)
			}
		}
		base = kept
	}
	floor := d.MonitoredFloor(ctx, entities, start, end)
	if len(entities) == 0 {
		return base, 0, nil
	}
	if bucketMin <= 0 {
		bucketMin = 1
	}
	bucketHours := float64(bucketMin) / 60.0
	monitoredKwh := d.MonitoredEnergy(ctx, entities, start, end)

	// Compare ENERGY per bucket on both sides. Reference: integrate per-minute
	// monitored power (W) over the bucket → Wh. Meter: the rise in the cumulative
	// counter SINCE the previous bucket (cross-bucket delta) — a single packet per
	// bucket still yields real consumption, where within-bucket max-min was 0.
	reg := map[int64]regrRow{}
	rows, err := d.pool.Query(ctx, `
WITH per_entity AS (
  SELECT time_bucket_gapfill('1 minute', ts) AS mt, entity_id, locf(avg(power_w)) AS w
  FROM reference_samples
  WHERE entity_id = ANY($3) AND ts >= $1 AND ts <= $2
  GROUP BY mt, entity_id),
per_min AS (SELECT mt, sum(coalesce(w,0)) AS w FROM per_entity GROUP BY mt),
ref AS (
  SELECT time_bucket(make_interval(mins => $4), mt) AS b, sum(w)/60.0 AS energy_wh
  FROM per_min GROUP BY b),
mb AS (
  SELECT r.endpoint_id, time_bucket(make_interval(mins => $4), r.bucket) AS b,
         max(r.max_c) AS cmax,
         CASE WHEN mi.msg_type = 'SCM' THEN 16777216.0 ELSE 4294967296.0 END AS modulus
  FROM readings_1m r JOIN meter_index mi USING (endpoint_id)
  WHERE r.bucket >= $1 AND r.bucket <= $2
  GROUP BY r.endpoint_id, b, modulus),
stepped AS (
  SELECT endpoint_id, b, modulus,
         cmax - lag(cmax) OVER (PARTITION BY endpoint_id ORDER BY b) AS raw
  FROM mb),
m AS (
  SELECT endpoint_id, b, `+rolloverDeltaSQL("raw", "modulus")+` AS delta
  FROM stepped)
SELECT m.endpoint_id,
       corr(m.delta, ref.energy_wh)            AS r,
       regr_slope(m.delta, ref.energy_wh)      AS slope,
       regr_intercept(m.delta, ref.energy_wh)  AS intercept,
       regr_r2(m.delta, ref.energy_wh)         AS r2,
       regr_sxx(m.delta, ref.energy_wh)        AS sxx,
       regr_syy(m.delta, ref.energy_wh)        AS syy,
       regr_sxy(m.delta, ref.energy_wh)        AS sxy,
       avg(ref.energy_wh)                       AS xbar,
       avg(m.delta)                             AS ybar,
       count(*)                                 AS nbuckets,
       percentile_cont(0.1) WITHIN GROUP (ORDER BY m.delta) AS p10
FROM m JOIN ref USING (b)
WHERE m.delta IS NOT NULL
GROUP BY m.endpoint_id
HAVING count(*) >= 5`, start, end, entities, bucketMin)
	if err == nil {
		for rows.Next() {
			var id int64
			var rr regrRow
			if err := rows.Scan(&id, &rr.r, &rr.slope, &rr.intercept, &rr.r2,
				&rr.sxx, &rr.syy, &rr.sxy, &rr.xbar, &rr.ybar, &rr.nbuckets, &rr.p10); err == nil {
				reg[id] = rr
			}
		}
		rows.Close()
	}

	for i := range base {
		rr, ok := reg[base[i].EndpointID]
		if !ok {
			continue
		}
		if rr.r != nil {
			v := round(*rr.r, 3)
			base[i].R = &v
		}
		if rr.r2 != nil {
			v := round(*rr.r2, 3)
			base[i].R2 = &v
		}
		// Calibration only makes sense for a positive relationship. Prefer the Deming
		// (total-least-squares) slope: OLS attenuates when the monitored reference is
		// itself noisy or only a partial subset of the home, biasing the multiplier.
		// slope is meter-units per Wh, so the multiplier (kWh per meter-unit) is
		// 1/(1000·slope) — independent of the chosen bucket width.
		slope := 0.0
		if rr.sxx != nil && rr.syy != nil && rr.sxy != nil {
			slope = demingSlope(*rr.sxx, *rr.syy, *rr.sxy)
		}
		if slope <= 0 && rr.slope != nil { // fall back to OLS if Deming is degenerate
			slope = *rr.slope
		}
		if slope > 0 {
			s := round(slope, 6)
			base[i].Slope = &s
			mult := round(1.0/(1000.0*slope), 8)
			base[i].SuggestedMultiplier = &mult
			// Deming intercept = ȳ − slope·x̄ (units/bucket); keep it as the unmonitored
			// baseline rather than forcing the fit through zero.
			intercept := 0.0
			if rr.xbar != nil && rr.ybar != nil {
				intercept = *rr.ybar - slope*(*rr.xbar)
			} else if rr.intercept != nil {
				intercept = *rr.intercept
			}
			// intercept: units/bucket → Wh/bucket (/slope) → W (/bucketHours)
			bw := round((intercept/slope)/bucketHours, 1)
			base[i].BaselineW = &bw
			// floor check: meter's calibrated low-rate power ≥ monitored floor
			if rr.p10 != nil && floor > 0 {
				minW := (*rr.p10 / slope) / bucketHours // (units/bucket)→Wh/bucket→W
				ok := minW >= 0.8*floor
				base[i].FloorOK = &ok
			}
			// candidate energy over the window at the suggested calibration (kWh).
			mkwh := round(base[i].WindowDelta*mult, 3)
			base[i].MeterEnergyKwh = &mkwh
		}
	}

	// Number of meters that produced a usable correlation this window — the size of
	// the multiple-comparison family for the data-snooping correction.
	nCandidates := 0
	for i := range base {
		if base[i].R != nil {
			nCandidates++
		}
	}

	// Compute the composite confidence from the within-window signals. The lag
	// signal is filled in below for the top candidates (Stage-2 enrichment).
	for i := range base {
		rr, ok := reg[base[i].EndpointID]
		if !ok {
			continue
		}
		sig := confidenceSignals{
			R: base[i].R, R2: base[i].R2,
			HasMultiplier: base[i].SuggestedMultiplier != nil,
			MonitoredKwh:  monitoredKwh,
			FloorOK:       base[i].FloorOK,
			WindowPackets: base[i].WindowPackets,
			NCandidates:   nCandidates,
		}
		if base[i].MeterEnergyKwh != nil {
			sig.MeterKwh = *base[i].MeterEnergyKwh
		}
		if rr.nbuckets != nil {
			sig.NBuckets = *rr.nbuckets
		}
		// change-point coherence: did the rate step up in-window vs the baseline?
		cp := changePointCoherence(base[i].WindowRate, base[i].BaselineRate)
		sig.ChangePoint = &cp
		conf, parts := combineConfidence(sig)
		base[i].Confidence = &conf
		base[i].ConfidenceParts = parts
	}

	// Stage-2 enrichment: for the strongest candidates, pull their aligned
	// per-bucket series and fold the cross-correlation lag plausibility into
	// confidence. Bounded to the top few so the extra query stays cheap.
	d.enrichLag(ctx, base, entities, start, end, bucketMin, monitoredKwh, nCandidates)

	sort.SliceStable(base, func(i, j int) bool {
		ci, cj := deref(base[i].Confidence), deref(base[j].Confidence)
		if ci != cj {
			return ci > cj
		}
		return base[i].Score > base[j].Score
	})
	return base, floor, nil
}

// enrichLag computes the cross-correlation lag for the top candidates and folds a
// lag-plausibility penalty into their confidence. Done as a bounded Stage-2 pass
// (not for every meter) because it pulls per-bucket series.
func (d *DB) enrichLag(ctx context.Context, base []model.CorrRow, entities []string, start, end time.Time, bucketMin int, monitoredKwh float64, nCandidates int) {
	const topN, maxLag = 8, 5
	// pick the current top-N by confidence
	idx := make([]int, 0, len(base))
	for i := range base {
		if base[i].Confidence != nil {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return deref(base[idx[a]].Confidence) > deref(base[idx[b]].Confidence)
	})
	if len(idx) > topN {
		idx = idx[:topN]
	}
	if len(idx) == 0 {
		return
	}
	ids := make([]int64, len(idx))
	for k, i := range idx {
		ids[k] = base[i].EndpointID
	}
	series, err := d.alignedSeries(ctx, ids, entities, start, end, bucketMin)
	if err != nil {
		return
	}
	for _, i := range idx {
		s, ok := series[base[i].EndpointID]
		if !ok || len(s.meter) < 4 {
			continue
		}
		lag, _ := crossCorrLag(s.meter, s.ref, maxLag)
		base[i].LagBuckets = &lag
		lp := lagPenalty(lag, maxLag)
		// recompute confidence with the lag penalty included
		sig := confidenceSignals{
			R: base[i].R, R2: base[i].R2,
			HasMultiplier: base[i].SuggestedMultiplier != nil,
			MonitoredKwh:  monitoredKwh,
			FloorOK:       base[i].FloorOK,
			WindowPackets: base[i].WindowPackets,
			NBuckets:      len(s.meter),
			NCandidates:   nCandidates,
			LagPenalty:    &lp,
		}
		if base[i].MeterEnergyKwh != nil {
			sig.MeterKwh = *base[i].MeterEnergyKwh
		}
		cp := changePointCoherence(base[i].WindowRate, base[i].BaselineRate)
		sig.ChangePoint = &cp
		conf, parts := combineConfidence(sig)
		base[i].Confidence = &conf
		base[i].ConfidenceParts = parts
	}
}

// alignedPair holds one meter's bucket-ordered energy deltas and the matching
// monitored-energy values, for cross-correlation.
type alignedPair struct{ meter, ref []float64 }

// alignedSeries returns, per meter id, the bucket-ordered (delta, reference Wh)
// pairs over the window — the same rollover-aware energy basis the correlation
// uses, but as raw series rather than aggregates (for lag estimation).
func (d *DB) alignedSeries(ctx context.Context, ids []int64, entities []string, start, end time.Time, bucketMin int) (map[int64]alignedPair, error) {
	out := map[int64]alignedPair{}
	if len(ids) == 0 || len(entities) == 0 {
		return out, nil
	}
	rows, err := d.pool.Query(ctx, `
WITH per_entity AS (
  SELECT time_bucket_gapfill('1 minute', ts) AS mt, entity_id, locf(avg(power_w)) AS w
  FROM reference_samples
  WHERE entity_id = ANY($3) AND ts >= $1 AND ts <= $2
  GROUP BY mt, entity_id),
per_min AS (SELECT mt, sum(coalesce(w,0)) AS w FROM per_entity GROUP BY mt),
ref AS (
  SELECT time_bucket(make_interval(mins => $4), mt) AS b, sum(w)/60.0 AS energy_wh
  FROM per_min GROUP BY b),
mb AS (
  SELECT r.endpoint_id, time_bucket(make_interval(mins => $4), r.bucket) AS b,
         max(r.max_c) AS cmax,
         CASE WHEN mi.msg_type = 'SCM' THEN 16777216.0 ELSE 4294967296.0 END AS modulus
  FROM readings_1m r JOIN meter_index mi USING (endpoint_id)
  WHERE r.endpoint_id = ANY($5) AND r.bucket >= $1 AND r.bucket <= $2
  GROUP BY r.endpoint_id, b, modulus),
stepped AS (
  SELECT endpoint_id, b, modulus,
         cmax - lag(cmax) OVER (PARTITION BY endpoint_id ORDER BY b) AS raw
  FROM mb),
m AS (
  SELECT endpoint_id, b, `+rolloverDeltaSQL("raw", "modulus")+` AS delta
  FROM stepped)
SELECT m.endpoint_id, m.delta, ref.energy_wh
FROM m JOIN ref USING (b)
WHERE m.delta IS NOT NULL
ORDER BY m.endpoint_id, m.b`, start, end, entities, bucketMin, ids)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var delta, wh float64
		if err := rows.Scan(&id, &delta, &wh); err != nil {
			return out, err
		}
		p := out[id]
		p.meter = append(p.meter, delta)
		p.ref = append(p.ref, wh)
		out[id] = p
	}
	return out, rows.Err()
}

// AggRow is a meter's standing across multiple test windows.
type AggRow struct {
	EndpointID   int64    `json:"endpoint_id"`
	Commodity    string   `json:"commodity"`
	AvgScore     float64  `json:"avg_score"`
	MinScore     float64  `json:"min_score"`
	AvgR         *float64 `json:"avg_r"`
	Wins         int      `json:"wins"`
	TestsPresent int      `json:"tests_present"`
	TestsTotal   int      `json:"tests_total"`
	// cross-window confidence: the per-window confidence averaged, then scaled by
	// agreement (how many windows it appears/wins in) and calibration stability.
	Confidence       *float64 `json:"confidence"`
	AvgConfidence    *float64 `json:"avg_confidence"`
	MultiplierCoV    *float64 `json:"multiplier_cov"`    // coeff. of variation of the per-window multiplier
	AnchorMultiplier *float64 `json:"anchor_multiplier"` // known-load anchor (mean across windows that had one)
	SuggestedMult    *float64 `json:"suggested_multiplier"`
}

// CombinedRanking aggregates correlation across ALL closed windows (the meter
// that wins every test is almost certainly yours). When ground-truth sensors are
// configured it ranks against them (composite confidence + known-load anchor);
// otherwise it falls back to the rate-ratio score.
func (d *DB) CombinedRanking(ctx context.Context, entities []string) (map[string]any, error) {
	return d.aggregateWindows(ctx, len(entities) > 0, "", entities)
}

// IdentifyAuto aggregates reference correlation across recent closed auto windows.
func (d *DB) IdentifyAuto(ctx context.Context, entities []string) (map[string]any, error) {
	return d.aggregateWindows(ctx, true, "auto", entities)
}

func (d *DB) aggregateWindows(ctx context.Context, useRef bool, src string, entities []string) (map[string]any, error) {
	wins, err := d.closedWindows(ctx, src)
	if err != nil {
		return nil, err
	}
	type agg struct {
		commodity string
		scores    []float64
		rs        []float64
		confs     []float64
		mults     []float64 // per-window suggested multiplier (for slope stability)
		anchors   []float64 // per-window known-load anchor multiplier
		wins      int
		present   int
	}
	per := map[int64]*agg{}
	used := []map[string]any{}
	for _, w := range wins {
		var ranked []model.CorrRow
		if useRef {
			ranked, _, err = d.CorrelationVsReference(ctx, entities, w.start, w.end, PickBucketMin(int(w.end.Sub(w.start).Minutes())), true)
		} else {
			ranked, err = d.Correlation(ctx, w.start, w.end)
		}
		if err != nil || len(ranked) == 0 {
			continue
		}
		// known-load anchor for this window (direct, regression-free calibration).
		expectedKwh := 0.0
		if w.knownLoadW != nil {
			expectedKwh = *w.knownLoadW * w.end.Sub(w.start).Hours() / 1000.0
		} else if w.knownEntity != nil {
			expectedKwh = d.EntityEnergy(ctx, *w.knownEntity, w.start, w.end)
		}
		used = append(used, map[string]any{"id": w.id, "label": w.label})
		for rank, r := range ranked {
			a := per[r.EndpointID]
			if a == nil {
				a = &agg{commodity: r.Commodity}
				per[r.EndpointID] = a
			}
			a.scores = append(a.scores, r.Score)
			if r.R != nil {
				a.rs = append(a.rs, *r.R)
			}
			if r.Confidence != nil {
				a.confs = append(a.confs, *r.Confidence)
			}
			if r.SuggestedMultiplier != nil {
				a.mults = append(a.mults, *r.SuggestedMultiplier)
			}
			if expectedKwh > 0 && r.WindowDelta > 0 {
				a.anchors = append(a.anchors, expectedKwh/r.WindowDelta)
			}
			a.present++
			if rank == 0 {
				a.wins++
			}
		}
	}
	ranking := []AggRow{}
	for id, a := range per {
		row := AggRow{EndpointID: id, Commodity: a.commodity, Wins: a.wins,
			TestsPresent: a.present, TestsTotal: len(used)}
		row.AvgScore = round(mean(a.scores), 3)
		row.MinScore = round(minOf(a.scores), 3)
		if len(a.rs) > 0 {
			r := round(mean(a.rs), 3)
			row.AvgR = &r
		}
		if useRef {
			d.aggregateConfidence(&row, a.confs, a.mults, a.anchors)
		}
		ranking = append(ranking, row)
	}
	sort.SliceStable(ranking, func(i, j int) bool {
		// in the reference path the composite confidence is the headline.
		if useRef && ranking[i].Confidence != nil && ranking[j].Confidence != nil && *ranking[i].Confidence != *ranking[j].Confidence {
			return *ranking[i].Confidence > *ranking[j].Confidence
		}
		if useRef && ranking[i].AvgR != nil && ranking[j].AvgR != nil && *ranking[i].AvgR != *ranking[j].AvgR {
			return *ranking[i].AvgR > *ranking[j].AvgR
		}
		if ranking[i].Wins != ranking[j].Wins {
			return ranking[i].Wins > ranking[j].Wins
		}
		return ranking[i].AvgScore > ranking[j].AvgScore
	})
	return map[string]any{"tests": used, "ranking": ranking}, nil
}

// aggregateConfidence folds a meter's per-window confidences into an overall
// cross-window confidence: the mean confidence, scaled up by agreement (winning
// across independent windows defeats single-window data-snooping) and by
// calibration stability (a consistent multiplier across windows), and boosted
// when a known-load anchor agrees with the regression multiplier.
func (d *DB) aggregateConfidence(row *AggRow, confs, mults, anchors []float64) {
	if len(confs) == 0 {
		return
	}
	avg := round(mean(confs), 3)
	row.AvgConfidence = &avg

	conf := mean(confs)
	// agreement: reward appearing and winning across multiple windows.
	if row.TestsTotal > 1 {
		winRate := float64(row.Wins) / float64(row.TestsTotal)
		presentRate := float64(row.TestsPresent) / float64(row.TestsTotal)
		conf *= 0.6 + 0.25*presentRate + 0.15*winRate
	}
	// slope stability: low coefficient of variation of the per-window multiplier.
	if len(mults) >= 2 {
		m := mean(mults)
		if m > 0 {
			cov := stddev(mults) / m
			c := round(cov, 3)
			row.MultiplierCoV = &c
			conf *= clamp01(1.1 - cov) // CoV 0 → ×1.1 (capped), CoV ≥1 → heavy cut
		}
	}
	if len(mults) > 0 {
		sm := round(mean(mults), 8)
		row.SuggestedMult = &sm
	}
	// known-load anchor: a direct multiplier; if it agrees with the regression
	// multiplier, that is strong independent confirmation.
	if len(anchors) > 0 {
		am := round(mean(anchors), 8)
		row.AnchorMultiplier = &am
		if row.SuggestedMult != nil && *row.SuggestedMult > 0 && am > 0 {
			ratio := am / *row.SuggestedMult
			if ratio < 1 {
				ratio = 1 / ratio
			}
			conf *= clamp01(1.2 - 0.3*(ratio-1)) // close agreement boosts, divergence cuts
		}
	}
	c := round(clamp01(conf), 3)
	row.Confidence = &c
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}
func minOf(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	m := xs[0]
	for _, x := range xs {
		if x < m {
			m = x
		}
	}
	return m
}
func stddev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := mean(xs)
	var s float64
	for _, x := range xs {
		s += (x - m) * (x - m)
	}
	return math.Sqrt(s / float64(len(xs)-1))
}
