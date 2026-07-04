package db

import (
	"context"
	"testing"
	"time"

	"winnow/internal/config"
)

// TestBoundedReferenceCarry reproduces the June 2026 incident in miniature: the
// feed sends 1 kW for an hour, then dies. Unbounded locf carried that 1 kW
// forward forever (23 days in production); the bounded carry must stop crediting
// energy ~refCarryLimit after the last real sample.
func TestBoundedReferenceCarry(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()

	// live minutes 0..60 at 1000 W, then silence through minute 300
	for m := 0; m <= 60; m += 5 {
		addRef(t, d, float64(m), 1000)
	}
	start, end := base, base.Add(300*time.Minute)

	kwh := d.MonitoredEnergy(ctx, []string{"sensor.plug"}, start, end)
	// honest: ~60 live minutes + ≤15 min carry ≈ 1.0–1.3 kWh.
	// fabricated (the bug): 300 min × 1 kW = 5 kWh.
	if kwh < 0.8 || kwh > 1.5 {
		t.Fatalf("bounded energy = %.3f kWh, want ≈1.0–1.3 (unbounded locf would give 5.0)", kwh)
	}

	// AggregateSeries: buckets past the carry bound are gaps, never 0 W or a
	// fabricated flat line
	pts, err := d.AggregateSeries(ctx, []string{"sensor.plug"}, start, end, "5m")
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	if len(pts) == 0 {
		t.Fatalf("no series points")
	}
	deadEdge := base.Add(80 * time.Minute)
	for _, p := range pts {
		ts, _ := time.Parse(time.RFC3339Nano, p.Bucket)
		if ts.After(deadEdge) {
			t.Fatalf("bucket %s (=%v W) exists after the feed died — expected a gap", p.Bucket, p.Value)
		}
		if p.Value == 0 {
			t.Fatalf("bucket %s reports 0 W — gaps must be omitted, not zero-filled", p.Bucket)
		}
	}
}

// TestReferenceCoverageGatesDailyScreen is the incident regression: dead-feed
// days must drop out of the daily physics screen as "no evidence" instead of
// poisoning the monitored average, and the true meter must still pass on the
// remaining covered days.
func TestReferenceCoverageGatesDailyScreen(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()
	if err := d.SetSetting(ctx, config.KeyHATimeZone, "UTC"); err != nil {
		t.Fatalf("set tz: %v", err)
	}

	// Anchor fixtures to a UTC midnight so "full local days" are deterministic:
	// base is now−6d truncated to the hour; day boundaries derive from it.
	day0 := time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -5)
	startMin := day0.Sub(base).Minutes() // negative offsets are fine for the helpers

	// reference: 1250 W (30 kWh/day) at keepalive cadence for days 0–2 and day 5;
	// days 3–4 the feed is DEAD (zero samples).
	for m := 0; m <= 3*24*60; m += 5 {
		addRef(t, d, startMin+float64(m), 1250)
	}
	for m := 5 * 24 * 60; m <= 6*24*60; m += 5 {
		addRef(t, d, startMin+float64(m), 1250)
	}

	// true meter 6101: 35 kWh/day (unit 1 kWh), hourly readings across ALL days —
	// its counter keeps counting through the reference outage.
	cum := 20000.0
	for h := 0; h <= 6*24; h++ {
		add(t, d, 6101, startMin+float64(h*60), cum, 4)
		cum += 35.0 / 24
	}

	screen, err := d.DailyReconciliation(ctx, []string{"sensor.plug"}, "UTC", nil)
	if err != nil {
		t.Fatalf("reconciliation: %v", err)
	}
	if screen.ExcludedDays < 2 {
		t.Fatalf("excluded_days = %d, want ≥2 (the dead-feed days)", screen.ExcludedDays)
	}
	for i, day := range screen.Days {
		kwh := screen.MonitoredKwh[i]
		if kwh < 25 || kwh > 35 {
			t.Fatalf("covered day %s has monitored %.1f kWh — fabricated or truncated data leaked in", day, kwh)
		}
	}
	if screen.Survivors < 1 {
		t.Fatalf("survivors = 0 — the true meter must still pass on covered days (rows: %+v)", screen.Rows)
	}
	found := false
	for _, r := range screen.Rows {
		if r.EndpointID == 6101 && r.Pass {
			found = true
		}
	}
	if !found {
		t.Fatalf("meter 6101 did not pass the coverage-gated screen")
	}
}

