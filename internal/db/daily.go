package db

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"winnow/internal/ert"
)

// This file implements the daily-reconciliation "physics screen": a per-local-day
// energy comparison of every electric meter against the monitored reference and
// the utility bill. It is the identification signal that still works when the
// monitored load is nearly constant (correlation has no variance to grab) and
// when a meter's counter is coarse (1 kWh units make sub-hourly correlation
// quantization noise): the user's meter must read AT LEAST the monitored subset's
// energy every single day — a hard physical containment — and its magnitude must
// sit in the band the utility bill establishes. In practice this cuts hundreds of
// overheard meters down to a handful in a few days of capture.

// DailyDay is one local day of a meter's screen row.
type DailyDay struct {
	Day   string   `json:"day"`             // YYYY-MM-DD (local)
	Kwh   *float64 `json:"kwh"`             // meter energy that day at the inferred unit
	Resid *float64 `json:"resid,omitempty"` // kwh − monitored (≥ ~0 for the true meter)
}

// DailyMeterRow is one meter's daily-reconciliation result.
type DailyMeterRow struct {
	EndpointID   int64      `json:"endpoint_id"`
	MsgType      string     `json:"msg_type"`
	EndpointType *int       `json:"endpoint_type"`
	Label        *string    `json:"label,omitempty"`
	Unit         *float64   `json:"unit"`        // inferred kWh per counter unit (1 | 0.1 | 0.01)
	KwhPerDay    *float64   `json:"kwh_per_day"` // mean daily energy at that unit
	ResidMean    *float64   `json:"resid_mean"`
	ResidSD      *float64   `json:"resid_sd"`
	Pass         bool       `json:"pass"`
	Score        float64    `json:"score"`            // 0..1 physics confidence (0 when failed)
	Reason       string     `json:"reason,omitempty"` // why it failed the screen
	Days         []DailyDay `json:"days"`
	Packets      int64      `json:"packets"`
	Sources      int        `json:"sources"`
}

// DailyScreen is the full physics-screen result.
type DailyScreen struct {
	Days         []string        `json:"days"`          // full local days analyzed, ascending
	MonitoredKwh []float64       `json:"monitored_kwh"` // aligned to Days
	MonitoredAvg float64         `json:"monitored_avg"` // mean kWh/day of the monitored subset
	BillLo       *float64        `json:"bill_lo"`       // utility-bill daily band (kWh/day), nil without bill data
	BillHi       *float64        `json:"bill_hi"`
	BandLo       float64         `json:"band_lo"` // final magnitude band applied (kWh/day)
	BandHi       float64         `json:"band_hi"`
	MinDays      int             `json:"min_days"`
	Survivors    int             `json:"survivors"`
	ExcludedDays int             `json:"excluded_days"` // days dropped: reference feed didn't cover them
	CoverageMin  float64         `json:"coverage_min"`  // per-day coverage threshold applied
	Rows         []DailyMeterRow `json:"rows"`          // passers (best first), then any requested failures

	// verdicts holds a signal for EVERY meter the screen could evaluate (full
	// day coverage), not just the rows kept for display: passers carry their
	// score; below-monitored / wrong-magnitude meters carry a violation. Meters
	// without full coverage are absent (no evidence ≠ negative evidence).
	verdicts map[int64]PhysicsSignal
}

// PhysicsSignal is the screen verdict folded into identification confidence.
type PhysicsSignal struct {
	Score float64 // 0..1 residual-stability score (passers)
	Pass  bool
}

// PhysicsMap exposes the screen as a per-endpoint signal for the confidence model.
func (s *DailyScreen) PhysicsMap() map[int64]PhysicsSignal {
	if s == nil {
		return nil
	}
	return s.verdicts
}

const (
	dailyMinDays     = 3    // need at least this many full local days to screen
	dailyMaxDays     = 60   // bound the window so the screen stays cheap forever
	dailyCoverageMin = 0.90 // fraction of a day the reference feed must actually cover
)

