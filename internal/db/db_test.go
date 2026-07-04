package db

import (
	"context"
	"os"
	"testing"
	"time"

	"winnow/internal/model"
)

// base anchors all seeded fixtures ~6 days in the past so now()-relative
// queries (profiles, leaderboard defaults) still see them. A fixed calendar
// date here rots: the analytics test fails as soon as the wall clock moves
// 7 days past it.
var base = time.Now().UTC().AddDate(0, 0, -6).Truncate(time.Hour)

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
	_, err = d.pool.Exec(ctx, `TRUNCATE readings, meters, test_windows, capture_heartbeat, reference_samples, utility_energy, meter_index, meter_source, sdr_devices`)
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

	ranking, floor, err := d.CorrelationVsReference(context.Background(), entities, start, end, 1, true)
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
	// the composite confidence is the headline and should be high for the match.
	if top.Confidence == nil || *top.Confidence < 0.5 {
		t.Fatalf("expected high confidence for the tracking meter, got %v", top.Confidence)
	}
	// and it should outrank the unrelated steady meter's confidence.
	for _, r := range ranking {
		if r.EndpointID == 1002 && r.Confidence != nil && *r.Confidence >= *top.Confidence {
			t.Fatalf("steady meter 1002 confidence %v should be below 1001 %v", *r.Confidence, *top.Confidence)
		}
	}
}

// TestRolloverAwareDelta verifies the cross-bucket delta treats a counter wrap at
// the 2^24 (SCM) boundary as a real small rise, while a genuine mid-range reset
// becomes a NULL gap — not the old "any decrease = reset" behavior.
func TestRolloverAwareDelta(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()
	// meter 3001 (SCM): counter wraps 2^24 in the 3rd bucket (…215 → 7). Readings are
	// 5 min apart so each lands in its own "5m" bucket.
	for i, v := range []float64{16777200, 16777210, 16777215, 7, 12} {
		add(t, d, 3001, float64(i*5)+0.25, v, 4)
	}
	// meter 3002 (SCM): a genuine reset from mid-range (5010 → 100) in the 3rd bucket.
	for i, v := range []float64{5000, 5010, 100, 110} {
		add(t, d, 3002, float64(i*5)+0.25, v, 4)
	}
	since := base

	s1, err := d.MeterSeries(ctx, 3001, &since, nil, "5m")
	if err != nil {
		t.Fatal(err)
	}
	nonNil := 0
	for _, b := range s1.Deltas {
		if b.Delta != nil {
			nonNil++
			if *b.Delta < 0 || *b.Delta > 100 {
				t.Fatalf("rollover delta should be a small positive rise, got %v", *b.Delta)
			}
		}
	}
	if nonNil != 4 { // m1,m2,m3(wrap),m4 — none dropped
		t.Fatalf("rollover meter should keep all 4 steps, got %d non-nil deltas", nonNil)
	}

	s2, err := d.MeterSeries(ctx, 3002, &since, nil, "5m")
	if err != nil {
		t.Fatal(err)
	}
	gaps := 0
	for _, b := range s2.Deltas {
		if b.Delta == nil {
			gaps++
		}
	}
	if gaps < 2 { // the first bucket (no lag) + the reset bucket
		t.Fatalf("genuine reset should drop to a gap, got %d nil deltas", gaps)
	}
}

