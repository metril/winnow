package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"log"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"winnow/internal/agentwire"
	"winnow/internal/config"
)

// pendingAgents tracks agents that connected but aren't yet authorized, so the
// dashboard can show them for one-click approval. It's in-memory (unauthenticated
// keys never touch the DB), bounded, and self-expiring: an agent retrying every 5s
// keeps its entry fresh; one that gives up ages out.
type pendingAgents struct {
	mu sync.Mutex
	m  map[string]*pendingAgent // keyed by base64 public key
}

type pendingAgent struct {
	PubKey      string
	Fingerprint string
	Name        string
	RemoteAddr  string
	FirstSeen   time.Time
	LastSeen    time.Time
}

const (
	pendingTTL = 10 * time.Minute
	pendingMax = 32
)

func newPendingAgents() *pendingAgents { return &pendingAgents{m: map[string]*pendingAgent{}} }

// add records (or refreshes) a connection attempt from an unauthorized key.
// Returns true only when the key was newly added (not a refresh of an existing
// entry) so callers can avoid re-notifying on a 5s-retry flapping agent.
func (p *pendingAgents) add(pub [32]byte, name, addr string) bool {
	key := agentwire.EncodeKey(pub)
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if e, ok := p.m[key]; ok {
		e.LastSeen = now
		e.RemoteAddr = addr
		if name != "" {
			e.Name = name
		}
		return false
	}
	p.gcLocked(now)
	if len(p.m) >= pendingMax {
		oldestKey, oldest := "", now
		for k, e := range p.m {
			if oldestKey == "" || e.LastSeen.Before(oldest) {
				oldestKey, oldest = k, e.LastSeen
			}
		}
		delete(p.m, oldestKey)
	}
	p.m[key] = &pendingAgent{
		PubKey: key, Fingerprint: agentwire.Fingerprint(pub),
		Name: name, RemoteAddr: addr, FirstSeen: now, LastSeen: now,
	}
	return true
}

func (p *pendingAgents) remove(pubkey string) {
	p.mu.Lock()
	delete(p.m, pubkey)
	p.mu.Unlock()
}

func (p *pendingAgents) list() []pendingAgent {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gcLocked(time.Now())
	out := make([]pendingAgent, 0, len(p.m))
	for _, e := range p.m {
		out = append(out, *e)
	}
	return out
}

func (p *pendingAgents) gcLocked(now time.Time) {
	for k, e := range p.m {
		if now.Sub(e.LastSeen) > pendingTTL {
			delete(p.m, k)
		}
	}
}

// agentCrypto holds the app's static identity for the remote-agent channel.
type agentCrypto struct {
	pub   [32]byte
	priv  [32]byte
	cert  tls.Certificate
	tlsFP string // hex SHA-256 of the TLS cert (for optional agent pinning)
}

// ensureAgentCrypto loads the app's Curve25519 keypair and self-signed TLS cert
// from settings, generating and persisting them on first boot. Both survive
// restarts (no extra volume) and need no external CA.
func ensureAgentCrypto(ctx context.Context, d settingsStore) (*agentCrypto, error) {
	m, err := d.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	ac := &agentCrypto{}

	if m[config.KeyAgentServerPub] == "" || m[config.KeyAgentServerPriv] == "" {
		pub, priv, err := agentwire.GenerateKey()
		if err != nil {
			return nil, err
		}
		_ = d.SetSetting(ctx, config.KeyAgentServerPub, agentwire.EncodeKey(pub))
		_ = d.SetSetting(ctx, config.KeyAgentServerPriv, agentwire.EncodeKey(priv))
		ac.pub, ac.priv = pub, priv
		log.Printf("[api] generated agent identity key %s", agentwire.Fingerprint(pub))
	} else {
		if ac.pub, err = agentwire.DecodeKey(m[config.KeyAgentServerPub]); err != nil {
			return nil, err
		}
		if ac.priv, err = agentwire.DecodeKey(m[config.KeyAgentServerPriv]); err != nil {
			return nil, err
		}
	}

	if m[config.KeyAgentTLSCert] == "" || m[config.KeyAgentTLSKey] == "" {
		certPEM, keyPEM, err := genSelfSignedCert()
		if err != nil {
			return nil, err
		}
		_ = d.SetSetting(ctx, config.KeyAgentTLSCert, string(certPEM))
		_ = d.SetSetting(ctx, config.KeyAgentTLSKey, string(keyPEM))
		m[config.KeyAgentTLSCert], m[config.KeyAgentTLSKey] = string(certPEM), string(keyPEM)
		log.Printf("[api] generated self-signed TLS cert for the agent listener")
	}
	cert, err := tls.X509KeyPair([]byte(m[config.KeyAgentTLSCert]), []byte(m[config.KeyAgentTLSKey]))
	if err != nil {
		return nil, err
	}
	ac.cert = cert
	if len(cert.Certificate) > 0 {
		sum := sha256.Sum256(cert.Certificate[0])
		ac.tlsFP = hexFP(sum[:])
	}
	return ac, nil
}

