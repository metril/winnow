// Package config models winnow's runtime configuration. Values are stored in
// the DB `settings` table (editable from the dashboard) and overlaid on
// optional environment-variable bootstrap defaults — so a fresh deploy can come
// up with no env, and all connection details are set from the web UI.
package config

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

// Setting keys as stored in the DB `settings` table.
const (
	KeyHAURL      = "ha_url"
	KeyHAToken    = "ha_token"
	KeyMQTTHost   = "mqtt_host"
	KeyMQTTPort   = "mqtt_port"
	KeyMQTTUser   = "mqtt_user"
	KeyMQTTPass   = "mqtt_pass"
	KeyMQTTPrefix = "mqtt_prefix"
	// KeyMonitoredEntities is a JSON array of HA entity_ids whose summed power is
	// the "total monitored consumption" ground truth. One entity = a single
	// pre-aggregated sensor (power or energy/utility_meter); many = winnow sums.
	KeyMonitoredEntities = "monitored_entities"
	KeyThresholdW        = "threshold_w"
	KeyDefaultMultiplier = "default_multiplier"
	KeyDefaultUnit       = "default_unit"

	// Capture scan settings (read live by the capture service).
	KeyScanFreq     = "scan_freq"
	KeyScanGain     = "scan_gain"
	KeyScanPPM      = "scan_ppm"
	KeyScanMsgType  = "scan_msgtype"
	KeyScanFilterID = "scan_filterid"
	// KeyCaptureDevices is a JSON object keyed by source id (serial) →
	// {enabled, gain, label}. A device absent from the map is enabled by default.
	KeyCaptureDevices = "capture_devices"

	// Cost/tariff (used to estimate $ for published meters).
	KeyCostPerKwh = "cost_per_kwh"
	KeyCurrency   = "currency"
)

// SecretKeys are never returned in plaintext by the API (masked as set/unset).
var SecretKeys = map[string]bool{KeyHAToken: true, KeyMQTTPass: true}

// Config is the resolved runtime configuration.
type Config struct {
	HAURL             string
	HAToken           string
	MQTTHost          string
	MQTTPort          int
	MQTTUser          string
	MQTTPass          string
	MQTTPrefix        string
	MonitoredEntities []string
	ThresholdW        float64
	DefaultMultiplier float64
	DefaultUnit       string
	Capture           CaptureConfig
	CostPerKwh        float64
	Currency          string
}

// DeviceConfig is per-dongle capture configuration (keyed by source id). Every
// scan field is an optional override: empty means "inherit the global default".
type DeviceConfig struct {
	Enabled  *bool  `json:"enabled"` // nil/absent = enabled by default
	Label    string `json:"label"`
	Freq     string `json:"freq"`
	Gain     string `json:"gain"`
	PPM      string `json:"ppm"`
	MsgType  string `json:"msgtype"`
	FilterID string `json:"filterid"`
}

// CaptureConfig is the live scan configuration the capture service applies.
type CaptureConfig struct {
	Freq     string
	Gain     string
	PPM      string
	MsgType  string
	FilterID string
	Devices  map[string]DeviceConfig
}

// DeviceEnabled reports whether a dongle should be captured (default true).
func (c CaptureConfig) DeviceEnabled(source string) bool {
	if dc, ok := c.Devices[source]; ok && dc.Enabled != nil {
		return *dc.Enabled
	}
	return true
}

// pick returns the per-device override when set, else the global default.
func pick(override, def string) string {
	if override != "" {
		return override
	}
	return def
}

// Effective per-dongle scan settings (override beats the global default).
func (c CaptureConfig) DeviceFreq(source string) string     { return pick(c.Devices[source].Freq, c.Freq) }
func (c CaptureConfig) DeviceGain(source string) string     { return pick(c.Devices[source].Gain, c.Gain) }
func (c CaptureConfig) DevicePPM(source string) string      { return pick(c.Devices[source].PPM, c.PPM) }
func (c CaptureConfig) DeviceMsgType(source string) string  { return pick(c.Devices[source].MsgType, c.MsgType) }
func (c CaptureConfig) DeviceFilterID(source string) string { return pick(c.Devices[source].FilterID, c.FilterID) }

