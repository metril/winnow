package ha

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (c *Client) post(ctx context.Context, path string, body any) ([]byte, error) {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HA %s -> %d: %s", path, resp.StatusCode, truncate(b, 300))
	}
	return b, nil
}

type flowStep struct {
	FlowID string `json:"flow_id"`
	Type   string `json:"type"` // form | menu | create_entry | abort
	StepID string `json:"step_id"`
	Reason string `json:"reason"`
}

// CreateGroupSum creates a Home Assistant Sensor Group helper that SUMs the
// given entities, via HA's config-flow REST API. deviceClass ("power"/"energy")
// makes the resulting sensor self-classify so winnow auto-detects it. Returns
// the new entity_id. Errors are descriptive so the UI can fall back to manual
// setup instructions.
func (c *Client) CreateGroupSum(ctx context.Context, name string, entities []string, deviceClass string) (string, error) {
	// 1. start the "group" helper flow
	b, err := c.post(ctx, "/api/config/config_entries/flow",
		map[string]any{"handler": "group", "show_advanced_options": true})
	if err != nil {
		return "", err
	}
	var step flowStep
	_ = json.Unmarshal(b, &step)
	if step.FlowID == "" {
		return "", fmt.Errorf("HA did not start a group flow: %s", truncate(b, 200))
	}
	// 2. choose the "sensor" group type (the first step is a menu)
	if _, err := c.post(ctx, "/api/config/config_entries/flow/"+step.FlowID,
		map[string]any{"next_step_id": "sensor"}); err != nil {
		return "", err
	}
	// 3. submit the sensor-group config
	input := map[string]any{
		"name":               name,
		"entities":           entities,
		"type":               "sum",
		"ignore_non_numeric": true,
	}
	if deviceClass != "" {
		input["device_class"] = deviceClass
	}
	b, err = c.post(ctx, "/api/config/config_entries/flow/"+step.FlowID, input)
	if err != nil {
		return "", err
	}
	var done flowStep
	_ = json.Unmarshal(b, &done)
	if done.Type != "create_entry" {
		return "", fmt.Errorf("helper not created (HA returned step %q): %s", done.StepID, truncate(b, 200))
	}
	return c.findSensorByName(ctx, name)
}

func (c *Client) findSensorByName(ctx context.Context, name string) (string, error) {
	for i := 0; i < 6; i++ {
		body, err := c.get(ctx, "/api/states")
		if err == nil {
			var states []haState
			_ = json.Unmarshal(body, &states)
			for _, s := range states {
				if !strings.HasPrefix(s.EntityID, "sensor.") {
					continue
				}
				var a stateAttrs
				_ = json.Unmarshal(s.Attributes, &a)
				if a.FriendlyName == name {
					return s.EntityID, nil
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "sensor." + slug(name), nil // best-effort fallback
}

func slug(s string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevUnderscore = false
		default:
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}
