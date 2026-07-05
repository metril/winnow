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

// Liveness tuning (vars so tests can shrink them). HA sends nothing while no
// subscribed state changes, so Stream pings on a schedule (HA answers pong) and
// bounds every read: a connection silent past wsIdleTimeout is dead, not quiet.
// Without these, a half-open TCP connection (HA restart, proxy drop) could
// wedge a caller forever.
var (
	wsHandshakeTimeout = 30 * time.Second
	wsPingEvery        = 2 * time.Minute
	wsIdleTimeout      = 6 * time.Minute
)

// authConn dials HA's WebSocket API and completes the auth handshake, returning
// the open connection plus ctx-bound read/write JSON helpers. Callers must Close
// the connection. Shared by the live Stream subscription and the short-lived
// statistics request/response calls. The dial and handshake run under their own
// deadline so a black-holed connection errors instead of hanging; the returned
// helpers stay bound to the caller's ctx.
func authConn(ctx context.Context, base, token string) (*websocket.Conn, func(any) error, func(any) error, error) {
	hctx, hcancel := context.WithTimeout(ctx, wsHandshakeTimeout)
	defer hcancel()
	c, _, err := websocket.Dial(hctx, wsURL(base), nil)
	if err != nil {
		return nil, nil, nil, err
	}
	c.SetReadLimit(1 << 24) // statistics responses can be large
	readCtx := func(rctx context.Context, v any) error {
		_, data, err := c.Read(rctx)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, v)
	}
	read := func(v any) error { return readCtx(ctx, v) }
	write := func(v any) error {
		data, _ := json.Marshal(v)
		return c.Write(ctx, websocket.MessageText, data)
	}
	fail := func(err error) (*websocket.Conn, func(any) error, func(any) error, error) {
		c.Close(websocket.StatusInternalError, "")
		return nil, nil, nil, err
	}
	var msg map[string]any
	if err := readCtx(hctx, &msg); err != nil {
		return fail(err)
	}
	if msg["type"] != "auth_required" {
		return fail(fmt.Errorf("unexpected first message: %v", msg["type"]))
	}
	authMsg, _ := json.Marshal(map[string]any{"type": "auth", "access_token": token})
	if err := c.Write(hctx, websocket.MessageText, authMsg); err != nil {
		return fail(err)
	}
	if err := readCtx(hctx, &msg); err != nil {
		return fail(err)
	}
	if msg["type"] != "auth_ok" {
		return fail(fmt.Errorf("HA auth failed: %v", msg["type"]))
	}
	return c, read, write, nil
}

// Stream connects to HA's WebSocket API, authenticates, subscribes to state
// changes of all `entities`, and calls onSample(entity_id, sample) for each
// numeric update. Runs until ctx is cancelled or an error occurs (caller
// reconnects). A ping heartbeat plus a per-read idle deadline guarantee that a
// dead connection surfaces as an error within wsIdleTimeout — it can never
// hang silently.
func Stream(ctx context.Context, base, token string, entities []string, onSample func(string, Sample)) error {
	c, _, write, err := authConn(ctx, base, token)
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

	// 3. heartbeat — HA answers ping with pong, so a healthy-but-quiet link
	// always carries a frame within the idle window, and a dead one either
	// errors the write or starves the read deadline below
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	pingFail := make(chan error, 1)
	go func() {
		t := time.NewTicker(wsPingEvery)
		defer t.Stop()
		for id := 2; ; id++ { // ids must increase; 1 was the subscription
			select {
			case <-sctx.Done():
				return
			case <-t.C:
				data, _ := json.Marshal(map[string]any{"id": id, "type": "ping"})
				if err := c.Write(sctx, websocket.MessageText, data); err != nil {
					select {
					case pingFail <- err:
					default:
					}
					scancel() // wake the reader so Stream returns promptly
					return
				}
			}
		}
	}()

	// 4. event loop
	for {
		rctx, rcancel := context.WithTimeout(sctx, wsIdleTimeout)
		_, data, err := c.Read(rctx)
		rcancel()
		if err != nil {
			select {
			case perr := <-pingFail:
				return fmt.Errorf("heartbeat: %w", perr)
			default:
			}
			return err
		}
		var ev wsEvent
		if json.Unmarshal(data, &ev) != nil || ev.Type != "event" {
			continue // pong replies, result acks, or noise
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