func hexFP(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, x := range b {
		out = append(out, hexdigits[x>>4], hexdigits[x&0xf])
	}
	return string(out)
}

// settingsStore is the slice of *db.DB the agent module needs (kept small so
// helpers are easy to test/read).
type settingsStore interface {
	GetSettings(ctx context.Context) (map[string]string, error)
	SetSetting(ctx context.Context, key, value string) error
}

func genSelfSignedCert() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "winnow-agent"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// agentReadIdle bounds every read from an agent connection. Updated agents
// send a keepalive ping every minute, so a connection silent past this window
// is dead (half-open TCP), not quiet — close it and let the agent reconnect.
// Var, not const, so tests can shrink it.
var agentReadIdle = 5 * time.Minute

// wsConn adapts a coder/websocket connection to agentwire.Conn (one binary
// message per frame). Each read is bounded by agentReadIdle so a half-open
// connection can never hang the ingest loop silently (the same invariant as
// internal/ha's stream watchdog).
type wsConn struct {
	c   *websocket.Conn
	ctx context.Context
}

func (w wsConn) ReadMsg() ([]byte, error) {
	rctx, cancel := context.WithTimeout(w.ctx, agentReadIdle)
	defer cancel()
	_, data, err := w.c.Read(rctx)
	return data, err
}
func (w wsConn) WriteMsg(b []byte) error {
	// bounded so a config push to a stuck agent can't hang its goroutine
	wctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
	defer cancel()
	return w.c.Write(wctx, websocket.MessageBinary, b)
}