// DailyReconciliation runs the physics screen over every (non-ignored) electric
// meter: per-local-day glitch-filtered energy vs the monitored reference, with a
// magnitude band from the utility bill. extraIDs are always included in the
// result (with their failure reason) even when they fail — the UI uses that to
// chart arbitrary meters against the monitored/bill lines.
func (d *DB) DailyReconciliation(ctx context.Context, entities []string, tz string, extraIDs []int64) (*DailyScreen, error) {
	// The screen is window-independent and only changes when a new full local
	// day materializes, yet identify/stop-test/combined all recompute it. Memo
	// the extraIDs-free form for a few minutes; the key IS the config inputs,
	// so a settings change misses the cache naturally. Cached screens are
	// shared — treat them as immutable.
	const memoTTL = 5 * time.Minute
	memoKey := ""
	if len(extraIDs) == 0 {
		memoKey = tz + "|" + strings.Join(entities, ",")
		d.dailyMemo.mu.Lock()
		if d.dailyMemo.screen != nil && d.dailyMemo.key == memoKey && time.Since(d.dailyMemo.at) < memoTTL {
			s := d.dailyMemo.screen
			d.dailyMemo.mu.Unlock()
			return s, nil
		}
		d.dailyMemo.mu.Unlock()
	}
	screenOut, err := d.dailyReconciliation(ctx, entities, tz, extraIDs)
	if err == nil && memoKey != "" && screenOut != nil {
		d.dailyMemo.mu.Lock()
		d.dailyMemo.key, d.dailyMemo.at, d.dailyMemo.screen = memoKey, time.Now(), screenOut
		d.dailyMemo.mu.Unlock()
	}
	return screenOut, err
}

func (d *DB) dailyReconciliation(ctx context.Context, entities []string, tz string, extraIDs []int64) (*DailyScreen, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc, tz = time.UTC, "UTC"
	}
	lo, hi, ok := d.dataSpan(ctx)
	if !ok {
		return &DailyScreen{Days: []string{}, MonitoredKwh: []float64{}, MinDays: dailyMinDays, Rows: []DailyMeterRow{}}, nil
	}
	if hi.Sub(lo) > dailyMaxDays*24*time.Hour {
		lo = hi.Add(-dailyMaxDays * 24 * time.Hour)
	}
	monitored := d.monitoredDailyKwh(ctx, entities, tz, lo, hi)
	coverage := d.referenceCoverage(ctx, entities, tz, lo, hi)

	// Full local days: strictly inside the capture span (both edge days are
	// partial), with monitored coverage, ascending. A day the reference feed
	// didn't genuinely cover is EXCLUDED as evidence — during the June 2026
	// outage, unbounded gap-fill fabricated 23 identical days that inflated the
	// monitored average and pushed the real meter out of the magnitude band.
	firstDay := lo.In(loc).Format("2006-01-02")
	lastDay := hi.In(loc).Format("2006-01-02")
	days := make([]string, 0, len(monitored))
	excluded := 0
	for day, kwh := range monitored {
		if day <= firstDay || day >= lastDay {
			continue
		}
		// coverage first: a fully-dead day integrates to 0 kWh and would
		// otherwise vanish without being counted in the outage tally
		if coverage[day] < dailyCoverageMin {
			excluded++
			continue
		}
		if kwh > 0 {
			days = append(days, day)
		}
	}
	sort.Strings(days)
	screen := &DailyScreen{Days: days, MinDays: dailyMinDays, Rows: []DailyMeterRow{},
		ExcludedDays: excluded, CoverageMin: dailyCoverageMin}
	screen.MonitoredKwh = make([]float64, len(days))
	monAvg := 0.0
	for i, day := range days {
		screen.MonitoredKwh[i] = round(monitored[day], 3)
		monAvg += monitored[day]
	}
	if len(days) < dailyMinDays {
		return screen, nil
	}
	monAvg /= float64(len(days))
	screen.MonitoredAvg = round(monAvg, 3)

	// Magnitude band: the meter is the whole home, so a bit under the monitored
	// subset (measurement slack) up to a few× it — tightened by what the utility
	// bill says homes like this one actually use in this calendar month.
	bandLo, bandHi := 0.9*monAvg, 3.0*monAvg
	if blo, bhi, ok := d.billDailyBand(ctx, days[len(days)-1][5:7]); ok {
		screen.BillLo, screen.BillHi = &blo, &bhi
		if l := math.Max(bandLo, 0.6*blo); l < math.Min(bandHi, 1.4*bhi) {
			bandLo, bandHi = l, math.Min(bandHi, 1.4*bhi)
		}
	}
	screen.BandLo, screen.BandHi = round(bandLo, 3), round(bandHi, 3)

	perDay, err := d.meterDailyUnits(ctx, lo, hi, tz)
	if err != nil {
		return nil, err
	}
	meta, err := d.meterScreenMeta(ctx)
	if err != nil {
		return nil, err
	}
	requested := map[int64]bool{}
	for _, id := range extraIDs {
		requested[id] = true
	}

	screen.verdicts = map[int64]PhysicsSignal{}
	for id, byDay := range perDay {
		m, known := meta[id]
		if !known {
			continue
		}
		if !requested[id] {
			if m.ignored || ert.Commodity(m.endpointType) != "electric" {
				continue
			}
		}
		row := DailyMeterRow{EndpointID: id, MsgType: m.msgType, EndpointType: m.endpointType,
			Label: m.label, Packets: m.packets, Sources: m.sources}
		d.evalDailyRow(&row, byDay, days, monitored, monAvg, bandLo, bandHi)
		// Verdict for the confidence model: full-coverage meters only. A pass
		// carries its score; a magnitude/containment failure is a violation;
		// missing coverage is no evidence either way.
		if len(row.Days) == len(days) {
			screen.verdicts[id] = PhysicsSignal{Score: row.Score, Pass: row.Pass}
		}
		if row.Pass || requested[id] {
			screen.Rows = append(screen.Rows, row)
		}
	}
	// requested ids with no readings at all still get an (empty, failed) row
	for _, id := range extraIDs {
		if _, ok := perDay[id]; ok {
			continue
		}
		m := meta[id]
		screen.Rows = append(screen.Rows, DailyMeterRow{EndpointID: id, MsgType: m.msgType,
			EndpointType: m.endpointType, Label: m.label, Packets: m.packets, Sources: m.sources,
			Reason: "no readings in the analysis window", Days: []DailyDay{}})
	}

	sort.SliceStable(screen.Rows, func(i, j int) bool {
		if screen.Rows[i].Pass != screen.Rows[j].Pass {
			return screen.Rows[i].Pass
		}
		return screen.Rows[i].Score > screen.Rows[j].Score
	})
	for _, r := range screen.Rows {
		if r.Pass {
			screen.Survivors++
		}
	}
	return screen, nil
}

