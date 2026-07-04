package db

import (
	"context"
	"testing"
	"time"

	"winnow/internal/config"
	"winnow/internal/model"
)

// --- pure calendar logic (no DB) ---

func TestPeriodCalendar(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("tz: %v", err)
	}

	// weeks start Sunday, whatever the anchor weekday
	wed := time.Date(2026, 7, 1, 15, 0, 0, 0, ny) // a Wednesday
	ws := periodStartAt("week", wed, ny)
	if ws.Weekday() != time.Sunday {
		t.Fatalf("week start = %s, want Sunday", ws.Weekday())
	}
	if got := ws.Format("2006-01-02"); got != "2026-06-28" {
		t.Fatalf("week start = %s, want 2026-06-28", got)
	}
	if n := periodNext("week", ws); n.Sub(ws) != 7*24*time.Hour {
		t.Fatalf("plain week length = %v", n.Sub(ws))
	}

	// DST spring-forward day (2026-03-08) has 23 hour slots; fall-back
	// (2026-11-01) has 25. The slot enumerator steps UTC hours between local
	// midnights, so the counts fall out of the calendar itself.
	spring := periodStartAt("day", time.Date(2026, 3, 8, 12, 0, 0, 0, ny), ny)
	if n := len(consumptionSlots("day", spring, periodNext("day", spring), ny)); n != 23 {
		t.Fatalf("spring-forward day slots = %d, want 23", n)
	}
	fall := periodStartAt("day", time.Date(2026, 11, 1, 12, 0, 0, 0, ny), ny)
	if n := len(consumptionSlots("day", fall, periodNext("day", fall), ny)); n != 25 {
		t.Fatalf("fall-back day slots = %d, want 25", n)
	}

	// month pages have the right number of day slots (Feb 2026 = 28)
	feb := periodStartAt("month", time.Date(2026, 2, 10, 0, 0, 0, 0, ny), ny)
	if n := len(consumptionSlots("month", feb, periodNext("month", feb), ny)); n != 28 {
		t.Fatalf("feb slots = %d, want 28", n)
	}
	// year pages have 12 month slots
	yr := periodStartAt("year", time.Date(2026, 7, 4, 0, 0, 0, 0, ny), ny)
	if n := len(consumptionSlots("year", yr, periodNext("year", yr), ny)); n != 12 {
		t.Fatalf("year slots = %d, want 12", n)
	}

	// prev/next are inverses across a DST boundary
	m := periodStartAt("month", time.Date(2026, 3, 15, 0, 0, 0, 0, ny), ny)
	if got := periodPrev("month", periodNext("month", m)); !got.Equal(m) {
		t.Fatalf("prev(next(month)) = %v, want %v", got, m)
	}
}

// --- DB integration ---

