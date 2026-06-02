// Package agentwire is the shared, transport-independent protocol between the
// winnow API server and a remote capture agent. It defines the application
// messages and an authenticated, forward-secret session built on NaCl
// (Curve25519 + XSalsa20-Poly1305) so the data stream is encrypted and mutually
// authenticated by public key at the application layer — independent of whether
// any TLS/`wss` is in front. A reverse proxy that terminates TLS still only sees
// ciphertext.
package agentwire

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
	"golang.org/x/crypto/nacl/secretbox"

	"winnow/internal/model"
)

// context string bound into the handshake to prevent cross-protocol reuse.
const ctxTag = "winnow-agent-v1"

// Conn is the minimal framed transport the handshake/session ride on. Callers
// adapt their WebSocket to it (one Read/Write == one WS binary message).
type Conn interface {
	ReadMsg() ([]byte, error)
	WriteMsg([]byte) error
}

// --- application messages ---------------------------------------------------

// DeviceInfo describes one of an agent's local dongles (sent in hello).
type DeviceInfo struct {
	Source string `json:"source"`
	Name   string `json:"name"`
	Tuner  string `json:"tuner"`
}

// ReadingMsg carries one decoded reading plus its raw JSON line.
type ReadingMsg struct {
	Reading model.Reading `json:"r"`
	Raw     string        `json:"raw"`
}

// HeartbeatMsg mirrors db.UpdateHeartbeat's arguments.
type HeartbeatMsg struct {
	Source string `json:"source"`
	LastTS string `json:"last_ts"` // RFC3339Nano
	Total  int64  `json:"total"`
}

// SourceConfig is one dongle's resolved scan params, pushed server→agent.
type SourceConfig struct {
	Source   string `json:"source"`
	Enabled  bool   `json:"enabled"`
	Freq     string `json:"freq"`
	Gain     string `json:"gain"`
	PPM      string `json:"ppm"`
	MsgType  string `json:"msgtype"`
	FilterID string `json:"filterid"`
}

// Message is the tagged union exchanged after the handshake (sealed on the wire).
type Message struct {
	Type      string         `json:"type"` // hello | reading | heartbeat | config
	Agent     string         `json:"agent,omitempty"`
	Devices   []DeviceInfo   `json:"devices,omitempty"`
	Reading   *ReadingMsg    `json:"reading,omitempty"`
	Heartbeat *HeartbeatMsg  `json:"heartbeat,omitempty"`
	Config    []SourceConfig `json:"config,omitempty"`
}

// --- keys -------------------------------------------------------------------

// GenerateKey returns a fresh Curve25519 keypair (public, private).
func GenerateKey() (pub, priv [32]byte, err error) {
	p, s, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return pub, priv, err
	}
	return *p, *s, nil
}

// PublicFromPrivate derives the Curve25519 public key for a private key.
func PublicFromPrivate(priv [32]byte) ([32]byte, error) {
	var pub [32]byte
	p, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return pub, err
	}
	copy(pub[:], p)
	return pub, nil
}

func EncodeKey(k [32]byte) string { return base64.StdEncoding.EncodeToString(k[:]) }

func DecodeKey(s string) ([32]byte, error) {
	var k [32]byte
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return k, err
	}
	if len(b) != 32 {
		return k, fmt.Errorf("key must be 32 bytes, got %d", len(b))
	}
	copy(k[:], b)
	return k, nil
}

// Fingerprint is a short, human-comparable digest of a public key.
func Fingerprint(k [32]byte) string {
	h := sha256.Sum256(k[:])
	return base64.RawStdEncoding.EncodeToString(h[:])[:16]
}

// --- session ----------------------------------------------------------------

// Session is an established encrypted channel keyed by the ephemeral ECDH secret.
type Session struct{ key [32]byte }

func randNonce() ([24]byte, error) {
	var n [24]byte
	_, err := rand.Read(n[:])
	return n, err
}

// Seal encrypts a plaintext frame (nonce prepended).
func (s *Session) Seal(plain []byte) ([]byte, error) {
	n, err := randNonce()
	if err != nil {
		return nil, err
	}
	return secretbox.Seal(n[:], plain, &n, &s.key), nil
}

// Open decrypts a frame produced by Seal.
func (s *Session) Open(data []byte) ([]byte, error) {
	if len(data) < 24 {
		return nil, errors.New("short frame")
	}
	var n [24]byte
	copy(n[:], data[:24])
	out, ok := secretbox.Open(nil, data[24:], &n, &s.key)
	if !ok {
		return nil, errors.New("decrypt failed")
	}
	return out, nil
}

// SendMsg seals and writes a Message.
func (s *Session) SendMsg(c Conn, m Message) error {
	plain, err := json.Marshal(m)
	if err != nil {
		return err
	}
	sealed, err := s.Seal(plain)
	if err != nil {
		return err
	}
	return c.WriteMsg(sealed)
}

// RecvMsg reads, opens, and decodes a Message.
func (s *Session) RecvMsg(c Conn) (Message, error) {
	var m Message
	data, err := c.ReadMsg()
	if err != nil {
		return m, err
	}
	plain, err := s.Open(data)
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(plain, &m)
}

