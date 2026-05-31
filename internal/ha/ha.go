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

// Entity is a candidate reference power sensor for the dashboard picker.
type Entity struct {
	EntityID string `json:"entity_id"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Unit     string `json:"unit"`
}

type stateAttrs struct {
	FriendlyName    string `json:"friendly_name"`
	DeviceClass     string `json:"device_class"`
	UnitMeasurement string `json:"unit_of_measurement"`
}

// PowerSensors lists sensor.* entities with device_class=power (or unit W/kW).
func (c *Client) PowerSensors(ctx context.Context) ([]Entity, error) {
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
		unit := strings.ToUpper(a.UnitMeasurement)
		if a.DeviceClass == "power" || unit == "W" || unit == "KW" {
			name := a.FriendlyName
			if name == "" {
				name = s.EntityID
			}
			out = append(out, Entity{EntityID: s.EntityID, Name: name, State: s.State, Unit: a.UnitMeasurement})
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
