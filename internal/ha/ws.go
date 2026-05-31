package ha

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Stream connects to HA's WebSocket API, authenticates, subscribes to state
// changes of `entity`, and calls onSample for each numeric update. It runs
// until ctx is cancelled or an error occurs (the caller reconnects).
func Stream(ctx context.Context, base, token, entity string, onSample func(Sample)) error {
	url := wsURL(base)
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return err
	}
	c.SetReadLimit(1 << 20)
	defer c.Close(websocket.StatusNormalClosure, "")

	read := func(v any) error {
		_, data, err := c.Read(ctx)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, v)
	}
	write := func(v any) error {
		data, _ := json.Marshal(v)
		return c.Write(ctx, websocket.MessageText, data)
	}

	// 1. auth handshake
	var msg map[string]any
	if err := read(&msg); err != nil {
		return err
	}
	if msg["type"] != "auth_required" {
		return fmt.Errorf("unexpected first message: %v", msg["type"])
	}
	if err := write(map[string]any{"type": "auth", "access_token": token}); err != nil {
		return err
	}
	if err := read(&msg); err != nil {
		return err
	}
	if msg["type"] != "auth_ok" {
		return fmt.Errorf("HA auth failed: %v", msg["type"])
	}

	// 2. subscribe to the entity's state changes
	if err := write(map[string]any{
		"id":      1,
		"type":    "subscribe_trigger",
		"trigger": map[string]any{"platform": "state", "entity_id": entity},
	}); err != nil {
		return err
	}

	// 3. event loop
	for {
		var ev wsEvent
		if err := read(&ev); err != nil {
			return err
		}
		if ev.Type != "event" {
			continue
		}
		ts := ev.Event.Variables.Trigger.ToState.LastUpdated
		if ts.IsZero() {
			ts = time.Now().UTC()
		}
		if v, err := strconv.ParseFloat(ev.Event.Variables.Trigger.ToState.State, 64); err == nil {
			onSample(Sample{TS: ts.UTC(), Power: v})
		}
	}
}

type wsEvent struct {
	Type  string `json:"type"`
	Event struct {
		Variables struct {
			Trigger struct {
				ToState struct {
					State       string    `json:"state"`
					LastUpdated time.Time `json:"last_updated"`
				} `json:"to_state"`
			} `json:"trigger"`
		} `json:"variables"`
	} `json:"event"`
}

func wsURL(base string) string {
	u := strings.TrimRight(base, "/")
	u = strings.Replace(u, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	return u + "/api/websocket"
}
