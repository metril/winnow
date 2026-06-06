package ha

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// This file reaches HA long-term statistics over the WebSocket API. Statistics
// (where the Opower/utility integrations store billed energy) are NOT in the
// REST API — only `recorder/list_statistic_ids` and
// `recorder/statistics_during_period` expose them. Each call opens its own
// short-lived authenticated connection (reusing authConn) so it stays isolated
// from the long-lived live Stream subscription.

// StatID is one long-term-statistic available in HA's recorder.
type StatID struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Unit string `json:"unit"`
}

// StatPoint is one period bucket from statistics_during_period. Sum is the
// monotonic cumulative total; Change is the per-bucket delta when HA provides it.
type StatPoint struct {
	Start  time.Time
	Sum    *float64
	Change *float64
}

// UtilitySample is a normalized per-bucket consumed-energy value (kWh).
type UtilitySample struct {
	TS  time.Time
	Kwh float64
}

// wsResult is the generic envelope for a recorder result frame.
type wsResult struct {
	ID      int             `json:"id"`
	Type    string          `json:"type"`
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Error   struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// readResult reads frames until one with the matching id arrives (skipping any
// unrelated frames), then returns its result payload or an error.
func readResult(read func(any) error, id int) (json.RawMessage, error) {
	for {
		var r wsResult
		if err := read(&r); err != nil {
			return nil, err
		}
		if r.ID != id {
			continue // not ours (shouldn't happen on a single-command conn, but be safe)
		}
		if r.Type != "result" {
			continue
		}
		if !r.Success {
			return nil, fmt.Errorf("HA %s: %s", r.Error.Code, r.Error.Message)
		}
		return r.Result, nil
	}
}

// ListEnergyStatisticIDs returns the recorder's "sum" statistics whose unit is an
// energy unit (kWh/Wh/MWh) — the candidates for the utility-energy picker.
func ListEnergyStatisticIDs(ctx context.Context, base, token string) ([]StatID, error) {
	c, read, write, err := authConn(ctx, base, token)
	if err != nil {
		return nil, err
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	if err := write(map[string]any{"id": 1, "type": "recorder/list_statistic_ids", "statistic_type": "sum"}); err != nil {
		return nil, err
	}
	payload, err := readResult(read, 1)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		StatisticID string `json:"statistic_id"`
		Name        string `json:"name"`
		DisplayUnit string `json:"display_unit_of_measurement"`
		StatsUnit   string `json:"statistics_unit_of_measurement"`
		UnitClass   string `json:"unit_class"`
	}
	if err := json.Unmarshal(payload, &rows); err != nil {
		return nil, err
	}
	out := []StatID{}
	for _, r := range rows {
		unit := r.DisplayUnit
		if unit == "" {
			unit = r.StatsUnit
		}
		if !isEnergyUnit(unit) && r.UnitClass != "energy" {
			continue
		}
		name := r.Name
		if name == "" {
			name = r.StatisticID
		}
		out = append(out, StatID{ID: r.StatisticID, Name: name, Unit: unit})
	}
	return out, nil
}

func isEnergyUnit(u string) bool {
	switch strings.ToUpper(strings.TrimSpace(u)) {
	case "WH", "KWH", "MWH":
		return true
	}
	return false
}

// StatisticsDuringPeriod fetches one statistic's buckets over [start,end] at the
// given period ("hour"|"day"|"month"). Returns the raw points (Start + Sum +
// optional Change), oldest first.
func StatisticsDuringPeriod(ctx context.Context, base, token, statID string, start, end time.Time, period string) ([]StatPoint, error) {
	c, read, write, err := authConn(ctx, base, token)
	if err != nil {
		return nil, err
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	if err := write(map[string]any{
		"id":            1,
		"type":          "recorder/statistics_during_period",
		"statistic_ids": []string{statID},
		"period":        period,
		"start_time":    start.UTC().Format(time.RFC3339),
		"end_time":      end.UTC().Format(time.RFC3339),
		"types":         []string{"sum", "change"},
	}); err != nil {
		return nil, err
	}
	payload, err := readResult(read, 1)
	if err != nil {
		return nil, err
	}
	// result is an object keyed by statistic_id → array of points.
	var byID map[string][]struct {
		Start  flexTime `json:"start"`
		Sum    *float64 `json:"sum"`
		Change *float64 `json:"change"`
	}
	if err := json.Unmarshal(payload, &byID); err != nil {
		return nil, err
	}
	raw := byID[statID]
	out := make([]StatPoint, 0, len(raw))
	for _, p := range raw {
		out = append(out, StatPoint{Start: p.Start.Time, Sum: p.Sum, Change: p.Change})
	}
	return out, nil
}

// ResolvePeriod probes hour→day→month and returns the finest period that yields
// more than one point over the window, plus that period's points. Used when the
// configured period is "auto" (utilities differ: some expose hourly, many only
// monthly).
func ResolvePeriod(ctx context.Context, base, token, statID string, start, end time.Time) (string, []StatPoint, error) {
	var lastErr error
	for _, p := range []string{"hour", "day", "month"} {
		pts, err := StatisticsDuringPeriod(ctx, base, token, statID, start, end, p)
		if err != nil {
			lastErr = err
			continue
		}
		if len(pts) > 1 {
			return p, pts, nil
		}
	}
	// Nothing finer than a single bucket — fall back to month (best effort).
	pts, err := StatisticsDuringPeriod(ctx, base, token, statID, start, end, "month")
	if err != nil {
		if lastErr != nil {
			return "", nil, lastErr
		}
		return "", nil, err
	}
	return "month", pts, nil
}

// BucketDeltas converts raw statistics points into per-bucket consumed kWh.
// It prefers HA's `change` field; otherwise it differences the monotonic `sum`.
// A negative step (meter/recorder reset) is clamped to 0. Points must be ordered
// oldest-first (HA returns them that way); the first sum-only point has no prior
// reference and is dropped.
func BucketDeltas(points []StatPoint) []UtilitySample {
	out := []UtilitySample{}
	var prevSum *float64
	for _, p := range points {
		switch {
		case p.Change != nil:
			v := *p.Change
			if v < 0 {
				v = 0
			}
			out = append(out, UtilitySample{TS: p.Start.UTC(), Kwh: v})
		case p.Sum != nil:
			if prevSum != nil {
				v := *p.Sum - *prevSum
				if v < 0 {
					v = 0
				}
				out = append(out, UtilitySample{TS: p.Start.UTC(), Kwh: v})
			}
			prevSum = p.Sum
		}
	}
	return out
}

// flexTime parses a statistics `start`, which HA returns as epoch-milliseconds
// (recent cores) or an RFC3339 string (older cores).
type flexTime struct{ time.Time }

func (f *flexTime) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "" {
		return nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		t, err := time.Parse(time.RFC3339, str)
		if err != nil {
			return err
		}
		f.Time = t.UTC()
		return nil
	}
	ms, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	f.Time = time.UnixMilli(int64(ms)).UTC()
	return nil
}
