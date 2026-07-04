package db

import (
	"context"
	"fmt"
	"time"

	"winnow/internal/config"
)

// hourlyDeltaCTEs emits the shared "hourly counter maxima → lag → rollover-aware
// delta → glitch filter" CTE ladder, reading the readings_1h continuous aggregate
// so callers never touch compressed raw chunks. where filters readings_1h under
// alias r (its params must already be numbered by the caller). Callers read from
// glitch_clean (endpoint_id, hb, n, delta).
//
// Glitch rule: a meter's own median positive delta bounds what one hour may add
// (50×, floored at 1000 counts) — but that self-median is only trustworthy once
// the meter has ≥3 positive deltas in the window. Below that, only the absolute
// floor applies: a lone unvalidated jump (the 2-packet meter whose single delta
// IS its median) can't smuggle in millions of counts, while genuine small usage
// still shows.
func hourlyDeltaCTEs(where string) string {
	return `
hb AS (
  SELECT r.endpoint_id, r.bucket AS hb, r.max_c AS cmax, r.n,
         CASE WHEN mi.msg_type = 'SCM' THEN 16777216.0 ELSE 4294967296.0 END AS modulus
  FROM readings_1h r JOIN meter_index mi USING (endpoint_id)
  WHERE ` + where + `),
stepped AS (
  SELECT endpoint_id, hb, n, modulus,
         cmax - lag(cmax) OVER (PARTITION BY endpoint_id ORDER BY hb) AS raw
  FROM hb),
m AS (
  SELECT endpoint_id, hb, n, ` + rolloverDeltaSQL("raw", "modulus") + ` AS delta
  FROM stepped),
med AS (
  SELECT endpoint_id,
         percentile_cont(0.5) WITHIN GROUP (ORDER BY delta) AS md,
         count(*) FILTER (WHERE delta > 0) AS pos_n
  FROM m WHERE delta > 0 GROUP BY endpoint_id),
glitch_clean AS (
  SELECT s.endpoint_id, s.hb, s.n, s.delta
  FROM m s LEFT JOIN med USING (endpoint_id)
  WHERE s.delta IS NOT NULL
    AND s.delta <= CASE WHEN coalesce(med.pos_n, 0) >= 3
                        THEN greatest(med.md * 50, 1000)
                        ELSE 1000 END)`
}

// ConsumptionBucket is one slot of a consumption page. Value nil = no data (a
// gap, never rendered as 0).
type ConsumptionBucket struct {
	Start      string   `json:"start"` // bucket start, RFC3339 UTC
	Label      string   `json:"label"` // display label in the user's timezone
	Value      *float64 `json:"value"`
	Packets    int64    `json:"packets"`
	Monitored  *float64 `json:"monitored,omitempty"`
	UtilityEst *float64 `json:"utility_est,omitempty"`
}

// ConsumptionResult is one page of a meter's consumption at a calendar period:
// the hours of a day, days of a week/month, or months of a year, in the user's
// (HA's) timezone, with server-computed prev/next cursors so the client cycles
// periods without any calendar math.
type ConsumptionResult struct {
	EndpointID  int64               `json:"endpoint_id"`
	View        string              `json:"view"` // day|week|month|year
	TZ          string              `json:"tz"`
	Anchor      string              `json:"anchor"`       // this page's period start (local date)
	PeriodStart string              `json:"period_start"` // RFC3339 UTC
	PeriodEnd   string              `json:"period_end"`   // RFC3339 UTC (exclusive)
	PrevAnchor  *string             `json:"prev_anchor"`  // nil when paging before first data
	NextAnchor  *string             `json:"next_anchor"`  // nil at the current period
	Granularity string              `json:"granularity"`  // hour|day|month
	Unit        string              `json:"unit"`         // pub_unit when calibrated, else "counts"
	Multiplier  float64             `json:"multiplier"`
	Calibrated  bool                `json:"calibrated"`
	Buckets     []ConsumptionBucket `json:"buckets"`
	Total       *float64            `json:"total"`
	AvgPerDay   *float64            `json:"avg_per_day"`
	PrevTotal   *float64            `json:"prev_total"`
	Coverage    float64             `json:"coverage"` // fraction of slots with data
}