// TestElectricOnlyFilter verifies the commodity gate: a gas meter that correlates
// with the (electrical) reference is excluded when electricOnly is set.
func TestElectricOnlyFilter(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()
	for m := 0; m < 60; m++ {
		power := 100.0
		if m%2 == 0 {
			power = 300.0
		}
		addRef(t, d, float64(m)+0.25, power)
		// electric meter 1001 and gas meter 9001 both track the plug.
		eLo := 10000.0 + sumTracked(m)
		add(t, d, 1001, float64(m), eLo, 4)
		add(t, d, 1001, float64(m)+0.5, eLo+0.1*power, 4)
		gLo := 30000.0 + sumTracked(m)
		add(t, d, 9001, float64(m), gLo, 2) // endpoint_type 2 = gas
		add(t, d, 9001, float64(m)+0.5, gLo+0.1*power, 2)
	}
	start, end := base, base.Add(60*time.Minute)

	elec, _, err := d.CorrelationVsReference(ctx, []string{"sensor.plug"}, start, end, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range elec {
		if r.EndpointID == 9001 {
			t.Fatal("electric-only ranking must not include the gas meter 9001")
		}
	}
	all, _, err := d.CorrelationVsReference(ctx, []string{"sensor.plug"}, start, end, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCorr(all, 9001) {
		t.Fatal("commodity=all should include the gas meter 9001")
	}
}

func hasCorr(rows []model.CorrRow, id int64) bool {
	for _, r := range rows {
		if r.EndpointID == id {
			return true
		}
	}
	return false
}

// TestCombinedRankingConfidenceAndAnchor checks the cross-window aggregation: a
// meter that tracks the plug across two independent test windows (each tagged with
// a known load) wins on composite confidence and gets a known-load anchor
// multiplier; a steady unrelated meter does not.
func TestCombinedRankingConfidenceAndAnchor(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()
	entities := []string{"sensor.plug"}

	cum1, cum2 := 10000.0, 20000.0
	mkWindow := func(offset int) (time.Time, time.Time) {
		for m := 0; m < 40; m++ {
			mm := offset + m
			power := 100.0
			if m%2 == 0 {
				power = 300.0
			}
			addRef(t, d, float64(mm)+0.25, power)
			add(t, d, 1001, float64(mm), cum1, 4)
			cum1 += 0.1 * power
			add(t, d, 1001, float64(mm)+0.5, cum1, 4)
			add(t, d, 1002, float64(mm), cum2, 4)
			cum2 += 5
			add(t, d, 1002, float64(mm)+0.5, cum2, 4)
		}
		return base.Add(time.Duration(offset) * time.Minute), base.Add(time.Duration(offset+40) * time.Minute)
	}
	s1, e1 := mkWindow(0)
	s2, e2 := mkWindow(60)
	kw := 200.0
	if _, err := d.CreateTest(ctx, "w1", s1, &e1, "manual", &kw, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateTest(ctx, "w2", s2, &e2, "manual", &kw, nil); err != nil {
		t.Fatal(err)
	}

	res, err := d.CombinedRanking(ctx, entities)
	if err != nil {
		t.Fatal(err)
	}
	ranking, _ := res["ranking"].([]AggRow)
	if len(ranking) == 0 {
		t.Fatal("no combined ranking")
	}
	top := ranking[0]
	if top.EndpointID != 1001 {
		t.Fatalf("expected 1001 to win across windows, got %d (%+v)", top.EndpointID, ranking)
	}
	if top.Confidence == nil || *top.Confidence < 0.4 {
		t.Fatalf("expected a cross-window confidence for the winner, got %v", top.Confidence)
	}
	if top.AnchorMultiplier == nil || *top.AnchorMultiplier <= 0 {
		t.Fatalf("expected a known-load anchor multiplier, got %v", top.AnchorMultiplier)
	}
	if top.TestsPresent < 2 {
		t.Fatalf("winner should appear in both windows, got %d", top.TestsPresent)
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
	ranking, _, err := d.CorrelationVsReference(context.Background(), []string{"sensor.plug"}, start, end, 1, true)
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
	// Two monitored entities; one goes silent at minute 15. The carry must hold
	// its value WITHIN refCarryLimit (change-driven sensors are legitimately
	// quiet; the worker keepalive refreshes them every 5 min), then expire —
	// never fabricate it indefinitely like the pre-incident unbounded locf.
	for m := 0; m < 45; m++ {
		addRefEntity(t, d, "sensor.a", float64(m)+0.2, 200)
		if m < 15 { // sensor.b stops reporting after minute 15
			addRefEntity(t, d, "sensor.b", float64(m)+0.3, 50)
		}
	}
	start, end := base, base.Add(45*time.Minute)
	series, err := d.AggregateSeries(ctx, []string{"sensor.a", "sensor.b"}, start, end, "5m")
	if err != nil {
		t.Fatal(err)
	}
	if len(series) == 0 {
		t.Fatal("empty aggregate series")
	}
	byBucket := func(min float64) float64 {
		want := base.Add(time.Duration(min) * time.Minute)
		for _, p := range series {
			ts, _ := time.Parse(time.RFC3339Nano, p.Bucket)
			if ts.Equal(want) {
				return p.Value
			}
		}
		t.Fatalf("bucket at +%vm missing", min)
		return 0
	}
	// minute 20–25: b silent but within the carry bound → still 250
	if v := byBucket(20); v < 240 || v > 260 {
		t.Fatalf("in-bound carry: got %v at +20m, want ~250", v)
	}
	// minute 40–45: b's carry expired → honest 200, not a fabricated 250
	if v := byBucket(40); v < 190 || v > 210 {
		t.Fatalf("expired carry: got %v at +40m, want ~200", v)
	}
	floor := d.MonitoredFloor(ctx, []string{"sensor.a", "sensor.b"}, start, end)
	if floor < 190 || floor > 260 {
		t.Fatalf("floor out of range: got %v", floor)
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
	// The profiles run on hourly deltas (lag over hourly counter maxima with a
	// ≥3-positive-deltas evidence floor), so one seeded hour isn't enough —
	// extend meter 1001 forward with a day of steady hourly readings.
	cum := 20000.0
	for h := 2; h <= 26; h++ {
		add(t, d, 1001, float64(h*60), cum, 4)
		cum += 12
	}

	hod, err := d.HourlyProfile(ctx, 1001, 7, "UTC")
	if err != nil || len(hod) == 0 {
		t.Fatalf("hourly profile empty: %v", err)
	}
	daily, err := d.DailyRollup(ctx, 1001, 30, "UTC")
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
