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
