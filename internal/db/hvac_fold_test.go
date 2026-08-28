package db

import (
	"context"
	"testing"
	"time"

	"winnow/internal/config"
)

// setHVAC configures the estimated-HVAC settings for a test (string kW values,
// matching how settings are actually stored).
func setHVAC(t *testing.T, d *DB, entity, heatingKW, coolingKW string) {
	t.Helper()
	ctx := context.Background()
	if err := d.SetSetting(ctx, config.KeyHVACEntityID, entity); err != nil {
		t.Fatalf("set hvac entity: %v", err)
	}
	if err := d.SetSetting(ctx, config.KeyHVACHeatingKW, heatingKW); err != nil {
		t.Fatalf("set heating kw: %v", err)
	}
	if err := d.SetSetting(ctx, config.KeyHVACCoolingKW, coolingKW); err != nil {
		t.Fatalf("set cooling kw: %v", err)
	}
}

// TestHVACFoldBaseline: no HVAC configured at all -> MonitoredEnergy is exactly
// the monitored entity's own energy (1000 W every 5 min over 2h = 2.0 kWh).
func TestHVACFoldBaseline(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()

	for m := 0; m <= 120; m += 5 {
		addRefEntity(t, d, "sensor.a", float64(m), 1000)
	}
	start, end := base, base.Add(2*time.Hour)

	kwh := d.MonitoredEnergy(ctx, []string{"sensor.a"}, start, end)
	if kwh < 1.95 || kwh > 2.05 {
		t.Fatalf("baseline energy = %.3f kWh, want ~2.0", kwh)
	}
}

// TestHVACFoldHeatingAddsEnergy: heating for hour 1 (then idle) adds
// heating_kW x 1h on top of the monitored baseline.
func TestHVACFoldHeatingAddsEnergy(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()

	for m := 0; m <= 120; m += 5 {
		addRefEntity(t, d, "sensor.a", float64(m), 1000) // 2.0 kWh baseline over 2h
	}
	const hvacEntity = "climate.t"
	setHVAC(t, d, hvacEntity, "0.5", "3.0")
	for m := 0; m < 60; m += 5 {
		if err := d.InsertHVACSample(ctx, hvacEntity, base.Add(time.Duration(m)*time.Minute), "heating"); err != nil {
			t.Fatalf("insert heating: %v", err)
		}
	}
	for m := 60; m <= 120; m += 5 {
		if err := d.InsertHVACSample(ctx, hvacEntity, base.Add(time.Duration(m)*time.Minute), "idle"); err != nil {
			t.Fatalf("insert idle: %v", err)
		}
	}
	start, end := base, base.Add(2*time.Hour)
	kwh := d.MonitoredEnergy(ctx, []string{"sensor.a"}, start, end)
	// baseline 2.0 + heating 1h*0.5kW = 2.5
	if kwh < 2.45 || kwh > 2.55 {
		t.Fatalf("heating energy = %.3f kWh, want ~2.5", kwh)
	}
}

// TestHVACFoldCoolingIndependentAndRetroactive: cooling for hour 1 adds
// cooling_kW x 1h; retuning cooling_kW with NO re-insert changes the result
// (kW is read from settings at query time).
func TestHVACFoldCoolingIndependentAndRetroactive(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()

	for m := 0; m <= 120; m += 5 {
		addRefEntity(t, d, "sensor.a", float64(m), 1000) // 2.0 kWh baseline
	}
	const hvacEntity = "climate.t"
	setHVAC(t, d, hvacEntity, "0.5", "3.0")
	for m := 0; m < 60; m += 5 {
		if err := d.InsertHVACSample(ctx, hvacEntity, base.Add(time.Duration(m)*time.Minute), "cooling"); err != nil {
			t.Fatalf("insert cooling: %v", err)
		}
	}
	// terminate the carry exactly at the hour-2 boundary, so cooling doesn't
	// bleed into hour 2 via the bounded-carry window (covered separately below).
	for m := 60; m <= 120; m += 5 {
		if err := d.InsertHVACSample(ctx, hvacEntity, base.Add(time.Duration(m)*time.Minute), "idle"); err != nil {
			t.Fatalf("insert idle: %v", err)
		}
	}
	start, end := base, base.Add(2*time.Hour)
	kwh := d.MonitoredEnergy(ctx, []string{"sensor.a"}, start, end)
	// baseline 2.0 + cooling 1h*3.0kW = 5.0
	if kwh < 4.9 || kwh > 5.1 {
		t.Fatalf("cooling energy = %.3f kWh, want ~5.0", kwh)
	}

	if err := d.SetSetting(ctx, config.KeyHVACCoolingKW, "6"); err != nil {
		t.Fatalf("retune cooling kw: %v", err)
	}
	kwh2 := d.MonitoredEnergy(ctx, []string{"sensor.a"}, start, end)
	// baseline 2.0 + cooling 1h*6.0kW = 8.0, with no re-insert
	if kwh2 < 7.9 || kwh2 > 8.1 {
		t.Fatalf("retuned cooling energy = %.3f kWh, want ~8.0 (retroactive)", kwh2)
	}
}