// periodStartAt truncates t to the start of its view-period in loc. Weeks start
// Sunday (matches the heatmap's day-of-week rendering and US billing cycles).
func periodStartAt(view string, t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	switch view {
	case "day":
		return time.Date(y, m, d, 0, 0, 0, 0, loc)
	case "week":
		day := time.Date(y, m, d, 0, 0, 0, 0, loc)
		return day.AddDate(0, 0, -int(day.Weekday()))
	case "month":
		return time.Date(y, m, 1, 0, 0, 0, 0, loc)
	default: // year
		return time.Date(y, 1, 1, 0, 0, 0, 0, loc)
	}
}

// periodNext advances one period. AddDate does wall-clock arithmetic in the
// start's location, so DST days (23h/25h) and variable month lengths are right
// by construction.
func periodNext(view string, start time.Time) time.Time {
	switch view {
	case "day":
		return start.AddDate(0, 0, 1)
	case "week":
		return start.AddDate(0, 0, 7)
	case "month":
		return start.AddDate(0, 1, 0)
	default:
		return start.AddDate(1, 0, 0)
	}
}

func periodPrev(view string, start time.Time) time.Time {
	switch view {
	case "day":
		return start.AddDate(0, 0, -1)
	case "week":
		return start.AddDate(0, 0, -7)
	case "month":
		return start.AddDate(0, -1, 0)
	default:
		return start.AddDate(-1, 0, 0)
	}
}

type consumptionSlot struct {
	key   string // join key matching the SQL group expression
	start time.Time
	label string
}

// consumptionSlots enumerates every slot of the period so missing data renders
// as an explicit gap. Day view steps UTC hours (aligned to the cagg's buckets —
// a DST day naturally yields 23 or 25 slots); week/month step local midnights;
// year steps local month starts.
func consumptionSlots(view string, start, end time.Time, loc *time.Location) []consumptionSlot {
	var out []consumptionSlot
	switch view {
	case "day":
		for t := start.UTC().Truncate(time.Hour); t.Before(end); t = t.Add(time.Hour) {
			out = append(out, consumptionSlot{t.Format(time.RFC3339), t, t.In(loc).Format("15:04")})
		}
	case "week", "month":
		layout := "Mon 2"
		if view == "month" {
			layout = "Jan 2"
		}
		for t := start; t.Before(end); t = t.AddDate(0, 0, 1) {
			out = append(out, consumptionSlot{t.In(loc).Format("2006-01-02"), t, t.In(loc).Format(layout)})
		}
	default: // year
		for t := start; t.Before(end); t = t.AddDate(0, 1, 0) {
			out = append(out, consumptionSlot{t.In(loc).Format("2006-01"), t, t.In(loc).Format("Jan")})
		}
	}
	return out
}

// consumptionGroupExpr is the SQL group key matching consumptionSlots' keys.
// $5 is the IANA timezone.
func consumptionGroupExpr(view string) (expr, granularity string) {
	switch view {
	case "day":
		return `to_char(hb AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`, "hour"
	case "week", "month":
		return `to_char(hb AT TIME ZONE $5, 'YYYY-MM-DD')`, "day"
	default:
		return `to_char(hb AT TIME ZONE $5, 'YYYY-MM')`, "month"
	}
}

// lookback gives lag() the prior counter maximum so the period's first bucket
// has a real delta instead of NULL (meters can go quiet for a while; 48h covers
// every realistic gap without dragging in much extra data).
const consumptionLookback = 48 * time.Hour

