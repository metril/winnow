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
	_, err = d.pool.Exec(ctx, `TRUNCATE readings, meters, test_windows, capture_heartbeat, reference_samples`)
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
	ts := base.Add(time.Duration(minute * float64(time.Minute)))
	if err := d.InsertReferenceSample(context.Background(), "sensor.plug", ts, power); err != nil {
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

func TestCorrelationVsReference_RanksTrackingMeter(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	start, end := seed(t, d)

	ranking, err := d.CorrelationVsReference(context.Background(), "sensor.plug", start, end)
	if err != nil {
		t.Fatalf("corr: %v", err)
	}
	if len(ranking) == 0 {
		t.Fatal("no ranking")
	}
	if ranking[0].EndpointID != 1001 {
		t.Fatalf("expected 1001 first, got %d (ranking=%+v)", ranking[0].EndpointID, ranking)
	}
	if ranking[0].R == nil || *ranking[0].R < 0.95 {
		t.Fatalf("expected r>=0.95 for tracking meter, got %v", ranking[0].R)
	}
	// plug energy sanity: avg power (200W) over 1h ≈ 200 Wh
	if ranking[0].PlugEnergyWh == nil || *ranking[0].PlugEnergyWh < 150 || *ranking[0].PlugEnergyWh > 260 {
		t.Fatalf("plug energy out of range: %v", ranking[0].PlugEnergyWh)
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

func ptrBool(b bool) *bool { return &b }
