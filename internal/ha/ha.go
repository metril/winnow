// Package ha talks to Home Assistant: a REST client (history backfill +
// power-sensor discovery + connectivity test) and a WebSocket stream (live
// plug-power push). Auth is a long-lived access token.
package ha

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	base  string
	token string
	hc    *http.Client
}

func New(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		hc:    &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HA %s -> %d: %s", path, resp.StatusCode, truncate(body, 200))
	}
	return body, nil
}

// Test verifies reachability + auth (GET /api/ returns a running message).
func (c *Client) Test(ctx context.Context) error {
	_, err := c.get(ctx, "/api/")
	return err
}

// TimeZone returns Home Assistant's configured IANA timezone (e.g.
// "America/New_York") from GET /api/config, so calendar-period analysis (daily
// utility-bill breakdowns) can align to the user's local day, not UTC.
func (c *Client) TimeZone(ctx context.Context) (string, error) {
	body, err := c.get(ctx, "/api/config")
	if err != nil {
		return "", err
	}
	var cfg struct {
		TimeZone string `json:"time_zone"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return "", err
	}
	return cfg.TimeZone, nil
}

// Sample is one plug power reading.
type Sample struct {
	TS    time.Time
	Power float64
}

type haState struct {
	EntityID    string          `json:"entity_id"`
	State       string          `json:"state"`
	LastUpdated time.Time       `json:"last_updated"`
	Attributes  json.RawMessage `json:"attributes"`
}

// History fetches a plug's power history over [start,end].
func (c *Client) History(ctx context.Context, entity string, start, end time.Time) ([]Sample, error) {
	path := fmt.Sprintf("/api/history/period/%s?filter_entity_id=%s&end_time=%s&minimal_response=true",
		start.UTC().Format(time.RFC3339), entity,
		end.UTC().Format(time.RFC3339))
	body, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	var outer [][]haState
	if err := json.Unmarshal(body, &outer); err != nil {
		return nil, err
	}
	var out []Sample
	if len(outer) > 0 {
		for _, s := range outer[0] {
			if v, err := strconv.ParseFloat(s.State, 64); err == nil {
				out = append(out, Sample{TS: s.LastUpdated.UTC(), Power: v})
			}
		}
	}
	return out, nil
}

// Entity is a candidate reference sensor for the dashboard picker.
type Entity struct {
	EntityID string `json:"entity_id"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Unit     string `json:"unit"`
	Kind     string `json:"kind"` // "power" | "energy"
}

type stateAttrs struct {
	FriendlyName    string `json:"friendly_name"`
	DeviceClass     string `json:"device_class"`
	UnitMeasurement string `json:"unit_of_measurement"`
}

// classify returns "power", "energy", or "" for a sensor's attributes.
func classify(a stateAttrs) string {
	unit := strings.ToUpper(a.UnitMeasurement)
	switch {
	case a.DeviceClass == "power" || unit == "W" || unit == "KW":
		return "power"
	case a.DeviceClass == "energy" || unit == "WH" || unit == "KWH" || unit == "MWH":
		return "energy"
	default:
		return ""
	}
}

// MonitorableSensors lists sensor.* entities usable as a monitored-consumption
// reference: power (W/kW) and energy (kWh/Wh, incl. utility_meter), each labeled.
func (c *Client) MonitorableSensors(ctx context.Context) ([]Entity, error) {
	body, err := c.get(ctx, "/api/states")
	if err != nil {
		return nil, err
	}
	var states []haState
	if err := json.Unmarshal(body, &states); err != nil {
		return nil, err
	}
	out := []Entity{}
	for _, s := range states {
		if !strings.HasPrefix(s.EntityID, "sensor.") {
			continue
		}
		var a stateAttrs
		_ = json.Unmarshal(s.Attributes, &a)
		kind := classify(a)
		if kind == "" {
			continue
		}
		name := a.FriendlyName
		if name == "" {
			name = s.EntityID
		}
		out = append(out, Entity{EntityID: s.EntityID, Name: name, State: s.State, Unit: a.UnitMeasurement, Kind: kind})
	}
	return out, nil
}

// EntityKinds returns kind ("power"|"energy"|"") for the given entity_ids.
func (c *Client) EntityKinds(ctx context.Context, entities []string) (map[string]string, error) {
	body, err := c.get(ctx, "/api/states")
	if err != nil {
		return nil, err
	}
	var states []haState
	if err := json.Unmarshal(body, &states); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, e := range entities {
		want[e] = true
	}
	out := map[string]string{}
	for _, s := range states {
		if want[s.EntityID] {
			var a stateAttrs
			_ = json.Unmarshal(s.Attributes, &a)
			out[s.EntityID] = classify(a)
		}
	}
	return out, nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}
