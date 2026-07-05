package ha

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// These tests pin the liveness guarantees added after the July 2026 incident:
// a wedged or half-open HA connection must surface as an error the caller can
// reconnect from — never an indefinite silent hang.

func shrinkTimeouts(t *testing.T, handshake, ping, idle time.Duration) {
	t.Helper()
	oh, op, oi := wsHandshakeTimeout, wsPingEvery, wsIdleTimeout
	wsHandshakeTimeout, wsPingEvery, wsIdleTimeout = handshake, ping, idle
	t.Cleanup(func() { wsHandshakeTimeout, wsPingEvery, wsIdleTimeout = oh, op, oi })
}

// fakeHA runs a WebSocket server that performs HA's auth handshake, then hands
// the connection to handle.
func fakeHA(t *testing.T, handle func(ctx context.Context, c *websocket.Conn)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := r.Context()
		write := func(v any) error {
			b, _ := json.Marshal(v)
			return c.Write(ctx, websocket.MessageText, b)
		}
		_ = write(map[string]any{"type": "auth_required"})
		if _, _, err := c.Read(ctx); err != nil { // the auth message
			return
		}
		_ = write(map[string]any{"type": "auth_ok"})
		handle(ctx, c)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAuthConnDialDeadline(t *testing.T) {
	shrinkTimeouts(t, 300*time.Millisecond, time.Hour, time.Hour)
	// a TCP server that accepts and never answers the HTTP upgrade
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go io.Copy(io.Discard, conn) //nolint:errcheck // swallow the request, never respond
		}
	}()

	start := time.Now()
	_, _, _, err = authConn(context.Background(), "http://"+ln.Addr().String(), "tok")
	if err == nil {
		t.Fatal("dial against a black hole must error, not hang")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("handshake deadline did not bound the dial: took %s", elapsed)
	}
}

func TestAuthConnHandshakeDeadline(t *testing.T) {
	shrinkTimeouts(t, 300*time.Millisecond, time.Hour, time.Hour)
	// WS upgrade succeeds, but the server never sends auth_required
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		<-r.Context().Done()
		c.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(srv.Close)

	start := time.Now()
	_, _, _, err := authConn(context.Background(), srv.URL, "tok")
	if err == nil {
		t.Fatal("silent handshake must error, not hang")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("handshake deadline did not bound the auth read: took %s", elapsed)
	}
}

func TestStreamIdleWatchdog(t *testing.T) {
	shrinkTimeouts(t, 2*time.Second, time.Hour, 400*time.Millisecond)
	srv := fakeHA(t, func(ctx context.Context, c *websocket.Conn) {
		_, _, _ = c.Read(ctx) // the subscribe_trigger request
		<-ctx.Done()          // then total silence — a half-open connection
	})

	start := time.Now()
	err := Stream(context.Background(), srv.URL, "tok", []string{"sensor.a"}, func(string, Sample) {})
	if err == nil {
		t.Fatal("a silent connection must be declared dead")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("idle watchdog did not fire: took %s", elapsed)
	}
}

func TestStreamHeartbeatKeepsQuietLinkAlive(t *testing.T) {
	shrinkTimeouts(t, 2*time.Second, 100*time.Millisecond, 400*time.Millisecond)
	// the server answers pings but never sends an event — a healthy, quiet home
	srv := fakeHA(t, func(ctx context.Context, c *websocket.Conn) {
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var m struct {
				ID   int    `json:"id"`
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &m) == nil && m.Type == "ping" {
				b, _ := json.Marshal(map[string]any{"id": m.ID, "type": "pong"})
				if c.Write(ctx, websocket.MessageText, b) != nil {
					return
				}
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Stream(ctx, srv.URL, "tok", []string{"sensor.a"}, func(string, Sample) {})
	}()

	select {
	case err := <-done:
		t.Fatalf("stream died on a quiet-but-healthy link (pongs must reset the idle window): %v", err)
	case <-time.After(1200 * time.Millisecond): // 3× the idle window
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not return after context cancel")
	}
}
