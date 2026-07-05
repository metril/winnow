package agentwire

import (
	"io"
	"testing"
)

// chanConn is an in-memory Conn for tests; two crossed instances form a pipe.
type chanConn struct {
	in  chan []byte
	out chan []byte
}

func (c chanConn) ReadMsg() ([]byte, error) {
	b, ok := <-c.in
	if !ok {
		return nil, io.EOF
	}
	return b, nil
}
func (c chanConn) WriteMsg(b []byte) error {
	c.out <- b
	return nil
}

func pipePair() (client, server chanConn) {
	a := make(chan []byte, 16)
	b := make(chan []byte, 16)
	return chanConn{in: a, out: b}, chanConn{in: b, out: a}
}

// TestPingRoundTrip pins the keepalive wire format: a payload-less
// {"type":"ping"} survives the encrypted session in both directions and
// arrives typed — the server ignores it by design, and (compat) servers older
// than the ping fall through their message switch silently.
func TestPingRoundTrip(t *testing.T) {
	cliPub, cliPriv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	srvPub, srvPriv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	cliConn, srvConn := pipePair()
	type srvResult struct {
		sess  *Session
		label string
		err   error
	}
	srvCh := make(chan srvResult, 1)
	go func() {
		sess, label, err := ServerHandshake(srvConn, srvPub, srvPriv,
			func(pub [32]byte) (string, bool) { return "nomos", pub == cliPub },
			func([32]byte, string) {})
		srvCh <- srvResult{sess, label, err}
	}()

	cliSess, err := ClientHandshake(cliConn, cliPub, cliPriv, srvPub, "nomos")
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	srv := <-srvCh
	if srv.err != nil {
		t.Fatalf("server handshake: %v", srv.err)
	}
	if srv.label != "nomos" {
		t.Fatalf("label = %q", srv.label)
	}

	if err := cliSess.SendMsg(cliConn, Message{Type: "ping"}); err != nil {
		t.Fatalf("send ping: %v", err)
	}
	got, err := srv.sess.RecvMsg(srvConn)
	if err != nil {
		t.Fatalf("recv ping: %v", err)
	}
	if got.Type != "ping" {
		t.Fatalf("got type %q, want ping", got.Type)
	}
	if got.Reading != nil || got.Heartbeat != nil {
		t.Fatal("ping must carry no payload")
	}
}