// TestHVACFoldHeatingUnaffectedByCoolingChange: a heating-only run's energy
// must not move when the cooling kW setting changes.
func TestHVACFoldHeatingUnaffectedByCoolingChange(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()

	for m := 0; m <= 60; m += 5 {
		addRefEntity(t, d, "sensor.a", float64(m), 1000) // 1.0 kWh baseline over 1h
	}
	const hvacEntity = "climate.t"
	setHVAC(t, d, hvacEntity, "0.5", "3.0")
	for m := 0; m <= 60; m += 5 {
		if err := d.InsertHVACSample(ctx, hvacEntity, base.Add(time.Duration(m)*time.Minute), "heating"); err != nil {
			t.Fatalf("insert heating: %v", err)
		}
	}
	start, end := base, base.Add(time.Hour)
	before := d.MonitoredEnergy(ctx, []string{"sensor.a"}, start, end)

	if err := d.SetSetting(ctx, config.KeyHVACCoolingKW, "99"); err != nil {
		t.Fatalf("update cooling kw: %v", err)
	}
	after := d.MonitoredEnergy(ctx, []string{"sensor.a"}, start, end)
	if before != after {
		t.Fatalf("heating-only energy changed after a cooling-kW edit: %.3f -> %.3f", before, after)
	}
}

// TestHVACFoldNonActiveActionsAddNothing: idle/off/fan contribute 0 W.
func TestHVACFoldNonActiveActionsAddNothing(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()

	for m := 0; m <= 60; m += 5 {
		addRefEntity(t, d, "sensor.a", float64(m), 1000) // 1.0 kWh baseline over 1h
	}
	const hvacEntity = "climate.t"
	setHVAC(t, d, hvacEntity, "0.5", "3.0")
	actions := []string{"idle", "off", "fan"}
	i := 0
	for m := 0; m <= 60; m += 5 {
		if err := d.InsertHVACSample(ctx, hvacEntity, base.Add(time.Duration(m)*time.Minute), actions[i%len(actions)]); err != nil {
			t.Fatalf("insert %s: %v", actions[i%len(actions)], err)
		}
		i++
	}
	start, end := base, base.Add(time.Hour)
	kwh := d.MonitoredEnergy(ctx, []string{"sensor.a"}, start, end)
	if kwh < 0.95 || kwh > 1.05 {
		t.Fatalf("idle/off/fan energy = %.3f kWh, want ~1.0 (no HVAC contribution)", kwh)
	}
}

