package main

import (
	"testing"
	"time"

	"winnow/internal/ha"
)

var base = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

func TestHVACWatts(t *testing.T) {
	cases := []struct {
		action         string
		heatKW, coolKW float64
		want           float64
	}{
		{"heating", 3.5, 2.0, 3500},
		{"cooling", 3.5, 2.0, 2000},
		{"idle", 3.5, 2.0, 0},
		{"off", 3.5, 2.0, 0},
		{"fan", 3.5, 2.0, 0},
		{"", 3.5, 2.0, 0},
		{"heating", 0, 2.0, 0}, // heating configured off (0 kW)
	}
	for _, c := range cases {
		if got := hvacWatts(c.action, c.heatKW, c.coolKW); got != c.want {
			t.Errorf("hvacWatts(%q, %v, %v) = %v, want %v", c.action, c.heatKW, c.coolKW, got, c.want)
		}
	}
}

func ev(minute int, action string) ha.StateEvent {
	return ha.StateEvent{
		TS:         base.Add(time.Duration(minute) * time.Minute),
		Attributes: map[string]any{"hvac_action": action},
	}
}

func evNoAction(minute int) ha.StateEvent {
	return ha.StateEvent{TS: base.Add(time.Duration(minute) * time.Minute), Attributes: map[string]any{}}
}

func TestExpandHVACHistory_NoEvents(t *testing.T) {
	lo, hi := base, base.Add(time.Hour)
	ts, actions := expandHVACHistory(nil, lo, hi, 5*time.Minute)
	if len(ts) != 0 || len(actions) != 0 {
		t.Fatalf("expected no rows with no events, got %d", len(ts))
	}
}

func TestExpandHVACHistory_OneEventBeforeLoCoversWindow(t *testing.T) {
	lo, hi := base, base.Add(30*time.Minute)
	events := []ha.StateEvent{ev(-10, "heating")}
	ts, actions := expandHVACHistory(events, lo, hi, 5*time.Minute)
	if len(ts) == 0 {
		t.Fatal("expected rows covering the whole window")
	}
	if !ts[0].Equal(lo) {
		t.Fatalf("first row should be at lo, got %v", ts[0])
	}
	if !ts[len(ts)-1].Equal(hi) {
		t.Fatalf("last row should be at hi (inclusive), got %v", ts[len(ts)-1])
	}
	for i, a := range actions {
		if a != "heating" {
			t.Fatalf("row %d action = %q, want heating", i, a)
		}
	}
	// exactly one row per 5-minute step from lo to hi inclusive
	want := int(hi.Sub(lo)/(5*time.Minute)) + 1
	if len(ts) != want {
		t.Fatalf("row count = %d, want %d", len(ts), want)
	}
}

func TestExpandHVACHistory_TwoEventsSplitWindow(t *testing.T) {
	lo, hi := base, base.Add(60*time.Minute)
	events := []ha.StateEvent{ev(-5, "heating"), ev(30, "cooling")}
	ts, actions := expandHVACHistory(events, lo, hi, 5*time.Minute)
	if len(ts) == 0 {
		t.Fatal("expected rows")
	}
	split := base.Add(30 * time.Minute)
	seenSplit := false
	for i, tm := range ts {
		if tm.Before(split) && actions[i] != "heating" {
			t.Fatalf("row at %v before split should be heating, got %q", tm, actions[i])
		}
		if !tm.Before(split) && actions[i] != "cooling" {
			t.Fatalf("row at %v at/after split should be cooling, got %q", tm, actions[i])
		}
		if tm.Equal(split) {
			seenSplit = true
		}
	}
	if !seenSplit {
		t.Fatal("expected a row exactly at the split, carrying the new action")
	}
	// no duplicate timestamps straddling the split
	seen := map[time.Time]int{}
	for _, tm := range ts {
		seen[tm]++
	}
	for tm, n := range seen {
		if n > 1 {
			t.Fatalf("timestamp %v appears %d times — should be unique", tm, n)
		}
	}
}

func TestExpandHVACHistory_EventsAfterHiIgnored(t *testing.T) {
	lo, hi := base, base.Add(20*time.Minute)
	events := []ha.StateEvent{ev(-5, "heating"), ev(30, "cooling")} // second event is after hi
	ts, actions := expandHVACHistory(events, lo, hi, 5*time.Minute)
	for i, tm := range ts {
		if tm.After(hi) {
			t.Fatalf("row at %v is after hi", tm)
		}
		if actions[i] != "heating" {
			t.Fatalf("row %d action = %q, want heating (event after hi must not contribute)", i, actions[i])
		}
	}
	if !ts[len(ts)-1].Equal(hi) {
		t.Fatalf("last row should still reach hi under the in-window event, got %v", ts[len(ts)-1])
	}
}

func hvacKAEqual(a, b hvacKA) bool {
	return a.set == b.set && a.action == b.action && a.ts.Equal(b.ts)
}

