package main

import (
	"time"

	"winnow/internal/ha"
)

// hvacKA is the haLoop keepalive state for the configured HVAC entity: the
// last known hvac_action and when it was last confirmed, guarded by kaMu
// alongside kaLast. set is false when nothing is currently asserted (e.g. the
// thermostat is unavailable) — hvacWatts must not be fed a stale action.
type hvacKA struct {
	set    bool
	action string
	ts     time.Time
}

// hvacTransition computes the next keepalive state given a new hvac_action
// sample. An empty action (no attribute, or the thermostat reporting
// unavailable) clears the keepalive to the zero value rather than leaving the
// last-known action asserted — a dead thermostat must stop contributing to the
// estimate, not have its last state carried indefinitely by the 5-minute
// keepalive (the exact fabrication path bounded-carry gap-fill exists to
// prevent). A non-empty action always sets cur to {action, ts}, regardless of
// what was previously asserted.
func hvacTransition(cur hvacKA, action string, ts time.Time) hvacKA {
	if action == "" {
		return hvacKA{}
	}
	return hvacKA{set: true, action: action, ts: ts}
}

// planHVACBackfill computes the ordered [lo,hi] history ranges
// backfillHVACHistory must fetch from HA, given the recorded hvac_samples span
// (first, nil if the entity has no samples at all) and the interior/trailing
// gaps HVACGaps already found within [now-lookback, capEnd]. Pure planning —
// backfillHVACHistory does all the I/O around this.
//
// first == nil means nothing has ever been recorded for this entity: the
// whole lookback window is unfetched, so the single range [now-lookback,
// capEnd] is returned and gaps is ignored. Otherwise a leading range
// [now-lookback, first] is prepended when first is more than minGap past the
// start of the lookback window (there's a real unfetched span before the
// earliest sample); each of gaps follows, with hi clamped to capEnd and any
// range the clamp empties or inverts (!lo.Before(hi)) dropped.
func planHVACBackfill(first *time.Time, gaps [][2]time.Time, now time.Time, lookback, minGap time.Duration, capEnd time.Time) [][2]time.Time {
	lookStart := now.Add(-lookback)
	if first == nil {
		return [][2]time.Time{{lookStart, capEnd}}
	}
	ranges := [][2]time.Time{}
	if first.After(lookStart.Add(minGap)) {
		ranges = append(ranges, [2]time.Time{lookStart, *first})
	}
	for _, g := range gaps {
		lo, hi := g[0], g[1]
		if hi.After(capEnd) {
			hi = capEnd
		}
		if !lo.Before(hi) {
			continue
		}
		ranges = append(ranges, [2]time.Time{lo, hi})
	}
	return ranges
}

// hvacWatts derives the estimated instantaneous draw for one hvac_action
// sample from the user-set heating/cooling kW: "heating" and "cooling" scale
// to watts, anything else (idle, off, fan, unknown, "") draws zero.
func hvacWatts(action string, heatKW, coolKW float64) float64 {
	switch action {
	case "heating":
		return heatKW * 1000
	case "cooling":
		return coolKW * 1000
	default:
		return 0
	}
}

// expandHVACHistory turns ascending hvac_action state-change events into a
// dense (ts, action) series at `step` cadence for ReplaceHVACBackfill. Each
// event's hvac_action holds from its own TS until the next event's TS (the
// last event holds through hi); every emitted row is clipped to [lo, hi].
// Events with an empty hvac_action contribute nothing (they still bound the
// PRECEDING event's segment, so that segment stops at the empty event's TS).
// The boundary timestamp between two segments is emitted exactly once, under
// the newer segment's action.
func expandHVACHistory(events []ha.StateEvent, lo, hi time.Time, step time.Duration) (ts []time.Time, actions []string) {
	for i, e := range events {
		action := e.Attr("hvac_action")
		if action == "" {
			continue
		}
		segEnd, inclusive := hi, true
		if i+1 < len(events) {
			segEnd, inclusive = events[i+1].TS, false
			if segEnd.After(hi) {
				segEnd, inclusive = hi, true
			}
		}
		for t := e.TS; t.Before(segEnd) || (inclusive && t.Equal(segEnd)); t = t.Add(step) {
			if !t.Before(lo) && !t.After(hi) {
				ts = append(ts, t)
				actions = append(actions, action)
			}
		}
	}
	return ts, actions
}
