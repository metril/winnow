package db

import (
	"context"
	"testing"
	"time"
)

// addHourlyCum inserts one reading per hour for `hours` hours, the counter
// growing by perHour each hour, starting at start with value cum0.
func addHourlyCum(t *testing.T, d *DB, id int64, hours int, cum0, perHour float64, etype int) {
	t.Helper()
	cum := cum0
	for h := 0; h <= hours; h++ {
		add(t, d, id, float64(h*60)+0.25, cum, etype)
		cum += perHour
	}
}

// addHourlyRef inserts an hourly constant-power reference sample stream.
func addHourlyRef(t *testing.T, d *DB, entity string, hours int, powerW float64) {
	t.Helper()
	for h := 0; h <= hours; h++ {
		addRefEntity(t, d, entity, float64(h*60)+0.25, powerW)
	}
}

// TestGlitchFilteredMovement seeds a meter with steady consumption plus one
// bit-flipped decode (+2^17) and verifies the leaderboard movement reflects the
// real usage, not the corrupted jump (max−min would report ~131k).
func TestGlitchFilteredMovement(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()

	// 6 hours of 600 units/hour (10/min)…
	cum := 10000.0
	for m := 0; m < 360; m++ {
		add(t, d, 4001, float64(m)+0.25, cum, 4)
		cum += 10
	}
	// …plus a single corrupted reading mid-stream (counter + 2^17).
	add(t, d, 4001, 250.30, cum-1100+131072, 4)

	board, err := d.Leaderboard(ctx, LeaderboardOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var movement *float64
	for _, m := range board {
		if m.EndpointID == 4001 {
			movement = m.TotalMovement
		}
	}
	if movement == nil {
		t.Fatal("meter 4001 missing from leaderboard")
	}
	// real movement is 3600 units; the glitch would add ~131k.
	if *movement > 10000 {
		t.Fatalf("glitch jump leaked into total_movement: %v", *movement)
	}
	if *movement < 1000 {
		t.Fatalf("glitch filter ate the real movement: %v", *movement)
	}
}

// TestDailyReconciliationScreen seeds a constant ~1.25 kW monitored reference
// (30 kWh/day) plus four meters across six days and verifies the physics screen:
// the true meter (1 kWh unit, monitored + 5 kWh/day) and a 10 Wh-unit twin pass
// with the right inferred units; a meter that dips below the monitored subset
// and a magnitude-implausible one fail with reasons.
func TestDailyReconciliationScreen(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()
	const hours = 6 * 24

	addHourlyRef(t, d, "sensor.plug", hours, 1250) // 30 kWh/day
	addHourlyCum(t, d, 5001, hours, 10000, 35.0/24, 4)   // true meter: 35 kWh/day at unit 1
	addHourlyCum(t, d, 5004, hours, 90000, 3500.0/24, 4) // same usage, 10 Wh counter unit
	addHourlyCum(t, d, 5003, hours, 7000, 5.0/24, 4)     // 5/day: no unit fits the band

	// 5002 reads 32/day except one mid-span day at 12 — below the monitored 30.
	cum := 50000.0
	for h := 0; h <= hours; h++ {
		add(t, d, 5002, float64(h*60)+0.25, cum, 4)
		if h >= 72 && h < 96 { // the 4th day
			cum += 12.0 / 24
		} else {
			cum += 32.0 / 24
		}
	}

	screen, err := d.DailyReconciliation(ctx, []string{"sensor.plug"}, "UTC", []int64{5002, 5003})
	if err != nil {
		t.Fatal(err)
	}
	if len(screen.Days) < dailyMinDays {
		t.Fatalf("expected at least %d full days, got %v", dailyMinDays, screen.Days)
	}
	if screen.MonitoredAvg < 28 || screen.MonitoredAvg > 32 {
		t.Fatalf("monitored average should be ~30 kWh/day, got %v", screen.MonitoredAvg)
	}

	rows := map[int64]DailyMeterRow{}
	for _, r := range screen.Rows {
		rows[r.EndpointID] = r
	}

	true1 := rows[5001]
	if !true1.Pass {
		t.Fatalf("true meter 5001 should pass the screen: %+v", true1)
	}
	if true1.Unit == nil || *true1.Unit != 1 {
		t.Fatalf("5001 unit should infer 1 kWh/count, got %v", true1.Unit)
	}
	if true1.KwhPerDay == nil || *true1.KwhPerDay < 33 || *true1.KwhPerDay > 37 {
		t.Fatalf("5001 should read ~35 kWh/day, got %v", true1.KwhPerDay)
	}
	if true1.ResidMean == nil || *true1.ResidMean < 3 || *true1.ResidMean > 7 {
		t.Fatalf("5001 residual should be ~+5 kWh/day, got %v", true1.ResidMean)
	}

	twin := rows[5004]
	if !twin.Pass || twin.Unit == nil || *twin.Unit != 0.01 {
		t.Fatalf("10Wh-unit twin 5004 should pass with unit 0.01, got %+v", twin)
	}

	below := rows[5002]
	if below.Pass {
		t.Fatal("5002 dips below the monitored subset and must fail")
	}
	if below.Reason == "" || below.Unit == nil {
		t.Fatalf("5002 should fail the physics gate with a reason, got %+v", below)
	}

	mag := rows[5003]
	if mag.Pass || mag.Unit != nil {
		t.Fatalf("5003 (5 kWh/day) should fail on magnitude, got %+v", mag)
	}

	if screen.Survivors != 2 {
		t.Fatalf("expected exactly 2 survivors (5001, 5004), got %d", screen.Survivors)
	}

	// The physics screen must reach identification confidence: even in a window
	// where the coarse counters yield NO correlation (constant hourly deltas),
	// screen survivors rank with a physics-only confidence and violators stay nil.
	start := base.Add(24 * time.Hour)
	end := base.Add(48 * time.Hour)
	aux := AuxFromScreen(screen)
	ranked, _, err := d.CorrelationVsReferenceAux(ctx, []string{"sensor.plug"}, start, end, 60, true, aux)
	if err != nil {
		t.Fatal(err)
	}
	conf := map[int64]*float64{}
	parts := map[int64]map[string]float64{}
	for _, r := range ranked {
		conf[r.EndpointID] = r.Confidence
		parts[r.EndpointID] = r.ConfidenceParts
	}
	if conf[5001] == nil || *conf[5001] < 0.3 {
		t.Fatalf("survivor 5001 should carry a physics-only confidence, got %v", conf[5001])
	}
	if _, ok := parts[5001]["physics"]; !ok {
		t.Fatalf("5001 confidence should include the physics part: %v", parts[5001])
	}
	if conf[5003] != nil {
		t.Fatalf("magnitude-failing 5003 must not gain confidence: %v", *conf[5003])
	}
	if top := ranked[0]; top.EndpointID != 5001 && top.EndpointID != 5004 {
		t.Fatalf("a physics survivor should lead the ranking, got %d", top.EndpointID)
	}
}

// TestNilConfidenceForNoSignal guards the ranking-pathology fix: a meter whose
// window yields no computable correlation (constant counter) must keep a nil
// confidence and sort below every measured candidate — not score a neutral ~0.19
// from default component values.
func TestNilConfidenceForNoSignal(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	start, end := seed(t, d) // 1001 tracks the plug; 1002 is perfectly steady

	ranking, _, err := d.CorrelationVsReference(context.Background(), []string{"sensor.plug"}, start, end, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranking) == 0 || ranking[0].EndpointID != 1001 || ranking[0].Confidence == nil {
		t.Fatalf("tracking meter should lead with a real confidence: %+v", ranking[0])
	}
	for _, r := range ranking {
		if r.R == nil && r.Confidence != nil {
			t.Fatalf("meter %d has no correlation but a non-nil confidence %v", r.EndpointID, *r.Confidence)
		}
	}
}