// TestHVACFoldBoundedCarry: a single 'cooling' sample with no keepalive is
// only carried refCarryLimit (15 min), not the full hour it sits within —
// the same bounded-carry guard the reference feed itself uses.
func TestHVACFoldBoundedCarry(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()

	for m := 0; m <= 120; m += 5 {
		addRefEntity(t, d, "sensor.a", float64(m), 1000) // 2.0 kWh baseline over 2h
	}
	const hvacEntity = "climate.t"
	setHVAC(t, d, hvacEntity, "0.5", "3.0")
	if err := d.InsertHVACSample(ctx, hvacEntity, base, "cooling"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	start, end := base, base.Add(2*time.Hour)
	kwh := d.MonitoredEnergy(ctx, []string{"sensor.a"}, start, end)
	// baseline 2.0 + bounded cooling 3.0kW * 15/60h = 0.75 -> ~2.75, not 2.0+3.0=5.0
	if kwh < 2.65 || kwh > 2.85 {
		t.Fatalf("bounded-carry energy = %.3f kWh, want ~2.75 (not the unbounded 5.0)", kwh)
	}
}

// TestHVACFoldNoConfigEqualsBaseline: an unconfigured hvac_entity_id, and a
// configured entity with both kW at 0, must both leave MonitoredEnergy at the
// plain monitored baseline.
func TestHVACFoldNoConfigEqualsBaseline(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()

	for m := 0; m <= 120; m += 5 {
		addRefEntity(t, d, "sensor.a", float64(m), 1000) // 2.0 kWh baseline
	}
	start, end := base, base.Add(2*time.Hour)

	// hvac_entity_id never set, even though hvac_samples exist for some entity.
	for m := 0; m < 60; m += 5 {
		if err := d.InsertHVACSample(ctx, "climate.t", base.Add(time.Duration(m)*time.Minute), "cooling"); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	kwh := d.MonitoredEnergy(ctx, []string{"sensor.a"}, start, end)
	if kwh < 1.95 || kwh > 2.05 {
		t.Fatalf("unconfigured hvac entity leaked energy: got %.3f, want ~2.0", kwh)
	}

	// entity now configured, but both kW are 0.
	setHVAC(t, d, "climate.t", "0", "0")
	kwh2 := d.MonitoredEnergy(ctx, []string{"sensor.a"}, start, end)
	if kwh2 < 1.95 || kwh2 > 2.05 {
		t.Fatalf("zero-kW hvac leaked energy: got %.3f, want ~2.0", kwh2)
	}
}

// TestHVACFoldAggregateSeriesOmitsHVACOnlyBuckets: once the monitored feed's
// own carry expires, a bucket backed ONLY by the HVAC estimate must be a gap
// (omitted) — never painted as a fabricated line over a dead monitored feed —
// while a bucket backed by BOTH must show the real HVAC contribution (not
// just the monitored sensor's own power).
func TestHVACFoldAggregateSeriesOmitsHVACOnlyBuckets(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()

	// sensor.a live only for the first 20 minutes, then silent (no keepalive).
	for m := 0; m <= 20; m += 5 {
		addRefEntity(t, d, "sensor.a", float64(m), 1000)
	}
	const hvacEntity = "climate.t"
	setHVAC(t, d, hvacEntity, "0.5", "3.0")
	// HVAC keeps reporting every 5 min for the whole hour.
	for m := 0; m <= 60; m += 5 {
		if err := d.InsertHVACSample(ctx, hvacEntity, base.Add(time.Duration(m)*time.Minute), "cooling"); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	start, end := base, base.Add(time.Hour)
	pts, err := d.AggregateSeries(ctx, []string{"sensor.a"}, start, end, "5m")
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	if len(pts) == 0 {
		t.Fatal("no series points at all — HVAC contribution never reached AggregateSeries (or the query is broken)")
	}

	// sensor.a's own carry (15 min) expires at minute 35; from minute 40 onward
	// only HVAC data remains for the bucket -> must be a gap. The bucket at
	// exactly base+40m is itself HVAC-only, so it must be excluded too
	// (!Before, not just After).
	deadEdge := base.Add(40 * time.Minute)
	var maxTS time.Time
	var firstVal *float64
	for _, p := range pts {
		ts, perr := time.Parse(time.RFC3339Nano, p.Bucket)
		if perr != nil {
			t.Fatalf("bad bucket timestamp %q: %v", p.Bucket, perr)
		}
		if !ts.Before(deadEdge) {
			t.Fatalf("bucket %s (%v W) exists with only HVAC data — expected a gap", p.Bucket, p.Value)
		}
		if ts.After(maxTS) {
			maxTS = ts
		}
		if ts.Equal(base) {
			v := p.Value
			firstVal = &v
		}
	}
	// early bucket: real 1000 W sensor + estimated 3000 W cooling (0.5/3.0 kW
	// heating/cooling) must both land here — proves the HVAC estimate actually
	// reaches AggregateSeries, not just that dead buckets are gapped.
	if firstVal == nil {
		t.Fatal("no point at the first bucket (minute 0)")
	}
	if *firstVal < 3900 || *firstVal > 4100 {
		t.Fatalf("first bucket = %v W, want ~4000 (1000 W sensor + 3000 W HVAC)", *firstVal)
	}
	// last surviving bucket is the one covering the carry's last live minute (35).
	if !maxTS.Equal(base.Add(35 * time.Minute)) {
		t.Fatalf("last surviving bucket = %v, want %v (minute 35, the carry edge)", maxTS, base.Add(35*time.Minute))
	}
}

// TestHVACFoldEntityEnergyUnaffected: EntityEnergy (the known-load anchor) must
// stay exactly the real sensor's own energy, regardless of concurrent HVAC
// samples.
func TestHVACFoldEntityEnergyUnaffected(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()

	for m := 0; m <= 120; m += 5 {
		addRefEntity(t, d, "sensor.a", float64(m), 1000) // 2.0 kWh over 2h
	}
	const hvacEntity = "climate.t"
	setHVAC(t, d, hvacEntity, "0.5", "3.0")
	for m := 0; m < 120; m += 5 {
		if err := d.InsertHVACSample(ctx, hvacEntity, base.Add(time.Duration(m)*time.Minute), "cooling"); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	start, end := base, base.Add(2*time.Hour)
	kwh := d.EntityEnergy(ctx, "sensor.a", start, end)
	if kwh < 1.95 || kwh > 2.05 {
		t.Fatalf("EntityEnergy polluted by HVAC: got %.3f, want ~2.0", kwh)
	}
}