// Consumption returns one period page of a meter's glitch-filtered, rollover-
// aware energy use. anchor is a local date ("2006-01-02", empty = today);
// compare may include "monitored" and "utility".
func (d *DB) Consumption(ctx context.Context, id int64, view, anchor string, compare map[string]bool) (*ConsumptionResult, error) {
	switch view {
	case "day", "week", "month", "year":
	default:
		return nil, fmt.Errorf("bad view %q", view)
	}
	cfg, err := d.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	tz := cfg.HATimeZone
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc, tz = time.UTC, "UTC"
	}

	now := time.Now()
	at := now
	if anchor != "" {
		t, perr := time.ParseInLocation("2006-01-02", anchor, loc)
		if perr != nil {
			return nil, fmt.Errorf("bad anchor %q", anchor)
		}
		at = t
	}
	start := periodStartAt(view, at, loc)
	end := periodNext(view, start)

	res := &ConsumptionResult{
		EndpointID:  id,
		View:        view,
		TZ:          tz,
		Anchor:      start.In(loc).Format("2006-01-02"),
		PeriodStart: start.UTC().Format(time.RFC3339),
		PeriodEnd:   end.UTC().Format(time.RFC3339),
		Unit:        "counts",
		Multiplier:  1,
	}
	if !end.After(now) { // the next period has begun → navigable
		na := end.In(loc).Format("2006-01-02")
		res.NextAnchor = &na
	}
	// prev exists unless this page already starts at/before the meter's first data
	var firstSeen *time.Time
	_ = d.pool.QueryRow(ctx, `SELECT first_seen FROM meter_index WHERE endpoint_id=$1`, id).Scan(&firstSeen)
	if firstSeen == nil || start.After(*firstSeen) {
		pa := periodPrev(view, start).In(loc).Format("2006-01-02")
		res.PrevAnchor = &pa
	}

	m, _ := d.GetMeter(ctx, id)
	if m.PubUnit != nil && *m.PubUnit != "" {
		res.Calibrated = true
		res.Unit = *m.PubUnit
		res.Multiplier = m.PubMultiplier
	}

	groupExpr, gran := consumptionGroupExpr(view)
	res.Granularity = gran

	q := `WITH ` + hourlyDeltaCTEs("r.endpoint_id = $1 AND r.bucket >= $2 AND r.bucket < $3") + `
SELECT ` + groupExpr + ` AS g, sum(delta) AS units, sum(n) AS packets
FROM glitch_clean
WHERE hb >= $4
GROUP BY g ORDER BY g`
	args := []any{id, start.Add(-consumptionLookback), end, start}
	if view != "day" { // day view groups by UTC hour and takes no $5
		args = append(args, tz)
	}
	rows, err := d.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	type cell struct {
		units   float64
		packets int64
	}
	got := map[string]cell{}
	for rows.Next() {
		var g string
		var units *float64
		var packets int64
		if err := rows.Scan(&g, &units, &packets); err != nil {
			rows.Close()
			return nil, err
		}
		if units != nil {
			got[g] = cell{*units, packets}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	mon, utl := d.consumptionOverlays(ctx, cfg, view, tz, start, end, compare)

	slots := consumptionSlots(view, start, end, loc)
	res.Buckets = make([]ConsumptionBucket, 0, len(slots))
	var total float64
	haveData := false
	covered := 0
	for _, s := range slots {
		b := ConsumptionBucket{Start: s.start.UTC().Format(time.RFC3339), Label: s.label}
		if c, ok := got[s.key]; ok {
			v := round(c.units*res.Multiplier, 3)
			b.Value = &v
			b.Packets = c.packets
			total += v
			haveData = true
			covered++
		}
		if v, ok := mon[s.key]; ok {
			mv := round(v, 3)
			b.Monitored = &mv
		}
		if v, ok := utl[s.key]; ok {
			uv := round(v, 3)
			b.UtilityEst = &uv
		}
		res.Buckets = append(res.Buckets, b)
	}
	if len(slots) > 0 {
		res.Coverage = round(float64(covered)/float64(len(slots)), 3)
	}
	if haveData {
		t := round(total, 3)
		res.Total = &t
		// average per elapsed day (a partial current period divides by time so
		// far, not the full period length)
		elapsedEnd := end
		if now.Before(end) {
			elapsedEnd = now
		}
		if !elapsedEnd.After(start) { // future-dated page: use the full period
			elapsedEnd = end
		}
		days := elapsedEnd.Sub(start).Hours() / 24
		if days < 1.0/24 {
			days = 1.0 / 24
		}
		avg := round(total/days, 3)
		res.AvgPerDay = &avg
	}

	// previous period total, for the "vs previous" tile
	prevStart := periodPrev(view, start)
	pq := `WITH ` + hourlyDeltaCTEs("r.endpoint_id = $1 AND r.bucket >= $2 AND r.bucket < $3") + `
SELECT sum(delta) FROM glitch_clean WHERE hb >= $4`
	var prevUnits *float64
	_ = d.pool.QueryRow(ctx, pq, id, prevStart.Add(-consumptionLookback), start, prevStart).Scan(&prevUnits)
	if prevUnits != nil {
		pv := round(*prevUnits*res.Multiplier, 3)
		res.PrevTotal = &pv
	}
	return res, nil
}

// consumptionOverlays fetches the optional comparison series keyed like the
// page's slots: monitored kWh (per hour for day view, per local day otherwise,
// summed per month for year view) and the utility bill's flat daily estimate
// (per day; real bill months for year view). Day view has no utility overlay —
// bills carry no intra-day shape.
func (d *DB) consumptionOverlays(ctx context.Context, cfg config.Config, view, tz string, start, end time.Time, compare map[string]bool) (mon, utl map[string]float64) {
	mon, utl = map[string]float64{}, map[string]float64{}
	if compare["monitored"] && len(cfg.MonitoredEntities) > 0 {
		switch view {
		case "day":
			mon = d.monitoredHourlyKwh(ctx, cfg.MonitoredEntities, start, end)
		case "week", "month":
			mon = d.monitoredDailyKwh(ctx, cfg.MonitoredEntities, tz, start, end)
		default: // year: fold days into months
			for day, kwh := range d.monitoredDailyKwh(ctx, cfg.MonitoredEntities, tz, start, end) {
				if len(day) >= 7 {
					mon[day[:7]] += kwh
				}
			}
		}
	}
	if compare["utility"] && cfg.UtilityConfigured() {
		switch view {
		case "week", "month":
			from := start.In(mustLoc(tz)).Format("2006-01-02")
			to := end.In(mustLoc(tz)).AddDate(0, 0, -1).Format("2006-01-02")
			for _, e := range d.UtilityDailyEstimateRange(ctx, from, to) {
				utl[e.Day] = e.FlatKwh
			}
		case "year":
			utl = d.utilityMonthlyKwh(ctx, cfg.UtilityStatisticID, kwhFactor(cfg.UtilityUnit), tz)
		}
	}
	return mon, utl
}

func mustLoc(tz string) *time.Location {
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.UTC
}

// monitoredHourlyKwh integrates the monitored entities' summed power per UTC
// hour over [lo,hi) — the day-view counterpart to monitoredDailyKwh, clamped to
// the real sample span the same way. Keyed by RFC3339 UTC hour.
func (d *DB) monitoredHourlyKwh(ctx context.Context, entities []string, lo, hi time.Time) map[string]float64 {
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
WITH per_entity AS (
  SELECT time_bucket_gapfill('1 minute', ts) AS mt, entity_id, locf(avg(power_w)) AS w
  FROM reference_samples
  WHERE entity_id = ANY($1) AND ts >= $2 AND ts <= $3
  GROUP BY mt, entity_id),
per_min AS (SELECT mt, sum(coalesce(w,0)) AS w FROM per_entity GROUP BY mt)
SELECT time_bucket('1 hour', mt) AS h, sum(w)/60.0/1000.0 AS kwh
FROM per_min GROUP BY h ORDER BY h`, entities, start, end)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var h time.Time
		var kwh *float64
		if err := rows.Scan(&h, &kwh); err != nil {
			return out
		}
		out[h.UTC().Format(time.RFC3339)] = deref(kwh)
	}
	return out
}

// utilityMonthlyKwh returns the real billed kWh per local calendar month
// ("2006-01") — the year view's utility overlay.
func (d *DB) utilityMonthlyKwh(ctx context.Context, statID string, factor float64, tz string) map[string]float64 {
	out := map[string]float64{}
	rows, err := d.pool.Query(ctx, `
SELECT to_char(ts AT TIME ZONE $2, 'YYYY-MM') AS mon, sum(kwh)
FROM utility_energy WHERE statistic_id = $1 AND period = 'month'
GROUP BY mon`, statID, tz)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var mon string
		var kwh *float64
		if err := rows.Scan(&mon, &kwh); err != nil {
			return out
		}
		out[mon] = deref(kwh) * factor
	}
	return out
}

// RefreshReadings1h materializes readings_1h over [from,to) — the worker's
// one-time historical backfill after the cagg first ships. CALL cannot run in a
// transaction, so this must be a zero-argument simple-protocol Exec; the
// timestamps are interpolated as quoted literals (internal values only).
func (d *DB) RefreshReadings1h(ctx context.Context, from, to time.Time) error {
	sql := fmt.Sprintf(`CALL refresh_continuous_aggregate('readings_1h', '%s', '%s')`,
		from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	_, err := d.pool.Exec(ctx, sql)
	return err
}

// OldestReadingTS is the start of stored history (false when no readings yet).
func (d *DB) OldestReadingTS(ctx context.Context) (time.Time, bool) {
	var ts *time.Time
	_ = d.pool.QueryRow(ctx, `SELECT min(ts) FROM readings`).Scan(&ts)
	if ts == nil {
		return time.Time{}, false
	}
	return *ts, true
}
