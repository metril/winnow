package db

import (
	"context"
	"sort"
	"time"

	"winnow/internal/ert"
	"winnow/internal/model"
)

const epsilon = 1e-9

func (d *DB) dataSpan(ctx context.Context) (time.Time, time.Time, bool) {
	var lo, hi *time.Time
	err := d.pool.QueryRow(ctx,
		`SELECT min(ts), max(ts) FROM readings WHERE consumption IS NOT NULL AND endpoint_id IS NOT NULL`).Scan(&lo, &hi)
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
SELECT endpoint_id,
       max(msg_type)                          AS msg_type,
       max(endpoint_type)                     AS endpoint_type,
       max(consumption)-min(consumption)      AS total_delta,
       max(consumption) FILTER (WHERE ts BETWEEN $1 AND $2) -
       min(consumption) FILTER (WHERE ts BETWEEN $1 AND $2) AS window_delta,
       count(*) FILTER (WHERE ts BETWEEN $1 AND $2)         AS window_packets
FROM readings
WHERE consumption IS NOT NULL AND endpoint_id IS NOT NULL
GROUP BY endpoint_id`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.CorrRow{}
	for rows.Next() {
		var r model.CorrRow
		var msgType *string
		var totalDelta, windowDelta *float64
		if err := rows.Scan(&r.EndpointID, &msgType, &r.EndpointType, &totalDelta, &windowDelta, &r.WindowPackets); err != nil {
			return nil, err
		}
		if msgType != nil {
			r.MsgType = *msgType
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

// regrRow holds the in-DB regression of a meter's per-minute delta vs aggregate
// monitored power.
type regrRow struct {
	r, slope, intercept, r2, p10 *float64
}

// CorrelationVsReference ranks meters against the TOTAL monitored power (sum of
// the configured set). For each meter it computes, in-DB: Pearson r and the
// linear regression of per-minute delta vs aggregate power — which yields the
// unit multiplier (slope), the unmonitored baseline (intercept), and a floor
// check. Returns rows + the monitored floor (W). Floor is a soft down-rank.
func (d *DB) CorrelationVsReference(ctx context.Context, entities []string, start, end time.Time) ([]model.CorrRow, float64, error) {
	base, err := d.Correlation(ctx, start, end)
	if err != nil {
		return nil, 0, err
	}
	floor := d.MonitoredFloor(ctx, entities, start, end)
	if len(entities) == 0 {
		return base, 0, nil
	}

	reg := map[int64]regrRow{}
	rows, err := d.pool.Query(ctx, `
WITH per_entity AS (
  SELECT time_bucket_gapfill('1 minute', ts) AS b, entity_id, locf(avg(power_w)) AS w
  FROM reference_samples
  WHERE entity_id = ANY($3) AND ts >= $1 AND ts <= $2
  GROUP BY b, entity_id),
ref AS (SELECT b, sum(coalesce(w,0)) AS power FROM per_entity GROUP BY b),
m AS (
  SELECT endpoint_id, time_bucket('1 minute', ts) AS b,
         max(consumption)-min(consumption) AS delta
  FROM readings
  WHERE ts >= $1 AND ts <= $2 AND consumption IS NOT NULL AND endpoint_id IS NOT NULL
  GROUP BY endpoint_id, b)
SELECT m.endpoint_id,
       corr(m.delta, ref.power)            AS r,
       regr_slope(m.delta, ref.power)      AS slope,
       regr_intercept(m.delta, ref.power)  AS intercept,
       regr_r2(m.delta, ref.power)         AS r2,
       percentile_cont(0.1) WITHIN GROUP (ORDER BY m.delta) AS p10
FROM m JOIN ref USING (b)
GROUP BY m.endpoint_id
HAVING count(*) >= 5`, start, end, entities)
	if err == nil {
		for rows.Next() {
			var id int64
			var rr regrRow
			if err := rows.Scan(&id, &rr.r, &rr.slope, &rr.intercept, &rr.r2, &rr.p10); err == nil {
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
		// calibration only makes sense for a positive relationship
		if rr.slope != nil && *rr.slope > 0 {
			slope := *rr.slope
			s := round(slope, 6)
			base[i].Slope = &s
			// M = Wh per meter-unit = 1/(60·slope); suggested multiplier is kWh/unit
			mult := round(1.0/(60000.0*slope), 8)
			base[i].SuggestedMultiplier = &mult
			if rr.intercept != nil {
				bw := round(*rr.intercept/slope, 1) // unmonitored baseline (W)
				base[i].BaselineW = &bw
			}
			// floor check: meter's calibrated low-rate power ≥ monitored floor
			if rr.p10 != nil && floor > 0 {
				minW := *rr.p10 / slope // (units/min)/(units/min per W) = W
				ok := minW >= 0.8*floor
				base[i].FloorOK = &ok
			}
		}
	}

	rankScore := func(c model.CorrRow) float64 {
		s := -1.0
		if c.R != nil {
			s = *c.R
		}
		if c.FloorOK != nil && !*c.FloorOK {
			s -= 0.3 // soft penalty, stays visible
		}
		return s
	}
	sort.SliceStable(base, func(i, j int) bool {
		si, sj := rankScore(base[i]), rankScore(base[j])
		if si != sj {
			return si > sj
		}
		return base[i].Score > base[j].Score
	})
	return base, floor, nil
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
}

// CombinedRanking aggregates the rate-ratio correlation across all closed
// windows (the meter that wins every test is almost certainly yours).
func (d *DB) CombinedRanking(ctx context.Context) (map[string]any, error) {
	return d.aggregateWindows(ctx, false, nil)
}

// IdentifyAuto aggregates reference correlation across recent closed auto windows.
func (d *DB) IdentifyAuto(ctx context.Context, entities []string) (map[string]any, error) {
	return d.aggregateWindows(ctx, true, entities)
}

func (d *DB) aggregateWindows(ctx context.Context, useRef bool, entities []string) (map[string]any, error) {
	src := ""
	if useRef {
		src = "auto"
	}
	wins, err := d.closedWindows(ctx, src)
	if err != nil {
		return nil, err
	}
	type agg struct {
		commodity string
		scores    []float64
		rs        []float64
		wins      int
		present   int
	}
	per := map[int64]*agg{}
	used := []map[string]any{}
	for _, w := range wins {
		var ranked []model.CorrRow
		if useRef {
			ranked, _, err = d.CorrelationVsReference(ctx, entities, w.start, w.end)
		} else {
			ranked, err = d.Correlation(ctx, w.start, w.end)
		}
		if err != nil || len(ranked) == 0 {
			continue
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
		ranking = append(ranking, row)
	}
	sort.SliceStable(ranking, func(i, j int) bool {
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
