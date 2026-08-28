package config

import "testing"

func TestDeviceScanOverrides(t *testing.T) {
	on := true
	c := CaptureConfig{
		Freq: "912600155", Gain: "", PPM: "0", MsgType: "scm,scm+,idm", FilterID: "",
		Devices: map[string]DeviceConfig{
			"00000001": {Enabled: &on, Freq: "868000000", Gain: "40"}, // overrides freq+gain
			"stx:1":    {},                                            // present but no overrides
		},
	}

	// overridden dongle uses its own values, inherits the rest
	if got := c.DeviceFreq("00000001"); got != "868000000" {
		t.Fatalf("freq override: got %q", got)
	}
	if got := c.DeviceGain("00000001"); got != "40" {
		t.Fatalf("gain override: got %q", got)
	}
	if got := c.DeviceMsgType("00000001"); got != "scm,scm+,idm" {
		t.Fatalf("msgtype should inherit default: got %q", got)
	}

	// dongle with an empty config inherits everything
	if got := c.DeviceFreq("stx:1"); got != "912600155" {
		t.Fatalf("empty config should inherit freq: got %q", got)
	}

	// dongle absent from the map inherits everything
	if got := c.DeviceFreq("unknown"); got != "912600155" {
		t.Fatalf("absent dongle should inherit freq: got %q", got)
	}
	if got := c.DevicePPM("unknown"); got != "0" {
		t.Fatalf("absent dongle should inherit ppm: got %q", got)
	}
}

func TestHVACDefaults(t *testing.T) {
	c := FromMap(map[string]string{})
	if c.HVACEntityID != "" || c.HVACHeatingKW != 0 || c.HVACCoolingKW != 0 {
		t.Fatalf("expected zero-value HVAC defaults, got %+v", c)
	}
	if c.HVACConfigured() {
		t.Fatal("HVACConfigured should be false with nothing set")
	}
}

func TestHVACParsedValues(t *testing.T) {
	c := FromMap(map[string]string{
		KeyHVACEntityID:  " climate.living_room ",
		KeyHVACHeatingKW: "3.5",
		KeyHVACCoolingKW: "2.1",
	})
	if c.HVACEntityID != "climate.living_room" {
		t.Fatalf("expected trimmed entity id, got %q", c.HVACEntityID)
	}
	if c.HVACHeatingKW != 3.5 || c.HVACCoolingKW != 2.1 {
		t.Fatalf("expected parsed kW, got heating=%v cooling=%v", c.HVACHeatingKW, c.HVACCoolingKW)
	}
	if !c.HVACConfigured() {
		t.Fatal("HVACConfigured should be true with entity + both kW set")
	}
}

func TestHVACConfiguredWithOneKW(t *testing.T) {
	c := FromMap(map[string]string{
		KeyHVACEntityID:  "climate.living_room",
		KeyHVACHeatingKW: "3.5",
	})
	if !c.HVACConfigured() {
		t.Fatal("HVACConfigured should be true with only heating kW set")
	}
}

func TestHVACUnparsableOrNegativeKWDefaultsZero(t *testing.T) {
	c := FromMap(map[string]string{
		KeyHVACEntityID:  "climate.living_room",
		KeyHVACHeatingKW: "not-a-number",
		KeyHVACCoolingKW: "-2",
	})
	if c.HVACHeatingKW != 0 {
		t.Fatalf("expected unparsable heating kW to default to 0, got %v", c.HVACHeatingKW)
	}
	if c.HVACCoolingKW != 0 {
		t.Fatalf("expected negative cooling kW clamped to 0, got %v", c.HVACCoolingKW)
	}
	if c.HVACConfigured() {
		t.Fatal("HVACConfigured should be false when both kW end up 0")
	}
}