// evalDailyRow infers the counter unit and applies the magnitude band and the
// per-day physics gate to one meter, filling the row in place.
func (d *DB) evalDailyRow(row *DailyMeterRow, byDay map[string]float64, days []string,
	monitored map[string]float64, monAvg, bandLo, bandHi float64) {
	// the meter must cover every full day — a meter first heard mid-span can't be screened yet
	units := make([]float64, len(days))
	for i, day := range days {
		u, ok := byDay[day]
		if !ok {
			row.Reason = fmt.Sprintf("no readings on %s", day)
			row.Days = []DailyDay{}
			return
		}
		units[i] = u
	}
	meanUnits := mean(units)

	// Unit inference: the decade multiplier that lands the meter in the magnitude
	// band, preferring the one closest to "monitored plus a modest remainder".
	var unit *float64
	best := math.Inf(1)
	for _, mult := range []float64{1, 0.1, 0.01} {
		k := meanUnits * mult
		if k < bandLo || k > bandHi {
			continue
		}
		if dist := math.Abs(math.Log(k / (1.25 * monAvg))); dist < best {
			best, unit = dist, ptr(mult)
		}
	}
	row.Days = make([]DailyDay, len(days))
	if unit == nil {
		for i, day := range days {
			u := units[i]
			row.Days[i] = DailyDay{Day: day, Kwh: ptr(round(u, 3))} // raw counts: no unit fits
		}
		row.Reason = fmt.Sprintf("magnitude: %.1f counts/day fits no unit in %.1f–%.1f kWh/day", meanUnits, bandLo, bandHi)
		return
	}
	row.Unit = unit
	row.KwhPerDay = ptr(round(meanUnits**unit, 2))

	// Physics gate: whole-home ⊇ monitored, every single day (small tolerance for
	// metering slack and day-boundary smearing).
	resids := make([]float64, len(days))
	worstDay := ""
	for i, day := range days {
		kwh := units[i] * *unit
		resid := kwh - monitored[day]
		resids[i] = resid
		row.Days[i] = DailyDay{Day: day, Kwh: ptr(round(kwh, 3)), Resid: ptr(round(resid, 3))}
		if tol := math.Max(1.5, 0.05*monitored[day]); resid < -tol && worstDay == "" {
			worstDay = day
		}
	}
	rm, rs := mean(resids), stddev(resids)
	row.ResidMean, row.ResidSD = ptr(round(rm, 2)), ptr(round(rs, 2))
	if worstDay != "" {
		row.Reason = fmt.Sprintf("reads below the monitored subset on %s — physically impossible for your meter", worstDay)
		return
	}
	row.Pass = true
	// Score: stable, modest unmonitored remainder beats a wild one.
	stability := clamp01(1 - rs/(0.15*monAvg+2))
	smallness := clamp01(1 - rm/math.Max(monAvg, 1))
	row.Score = round(clamp01(0.3+0.4*stability+0.3*smallness), 3)
}

