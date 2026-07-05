package main

import "testing"

// Regression tests for the July 2026 incident: HA restarted, the reconnect's
// entity-info fetch came back empty, and the loop subscribed anyway — silently
// discarding every event forever. resolveEntityInfo is the gate that must fail
// closed (don't subscribe) instead of failing silent.

func TestResolveEntityInfoFailsClosedWithNothing(t *testing.T) {
	info, ok := resolveEntityInfo(nil, nil, []string{"sensor.a", "sensor.b"})
	if ok {
		t.Fatalf("resolved %d entities from nothing — subscribing now would discard all events", len(info))
	}
}

func TestResolveEntityInfoUsesCacheWhenFreshEmpty(t *testing.T) {
	cache := map[string]einfo{
		"sensor.a": {kind: "power", factor: 1},
		"sensor.b": {kind: "energy", factor: 1},
	}
	info, ok := resolveEntityInfo(map[string]einfo{}, cache, []string{"sensor.a", "sensor.b", "sensor.c"})
	if !ok {
		t.Fatal("cache alone should be enough to keep the subscription alive")
	}
	if len(info) != 2 || info["sensor.a"].kind != "power" || info["sensor.b"].kind != "energy" {
		t.Fatalf("cache not carried over: %+v", info)
	}
}

func TestResolveEntityInfoFreshWinsCacheFills(t *testing.T) {
	fresh := map[string]einfo{"sensor.a": {kind: "power", factor: 1000}}
	cache := map[string]einfo{
		"sensor.a": {kind: "power", factor: 1},
		"sensor.b": {kind: "energy", factor: 1},
	}
	info, ok := resolveEntityInfo(fresh, cache, []string{"sensor.a", "sensor.b"})
	if !ok || len(info) != 2 {
		t.Fatalf("expected merged map of 2, got ok=%v %+v", ok, info)
	}
	if info["sensor.a"].factor != 1000 {
		t.Fatalf("fresh resolution must win over cache: %+v", info["sensor.a"])
	}
	if info["sensor.b"].kind != "energy" {
		t.Fatalf("cache must fill fresh misses: %+v", info["sensor.b"])
	}
}

func TestResolveEntityInfoIgnoresUnrequestedEntities(t *testing.T) {
	cache := map[string]einfo{"sensor.gone": {kind: "power", factor: 1}}
	fresh := map[string]einfo{"sensor.a": {kind: "power", factor: 1}}
	info, ok := resolveEntityInfo(fresh, cache, []string{"sensor.a"})
	if !ok || len(info) != 1 {
		t.Fatalf("removed entities must not leak from cache: %+v", info)
	}
}
