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