func TestHVACTransition(t *testing.T) {
	cases := []struct {
		name   string
		cur    hvacKA
		action string
		ts     time.Time
		want   hvacKA
	}{
		{
			name:   "set to unavailable clears",
			cur:    hvacKA{set: true, action: "heating", ts: base},
			action: "",
			ts:     base.Add(5 * time.Minute),
			want:   hvacKA{},
		},
		{
			name:   "unavailable to heating sets",
			cur:    hvacKA{},
			action: "heating",
			ts:     base,
			want:   hvacKA{set: true, action: "heating", ts: base},
		},
		{
			name:   "cooling to heating updates ts and action",
			cur:    hvacKA{set: true, action: "cooling", ts: base},
			action: "heating",
			ts:     base.Add(10 * time.Minute),
			want:   hvacKA{set: true, action: "heating", ts: base.Add(10 * time.Minute)},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := hvacTransition(c.cur, c.action, c.ts)
			if !hvacKAEqual(got, c.want) {
				t.Errorf("hvacTransition(%+v, %q, %v) = %+v, want %+v", c.cur, c.action, c.ts, got, c.want)
			}
		})
	}
}

func assertRanges(t *testing.T, got, want [][2]time.Time) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d ranges %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if !got[i][0].Equal(want[i][0]) || !got[i][1].Equal(want[i][1]) {
			t.Fatalf("range %d = [%v,%v], want [%v,%v]", i, got[i][0], got[i][1], want[i][0], want[i][1])
		}
	}
}

func TestPlanHVACBackfill_NoSamples(t *testing.T) {
	now := base
	lookback, minGap := 24*time.Hour, 30*time.Minute
	capEnd := now.Add(-2 * time.Hour)
	got := planHVACBackfill(nil, nil, now, lookback, minGap, capEnd)
	want := [][2]time.Time{{now.Add(-lookback), capEnd}}
	assertRanges(t, got, want)
}

func TestPlanHVACBackfill_FirstInsideLookbackNoGaps(t *testing.T) {
	now := base
	lookback, minGap := 24*time.Hour, 30*time.Minute
	capEnd := now.Add(-2 * time.Hour)
	lookStart := now.Add(-lookback)
	first := lookStart.Add(2 * time.Hour) // well past lookStart+minGap
	got := planHVACBackfill(&first, nil, now, lookback, minGap, capEnd)
	want := [][2]time.Time{{lookStart, first}}
	assertRanges(t, got, want)
}

func TestPlanHVACBackfill_FirstInsideLookbackWithInteriorGap(t *testing.T) {
	now := base
	lookback, minGap := 24*time.Hour, 30*time.Minute
	capEnd := now.Add(-2 * time.Hour)
	lookStart := now.Add(-lookback)
	first := lookStart.Add(2 * time.Hour)
	gapLo, gapHi := lookStart.Add(5*time.Hour), lookStart.Add(6*time.Hour)
	got := planHVACBackfill(&first, [][2]time.Time{{gapLo, gapHi}}, now, lookback, minGap, capEnd)
	want := [][2]time.Time{{lookStart, first}, {gapLo, gapHi}}
	assertRanges(t, got, want)
}

func TestPlanHVACBackfill_GapPastCapEndClamped(t *testing.T) {
	now := base
	lookback, minGap := 24*time.Hour, 30*time.Minute
	capEnd := now.Add(-2 * time.Hour)
	lookStart := now.Add(-lookback)
	first := lookStart.Add(2 * time.Hour)
	gapLo := capEnd.Add(-1 * time.Hour)
	gapHi := capEnd.Add(1 * time.Hour) // past capEnd
	got := planHVACBackfill(&first, [][2]time.Time{{gapLo, gapHi}}, now, lookback, minGap, capEnd)
	want := [][2]time.Time{{lookStart, first}, {gapLo, capEnd}}
	assertRanges(t, got, want)
}

func TestPlanHVACBackfill_GapEntirelyPastCapEndDropped(t *testing.T) {
	now := base
	lookback, minGap := 24*time.Hour, 30*time.Minute
	capEnd := now.Add(-2 * time.Hour)
	lookStart := now.Add(-lookback)
	first := lookStart.Add(2 * time.Hour)
	gapLo := capEnd.Add(1 * time.Hour)
	gapHi := capEnd.Add(2 * time.Hour)
	got := planHVACBackfill(&first, [][2]time.Time{{gapLo, gapHi}}, now, lookback, minGap, capEnd)
	want := [][2]time.Time{{lookStart, first}}
	assertRanges(t, got, want)
}

func TestPlanHVACBackfill_FirstBeforeLookbackStartNoLeadingGap(t *testing.T) {
	now := base
	lookback, minGap := 24*time.Hour, 30*time.Minute
	capEnd := now.Add(-2 * time.Hour)
	lookStart := now.Add(-lookback)
	first := lookStart.Add(-time.Hour) // before the lookback window entirely
	got := planHVACBackfill(&first, nil, now, lookback, minGap, capEnd)
	want := [][2]time.Time{}
	assertRanges(t, got, want)
}

func TestExpandHVACHistory_EmptyActionSkipped(t *testing.T) {
	lo, hi := base, base.Add(20*time.Minute)
	events := []ha.StateEvent{ev(-5, "heating"), evNoAction(10)}
	ts, actions := expandHVACHistory(events, lo, hi, 5*time.Minute)
	// the second event contributes nothing; heating's segment still only runs
	// up to (not including) the second event's ts.
	for i, tm := range ts {
		if !tm.Before(base.Add(10 * time.Minute)) {
			t.Fatalf("row at %v should not extend past the empty-action event at +10m (got action %q)", tm, actions[i])
		}
		if actions[i] != "heating" {
			t.Fatalf("row %d action = %q, want heating", i, actions[i])
		}
	}
	if len(ts) == 0 {
		t.Fatal("expected some rows before the empty-action event")
	}
}
