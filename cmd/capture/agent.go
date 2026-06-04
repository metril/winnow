package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"winnow/internal/agentwire"
	"winnow/internal/model"
)

// runAgent is the remote-capture mode: enumerate local dongles, decode locally,
// and stream readings to the main app over an encrypted, mutually-authenticated
// session. Config is pushed by the server (no DB access from the remote host).
func runAgent(ctx context.Context) {
	name := env("AGENT_NAME", "agent")
	url := os.Getenv("AGENT_URL")
	serverKeyStr := os.Getenv("AGENT_SERVER_KEY")
	if url == "" || serverKeyStr == "" {
		log.Fatal("[agent] AGENT_URL and AGENT_SERVER_KEY are required in agent mode")
	}
	serverPub, err := agentwire.DecodeKey(serverKeyStr)
	if err != nil {
		log.Fatalf("[agent] invalid AGENT_SERVER_KEY: %v", err)
	}
	pub, priv := loadAgentKey()
	log.Printf("[agent] %q identity public key: %s", name, agentwire.EncodeKey(pub))
	log.Printf("[agent] authorize this key in the dashboard (fingerprint %s)", agentwire.Fingerprint(pub))

	for ctx.Err() == nil {
		if err := agentSession(ctx, name, url, pub, priv, serverPub); err != nil && ctx.Err() == nil {
			log.Printf("[agent] session ended: %v; reconnecting in 5s", err)
		}
		if !sleepCtx(ctx, 5*time.Second) {
			return
		}
	}
}

// loadAgentKey returns the agent's persistent Curve25519 keypair, from
// AGENT_PRIVATE_KEY or a key file (AGENT_KEY_FILE, default /data/agent.key),
// generating and persisting one on first run.
func loadAgentKey() (pub, priv [32]byte) {
	if s := os.Getenv("AGENT_PRIVATE_KEY"); s != "" {
		p, err := agentwire.DecodeKey(s)
		if err != nil {
			log.Fatalf("[agent] invalid AGENT_PRIVATE_KEY: %v", err)
		}
		pk, err := agentwire.PublicFromPrivate(p)
		if err != nil {
			log.Fatalf("[agent] deriving public key: %v", err)
		}
		return pk, p
	}
	path := env("AGENT_KEY_FILE", "/data/agent.key")
	if b, err := os.ReadFile(path); err == nil {
		if p, err := agentwire.DecodeKey(strings.TrimSpace(string(b))); err == nil {
			if pk, err := agentwire.PublicFromPrivate(p); err == nil {
				return pk, p
			}
		}
		log.Printf("[agent] key file %s unreadable; regenerating", path)
	}
	pk, p, err := agentwire.GenerateKey()
	if err != nil {
		log.Fatalf("[agent] generating key: %v", err)
	}
	if err := os.WriteFile(path, []byte(agentwire.EncodeKey(p)+"\n"), 0o600); err != nil {
		log.Printf("[agent] WARNING: could not persist key to %s (%v) — it will change on restart and need re-authorizing. Mount a volume there.", path, err)
	}
	return pk, p
}

// captureWS adapts a coder/websocket connection to agentwire.Conn.
type captureWS struct {
	c   *websocket.Conn
	ctx context.Context
}

func (w captureWS) ReadMsg() ([]byte, error) { _, d, err := w.c.Read(w.ctx); return d, err }
func (w captureWS) WriteMsg(b []byte) error  { return w.c.Write(w.ctx, websocket.MessageBinary, b) }

// wsSink streams decoded readings/heartbeats to the server. Writes from multiple
// dongle goroutines are serialized (coder/websocket forbids concurrent writes).
type wsSink struct {
	mu   sync.Mutex
	sess *agentwire.Session
	conn agentwire.Conn
}

func (s *wsSink) send(m agentwire.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sess.SendMsg(s.conn, m)
}
func (s *wsSink) InsertReading(ctx context.Context, r model.Reading, raw string) error {
	return s.send(agentwire.Message{Type: "reading", Reading: &agentwire.ReadingMsg{Reading: r, Raw: raw}})
}
func (s *wsSink) UpdateHeartbeat(ctx context.Context, source string, lastTS time.Time, total int64) error {
	return s.send(agentwire.Message{Type: "heartbeat", Heartbeat: &agentwire.HeartbeatMsg{
		Source: source, LastTS: lastTS.UTC().Format(time.RFC3339Nano), Total: total}})
}