// --- handshake --------------------------------------------------------------
// Two-message mutually-authenticated ECDH. Each side boxes its ephemeral public
// key to the peer under its static key (proving private-key possession and
// binding the exchange); the session key is the ephemeral-ephemeral shared
// secret (forward secrecy).

type hsInit struct {
	ClientStaticPub string `json:"cspub"`
	ClientEphPub    string `json:"cepub"`
	Nonce           string `json:"n"`
	Box             string `json:"box"`
}
type hsResp struct {
	ServerEphPub string `json:"sepub"`
	Nonce        string `json:"n"`
	Box          string `json:"box"`
}

func seal(payload []byte, peerPub, myPriv [32]byte) (nonceB64, boxB64 string, err error) {
	n, err := randNonce()
	if err != nil {
		return "", "", err
	}
	sealed := box.Seal(nil, payload, &n, &peerPub, &myPriv)
	return base64.StdEncoding.EncodeToString(n[:]), base64.StdEncoding.EncodeToString(sealed), nil
}

func open(nonceB64, boxB64 string, peerPub, myPriv [32]byte) ([]byte, bool) {
	nb, err := base64.StdEncoding.DecodeString(nonceB64)
	if err != nil || len(nb) != 24 {
		return nil, false
	}
	cb, err := base64.StdEncoding.DecodeString(boxB64)
	if err != nil {
		return nil, false
	}
	var n [24]byte
	copy(n[:], nb)
	return box.Open(nil, cb, &n, &peerPub, &myPriv)
}

// ClientHandshake authenticates to the server and returns an encrypted session.
func ClientHandshake(c Conn, clientPub, clientPriv, serverPub [32]byte) (*Session, error) {
	ePub, ePriv, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	nonceB64, boxB64, err := seal(append(ePub[:], []byte(ctxTag)...), serverPub, clientPriv)
	if err != nil {
		return nil, err
	}
	init, err := json.Marshal(hsInit{
		ClientStaticPub: EncodeKey(clientPub), ClientEphPub: EncodeKey(ePub),
		Nonce: nonceB64, Box: boxB64,
	})
	if err != nil {
		return nil, err
	}
	if err := c.WriteMsg(init); err != nil {
		return nil, err
	}
	raw, err := c.ReadMsg()
	if err != nil {
		return nil, err
	}
	var resp hsResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("bad handshake response: %w", err)
	}
	sEphPub, err := DecodeKey(resp.ServerEphPub)
	if err != nil {
		return nil, err
	}
	payload, ok := open(resp.Nonce, resp.Box, serverPub, clientPriv)
	if !ok || len(payload) < 64 {
		return nil, errors.New("server authentication failed")
	}
	// payload = serverEphPub(32) || clientEphPub(32) || ctxTag — bind both keys
	var gotSEph, gotCEph [32]byte
	copy(gotSEph[:], payload[:32])
	copy(gotCEph[:], payload[32:64])
	if gotSEph != sEphPub || gotCEph != ePub || string(payload[64:]) != ctxTag {
		return nil, errors.New("handshake binding mismatch")
	}
	var key [32]byte
	box.Precompute(&key, &sEphPub, &ePriv)
	return &Session{key: key}, nil
}

// ServerHandshake verifies the client against `authorized` (which maps a client
// static public key to a label) and returns the session and that label.
func ServerHandshake(c Conn, serverPub, serverPriv [32]byte, authorized func(clientPub [32]byte) (string, bool)) (*Session, string, error) {
	raw, err := c.ReadMsg()
	if err != nil {
		return nil, "", err
	}
	var init hsInit
	if err := json.Unmarshal(raw, &init); err != nil {
		return nil, "", fmt.Errorf("bad handshake init: %w", err)
	}
	cStaticPub, err := DecodeKey(init.ClientStaticPub)
	if err != nil {
		return nil, "", err
	}
	label, ok := authorized(cStaticPub)
	if !ok {
		return nil, "", errors.New("agent key not authorized")
	}
	cEphPub, err := DecodeKey(init.ClientEphPub)
	if err != nil {
		return nil, "", err
	}
	payload, ok := open(init.Nonce, init.Box, cStaticPub, serverPriv)
	if !ok || len(payload) < 32 {
		return nil, "", errors.New("client authentication failed")
	}
	var gotCEph [32]byte
	copy(gotCEph[:], payload[:32])
	if gotCEph != cEphPub || string(payload[32:]) != ctxTag {
		return nil, "", errors.New("handshake binding mismatch")
	}
	sEphPub, sEphPriv, err := GenerateKey()
	if err != nil {
		return nil, "", err
	}
	respPayload := append(append(sEphPub[:], cEphPub[:]...), []byte(ctxTag)...)
	nonceB64, boxB64, err := seal(respPayload, cStaticPub, serverPriv)
	if err != nil {
		return nil, "", err
	}
	resp, err := json.Marshal(hsResp{ServerEphPub: EncodeKey(sEphPub), Nonce: nonceB64, Box: boxB64})
	if err != nil {
		return nil, "", err
	}
	if err := c.WriteMsg(resp); err != nil {
		return nil, "", err
	}
	var key [32]byte
	box.Precompute(&key, &cEphPub, &sEphPriv)
	return &Session{key: key}, label, nil
}
