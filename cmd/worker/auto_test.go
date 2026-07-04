package main

import (
	"testing"
	"time"
)

// feed advances the detector with n samples of the same power, one per tick.
func feed(aw *autoState, power, openDelta float64, start time.Time, n int, tick time.Duration) (autoAction, time.Time) {
	act, now := autoNone, start
	for i := 0; i < n; i++ {
		now = now.Add(tick)
		act = autoDecide(aw, power, openDelta, now)
		if act != autoNone {
			return act, now
		}
	}
	return act, now
}

func TestAutoDecideOpensOnStepNotJitter(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	aw := &autoState{inited: true, baseline: 1400, lastClosedAt: t0.Add(-time.Hour)}

	// server jitter: ±250 W swings must not open a window — the noise tracker
	// adapts above them
	powers := []float64{1400, 1650, 1200, 1550, 1300, 1600, 1250, 1580, 1350, 1620}
	now := t0
	for i, p := range powers {
		now = now.Add(30 * time.Second)
		if act := autoDecide(aw, p, 400, now); act != autoNone {
			t.Fatalf("jitter sample %d (%.0fW) produced %v", i, p, act)
		}
	}

	// a real appliance step: +1500 W clears baseline + max(400, 3×dev)
	now = now.Add(30 * time.Second)
	if act := autoDecide(aw, aw.baseline+1500, 400, now); act != autoOpen {
		t.Fatalf("appliance step did not open (dev=%.0f baseline=%.0f)", aw.dev, aw.baseline)
	}
}

func TestAutoDecideCooldown(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	aw := &autoState{inited: true, baseline: 1000, lastClosedAt: t0}
	// big step 1 min after a close → cooldown blocks
	if act := autoDecide(aw, 3000, 400, t0.Add(time.Minute)); act != autoNone {
		t.Fatalf("cooldown violated: %v", act)
	}
	// same step after the 5-min cooldown → opens
	if act := autoDecide(aw, 3000, 400, t0.Add(6*time.Minute)); act != autoOpen {
		t.Fatalf("expected open after cooldown")
	}
}

func TestAutoDecideHysteresisCloseAndBlipDiscard(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	aw := &autoState{inited: true, baseline: 1000, open: true, openedAt: t0}

	// still high 4 min in → stays open
	if act := autoDecide(aw, 2500, 400, t0.Add(4*time.Minute)); act != autoNone {
		t.Fatalf("high load closed early: %v", act)
	}
	// falls back near baseline after 5 min → close (≥ min duration)
	if act := autoDecide(aw, 1050, 400, t0.Add(5*time.Minute)); act != autoClose {
		t.Fatalf("expected close on fall-back")
	}

	// a 60-second blip → discard, not close
	aw2 := &autoState{inited: true, baseline: 1000, open: true, openedAt: t0}
	if act := autoDecide(aw2, 1010, 400, t0.Add(time.Minute)); act != autoDiscard {
		t.Fatalf("expected blip discard, got %v", act)
	}
}

func TestAutoDecideMaxDurationCap(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	aw := &autoState{inited: true, baseline: 1000, open: true, openedAt: t0}
	// load never falls, but the 15-min cap closes it (well past min duration)
	if act := autoDecide(aw, 3000, 400, t0.Add(autoMaxDuration)); act != autoClose {
		t.Fatalf("expected cap close, got %v", act)
	}
}

func TestAutoDecideBaselineFrozenWhileOpen(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	aw := &autoState{inited: true, baseline: 1000, open: true, openedAt: t0}
	before := aw.baseline
	_ = autoDecide(aw, 3000, 400, t0.Add(4*time.Minute))
	if aw.baseline != before {
		t.Fatalf("baseline moved while a window was open: %.0f → %.0f", before, aw.baseline)
	}
}
