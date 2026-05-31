package main

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"time"
)

// mockMeter describes a synthetic meter.
type mockMeter struct {
	id    int64
	msg   string // SCM / SCM+ / IDM
	etype int
	base  float64
	rate  float64 // units/min baseline
}

var mockMeters = []mockMeter{
	{18273645, "SCM", 4, 100000, 0.8},  // electric — spike target
	{27384956, "SCM+", 7, 200000, 1.5}, // electric
	{39485012, "SCM", 4, 300000, 0.4},  // electric
	{48576123, "IDM", 4, 400000, 2.2},  // electric heavy
	{55512345, "SCM", 2, 50000, 0.2},   // gas
	{66698765, "SCM", 11, 80000, 0.1},  // water
}

const (
	mockSpikeID    = 18273645
	mockSpikeRate  = 30.0
	mockSpikeLoMin = 60.0
	mockSpikeHiMin = 150.0
)

func mockConsumption(m mockMeter, elapsedMin float64) float64 {
	v := m.base + m.rate*elapsedMin
	if m.id == mockSpikeID {
		on := clamp(elapsedMin, mockSpikeLoMin, mockSpikeHiMin) - mockSpikeLoMin
		if on > 0 {
			v += (mockSpikeRate - m.rate) * on
		}
	}
	return v
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func mockLine(m mockMeter, val float64, ts time.Time) string {
	t := ts.UTC().Format("2006-01-02T15:04:05.000Z")
	switch m.msg {
	case "SCM+":
		return fmt.Sprintf(`{"Time":"%s","Type":"SCM+","Message":{"EndpointID":%d,"EndpointType":%d,"Consumption":%.0f}}`, t, m.id, m.etype, val)
	case "IDM":
		return fmt.Sprintf(`{"Time":"%s","Type":"IDM","Message":{"ERTSerialNumber":%d,"LastConsumptionCount":%.0f}}`, t, m.id, val)
	default:
		return fmt.Sprintf(`{"Time":"%s","Type":"SCM","Message":{"ID":%d,"Type":%d,"Consumption":%.0f}}`, t, m.id, m.etype, val)
	}
}

// mockStream writes synthetic rtlamr JSON: backfill 3h fast, then live.
func mockStream(ctx context.Context, w io.Writer) {
	rng := rand.New(rand.NewSource(1))
	start := time.Now().UTC().Add(-3 * time.Hour)
	emit := func(ts time.Time) {
		el := ts.Sub(start).Minutes()
		for _, m := range mockMeters {
			if rng.Float64() < 0.05 {
				continue
			}
			fmt.Fprintln(w, mockLine(m, mockConsumption(m, el), ts))
		}
	}
	for ts := start; ts.Before(time.Now().UTC()); ts = ts.Add(30 * time.Second) {
		if ctx.Err() != nil {
			return
		}
		emit(ts)
	}
	for ctx.Err() == nil {
		emit(time.Now().UTC())
		time.Sleep(500 * time.Millisecond)
	}
}