// handleAgentWS terminates a remote capture agent: NaCl handshake (mutual
// public-key auth + session encryption), then ingest of decoded readings while
// pushing live config down on every winnow_config change.
func (s *server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	if s.agent == nil {
		http.Error(w, "agent channel not initialized", 503)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	c.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer c.Close(websocket.StatusNormalClosure, "")
	conn := wsConn{c: c, ctx: ctx}

	authorized := func(pub [32]byte) (string, bool) {
		m, _ := s.d.GetSettings(ctx)
		for _, a := range config.ParseAuthorizedAgents(m[config.KeyAgentAuthorized]) {
			if k, err := agentwire.DecodeKey(a.PubKey); err == nil && k == pub {
				s.pending.remove(agentwire.EncodeKey(pub)) // approved → no longer pending
				return a.Label, true
			}
		}
		return "", false
	}
	onUnauthorized := func(pub [32]byte, name string) {
		if s.pending.add(pub, name, r.RemoteAddr) { // newly pending → refresh the dashboard
			s.broker.publish([]byte(`{"type":"agent"}`))
		}
	}
	sess, label, err := agentwire.ServerHandshake(conn, s.agent.pub, s.agent.priv, authorized, onUnauthorized)
	if err != nil {
		log.Printf("[api] agent handshake rejected: %v", err)
		c.Close(websocket.StatusPolicyViolation, "unauthorized")
		return
	}
	log.Printf("[api] agent %q connected", label)
	s.broker.publish([]byte(`{"type":"agent"}`)) // a dongle just came online
	defer s.broker.publish([]byte(`{"type":"agent"}`))

	// First app message must be the hello carrying the agent's dongles.
	hello, err := sess.RecvMsg(conn)
	if err != nil || hello.Type != "hello" {
		return
	}
	sources := make([]string, 0, len(hello.Devices))
	for _, dev := range hello.Devices {
		name := dev.Name
		if name == "" {
			name = "remote dongle"
		}
		_ = s.d.UpsertDevice(ctx, dev.Source, 0, label+" · "+name, "remote")
		sources = append(sources, dev.Source)
	}

	// Push initial config, then push again on every config change.
	_ = sess.SendMsg(conn, agentwire.Message{Type: "config", Config: s.agentConfig(ctx, sources)})
	go s.pushConfigOnChange(ctx, conn, sess, sources)

	for {
		msg, err := sess.RecvMsg(conn)
		if err != nil {
			log.Printf("[api] agent %q disconnected: %v", label, err)
			return
		}
		switch msg.Type {
		case "reading":
			if msg.Reading != nil {
				_ = s.d.InsertReading(ctx, msg.Reading.Reading, msg.Reading.Raw)
			}
		case "ping":
			// connection keepalive only — deliberately NOT a heartbeat write:
			// capture_heartbeat.updated_at must keep meaning "data flowed", or
			// pings would mask a dead SDR from source_down and alive badges
		case "heartbeat":
			if hb := msg.Heartbeat; hb != nil {
				ts, _ := time.Parse(time.RFC3339Nano, hb.LastTS)
				if ts.IsZero() {
					ts = time.Now().UTC()
				}
				_ = s.d.UpdateHeartbeat(ctx, hb.Source, ts, hb.Total)
			}
		}
	}
}

// handleAgents returns the server's public key/fingerprint, the authorized-agent
// list, and the currently-known remote dongles (for the dashboard card).
func (s *server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if s.agent == nil {
		http.Error(w, "agent channel not initialized", 503)
		return
	}
	m, _ := s.d.GetSettings(r.Context())
	type authView struct {
		Label       string `json:"label"`
		PubKey      string `json:"pubkey"`
		Fingerprint string `json:"fingerprint"`
	}
	auth := []authView{}
	for _, a := range config.ParseAuthorizedAgents(m[config.KeyAgentAuthorized]) {
		fp := ""
		if k, err := agentwire.DecodeKey(a.PubKey); err == nil {
			fp = agentwire.Fingerprint(k)
		}
		auth = append(auth, authView{Label: a.Label, PubKey: a.PubKey, Fingerprint: fp})
	}
	// remote dongles (tuner='remote') from the inventory, with liveness
	type remoteView struct {
		Source   string  `json:"source"`
		Label    string  `json:"label"`
		Alive    bool    `json:"alive"`
		LastSeen *string `json:"last_seen"`
	}
	remotes := []remoteView{}
	if devs, err := s.d.ListDevices(r.Context(), 90*time.Second); err == nil {
		for _, d := range devs {
			if d.Tuner == "remote" {
				remotes = append(remotes, remoteView{Source: d.Serial, Label: d.Name, Alive: d.Alive, LastSeen: d.LastSeen})
			}
		}
	}
	// agents that connected but aren't authorized yet — for one-click approval.
	authedKeys := map[string]bool{}
	for _, a := range auth {
		authedKeys[a.PubKey] = true
	}
	type pendingView struct {
		PubKey      string `json:"pubkey"`
		Fingerprint string `json:"fingerprint"`
		Name        string `json:"name"`
		RemoteAddr  string `json:"remote_addr"`
		FirstSeen   string `json:"first_seen"`
		LastSeen    string `json:"last_seen"`
	}
	pending := []pendingView{}
	for _, p := range s.pending.list() {
		if authedKeys[p.PubKey] {
			continue
		}
		pending = append(pending, pendingView{
			PubKey: p.PubKey, Fingerprint: p.Fingerprint, Name: p.Name, RemoteAddr: p.RemoteAddr,
			FirstSeen: p.FirstSeen.UTC().Format(time.RFC3339), LastSeen: p.LastSeen.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, map[string]any{
		"server_key":         agentwire.EncodeKey(s.agent.pub),
		"server_fingerprint": agentwire.Fingerprint(s.agent.pub),
		"tls_fingerprint":    s.agent.tlsFP,
		"authorized":         auth,
		"remotes":            remotes,
		"pending":            pending,
	})
}

// handleAgentServerKey returns the server's static public key so an agent that
// wasn't handed AGENT_SERVER_KEY can trust-on-first-use pin it. The key is public
// (it only authenticates the server to agents), so this is intentionally
// unauthenticated; the optional cert-pinned TLS in front protects the fetch.
func (s *server) handleAgentServerKey(w http.ResponseWriter, r *http.Request) {
	if s.agent == nil {
		http.Error(w, "agent channel not initialized", 503)
		return
	}
	writeJSON(w, map[string]any{
		"server_key":         agentwire.EncodeKey(s.agent.pub),
		"server_fingerprint": agentwire.Fingerprint(s.agent.pub),
	})
}

// handleAuthorizeAgent adds (or relabels) an authorized agent public key.
func (s *server) handleAuthorizeAgent(w http.ResponseWriter, r *http.Request) {
	var body struct{ Label, PubKey string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badReq(w, "bad json")
		return
	}
	if _, err := agentwire.DecodeKey(body.PubKey); err != nil {
		badReq(w, "invalid public key")
		return
	}
	m, _ := s.d.GetSettings(r.Context())
	list := config.ParseAuthorizedAgents(m[config.KeyAgentAuthorized])
	found := false
	for i := range list {
		if list[i].PubKey == body.PubKey {
			list[i].Label = body.Label
			found = true
		}
	}
	if !found {
		list = append(list, config.AuthorizedAgent{Label: body.Label, PubKey: body.PubKey})
	}
	buf, _ := json.Marshal(list)
	if err := s.d.SetSetting(r.Context(), config.KeyAgentAuthorized, string(buf)); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.pending.remove(body.PubKey) // approved → drop from the pending list
	writeJSON(w, map[string]any{"ok": true})
}

// handleRevokeAgent removes an authorized agent public key.
func (s *server) handleRevokeAgent(w http.ResponseWriter, r *http.Request) {
	var body struct{ PubKey string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badReq(w, "bad json")
		return
	}
	m, _ := s.d.GetSettings(r.Context())
	out := []config.AuthorizedAgent{}
	for _, a := range config.ParseAuthorizedAgents(m[config.KeyAgentAuthorized]) {
		if a.PubKey != body.PubKey {
			out = append(out, a)
		}
	}
	buf, _ := json.Marshal(out)
	if err := s.d.SetSetting(r.Context(), config.KeyAgentAuthorized, string(buf)); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// agentConfig resolves the per-source scan params for one agent's dongles.
func (s *server) agentConfig(ctx context.Context, sources []string) []agentwire.SourceConfig {
	cfg, _ := s.d.LoadConfig(ctx)
	out := make([]agentwire.SourceConfig, 0, len(sources))
	for _, src := range sources {
		out = append(out, agentwire.SourceConfig{
			Source:   src,
			Enabled:  cfg.Capture.DeviceEnabled(src),
			Freq:     cfg.Capture.DeviceFreq(src),
			Gain:     cfg.Capture.DeviceGain(src),
			PPM:      cfg.Capture.DevicePPM(src),
			MsgType:  cfg.Capture.DeviceMsgType(src),
			FilterID: cfg.Capture.DeviceFilterID(src),
		})
	}
	return out
}

// pushConfigOnChange LISTENs winnow_config and pushes refreshed config to the
// agent whenever settings change (the same hot-reload local capture gets).
func (s *server) pushConfigOnChange(ctx context.Context, conn wsConn, sess *agentwire.Session, sources []string) {
	for ctx.Err() == nil {
		pc, err := s.d.Pool().Acquire(ctx)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		if _, err := pc.Exec(ctx, "LISTEN winnow_config"); err != nil {
			pc.Release()
			time.Sleep(2 * time.Second)
			continue
		}
		for ctx.Err() == nil {
			if _, err := pc.Conn().WaitForNotification(ctx); err != nil {
				break
			}
			if err := sess.SendMsg(conn, agentwire.Message{Type: "config", Config: s.agentConfig(ctx, sources)}); err != nil {
				pc.Release()
				return
			}
		}
		pc.Release()
	}
}