// agentSession dials, authenticates, announces local dongles, and runs
// supervisors against server-pushed config until the connection drops.
func agentSession(ctx context.Context, name, url string, pub, priv, serverPub [32]byte) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	c, _, err := websocket.Dial(sctx, url, &websocket.DialOptions{HTTPClient: agentHTTPClient()})
	if err != nil {
		return err
	}
	c.SetReadLimit(1 << 20)
	defer c.Close(websocket.StatusNormalClosure, "")
	conn := captureWS{c: c, ctx: sctx}

	sess, err := agentwire.ClientHandshake(conn, pub, priv, serverPub)
	if err != nil {
		return err
	}
	log.Printf("[agent] connected & authenticated to %s", url)

	// Enumerate local dongles and announce them once (the server processes hello
	// only once and binds its config push to this source set).
	byIndex, infos := agentInventory(sctx, name)
	sink := &wsSink{sess: sess, conn: conn}
	if err := sink.send(agentwire.Message{Type: "hello", Agent: name, Devices: infos}); err != nil {
		return err
	}

	running := map[string]*runningDev{}
	defer func() {
		for _, rd := range running {
			rd.cancel()
			<-rd.done
		}
	}()

	// RecvMsg is blocking, so pump messages into a channel and let the session loop
	// also service a periodic health-check tick (the agent's analogue of the local
	// manager's rescan: re-enumerate and restart a dongle that's stuck down).
	msgs := make(chan agentwire.Message)
	recvErr := make(chan error, 1)
	go func() {
		for {
			m, err := sess.RecvMsg(conn)
			if err != nil {
				recvErr <- err
				return
			}
			select {
			case msgs <- m:
			case <-sctx.Done():
				return
			}
		}
	}()

	var lastCfg []agentwire.SourceConfig
	var lastRescan time.Time
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-sctx.Done():
			return sctx.Err()
		case err := <-recvErr:
			return err
		case msg := <-msgs:
			if msg.Type == "config" {
				lastCfg = msg.Config
				reconcileAgent(sctx, sink, running, byIndex, lastCfg)
			}
		case <-ticker.C:
			if !dongleRescanNeeded(running) || time.Since(lastRescan) <= 5*time.Minute {
				continue
			}
			log.Printf("[agent] a dongle is unhealthy; re-enumerating")
			lastRescan = time.Now()
			for src, rd := range running {
				rd.cancel()
				<-rd.done
				delete(running, src)
			}
			newByIndex, _ := agentInventory(sctx, name)
			if !sameSources(newByIndex, byIndex) {
				// the dongle set changed (new/removed serial) — reconnect so a fresh
				// hello re-announces it to the server, which only reads hello once.
				return errors.New("dongle set changed; reconnecting to re-announce")
			}
			byIndex = newByIndex
			reconcileAgent(sctx, sink, running, byIndex, lastCfg)
		}
	}
}

// agentInventory enumerates local dongles and builds the namespaced source map and
// the announce payload. Each source is prefixed with the agent name so it is
// globally unique and clearly attributed in the dashboard inventory/coverage.
func agentInventory(ctx context.Context, name string) (byIndex map[string]sdrDevice, infos []agentwire.DeviceInfo) {
	devs := enumerateRTL(ctx)
	byIndex = make(map[string]sdrDevice, len(devs))
	infos = make([]agentwire.DeviceInfo, 0, len(devs))
	for _, d := range devs {
		src := name + "/" + d.source
		byIndex[src] = d
		infos = append(infos, agentwire.DeviceInfo{Source: src, Name: d.name, Tuner: d.tuner})
	}
	log.Printf("[agent] announcing %d dongle(s): %s", len(devs), describe(devs))
	return byIndex, infos
}

// sameSources reports whether two namespaced inventories cover the same source ids
// (the device index may differ after a re-enumerate; the serial-based id does not).
func sameSources(a, b map[string]sdrDevice) bool {
	if len(a) != len(b) {
		return false
	}
	for src := range a {
		if _, ok := b[src]; !ok {
			return false
		}
	}
	return true
}

// reconcileAgent starts/stops dongle supervisors to match server-pushed config.
func reconcileAgent(ctx context.Context, sink Sink, running map[string]*runningDev, byIndex map[string]sdrDevice, cfg []agentwire.SourceConfig) {
	desired := map[string]scanParams{}
	for _, sc := range cfg {
		dev, ok := byIndex[sc.Source]
		if !ok || !sc.Enabled {
			continue
		}
		desired[sc.Source] = scanParams{
			devIndex: dev.index, freq: sc.Freq, gain: sc.Gain,
			ppm: sc.PPM, msgtype: sc.MsgType, filt: sc.FilterID,
		}
	}
	for src, rd := range running {
		if want, ok := desired[src]; !ok || want != rd.params {
			rd.cancel()
			<-rd.done
			delete(running, src)
		}
	}
	for src, want := range desired {
		if _, ok := running[src]; ok {
			continue
		}
		dctx, dcancel := context.WithCancel(ctx)
		done := make(chan struct{})
		up := &atomic.Bool{}
		running[src] = &runningDev{cancel: dcancel, params: want, done: done, up: up}
		go func(src string, p scanParams, up *atomic.Bool) {
			defer close(done)
			superviseSDR(dctx, sink, src, p, up)
		}(src, want, up)
	}
}

// agentHTTPClient builds the dial client. The session is already end-to-end
// encrypted and server-authenticated by Curve25519, so outer TLS is optional
// hardening: pin the cert via AGENT_SERVER_FINGERPRINT when set, otherwise accept
// the self-signed cert (the NaCl layer still authenticates the server).
func agentHTTPClient() *http.Client {
	pin := os.Getenv("AGENT_SERVER_FINGERPRINT")
	tlsConf := &tls.Config{InsecureSkipVerify: true} // app-layer auth is the real guard
	if pin != "" {
		tlsConf.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			for _, raw := range rawCerts {
				sum := sha256.Sum256(raw)
				if strings.EqualFold(base64.RawStdEncoding.EncodeToString(sum[:])[:16], pin) ||
					strings.EqualFold(hexFP(sum[:]), pin) {
					return nil
				}
			}
			return errors.New("server TLS fingerprint mismatch")
		}
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConf}}
}

func hexFP(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, x := range b {
		out = append(out, hexdigits[x>>4], hexdigits[x&0xf])
	}
	return string(out)
}