// meterDailyUnits returns, per endpoint, the rollover-aware glitch-filtered sum
// of counter deltas per local day — one query over the hourly counter maxima.
func (d *DB) meterDailyUnits(ctx context.Context, lo, hi time.Time, tz string) (map[int64]map[string]float64, error) {
	rows, err := d.pool.Query(ctx, `
WITH `+hourlyDeltaCTEs("r.bucket >= $1 AND r.bucket <= $2")+`
SELECT endpoint_id, (hb AT TIME ZONE $3)::date AS day, sum(delta)
FROM glitch_clean GROUP BY endpoint_id, day`, lo, hi, tz)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[string]float64{}
	for rows.Next() {
		var id int64
		var day time.Time
		var units *float64
		if err := rows.Scan(&id, &day, &units); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string]float64{}
		}
		out[id][day.Format("2006-01-02")] = deref(units)
	}
	return out, rows.Err()
}

type screenMeta struct {
	msgType      string
	endpointType *int
	packets      int64
	sources      int
	ignored      bool
	label        *string
}

func (d *DB) meterScreenMeta(ctx context.Context) (map[int64]screenMeta, error) {
	rows, err := d.pool.Query(ctx, `
SELECT i.endpoint_id, coalesce(i.msg_type,''), i.endpoint_type, i.packets,
       coalesce((SELECT count(*) FROM meter_source ms WHERE ms.endpoint_id = i.endpoint_id), 0),
       coalesce(m.ignored, false), m.label
FROM meter_index i LEFT JOIN meters m USING (endpoint_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]screenMeta{}
	for rows.Next() {
		var id int64
		var m screenMeta
		if err := rows.Scan(&id, &m.msgType, &m.endpointType, &m.packets, &m.sources, &m.ignored, &m.label); err != nil {
			return nil, err
		}
		out[id] = m
	}
	return out, rows.Err()
}

// referenceCoverage returns, per local day, the fraction of minutes the
// bounded gap-fill would actually have data for (a real sample within
// refCarryLimit). With the worker's 5-minute keepalive a healthy day scores
// ~1.0; a day the feed was down scores ~0 — even though unbounded locf would
// happily have fabricated it.
func (d *DB) referenceCoverage(ctx context.Context, entities []string, tz string, lo, hi time.Time) map[string]float64 {
	out := map[string]float64{}
	if len(entities) == 0 {
		return out
	}
	rows, err := d.pool.Query(ctx, `
WITH per_min AS (
  SELECT time_bucket_gapfill('1 minute', ts) AS mt, locf(max(ts)) AS src_ts
  FROM reference_samples
  WHERE entity_id = ANY($1) AND ts >= $2 AND ts <= $3
  GROUP BY mt)
SELECT (mt AT TIME ZONE $4)::date AS day,
       count(*) FILTER (WHERE mt <= src_ts + interval '`+refCarryLimit+`') / 1440.0
FROM per_min GROUP BY day ORDER BY day`, entities, lo, hi, tz)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var day time.Time
		var frac *float64
		if err := rows.Scan(&day, &frac); err != nil {
			return out
		}
		out[day.Format("2006-01-02")] = deref(frac)
	}
	return out
}

// billDailyBand derives a kWh/day band for the given local calendar month ("06")
// from the stored monthly utility bills: the same month across all years (its
// seasonal shape), falling back to every month when that month never appears.
func (d *DB) billDailyBand(ctx context.Context, month string) (float64, float64, bool) {
	cfg, err := d.LoadConfig(ctx)
	if err != nil || !cfg.UtilityConfigured() {
		return 0, 0, false
	}
	factor := kwhFactor(cfg.UtilityUnit)
	tz := cfg.HATimeZone
	if tz == "" {
		tz = "UTC"
	}
	rows, err := d.pool.Query(ctx, `
SELECT to_char(ts AT TIME ZONE $2, 'MM') AS mon,
       kwh / greatest(extract(day FROM (date_trunc('month', ts AT TIME ZONE $2) + interval '1 month - 1 day')), 1)
FROM utility_energy
WHERE statistic_id = $1 AND period = 'month' AND kwh > 0`, cfg.UtilityStatisticID, tz)
	if err != nil {
		return 0, 0, false
	}
	defer rows.Close()
	var match, all []float64
	for rows.Next() {
		var mon string
		var rate float64
		if err := rows.Scan(&mon, &rate); err != nil {
			return 0, 0, false
		}
		rate *= factor
		all = append(all, rate)
		if mon == month {
			match = append(match, rate)
		}
	}
	use := match
	if len(use) == 0 {
		use = all
	}
	if len(use) == 0 {
		return 0, 0, false
	}
	lo, hi := use[0], use[0]
	for _, r := range use {
		lo, hi = math.Min(lo, r), math.Max(hi, r)
	}
	return round(lo, 2), round(hi, 2), true
}

func ptr[T any](v T) *T { return &v }
