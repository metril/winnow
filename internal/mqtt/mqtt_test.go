package mqtt

import (
	"encoding/json"
	"strings"
	"testing"

	"winnow/internal/model"
)

func TestBuildDiscovery(t *testing.T) {
	name := "Kitchen"
	unit := "kWh"
	m := model.Meter{EndpointID: 12345, Commodity: "electric", PubName: &name, PubUnit: &unit}
	msgs := BuildDiscovery("homeassistant", m)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 sensors, got %d", len(msgs))
	}
	byKind := map[string]map[string]any{}
	for _, d := range msgs {
		if !strings.HasPrefix(d.Topic, "homeassistant/sensor/winnow_12345_") {
			t.Fatalf("bad topic: %s", d.Topic)
		}
		var cfg map[string]any
		if err := json.Unmarshal(d.Payload, &cfg); err != nil {
			t.Fatalf("bad payload json: %v", err)
		}
		uid, _ := cfg["unique_id"].(string)
		switch {
		case strings.HasSuffix(uid, "_energy"):
			byKind["energy"] = cfg
		case strings.HasSuffix(uid, "_power"):
			byKind["power"] = cfg
		case strings.HasSuffix(uid, "_signal"):
			byKind["signal"] = cfg
		}
	}
	energy := byKind["energy"]
	if energy["device_class"] != "energy" || energy["state_class"] != "total_increasing" {
		t.Fatalf("energy sensor classes wrong: %v", energy)
	}
	if energy["unit_of_measurement"] != "kWh" {
		t.Fatalf("energy unit not applied: %v", energy["unit_of_measurement"])
	}
	if byKind["power"]["device_class"] != "power" || byKind["power"]["unit_of_measurement"] != "W" {
		t.Fatalf("power sensor wrong: %v", byKind["power"])
	}
	dev, _ := energy["device"].(map[string]any)
	ids, _ := dev["identifiers"].([]any)
	if len(ids) != 1 || ids[0] != "winnow_12345" {
		t.Fatalf("device identifiers wrong: %v", dev)
	}
	if dev["name"] != "Kitchen" {
		t.Fatalf("device name should use pub_name: %v", dev["name"])
	}
}
