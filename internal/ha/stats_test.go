package ha

import (
	"encoding/json"
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }

func TestBucketDeltas_ChangePreferred(t *testing.T) {
	pts := []StatPoint{
		{Start: time.Unix(0, 0).UTC(), Sum: f(100), Change: f(10)},
		{Start: time.Unix(3600, 0).UTC(), Sum: f(140), Change: f(40)},
	}
	got := BucketDeltas(pts)
	if len(got) != 2 {
		t.Fatalf("want 2 samples, got %d", len(got))
	}
	if got[0].Kwh != 10 || got[1].Kwh != 40 {
		t.Fatalf("change should win: %+v", got)
	}
}

func TestBucketDeltas_SumDifferenced(t *testing.T) {
	pts := []StatPoint{
		{Start: time.Unix(0, 0).UTC(), Sum: f(100)},
		{Start: time.Unix(3600, 0).UTC(), Sum: f(130)},
		{Start: time.Unix(7200, 0).UTC(), Sum: f(175)},
	}
	got := BucketDeltas(pts)
	// first sum-only point has no prior reference and is dropped.
	if len(got) != 2 {
		t.Fatalf("want 2 deltas, got %d: %+v", len(got), got)
	}
	if got[0].Kwh != 30 || got[1].Kwh != 45 {
		t.Fatalf("sum diff wrong: %+v", got)
	}
}

func TestBucketDeltas_ResetClampedToZero(t *testing.T) {
	pts := []StatPoint{
		{Start: time.Unix(0, 0).UTC(), Sum: f(500)},
		{Start: time.Unix(3600, 0).UTC(), Sum: f(20)}, // recorder reset
		{Start: time.Unix(7200, 0).UTC(), Sum: f(60)},
	}
	got := BucketDeltas(pts)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Kwh != 0 {
		t.Fatalf("reset should clamp to 0, got %v", got[0].Kwh)
	}
	if got[1].Kwh != 40 {
		t.Fatalf("post-reset delta wrong: %v", got[1].Kwh)
	}
}

func TestFlexTime_EpochMillisAndRFC3339(t *testing.T) {
	var a flexTime
	if err := json.Unmarshal([]byte("1700000000000"), &a); err != nil {
		t.Fatal(err)
	}
	if a.Time.UTC() != time.UnixMilli(1700000000000).UTC() {
		t.Fatalf("epoch ms parse wrong: %v", a.Time)
	}
	var b flexTime
	if err := json.Unmarshal([]byte(`"2026-01-02T03:00:00+00:00"`), &b); err != nil {
		t.Fatal(err)
	}
	if b.Time.Year() != 2026 || b.Time.Hour() != 3 {
		t.Fatalf("rfc3339 parse wrong: %v", b.Time)
	}
}