func addAt(t *testing.T, d *DB, id int64, ts time.Time, consumption float64) {
	t.Helper()
	e := 4
	r := model.Reading{TS: ts, MsgType: "SCM", EndpointID: id, EndpointType: &e, Consumption: &consumption, Source: "t"}
	if err := d.InsertReading(context.Background(), r, "{}"); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

// consumptionDay0 returns a fully-in-the-past local midnight (2 days before the
// shared fixture base) to anchor period pages on.
func consumptionDay0(loc *time.Location) time.Time {
	y, m, dd := base.In(loc).Date()
	return time.Date(y, m, dd, 0, 0, 0, 0, loc).AddDate(0, 0, -2)
}

func TestConsumptionDayPage(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	if err := d.SetSetting(ctx, config.KeyHATimeZone, "America/New_York"); err != nil {
		t.Fatalf("set tz: %v", err)
	}
	loc, _ := time.LoadLocation("America/New_York")
	day0 := consumptionDay0(loc)

	// hourly counter +2/hour from 6h before midnight (the lookback must supply
	// the prior maximum so the page's first hour has a real delta)
	for h := -6; h <= 30; h++ {
		addAt(t, d, 7001, day0.Add(time.Duration(h)*time.Hour), 1000+2*float64(h+6))
	}

	res, err := d.Consumption(ctx, 7001, "day", day0.Format("2006-01-02"), nil)
	if err != nil {
		t.Fatalf("consumption: %v", err)
	}
	if len(res.Buckets) != 24 {
		t.Fatalf("day buckets = %d, want 24", len(res.Buckets))
	}
	if res.Granularity != "hour" || res.Unit != "counts" || res.Calibrated {
		t.Fatalf("meta wrong: %+v", res)
	}
	for i, b := range res.Buckets {
		if b.Value == nil || *b.Value != 2 {
			t.Fatalf("bucket %d = %v, want 2 (first bucket exercises the 48h lookback)", i, b.Value)
		}
	}
	if res.Total == nil || *res.Total != 48 {
		t.Fatalf("total = %v, want 48", res.Total)
	}
	if res.Coverage != 1 {
		t.Fatalf("coverage = %v, want 1", res.Coverage)
	}
	if res.NextAnchor == nil || *res.NextAnchor != day0.AddDate(0, 0, 1).Format("2006-01-02") {
		t.Fatalf("next anchor = %v", res.NextAnchor)
	}
	if res.PrevAnchor == nil || *res.PrevAnchor != day0.AddDate(0, 0, -1).Format("2006-01-02") {
		t.Fatalf("prev anchor = %v", res.PrevAnchor)
	}

	// current period → next is nil
	cur, err := d.Consumption(ctx, 7001, "day", "", nil)
	if err != nil {
		t.Fatalf("current day: %v", err)
	}
	if cur.NextAnchor != nil {
		t.Fatalf("current period next = %v, want nil", *cur.NextAnchor)
	}

	// paging to before the meter's first data → prev is nil
	old, err := d.Consumption(ctx, 7001, "day", day0.AddDate(0, 0, -40).Format("2006-01-02"), nil)
	if err != nil {
		t.Fatalf("old day: %v", err)
	}
	if old.PrevAnchor != nil {
		t.Fatalf("pre-history prev = %v, want nil", *old.PrevAnchor)
	}

	// calibration: multiplier + unit flow through values and totals
	if _, err := d.UpdateMeter(ctx, 7001, MeterUpdate{PubMultiplier: ptr(0.5), PubUnit: ptr("kWh")}); err != nil {
		t.Fatalf("update meter: %v", err)
	}
	cal, err := d.Consumption(ctx, 7001, "day", day0.Format("2006-01-02"), nil)
	if err != nil {
		t.Fatalf("calibrated: %v", err)
	}
	if !cal.Calibrated || cal.Unit != "kWh" || cal.Total == nil || *cal.Total != 24 {
		t.Fatalf("calibrated page wrong: calibrated=%v unit=%s total=%v", cal.Calibrated, cal.Unit, cal.Total)
	}
}

func TestConsumptionWeekCursors(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	if err := d.SetSetting(ctx, config.KeyHATimeZone, "America/New_York"); err != nil {
		t.Fatalf("set tz: %v", err)
	}
	loc, _ := time.LoadLocation("America/New_York")
	day0 := consumptionDay0(loc)

	for h := -6; h <= 30; h++ {
		addAt(t, d, 7002, day0.Add(time.Duration(h)*time.Hour), 5000+3*float64(h+6))
	}

	wk, err := d.Consumption(ctx, 7002, "week", day0.Format("2006-01-02"), nil)
	if err != nil {
		t.Fatalf("week: %v", err)
	}
	if len(wk.Buckets) != 7 {
		t.Fatalf("week buckets = %d, want 7", len(wk.Buckets))
	}
	if ws, _ := time.ParseInLocation("2006-01-02", wk.Anchor, loc); ws.Weekday() != time.Sunday {
		t.Fatalf("week anchor %s is %s, want Sunday", wk.Anchor, ws.Weekday())
	}
	// round-trip: the page one week back must point its next cursor here
	// (its own prev may rightly be nil — there is no data before it)
	ws, _ := time.ParseInLocation("2006-01-02", wk.Anchor, loc)
	prev, err := d.Consumption(ctx, 7002, "week", ws.AddDate(0, 0, -7).Format("2006-01-02"), nil)
	if err != nil {
		t.Fatalf("prev week: %v", err)
	}
	if prev.NextAnchor == nil || *prev.NextAnchor != wk.Anchor {
		t.Fatalf("cursor round-trip broken: next(prev)=%v want %s", prev.NextAnchor, wk.Anchor)
	}
}

func TestConsumptionGlitchAndRollover(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	if err := d.SetSetting(ctx, config.KeyHATimeZone, "UTC"); err != nil {
		t.Fatalf("set tz: %v", err)
	}
	day0 := consumptionDay0(time.UTC)

	// meter 7003: steady +2/hour, but hour 12 contains a +2^17 bit-flip decode
	for h := -6; h <= 30; h++ {
		c := 1000 + 2*float64(h+6)
		if h == 12 {
			c += 131072
		}
		addAt(t, d, 7003, day0.Add(time.Duration(h)*time.Hour), c)
	}
	res, err := d.Consumption(ctx, 7003, "day", day0.Format("2006-01-02"), nil)
	if err != nil {
		t.Fatalf("glitch day: %v", err)
	}
	// hour 12 (glitch spike) is dropped; hour 13 is the giant negative step back
	// (a reset → NULL). Every other hour keeps its honest +2.
	if b := res.Buckets[12]; b.Value != nil {
		t.Fatalf("glitch bucket kept value %v, want nil", *b.Value)
	}
	if b := res.Buckets[13]; b.Value != nil {
		t.Fatalf("post-glitch reset bucket = %v, want nil", *b.Value)
	}
	if res.Total == nil || *res.Total != 44 {
		t.Fatalf("glitch-filtered total = %v, want 44", res.Total)
	}

	// meter 7004: SCM counter wraps at 2^24 inside the page — the wrap hour
	// reports the true rise, not a reset
	vals := []float64{16777202, 16777206, 16777210, 16777214, 6, 10}
	for i, v := range vals {
		addAt(t, d, 7004, day0.Add(time.Duration(8+i)*time.Hour), v)
	}
	r2, err := d.Consumption(ctx, 7004, "day", day0.Format("2006-01-02"), nil)
	if err != nil {
		t.Fatalf("rollover day: %v", err)
	}
	if b := r2.Buckets[12]; b.Value == nil || *b.Value != 8 {
		t.Fatalf("rollover bucket = %v, want 8", b.Value)
	}

	// meter 7005: two packets, one enormous unvalidated delta — the min-evidence
	// floor (1000 counts) drops it rather than reporting 7.8M
	addAt(t, d, 7005, day0.Add(9*time.Hour), 100)
	addAt(t, d, 7005, day0.Add(10*time.Hour), 7872676)
	r3, err := d.Consumption(ctx, 7005, "day", day0.Format("2006-01-02"), nil)
	if err != nil {
		t.Fatalf("sparse day: %v", err)
	}
	if r3.Total != nil {
		t.Fatalf("sparse meter total = %v, want nil", *r3.Total)
	}
	if r3.Coverage != 0 {
		t.Fatalf("sparse coverage = %v, want 0", r3.Coverage)
	}
}

func TestConsumptionMonitoredOverlay(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	if err := d.SetSetting(ctx, config.KeyHATimeZone, "UTC"); err != nil {
		t.Fatalf("set tz: %v", err)
	}
	if err := d.SetSetting(ctx, config.KeyMonitoredEntities, `["sensor.mon"]`); err != nil {
		t.Fatalf("set entities: %v", err)
	}
	day0 := consumptionDay0(time.UTC)

	for h := -6; h <= 30; h++ {
		addAt(t, d, 7006, day0.Add(time.Duration(h)*time.Hour), 2000+2*float64(h+6))
	}
	// constant 1200 W across day0 (samples every 5 min) → 28.8 kWh that day
	for m := 0; m < 24*60; m += 5 {
		if err := d.InsertReferenceSample(ctx, "sensor.mon", day0.Add(time.Duration(m)*time.Minute), 1200); err != nil {
			t.Fatalf("ref: %v", err)
		}
	}

	wk, err := d.Consumption(ctx, 7006, "week", day0.Format("2006-01-02"), map[string]bool{"monitored": true})
	if err != nil {
		t.Fatalf("week overlay: %v", err)
	}
	found := false
	for _, b := range wk.Buckets {
		if b.Monitored != nil {
			if *b.Monitored < 27 || *b.Monitored > 30 {
				t.Fatalf("monitored day = %v, want ≈28.8", *b.Monitored)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no monitored overlay on any bucket")
	}
}
