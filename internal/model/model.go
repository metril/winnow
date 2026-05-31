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
}

// TestWindow is a load-test span (manual or auto from the plug).
type TestWindow struct {
	ID      int64   `json:"id"`
	Label   string  `json:"label"`
	StartTS string  `json:"start_ts"`
	EndTS   *string `json:"end_ts"`
	Source  string  `json:"source"`
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
