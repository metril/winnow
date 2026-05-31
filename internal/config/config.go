// Package config models winnow's runtime configuration. Values are stored in
// the DB `settings` table (editable from the dashboard) and overlaid on
// optional environment-variable bootstrap defaults — so a fresh deploy can come
// up with no env, and all connection details are set from the web UI.
package config

import (
	"os"
	"strconv"
)

// Setting keys as stored in the DB `settings` table.
const (
	KeyHAURL             = "ha_url"
	KeyHAToken           = "ha_token"
	KeyMQTTHost          = "mqtt_host"
	KeyMQTTPort          = "mqtt_port"
	KeyMQTTUser          = "mqtt_user"
	KeyMQTTPass          = "mqtt_pass"
	KeyMQTTPrefix        = "mqtt_prefix"
	KeyReferenceEntity   = "reference_entity"
	KeyThresholdW        = "threshold_w"
	KeyDefaultMultiplier = "default_multiplier"
	KeyDefaultUnit       = "default_unit"
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
	ReferenceEntity   string
	ThresholdW        float64
	DefaultMultiplier float64
	DefaultUnit       string
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
	return Config{
		HAURL:             get(KeyHAURL, "HA_URL", ""),
		HAToken:           get(KeyHAToken, "HA_TOKEN", ""),
		MQTTHost:          get(KeyMQTTHost, "MQTT_HOST", ""),
		MQTTPort:          port,
		MQTTUser:          get(KeyMQTTUser, "MQTT_USER", ""),
		MQTTPass:          get(KeyMQTTPass, "MQTT_PASSWORD", ""),
		MQTTPrefix:        get(KeyMQTTPrefix, "MQTT_PREFIX", "homeassistant"),
		ReferenceEntity:   get(KeyReferenceEntity, "", ""),
		ThresholdW:        thr,
		DefaultMultiplier: mult,
		DefaultUnit:       get(KeyDefaultUnit, "", ""),
	}
}

// HAConfigured reports whether HA REST/WS can be used.
func (c Config) HAConfigured() bool { return c.HAURL != "" && c.HAToken != "" }

// MQTTConfigured reports whether the MQTT publisher can connect.
func (c Config) MQTTConfigured() bool { return c.MQTTHost != "" }

// DatabaseURL is infra config (not a dashboard setting).
func DatabaseURL() string {
	return env("DATABASE_URL", "postgres://winnow:winnow@localhost:5432/winnow")
}
