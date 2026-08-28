package main

import (
	"time"

	"winnow/internal/ha"
)

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
