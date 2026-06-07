package db

import (
	"context"
	"math"
	"testing"
	"time"

	"winnow/internal/config"
	"winnow/internal/model"
)

// addAtUTC inserts one electric meter reading at an absolute timestamp.
func addAtUTC(t *testing.T, d *DB, id int64, ts time.Time, consumption float64, etype int) {
	t.Helper()
	e := etype
	r := model.Reading{TS: ts, MsgType: "IDM", EndpointID: id, EndpointType: &e, Consumption: &consumption, Source: "0"}
	if err := d.InsertReading(context.Background(), r, "{}"); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

// seedHourly lays down one reading per hour for `hours` hours starting at `from`,
// with the counter rising by perHour each hour (so the rollover-aware hourly delta
// telescopes to perHour and the monthly total is perHour × hours-in-month).
func seedHourly(t *testing.T, d *DB, id int64, from time.Time, hours int, start, perHour float64) {
	for h := 0; h < hours; h++ {
		addAtUTC(t, d, id, from.Add(time.Duration(h)*time.Hour), start+float64(h)*perHour, 4)
	}
}

func TestUtilityEvidenceAndRanking(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()

	mar := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	apr := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	may := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	jun := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	const perHour = 10.0
	// meter 3001: continuous hourly readings across Mar+Apr+May (92 days).
	seedHourly(t, d, 3001, mar, 92*24, 1_000_000, perHour)
	// meter 3002: only the first 15 days of April → tests proration (partial bucket).
	seedHourly(t, d, 3002, apr, 15*24, 2_000_000, perHour)

	// Monthly bill = multiplier(0.05) × full-month counter delta (perHour × hours).
	statID := "sensor.eversource_energy_consumption"
	bills := []float64{
		0.05 * perHour * 31 * 24, // March (744h) = 372
		0.05 * perHour * 30 * 24, // April (720h) = 360
		0.05 * perHour * 31 * 24, // May  (744h) = 372
	}
	if err := d.UpsertUtilityEnergy(ctx, statID, "month",
		[]time.Time{mar, apr, may, jun}, []float64{bills[0], bills[1], bills[2], 0}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	ev, err := d.utilityMeterEvidence(ctx, statID, "month")
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}

	// 3001: full coverage across 3 billing buckets → stable multiplier ≈ 0.05.
	e1 := ev[3001]
	if e1 == nil || e1.multiplier == nil {
		t.Fatalf("3001 has no utility multiplier: %+v", e1)
	}
	if math.Abs(*e1.multiplier-0.05) > 0.002 {
		t.Errorf("3001 multiplier = %.5f, want ≈0.05", *e1.multiplier)
	}
	if e1.bucketsCovered != 3 {
		t.Errorf("3001 buckets covered = %d, want 3", e1.bucketsCovered)
	}
	if e1.cov == nil || *e1.cov > 0.05 {
		t.Errorf("3001 multiplier CoV = %v, want small (stable)", e1.cov)
	}

	// 3002: only half of April present. Proration scales the bill by coverage, so the
	// recovered multiplier still ≈ 0.05 rather than being dropped or doubled.
	e2 := ev[3002]
	if e2 == nil || e2.multiplier == nil {
		t.Fatalf("3002 has no utility multiplier (proration should keep it): %+v", e2)
	}
	if math.Abs(*e2.multiplier-0.05) > 0.004 {
		t.Errorf("3002 prorated multiplier = %.5f, want ≈0.05", *e2.multiplier)
	}

	// CombinedRanking with the utility statistic configured and NO test windows must
	// still surface utility-evidenced electric meters with a confidence + multiplier.
	if err := d.SetSetting(ctx, config.KeyUtilityStatisticID, statID); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	res, err := d.CombinedRanking(ctx, nil)
	if err != nil {
		t.Fatalf("combined: %v", err)
	}
	ranking, _ := res["ranking"].([]AggRow)
	var got *AggRow
	for i := range ranking {
		if ranking[i].EndpointID == 3001 {
			got = &ranking[i]
		}
	}
	if got == nil {
		t.Fatalf("3001 missing from utility-only combined ranking (%d rows)", len(ranking))
	}
	if got.Confidence == nil {
		t.Errorf("3001 should have a utility-only confidence")
	}
	if got.UtilityMultiplier == nil || math.Abs(*got.UtilityMultiplier-0.05) > 0.002 {
		t.Errorf("3001 ranking utility multiplier = %v, want ≈0.05", got.UtilityMultiplier)
	}
	if got.UtilityBuckets != 3 {
		t.Errorf("3001 ranking utility buckets = %d, want 3", got.UtilityBuckets)
	}
}

func TestUtilityCompare(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	mar := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	apr := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	may := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	const perHour = 10.0
	seedHourly(t, d, 4001, mar, 60*24, 500_000, perHour) // Mar + most of Apr
	statID := "sensor.bill_consumption"
	if err := d.UpsertUtilityEnergy(ctx, statID, "month",
		[]time.Time{mar, apr, may},
		[]float64{0.05 * perHour * 31 * 24, 0.05 * perHour * 30 * 24, 0}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting(ctx, config.KeyUtilityStatisticID, statID); err != nil {
		t.Fatal(err)
	}

	res, err := d.UtilityCompare(ctx, 4001)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if res.Period != "month" {
		t.Errorf("period = %q, want month", res.Period)
	}
	if res.UtilityMultiplier == nil || math.Abs(*res.UtilityMultiplier-0.05) > 0.003 {
		t.Errorf("multiplier = %v, want ≈0.05", res.UtilityMultiplier)
	}
	if len(res.Buckets) == 0 {
		t.Fatal("no comparison buckets")
	}
	for _, b := range res.Buckets {
		if b.MeterKwh == nil {
			t.Errorf("bucket %s missing meter kwh", b.TS)
		}
	}
	// daily estimate present for monthly bills; flat level positive, meter populated,
	// shaped nil here (no monitored sensors configured).
	if len(res.DailyEstimate) == 0 {
		t.Fatal("no daily estimate for monthly bill")
	}
	sawMeter := false
	for _, de := range res.DailyEstimate {
		if de.FlatKwh <= 0 {
			t.Errorf("day %s flat estimate not positive", de.Day)
		}
		if de.ShapedKwh != nil {
			t.Errorf("day %s shaped should be nil without monitored sensors", de.Day)
		}
		if de.MeterKwh != nil {
			sawMeter = true
		}
	}
	if !sawMeter {
		t.Error("expected at least some days with meter energy")
	}
}

func TestUtilityDailyEstimateLocalTimezone(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	// Bills start at LOCAL midnight (as Opower stores them) — Mar is EST, Apr/May EDT.
	mar := time.Date(2026, 3, 1, 0, 0, 0, 0, loc)
	apr := time.Date(2026, 4, 1, 0, 0, 0, 0, loc)
	may := time.Date(2026, 5, 1, 0, 0, 0, 0, loc)
	jun := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)

	const perHour = 10.0
	seedHourly(t, d, 7001, mar, 31*24, 3_000_000, perHour) // all of local March (spans DST 3/8)

	statID := "sensor.local_consumption"
	if err := d.UpsertUtilityEnergy(ctx, statID, "month",
		[]time.Time{mar, apr, may, jun}, []float64{372, 360, 372, 0}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting(ctx, config.KeyUtilityStatisticID, statID); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting(ctx, config.KeyHATimeZone, "America/New_York"); err != nil {
		t.Fatal(err)
	}

	res, err := d.UtilityCompare(ctx, 7001)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	// March must be exactly 31 local days starting on the 1st (DST-safe) and every
	// March day must carry metered energy.
	var marchDays []string
	meterDays := 0
	for _, de := range res.DailyEstimate {
		if len(de.Day) >= 7 && de.Day[:7] == "2026-03" {
			marchDays = append(marchDays, de.Day)
			if de.MeterKwh != nil {
				meterDays++
			}
		}
	}
	if len(marchDays) != 31 {
		t.Errorf("March local days = %d, want 31: %v", len(marchDays), marchDays)
	}
	if len(marchDays) > 0 && marchDays[0] != "2026-03-01" {
		t.Errorf("first March day = %q, want 2026-03-01", marchDays[0])
	}
	if meterDays < 30 {
		t.Errorf("March days with meter energy = %d, want ~31 (local-day aligned)", meterDays)
	}
}

func TestUtilitySeries(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	mar := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	apr := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	may := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	jun := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	const perHour = 10.0
	seedHourly(t, d, 8001, mar, 31*24, 5_000_000, perHour) // electric meter, all of March
	// Publish it with a multiplier so its recorded kWh reconciles against the bill.
	if _, err := d.pool.Exec(ctx,
		`INSERT INTO meters (endpoint_id, publish, is_mine, pub_multiplier, pub_unit) VALUES ($1,true,true,$2,'kWh')`,
		8001, 0.05); err != nil {
		t.Fatal(err)
	}

	statID := "sensor.bill_consumption"
	// Bills stored in the statistic's NATIVE unit (Wh) to exercise kWh conversion.
	if err := d.UpsertUtilityEnergy(ctx, statID, "month",
		[]time.Time{mar, apr, may, jun}, []float64{372000, 360000, 372000, 0}); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		config.KeyUtilityStatisticID: statID,
		config.KeyUtilityUnit:        "Wh",
		config.KeyCostPerKwh:         "0.20",
	} {
		if err := d.SetSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}

	res, err := d.UtilitySeries(ctx)
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	if res.Unit != "kWh" {
		t.Errorf("unit = %q, want kWh", res.Unit)
	}
	if len(res.Points) != 4 {
		t.Fatalf("points = %d, want 4", len(res.Points))
	}
	// Wh → kWh conversion: 372000 Wh = 372 kWh.
	if math.Abs(res.Points[0].Kwh-372) > 0.01 {
		t.Errorf("march kWh = %.3f, want 372 (Wh→kWh)", res.Points[0].Kwh)
	}
	if math.Abs(res.TotalKwh-1104) > 0.1 {
		t.Errorf("total kWh = %.3f, want 1104", res.TotalKwh)
	}
	// cost = kWh × 0.20.
	if res.Points[0].Cost == nil || math.Abs(*res.Points[0].Cost-74.4) > 0.01 {
		t.Errorf("march cost = %v, want 74.40", res.Points[0].Cost)
	}
	// reconciliation: March's published meter recorded ≈ bill (multiplier 0.05).
	if res.Points[0].MeterKwh == nil || math.Abs(*res.Points[0].MeterKwh-372) > 3 {
		t.Errorf("march meter_kwh = %v, want ≈372", res.Points[0].MeterKwh)
	}
	if res.Points[0].CoveragePct < 0.95 {
		t.Errorf("march coverage = %.3f, want ≈1", res.Points[0].CoveragePct)
	}
	// June bucket (no meter data, no closing lead) → no reconciliation.
	if res.Points[3].MeterKwh != nil {
		t.Errorf("june meter_kwh = %v, want nil", res.Points[3].MeterKwh)
	}
	found := false
	for _, id := range res.ReconcileMeters {
		if id == 8001 {
			found = true
		}
	}
	if !found {
		t.Errorf("reconcile_meters = %v, want to include 8001", res.ReconcileMeters)
	}

	// Daily estimate (whole-home) present for a monthly bill: flat = bill ÷ days in
	// kWh (372 kWh ÷ 31 ≈ 12), with the published meter's recorded energy overlaid on
	// March days (perHour 10 × 24 × mult 0.05 = 12 kWh/day). No monitored sensors → no
	// shaped curve.
	if len(res.DailyEstimate) == 0 {
		t.Fatal("no daily estimate for monthly series")
	}
	marchMeterDays := 0
	for _, de := range res.DailyEstimate {
		if de.ShapedKwh != nil {
			t.Errorf("day %s shaped should be nil without monitored sensors", de.Day)
		}
		if de.Day[:7] == "2026-03" {
			// flat is pure arithmetic (bill ÷ days, Wh→kWh): exactly 372/31 = 12.
			if math.Abs(de.FlatKwh-12) > 0.01 {
				t.Errorf("march day %s flat = %.3f, want ≈12 kWh (Wh→kWh)", de.Day, de.FlatKwh)
			}
			// meter overlay magnitude ≈12 kWh/day (240 counts × 0.05); allow boundary
			// days to read lower, as the existing daily tests do.
			if de.MeterKwh != nil {
				if *de.MeterKwh < 8 || *de.MeterKwh > 13 {
					t.Errorf("march day %s meter = %.3f, want ≈12 kWh", de.Day, *de.MeterKwh)
				}
				marchMeterDays++
			}
		}
	}
	if marchMeterDays < 30 {
		t.Errorf("march days with meter overlay = %d, want ~31", marchMeterDays)
	}
}

// TestUtilitySeriesNoDailyForHourly verifies the daily estimate is only produced for
// coarse (monthly) bills — finer periods are used directly with no day-spread.
func TestUtilitySeriesNoDailyForHourly(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	h0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	statID := "sensor.hourly_consumption"
	ts := []time.Time{h0, h0.Add(time.Hour), h0.Add(2 * time.Hour)}
	if err := d.UpsertUtilityEnergy(ctx, statID, "hour", ts, []float64{1.0, 1.2, 0.9}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting(ctx, config.KeyUtilityStatisticID, statID); err != nil {
		t.Fatal(err)
	}
	res, err := d.UtilitySeries(ctx)
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	if res.Period != "hour" {
		t.Errorf("period = %q, want hour", res.Period)
	}
	if len(res.DailyEstimate) != 0 {
		t.Errorf("daily estimate = %d entries, want 0 for hourly period", len(res.DailyEstimate))
	}
}

// TestUtilitySeriesShapedFromMonitored exercises the monitored profile-shaped daily
// estimate path (monitoredDailyKwh): with a monitored sensor present, each day gets a
// shaped value and the shaped curve over a fully-covered month sums to the bill.
func TestUtilitySeriesShapedFromMonitored(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	mar := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	apr := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	// Monitored sensor: one hourly power sample across all of March (constant 1000 W).
	for h := 0; h < 31*24; h++ {
		if err := d.InsertReferenceSample(ctx, "sensor.mon", mar.Add(time.Duration(h)*time.Hour), 1000); err != nil {
			t.Fatal(err)
		}
	}
	const bill = 310.0 // kWh for March
	statID := "sensor.shaped_consumption"
	if err := d.UpsertUtilityEnergy(ctx, statID, "month", []time.Time{mar, apr}, []float64{bill, 0}); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		config.KeyUtilityStatisticID: statID,
		config.KeyUtilityUnit:        "kWh",
		config.KeyMonitoredEntities:  `["sensor.mon"]`,
	} {
		if err := d.SetSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}

	res, err := d.UtilitySeries(ctx)
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	var shapedSum float64
	shapedDays := 0
	for _, de := range res.DailyEstimate {
		if de.Day[:7] != "2026-03" || de.ShapedKwh == nil {
			continue
		}
		shapedSum += *de.ShapedKwh
		shapedDays++
	}
	if shapedDays < 30 {
		t.Errorf("March days with shaped estimate = %d, want ~31", shapedDays)
	}
	// Σ shaped over a fully-monitored month equals the bill (bill × Σday/month).
	if math.Abs(shapedSum-bill) > 5 {
		t.Errorf("shaped sum over March = %.2f, want ≈%.0f (= bill)", shapedSum, bill)
	}
}

func TestUpsertUtilityEnergyIdempotent(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	statID := "sensor.x_consumption"
	mar := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	apr := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	if err := d.UpsertUtilityEnergy(ctx, statID, "month", []time.Time{mar, apr}, []float64{100, 200}); err != nil {
		t.Fatal(err)
	}
	// Re-fetch corrects a late utility revision (March 100 → 150) and must not dup.
	if err := d.UpsertUtilityEnergy(ctx, statID, "month", []time.Time{mar, apr}, []float64{150, 200}); err != nil {
		t.Fatal(err)
	}
	lo, hi, n := d.UtilityCoverage(ctx, statID, "month")
	if n != 2 {
		t.Fatalf("want 2 rows after idempotent re-upsert, got %d", n)
	}
	if !lo.Equal(mar) || !hi.Equal(apr) {
		t.Errorf("coverage span = [%v,%v], want [%v,%v]", lo, hi, mar, apr)
	}
	if e := d.UtilityEnergy(ctx, statID, mar, apr.Add(time.Hour)); math.Abs(e-350) > 0.001 {
		t.Errorf("total energy = %.3f, want 350 (revised)", e)
	}
}
