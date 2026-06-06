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

// authConn dials HA's WebSocket API and completes the auth handshake, returning
// the open connection plus ctx-bound read/write JSON helpers. Callers must Close
// the connection. Shared by the live Stream subscription and the short-lived
// statistics request/response calls.
func authConn(ctx context.Context, base, token string) (*websocket.Conn, func(any) error, func(any) error, error) {
	c, _, err := websocket.Dial(ctx, wsURL(base), nil)
	if err != nil {
		return nil, nil, nil, err
	}
	c.SetReadLimit(1 << 24) // statistics responses can be large
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
	var msg map[string]any
	if err := read(&msg); err != nil {
		c.Close(websocket.StatusInternalError, "")
		return nil, nil, nil, err
	}
	if msg["type"] != "auth_required" {
		c.Close(websocket.StatusInternalError, "")
		return nil, nil, nil, fmt.Errorf("unexpected first message: %v", msg["type"])
	}
	if err := write(map[string]any{"type": "auth", "access_token": token}); err != nil {
		c.Close(websocket.StatusInternalError, "")
		return nil, nil, nil, err
	}
	if err := read(&msg); err != nil {
		c.Close(websocket.StatusInternalError, "")
		return nil, nil, nil, err
	}
	if msg["type"] != "auth_ok" {
		c.Close(websocket.StatusInternalError, "")
		return nil, nil, nil, fmt.Errorf("HA auth failed: %v", msg["type"])
	}
	return c, read, write, nil
}

// Stream connects to HA's WebSocket API, authenticates, subscribes to state
// changes of all `entities`, and calls onSample(entity_id, sample) for each
// numeric update. Runs until ctx is cancelled or an error occurs (caller
// reconnects).
func Stream(ctx context.Context, base, token string, entities []string, onSample func(string, Sample)) error {
	c, read, write, err := authConn(ctx, base, token)
	if err != nil {
		return err
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// 2. subscribe to state changes of all entities (entity_id accepts a list)
	if err := write(map[string]any{
		"id":      1,
		"type":    "subscribe_trigger",
		"trigger": map[string]any{"platform": "state", "entity_id": entities},
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
		to := ev.Event.Variables.Trigger.ToState
		ts := to.LastUpdated
		if ts.IsZero() {
			ts = time.Now().UTC()
		}
		if v, err := strconv.ParseFloat(to.State, 64); err == nil {
			onSample(to.EntityID, Sample{TS: ts.UTC(), Power: v})
		}
	}
}

type wsEvent struct {
	Type  string `json:"type"`
	Event struct {
		Variables struct {
			Trigger struct {
				ToState struct {
					EntityID    string    `json:"entity_id"`
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
