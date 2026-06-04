package db

import (
	"context"
	"os"
	"testing"
	"time"

	"winnow/internal/model"
)

var base = time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)

func testDB(t *testing.T) *DB {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set (run a timescaledb container and point at it)")
	}
	ctx := context.Background()
	d, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := d.InitSchema(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}
	// Stop the background refresh policy from materializing our fixed-date test
	// data mid-run (TRUNCATE wouldn't clear materialized buckets). With nothing
	// materialized, real-time aggregation always reflects current raw rows.
	_, _ = d.pool.Exec(ctx, `SELECT delete_job(job_id) FROM timescaledb_information.jobs WHERE proc_name = 'policy_refresh_continuous_aggregate'`)
	_, err = d.pool.Exec(ctx, `TRUNCATE readings, meters, test_windows, capture_heartbeat, reference_samples, meter_index, meter_source, sdr_devices`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return d
}

func add(t *testing.T, d *DB, id int64, minute float64, consumption float64, etype int) {
	ts := base.Add(time.Duration(minute * float64(time.Minute)))
	e := etype
	r := model.Reading{TS: ts, MsgType: "SCM", EndpointID: id, EndpointType: &e, Consumption: &consumption, Source: "0"}
	if err := d.InsertReading(context.Background(), r, "{}"); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func addRef(t *testing.T, d *DB, minute, power float64) {
	addRefEntity(t, d, "sensor.plug", minute, power)
}

func addRefEntity(t *testing.T, d *DB, entity string, minute, power float64) {
	ts := base.Add(time.Duration(minute * float64(time.Minute)))
	if err := d.InsertReferenceSample(context.Background(), entity, ts, power); err != nil {
		t.Fatalf("ref: %v", err)
	}
}

// seed builds a plug power profile that varies each minute, a meter (1001)
// whose per-minute delta tracks the plug, and an unrelated steady meter (1002).
func seed(t *testing.T, d *DB) (start, end time.Time) {
	for m := 0; m < 60; m++ {
		power := 100.0
		if m%2 == 0 {
			power = 300.0
		}
		addRef(t, d, float64(m)+0.25, power)
		// meter 1001: two readings in the minute, delta = 0.1*power (tracks plug)
		cumLo := 10000.0 + sumTracked(m)
		add(t, d, 1001, float64(m), cumLo, 4)
		add(t, d, 1001, float64(m)+0.5, cumLo+0.1*power, 4)
		// meter 1002: steady delta of 5/min (no relation to plug)
		cum2 := 20000.0 + float64(m)*5
		add(t, d, 1002, float64(m), cum2, 4)
		add(t, d, 1002, float64(m)+0.5, cum2+5, 4)
	}
	return base, base.Add(60 * time.Minute)
}

func sumTracked(uptoMinute int) float64 {
	s := 0.0
	for m := 0; m < uptoMinute; m++ {
		p := 100.0
		if m%2 == 0 {
			p = 300.0
		}
		s += 0.1 * p
	}
	return s
}

func TestCorrelationVsReference_RanksAndCalibrates(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	start, end := seed(t, d)
	entities := []string{"sensor.plug"}

	ranking, floor, err := d.CorrelationVsReference(context.Background(), entities, start, end, 1)
	if err != nil {
		t.Fatalf("corr: %v", err)
	}
	if len(ranking) == 0 {
		t.Fatal("no ranking")
	}
	top := ranking[0]
	if top.EndpointID != 1001 {
		t.Fatalf("expected 1001 first, got %d (ranking=%+v)", top.EndpointID, ranking)
	}
	if top.R == nil || *top.R < 0.95 {
		t.Fatalf("expected r>=0.95 for tracking meter, got %v", top.R)
	}
	// energy basis at 1-min buckets: meter delta = 0.1*power, ref energy = power/60 Wh
	// => regr_slope = 0.1 / (1/60) ≈ 6 (meter-units per Wh)
	if top.Slope == nil || *top.Slope < 5 || *top.Slope > 7 {
		t.Fatalf("slope should recover ~6 units/Wh, got %v", top.Slope)
	}
	// multiplier = 1/(1000*slope) ≈ 1.667e-4 kWh per meter-unit
	if top.SuggestedMultiplier == nil || *top.SuggestedMultiplier < 1.3e-4 || *top.SuggestedMultiplier > 2.1e-4 {
		t.Fatalf("expected multiplier ~1.667e-4 kWh/unit, got %v", top.SuggestedMultiplier)
	}
	// alternating 100/300 W -> 5th percentile floor ≈ 100
	if floor < 90 || floor > 130 {
		t.Fatalf("monitored floor out of range: %v", floor)
	}
}

// TestCorrelationVsReference_SinglePacketPerMinute guards the meter-side fix: a
// meter that sends ONE packet per minute has within-bucket max-min == 0 every
// minute, so the old correlation got no signal (r == nil). The cross-bucket
// cumulative delta recovers a real per-bucket consumption and correlates.
func TestCorrelationVsReference_SinglePacketPerMinute(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	cum := 50000.0
	for m := 0; m <= 90; m++ {
		p := 100.0
		if m%2 == 0 {
			p = 300.0
		}
		addRef(t, d, float64(m)+0.25, p)
		cum += 0.2 * p // this minute's consumption, baked into the single reading
		add(t, d, 2001, float64(m)+0.25, cum, 4)
	}
	start, end := base, base.Add(91*time.Minute)
	ranking, _, err := d.CorrelationVsReference(context.Background(), []string{"sensor.plug"}, start, end, 1)
	if err != nil {
		t.Fatalf("corr: %v", err)
	}
	var got *model.CorrRow
	for i := range ranking {
		if ranking[i].EndpointID == 2001 {
			got = &ranking[i]
		}
	}
	if got == nil {
		t.Fatal("single-packet meter 2001 missing from ranking")
	}
	// cross-bucket delta = 0.2*power, ref energy = power/60 Wh -> strong correlation
	if got.R == nil || *got.R < 0.95 {
		t.Fatalf("single-packet meter should now correlate (r>=0.95), got %v", got.R)
	}
}

func TestMonitoredFloorAndAggregateSum(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()
	// two monitored entities; one drops out for a stretch (locf must carry it).
	for m := 0; m < 30; m++ {
		addRefEntity(t, d, "sensor.a", float64(m)+0.2, 200)
		if m < 15 { // sensor.b stops reporting after minute 15 -> locf holds 50
			addRefEntity(t, d, "sensor.b", float64(m)+0.3, 50)
		}
	}
	start, end := base, base.Add(30*time.Minute)
	series, err := d.AggregateSeries(ctx, []string{"sensor.a", "sensor.b"}, start, end, "5m")
	if err != nil {
		t.Fatal(err)
	}
	if len(series) == 0 {
		t.Fatal("empty aggregate series")
	}
	// after b drops out, aggregate stays 250 (200 + locf 50), never back to 200
	last := series[len(series)-1].Value
	if last < 240 || last > 260 {
		t.Fatalf("locf sum wrong at end: %v (want ~250)", last)
	}
	floor := d.MonitoredFloor(ctx, []string{"sensor.a", "sensor.b"}, start, end)
	if floor < 240 || floor > 260 {
		t.Fatalf("floor should be ~250 (both on, locf), got %v", floor)
	}
}

func TestCorrelation_RateRatio(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	// 1001 spikes only in [20,40]; 1002 steady all hour.
	for m := 0; m < 60; m++ {
		rate := 1.0
		if m >= 20 && m < 40 {
			rate = 50.0
		}
		add(t, d, 1001, float64(m), 1000+spikeCum(m), 4)
		add(t, d, 1001, float64(m)+0.5, 1000+spikeCum(m)+rate, 4)
		add(t, d, 1002, float64(m), 5000+float64(m)*3, 4)
		add(t, d, 1002, float64(m)+0.5, 5000+float64(m)*3+3, 4)
	}
	ranking, err := d.Correlation(context.Background(), base.Add(20*time.Minute), base.Add(40*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if ranking[0].EndpointID != 1001 {
		t.Fatalf("expected 1001 first, got %d", ranking[0].EndpointID)
	}
	if ranking[0].Score <= ranking[1].Score {
		t.Fatalf("spiking meter should outscore steady one: %v vs %v", ranking[0].Score, ranking[1].Score)
	}
}

func spikeCum(upto int) float64 {
	s := 0.0
	for m := 0; m < upto; m++ {
		if m >= 20 && m < 40 {
			s += 50
		} else {
			s += 1
		}
	}
	return s
}

func TestLeaderboardAndFlags(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	seed(t, d)
	ctx := context.Background()

	// ignore 1002, publish 1001
	if _, err := d.UpdateMeter(ctx, 1002, MeterUpdate{Ignored: ptrBool(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.UpdateMeter(ctx, 1001, MeterUpdate{Publish: ptrBool(true), IsMine: ptrBool(true)}); err != nil {
		t.Fatal(err)
	}

	board, err := d.Leaderboard(ctx, LeaderboardOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range board {
		if m.EndpointID == 1002 {
			t.Fatal("ignored meter should be hidden by default")
		}
	}
	pub, err := d.MetersForPublish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) != 1 || pub[0].EndpointID != 1001 {
		t.Fatalf("expected 1001 published, got %+v", pub)
	}
}

func TestMultiSeries(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	since, _ := seed(t, d)
	out, err := d.MultiSeries(context.Background(), []int64{1001, 1002}, &since, nil, "5m", "delta")
	if err != nil {
		t.Fatal(err)
	}
	if len(out["1001"]) == 0 || len(out["1002"]) == 0 {
		t.Fatalf("expected series for both meters, got %v keys", len(out))
	}
}

func TestDeleteMeterUntrackVsPurge(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()
	seed(t, d)
	if _, err := d.UpdateMeter(ctx, 1001, MeterUpdate{IsMine: ptrBool(true)}); err != nil {
		t.Fatal(err)
	}

	// untrack: annotation gone, but the meter still shows from history.
	if err := d.DeleteMeter(ctx, 1001, false); err != nil {
		t.Fatal(err)
	}
	m, _ := d.GetMeter(ctx, 1001)
	if m.IsMine {
		t.Fatal("annotation should be cleared after untrack")
	}
	board, _ := d.Leaderboard(ctx, LeaderboardOpts{})
	if !hasMeter(board, 1001) {
		t.Fatal("untracked meter should still appear from history")
	}

	// purge: gone entirely.
	if err := d.DeleteMeter(ctx, 1001, true); err != nil {
		t.Fatal(err)
	}
	board, _ = d.Leaderboard(ctx, LeaderboardOpts{})
	if hasMeter(board, 1001) {
		t.Fatal("purged meter should be gone")
	}
}

func hasMeter(board []model.Meter, id int64) bool {
	for _, m := range board {
		if m.EndpointID == id {
			return true
		}
	}
	return false
}

func TestAnalyticsProfilesAndCoverage(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()
	seed(t, d)

	hod, err := d.HourlyProfile(ctx, 1001, 7)
	if err != nil || len(hod) == 0 {
		t.Fatalf("hourly profile empty: %v", err)
	}
	daily, err := d.DailyRollup(ctx, 1001, 30)
	if err != nil || len(daily) == 0 {
		t.Fatalf("daily rollup empty: %v", err)
	}
	cov, err := d.CoverageMatrix(ctx)
	if err != nil || len(cov) == 0 {
		t.Fatalf("coverage empty: %v", err)
	}
	b, err := d.BenchmarkMeter(ctx, 1001, 7)
	if err != nil {
		t.Fatalf("benchmark: %v", err)
	}
	if b.Peers < 1 || b.Yours <= 0 {
		t.Fatalf("benchmark looks wrong: %+v", b)
	}
}

func ptrBool(b bool) *bool { return &b }