// TestReferenceHealth verifies staleness and gap detection.
func TestReferenceHealth(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()

	// unconfigured → stale but not configured
	h := d.ReferenceHealth(ctx, nil)
	if h.Configured {
		t.Fatalf("unconfigured health: %+v", h)
	}

	// samples ending ~6 days ago (base) → stale, with the trailing gap implicit
	for m := 0; m <= 120; m += 5 {
		addRef(t, d, float64(m), 900)
	}
	h = d.ReferenceHealth(ctx, []string{"sensor.plug"})
	if !h.Configured || !h.Stale || h.LastSampleTS == nil {
		t.Fatalf("expected stale health with last sample set: %+v", h)
	}

	// a fresh sample now → not stale; the 6-day hole shows up as a gap
	if err := d.InsertReferenceSample(ctx, "sensor.plug", time.Now(), 900); err != nil {
		t.Fatalf("insert: %v", err)
	}
	h = d.ReferenceHealth(ctx, []string{"sensor.plug"})
	if h.Stale {
		t.Fatalf("fresh sample should clear staleness: %+v", h)
	}
	if len(h.Gaps7d) == 0 {
		t.Fatalf("expected the multi-day hole in gaps_7d")
	}
}

// TestReferenceGapsAndBackfillReplace covers the backfill contract: gap
// detection (incl. the trailing hole) and idempotent replacement that never
// touches live rows.
func TestReferenceGapsAndBackfillReplace(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()

	// live: minutes 0–60, then dead until minute 240, live 240–300
	for m := 0; m <= 60; m += 5 {
		addRef(t, d, float64(m), 1000)
	}
	for m := 240; m <= 300; m += 5 {
		addRef(t, d, float64(m), 1100)
	}
	capEnd := time.Now()
	gaps := d.ReferenceGaps(ctx, []string{"sensor.plug"}, 30*24*time.Hour, 30*time.Minute, capEnd)
	if len(gaps) < 2 {
		t.Fatalf("gaps = %d, want ≥2 (middle hole + trailing hole to now): %v", len(gaps), gaps)
	}
	mid := gaps[0]
	if mid[1].Sub(mid[0]) < 170*time.Minute {
		t.Fatalf("middle gap too small: %v..%v", mid[0], mid[1])
	}

	// backfill the middle gap at 5-min density, twice — row count must not grow
	var ts []time.Time
	var pw []float64
	for x := mid[0].Add(5 * time.Minute); x.Before(mid[1]); x = x.Add(5 * time.Minute) {
		ts = append(ts, x)
		pw = append(pw, 950)
	}
	for i := 0; i < 2; i++ {
		if err := d.ReplaceBackfillSamples(ctx, "sensor.plug", mid[0], mid[1], ts, pw); err != nil {
			t.Fatalf("replace: %v", err)
		}
	}
	var nBack, nLive int
	_ = d.pool.QueryRow(ctx, `SELECT count(*) FROM reference_samples WHERE src='lts_backfill'`).Scan(&nBack)
	_ = d.pool.QueryRow(ctx, `SELECT count(*) FROM reference_samples WHERE src='live'`).Scan(&nLive)
	if nBack != len(ts) {
		t.Fatalf("backfill rows = %d, want %d (idempotent replace)", nBack, len(ts))
	}
	if nLive != 13+13 {
		t.Fatalf("live rows = %d, want 26 — backfill must never touch live", nLive)
	}

	// the middle gap is now healed: only the trailing hole remains
	gaps = d.ReferenceGaps(ctx, []string{"sensor.plug"}, 30*24*time.Hour, 30*time.Minute, capEnd)
	for _, g := range gaps {
		if g[0].Before(mid[1]) && g[1].After(mid[0]) && g[1] != capEnd {
			t.Fatalf("middle gap still present after backfill: %v", g)
		}
	}
}

// TestFreezeTestSnoopK: closing a window pins the snooping pool from the
// current physics-screen survivor count (shared by manual stop and the
// worker's auto close, which used to skip it).
func TestFreezeTestSnoopK(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()
	if err := d.SetSetting(ctx, config.KeyHATimeZone, "UTC"); err != nil {
		t.Fatalf("set tz: %v", err)
	}

	day0 := time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -5)
	startMin := day0.Sub(base).Minutes()
	for m := 0; m <= 6*24*60; m += 5 {
		addRef(t, d, startMin+float64(m), 1250) // 30 kWh/day
	}
	cum := 20000.0
	for h := 0; h <= 6*24; h++ {
		add(t, d, 6201, startMin+float64(h*60), cum, 4) // 35 kWh/day → survivor
		cum += 35.0 / 24
	}

	w, err := d.CreateTest(ctx, "freeze me", time.Now().Add(-10*time.Minute), nil, "auto", nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := d.StopTest(ctx, w.ID, time.Now()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	d.FreezeTestSnoopK(ctx, w.ID, []string{"sensor.plug"}, "UTC")

	var k *int
	if err := d.Pool().QueryRow(ctx, `SELECT snoop_k FROM test_windows WHERE id=$1`, w.ID).Scan(&k); err != nil {
		t.Fatalf("read snoop_k: %v", err)
	}
	if k == nil || *k < 1 {
		t.Fatalf("snoop_k not frozen: %v", k)
	}
}
