// Package model holds the shared types for winnow's data layer and API.
package model

import "time"

// Reading is one decoded rtlamr packet as stored.
type Reading struct {
	TS           time.Time `json:"ts"`
	MsgType      string    `json:"msg_type"`
	EndpointID   int64     `json:"endpoint_id"`
	EndpointType *int      `json:"endpoint_type"`
	Consumption  *float64  `json:"consumption"`
	Source       string    `json:"source"`
}

// Meter is a per-endpoint summary row (leaderboard) joined with annotations.
type Meter struct {
	EndpointID        int64    `json:"endpoint_id"`
	MsgType           string   `json:"msg_type"`
	EndpointType      *int     `json:"endpoint_type"`
	Commodity         string   `json:"commodity"`
	Packets           int64    `json:"packets"`
	PacketsPerHour    float64  `json:"packets_per_hour"`
	Sources           int      `json:"sources"`
	FirstSeen         string   `json:"first_seen"`
	LastSeen          string   `json:"last_seen"`
	LatestConsumption *float64 `json:"latest_consumption"`
	TotalMovement     *float64 `json:"total_movement"`
	// annotations
	Label         *string `json:"label"`
	Notes         *string `json:"notes"`
	IsCandidate   bool    `json:"is_candidate"`
	IsMine        bool    `json:"is_mine"`
	Ignored       bool    `json:"ignored"`
	Publish       bool    `json:"publish"`
	PubName       *string `json:"pub_name"`
	PubMultiplier float64 `json:"pub_multiplier"`
	PubUnit       *string `json:"pub_unit"`
}

// Point is a cumulative consumption sample for charts.
type Point struct {
	TS          string   `json:"ts"`
	Consumption *float64 `json:"consumption"`
}

// Bucket is a per-interval consumption delta.
type Bucket struct {
	Bucket  string   `json:"bucket"`
	Delta   *float64 `json:"delta"`
	Packets int64    `json:"packets"`
}

// Series is the per-meter detail payload.
type Series struct {
	EndpointID int64    `json:"endpoint_id"`
	Bucket     string   `json:"bucket"`
	Points     []Point  `json:"points"`
	Deltas     []Bucket `json:"deltas"`
}

// CorrRow is one ranked candidate from a correlation/identify query.
type CorrRow struct {
	EndpointID    int64    `json:"endpoint_id"`
	Commodity     string   `json:"commodity"`
	MsgType       string   `json:"msg_type"`
	EndpointType  *int     `json:"endpoint_type"`
	R             *float64 `json:"r"`     // Pearson correlation vs reference (nil if N/A)
	Score         float64  `json:"score"` // rate-ratio score
	WindowDelta   float64  `json:"window_delta"`
	WindowRate    float64  `json:"window_rate"`
	BaselineRate  float64  `json:"baseline_rate"`
	WindowPackets int64    `json:"window_packets"`
	PlugEnergyWh  *float64 `json:"plug_energy_wh"` // ground-truth energy over the window
	// regression of the meter's per-bucket energy delta vs aggregate monitored energy:
	R2                  *float64 `json:"r2"`
	Slope               *float64 `json:"slope"`                // meter-units per Wh (Deming)
	BaselineW           *float64 `json:"baseline_w"`           // unmonitored baseline (intercept)
	SuggestedMultiplier *float64 `json:"suggested_multiplier"` // kWh per meter-unit (regression)
	AnchorMultiplier    *float64 `json:"anchor_multiplier"`    // kWh per meter-unit (known-load anchor)
	UtilityMultiplier   *float64 `json:"utility_multiplier"`   // kWh per meter-unit (utility-bill anchor)
	MeterEnergyKwh      *float64 `json:"meter_energy_kwh"`     // candidate energy over window at suggested calibration
	FloorOK             *bool    `json:"floor_ok"`             // calibrated min ≥ monitored floor
	LagBuckets          *int     `json:"lag_buckets"`          // best meter-vs-reference lag (buckets)
	// composite identification confidence (0..1) and its component breakdown:
	Confidence      *float64           `json:"confidence"`
	ConfidenceParts map[string]float64 `json:"confidence_parts,omitempty"`
	// current applied calibration (so the UI can show current vs suggested vs anchor):
	PubMultiplier float64 `json:"pub_multiplier"`
	PubUnit       *string `json:"pub_unit"`
	// annotations (so the ranking can show/toggle tracked & published state)
	IsMine  bool `json:"is_mine"`
	Publish bool `json:"publish"`
}

// UtilityComparePoint is one period bucket aligning the meter's metered energy
// against the utility bill (and, for monthly data, an estimated-daily breakdown).
type UtilityComparePoint struct {
	TS          string   `json:"ts"`           // bucket start (RFC3339)
	UtilityKwh  float64  `json:"utility_kwh"`  // billed energy this bucket
	MeterKwh    *float64 `json:"meter_kwh"`    // candidate's metered energy this bucket (at the utility multiplier)
	CoveragePct float64  `json:"coverage_pct"` // fraction of the bucket winnow actually covered (0..1)
}

// UtilityDayEstimate is one day's estimated usage derived from a coarse (monthly)
// bill: a flat (bill/days) level and, when monitored sensors exist, a profile-
// shaped estimate. MeterKwh is the candidate's actual metered energy that day.
type UtilityDayEstimate struct {
	Day       string   `json:"day"`        // YYYY-MM-DD
	FlatKwh   float64  `json:"flat_kwh"`   // bill / days-in-month
	ShapedKwh *float64 `json:"shaped_kwh"` // bill × monitored_day/monitored_month (nil if no monitored sensors)
	MeterKwh  *float64 `json:"meter_kwh"`  // candidate's metered energy that day (at the utility multiplier)
}

// UtilityCompareResult backs the per-meter "compare vs utility bill" panel.
type UtilityCompareResult struct {
	StatisticID       string                `json:"statistic_id"`
	Period            string                `json:"period"` // resolved: month|day|hour
	UtilityMultiplier *float64              `json:"utility_multiplier"`
	BucketsCovered    int                   `json:"buckets_covered"`
	Buckets           []UtilityComparePoint `json:"buckets"`
	DailyEstimate     []UtilityDayEstimate  `json:"daily_estimate,omitempty"`
}

// TestWindow is a load-test span (manual or auto from the plug). KnownLoadW /
// KnownEntityID optionally record a toggled load of known wattage for direct
// (regression-free) calibration of the meter that saw it.
type TestWindow struct {
	ID            int64    `json:"id"`
	Label         string   `json:"label"`
	StartTS       string   `json:"start_ts"`
	EndTS         *string  `json:"end_ts"`
	Source        string   `json:"source"`
	KnownLoadW    *float64 `json:"known_load_w"`
	KnownEntityID *string  `json:"known_entity_id"`
}

// SourceHealth is per-SDR capture liveness.
type SourceHealth struct {
	Source         string   `json:"source"`
	Alive          bool     `json:"alive"`
	AgeSeconds     *float64 `json:"age_seconds"`
	LastTS         *string  `json:"last_ts"`
	TotalCount     int64    `json:"total_count"`
	PacketsLastMin int64    `json:"packets_last_min"`
}

// Health is the capture-health payload.
type Health struct {
	Alive          bool           `json:"alive"`
	Sources        []SourceHealth `json:"sources"`
	UniqueMeters   int64          `json:"unique_meters"`
	PacketsLastMin int64          `json:"packets_last_min"`
}