func parseDevices(v string) map[string]DeviceConfig {
	out := map[string]DeviceConfig{}
	if v = strings.TrimSpace(v); v == "" {
		return out
	}
	_ = json.Unmarshal([]byte(v), &out)
	return out
}

// parseEntities accepts a JSON array or a comma-separated list of entity_ids.
func parseEntities(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if strings.HasPrefix(v, "[") {
		var out []string
		if json.Unmarshal([]byte(v), &out) == nil {
			return cleanEntities(out)
		}
	}
	return cleanEntities(strings.Split(v, ","))
}

func cleanEntities(in []string) []string {
	out := []string{}
	for _, e := range in {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// FromMap overlays DB-stored settings on env bootstrap defaults. DB wins.
func FromMap(m map[string]string) Config {
	get := func(key, envName, def string) string {
		if v, ok := m[key]; ok && v != "" {
			return v
		}
		return env(envName, def)
	}
	port, _ := strconv.Atoi(get(KeyMQTTPort, "MQTT_PORT", "1883"))
	if port == 0 {
		port = 1883
	}
	thr, _ := strconv.ParseFloat(get(KeyThresholdW, "", "50"), 64)
	mult, _ := strconv.ParseFloat(get(KeyDefaultMultiplier, "", "1"), 64)
	if mult == 0 {
		mult = 1
	}
	cost, _ := strconv.ParseFloat(get(KeyCostPerKwh, "", "0"), 64)
	capture := CaptureConfig{
		Freq:     get(KeyScanFreq, "FREQ", "912600155"),
		Gain:     get(KeyScanGain, "GAIN", ""),
		PPM:      get(KeyScanPPM, "PPM", "0"),
		MsgType:  get(KeyScanMsgType, "RTLAMR_MSGTYPE", "scm,scm+,idm"),
		FilterID: get(KeyScanFilterID, "RTLAMR_FILTERID", ""),
		Devices:  parseDevices(get(KeyCaptureDevices, "", "")),
	}
	return Config{
		HAURL:             get(KeyHAURL, "HA_URL", ""),
		HAToken:           get(KeyHAToken, "HA_TOKEN", ""),
		MQTTHost:          get(KeyMQTTHost, "MQTT_HOST", ""),
		MQTTPort:          port,
		MQTTUser:          get(KeyMQTTUser, "MQTT_USER", ""),
		MQTTPass:          get(KeyMQTTPass, "MQTT_PASSWORD", ""),
		MQTTPrefix:        get(KeyMQTTPrefix, "MQTT_PREFIX", "homeassistant"),
		MonitoredEntities: parseEntities(get(KeyMonitoredEntities, "", "")),
		ThresholdW:        thr,
		DefaultMultiplier: mult,
		DefaultUnit:       get(KeyDefaultUnit, "", ""),
		Capture:           capture,
		CostPerKwh:        cost,
		Currency:          get(KeyCurrency, "", "$"),
	}
}

// HAConfigured reports whether HA REST/WS can be used.
func (c Config) HAConfigured() bool { return c.HAURL != "" && c.HAToken != "" }

// MQTTConfigured reports whether the MQTT publisher can connect.
func (c Config) MQTTConfigured() bool { return c.MQTTHost != "" }

// ReferenceConfigured reports whether a monitored set is configured.
func (c Config) ReferenceConfigured() bool { return len(c.MonitoredEntities) > 0 }

// DatabaseURL is infra config (not a dashboard setting).
func DatabaseURL() string {
	return env("DATABASE_URL", "postgres://winnow:winnow@localhost:5432/winnow")
}
