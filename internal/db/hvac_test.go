package db

import (
	"context"
	"testing"
	"time"
)

// TestHVACStatus covers the latest-sample read: empty before any insert, then
// the just-inserted action/ts.
func TestHVACStatus(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()
	entity := "climate.living_room"

	action, ts := d.HVACStatus(ctx, entity)
	if action != "" || ts != nil {
		t.Fatalf("expected empty status before any insert, got action=%q ts=%v", action, ts)
	}

	want := base.Add(10 * time.Minute)
	if err := d.InsertHVACSample(ctx, entity, want, "heating"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	action, ts = d.HVACStatus(ctx, entity)
	if action != "heating" || ts == nil || !ts.Equal(want) {
		t.Fatalf("expected heating @ %v, got action=%q ts=%v", want, action, ts)
	}
}

// TestHVACBackfillReplace covers the backfill contract: idempotent
// replacement of history_backfill rows IN RANGE only — an out-of-range
// history_backfill row and a live row both survive.
func TestHVACBackfillReplace(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()
	entity := "climate.living_room"

	live := base.Add(20 * time.Minute)
	if err := d.InsertHVACSample(ctx, entity, live, "cooling"); err != nil {
		t.Fatalf("live insert: %v", err)
	}

	// an earlier, out-of-range backfill span: a later in-range replace must
	// not touch it.
	earlierFrom, earlierTo := base.Add(-2*time.Hour), base.Add(-1*time.Hour)
	earlierAt := base.Add(-90 * time.Minute)
	if err := d.ReplaceHVACBackfill(ctx, entity, earlierFrom, earlierTo, []time.Time{earlierAt}, []string{"idle"}); err != nil {
		t.Fatalf("earlier backfill: %v", err)
	}

	from, to := base, base.Add(60*time.Minute)
	var ts []time.Time
	var actions []string
	for m := 0; m < 60; m += 5 {
		ts = append(ts, base.Add(time.Duration(m)*time.Minute))
		actions = append(actions, "heating")
	}
	// run twice: row count must not grow (idempotent)
	for i := 0; i < 2; i++ {
		if err := d.ReplaceHVACBackfill(ctx, entity, from, to, ts, actions); err != nil {
			t.Fatalf("replace: %v", err)
		}
	}

	var nBackInRange, nLive, nEarlier int
	_ = d.pool.QueryRow(ctx,
		`SELECT count(*) FROM hvac_samples WHERE src='history_backfill' AND ts >= $1 AND ts <= $2`,
		from, to).Scan(&nBackInRange)
	_ = d.pool.QueryRow(ctx, `SELECT count(*) FROM hvac_samples WHERE src='live'`).Scan(&nLive)
	_ = d.pool.QueryRow(ctx,
		`SELECT count(*) FROM hvac_samples WHERE src='history_backfill' AND ts=$1`, earlierAt).Scan(&nEarlier)
	if nBackInRange != len(ts) {
		t.Fatalf("in-range backfill rows = %d, want %d (idempotent replace)", nBackInRange, len(ts))
	}
	if nLive != 1 {
		t.Fatalf("live rows = %d, want 1 — backfill must never touch live", nLive)
	}
	if nEarlier != 1 {
		t.Fatalf("out-of-range backfill row missing — in-range replace must only delete rows IN RANGE")
	}
}

// TestHVACGaps mirrors ReferenceGaps semantics (middle hole, trailing hole,
// dense = no gaps) but over hvac_samples for a single entity.
func TestHVACGaps(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	ctx := context.Background()
	entity := "climate.living_room"
	t0 := base

	if err := d.InsertHVACSample(ctx, entity, t0, "heating"); err != nil {
		t.Fatalf("insert t0: %v", err)
	}
	if err := d.InsertHVACSample(ctx, entity, t0.Add(45*time.Minute), "idle"); err != nil {
		t.Fatalf("insert t0+45m: %v", err)
	}

	// capEnd close behind the last sample: only the middle gap, no trailing one.
	nearCapEnd := t0.Add(50 * time.Minute)
	gaps := d.HVACGaps(ctx, entity, 30*24*time.Hour, 30*time.Minute, nearCapEnd)
	if len(gaps) != 1 {
		t.Fatalf("gaps = %d, want 1 (just the middle hole): %v", len(gaps), gaps)
	}
	if !gaps[0][0].Equal(t0) || !gaps[0][1].Equal(t0.Add(45*time.Minute)) {
		t.Fatalf("middle gap wrong: %v", gaps[0])
	}

	// capEnd well past the last sample (older than minGap): a trailing gap appears too.
	farCapEnd := t0.Add(120 * time.Minute)
	gaps = d.HVACGaps(ctx, entity, 30*24*time.Hour, 30*time.Minute, farCapEnd)
	if len(gaps) != 2 {
		t.Fatalf("gaps = %d, want 2 (middle + trailing): %v", len(gaps), gaps)
	}
	trailing := gaps[1]
	if !trailing[0].Equal(t0.Add(45*time.Minute)) || !trailing[1].Equal(farCapEnd) {
		t.Fatalf("trailing gap wrong: %v", trailing)
	}

	// dense samples (every 5 min, well under minGap) -> no gaps at all.
	dense := "climate.dense"
	for m := 0; m <= 45; m += 5 {
		if err := d.InsertHVACSample(ctx, dense, t0.Add(time.Duration(m)*time.Minute), "idle"); err != nil {
			t.Fatalf("insert dense: %v", err)
		}
	}
	denseGaps := d.HVACGaps(ctx, dense, 30*24*time.Hour, 30*time.Minute, t0.Add(50*time.Minute))
	if len(denseGaps) != 0 {
		t.Fatalf("dense samples should have no gaps, got %v", denseGaps)
	}
}
