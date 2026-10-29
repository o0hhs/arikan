package cascade

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/metacubex/yamux"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
)

// stand is a panel with its cascade hub, an exit node and an entry node, all in this
// process: the exit knocks on the panel's endpoint, the entry's bridge is the panel's dial
// to a pipe, and the test holds both node ends.
type stand struct {
	t     *testing.T
	hub   *Hub
	key   nodetls.Key
	srv   *httptest.Server
	dials chan net.Conn
}

const testEntry int64 = 1
const testExit int64 = 3

func mux(t *testing.T) *yamux.Config {
	t.Helper()
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	return cfg
}

func newStand(t *testing.T, exits ...int64) *stand {
	t.Helper()
	now := time.Now()
	pair, err := nodetls.Generate("node-3.mikan", x509.ExtKeyUsageServerAuth, now)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := nodetls.Fingerprint(pair.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	s := &stand{t: t, key: nodetls.Key{CertPEM: pair.CertPEM, KeyPEM: pair.KeyPEM}, dials: make(chan net.Conn, 4)}
	s.hub = New(Deps{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: time.Now,
		NodeByPin: func(_ context.Context, p string) (int64, bool) {
			if nodetls.SamePin(p, pin) {
				return testExit, true
			}
			return 0, false
		},
		IsExit: func(_ context.Context, id int64) (bool, error) {
			for _, x := range exits {
				if x == id {
					return true, nil
				}
			}
			return false, nil
		},
		Dial: func(_ context.Context, _ int64) (net.Conn, error) {
			panel, node := net.Pipe()
			s.dials <- node
			return panel, nil
		},
	})
	t.Cleanup(s.hub.Close)
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hub.Endpoint(w, r) {
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// exit keeps the exit's stream: a knock, then the multiplexer the panel opens streams on.
func (s *stand) exit() *yamux.Session {
	s.t.Helper()
	hello, err := nodeapi.NewCascadeHello(s.key, time.Now())
	if err != nil {
		s.t.Fatal(err)
	}
	raw, err := json.Marshal(hello)
	if err != nil {
		s.t.Fatal(err)
	}
	resp, err := s.srv.Client().Post(s.srv.URL+nodeapi.CascadePath, "application/json", bytes.NewReader(raw))
	if err != nil {
		s.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		s.t.Fatalf("the panel did not take the exit: %s", resp.Status)
	}
	rw, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		s.t.Fatal("the 101 answer carries no stream")
	}
	sess, err := yamux.Server(streamConn{rw}, mux(s.t), nil)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// streamConn is the 101's body as the multiplexer wants it: a net.Conn. The test's
// connection has no addresses and no deadlines to keep (a real node dials its own and has
// both).
type streamConn struct{ io.ReadWriteCloser }

func (streamConn) LocalAddr() net.Addr              { return streamAddr{} }
func (streamConn) RemoteAddr() net.Addr             { return streamAddr{} }
func (streamConn) SetDeadline(time.Time) error      { return nil }
func (streamConn) SetReadDeadline(time.Time) error  { return nil }
func (streamConn) SetWriteDeadline(time.Time) error { return nil }

type streamAddr struct{}

func (streamAddr) Network() string { return "cascade-test" }
func (streamAddr) String() string  { return "cascade-test" }

// echoes answers every stream the panel opens: the target, then the connection's bytes back.
func echoes(t *testing.T, sess *yamux.Session, targets chan<- nodeapi.CascadeTarget) {
	t.Helper()
	go func() {
		for {
			st, err := sess.AcceptStream()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = st.Close() }()
				_ = st.SetReadDeadline(time.Now().Add(5 * time.Second))
				target, err := nodeapi.ReadCascadeTarget(st)
				if err != nil {
					return
				}
				_ = st.SetReadDeadline(time.Time{})
				if targets != nil {
					targets <- target
				}
				_, _ = io.Copy(st, st)
			}()
		}
	}()
}

// entry is the node's side of the bridge: the other end of the panel's dial, with the
// routes the panel gave it.
func (s *stand) entry(routes ...int64) *yamux.Session {
	s.t.Helper()
	s.hub.SetRoutes(testEntry, routes)
	var node net.Conn
	select {
	case node = <-s.dials:
	case <-time.After(5 * time.Second):
		s.t.Fatal("the panel did not open a bridge")
	}
	sess, err := yamux.Client(node, mux(s.t), nil)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// ask opens a stream and asks for exit; the answer is nil when the panel paired it.
func ask(t *testing.T, sess *yamux.Session, exit int64) *yamux.Stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := sess.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetDeadline(time.Now().Add(5 * time.Second))
	if err := nodeapi.WriteCascadeAsk(st, nodeapi.CascadeStreamData, exit); err != nil {
		t.Fatal(err)
	}
	return st
}

// The whole way: a stream of the entry's bridge is paired with the exit's connection, and
// the bytes cross both.
func TestTheHubPairsAStreamWithTheExit(t *testing.T) {
	s := newStand(t, testExit)
	targets := make(chan nodeapi.CascadeTarget, 1)
	echoes(t, s.exit(), targets)
	entry := s.entry(testExit)

	st := ask(t, entry, testExit)
	if err := nodeapi.ReadCascadeAnswer(st); err != nil {
		t.Fatalf("the panel refused: %v", err)
	}
	if err := nodeapi.WriteCascadeTarget(st, nodeapi.CascadeTarget{Host: "example.com", Port: 443}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(st, buf); err != nil {
		t.Fatalf("the exit's answer: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("the exit answered %q", buf)
	}
	select {
	case target := <-targets:
		if target.Host != "example.com" || target.Port != 443 {
			t.Fatalf("the exit got %+v", target)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the exit got no target")
	}
}

// An exit the node's state does not name is not a stream the panel pairs: the bridge of one
// entry is no door to every exit of the panel.
func TestTheHubRefusesAnExitTheEntryDoesNotName(t *testing.T) {
	s := newStand(t, testExit)
	echoes(t, s.exit(), nil)
	entry := s.entry(testExit)

	st := ask(t, entry, testExit+1)
	var refused *nodeapi.CascadeRefused
	if err := nodeapi.ReadCascadeAnswer(st); !errors.As(err, &refused) {
		t.Fatalf("an exit the entry does not name: %v", err)
	}
}

// An exit that is not in the tunnel is refused, and the bridge stays up for the reserves.
func TestTheHubRefusesAnExitThatIsNotInTheTunnel(t *testing.T) {
	s := newStand(t, testExit, testExit+1)
	entry := s.entry(testExit, testExit+1)

	st := ask(t, entry, testExit)
	var refused *nodeapi.CascadeRefused
	if err := nodeapi.ReadCascadeAnswer(st); !errors.As(err, &refused) {
		t.Fatalf("an exit with no stream: %v", err)
	}
	if _, ok := s.hub.BridgeOnline(testEntry); !ok {
		t.Fatal("the bridge must stay up")
	}
}

// Nothing but a node that is an exit, with a knock its own certificate signed, is taken.
func TestTheHubTakesOnlyARealExit(t *testing.T) {
	s := newStand(t, testExit) // the key of the stand is node 3, an exit
	other, err := nodetls.Generate("node-9.mikan", x509.ExtKeyUsageServerAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	hello, err := nodeapi.NewCascadeHello(nodetls.Key{CertPEM: other.CertPEM, KeyPEM: other.KeyPEM}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(hello)
	resp, err := s.srv.Client().Post(s.srv.URL+nodeapi.CascadePath, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a node the panel does not know: %s", resp.Status)
	}

	// A node of the panel that is not an exit is no exit either.
	quiet := newStand(t)
	hello, err = nodeapi.NewCascadeHello(quiet.key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(hello)
	resp, err = quiet.srv.Client().Post(quiet.srv.URL+nodeapi.CascadePath, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a node that is no exit: %s", resp.Status)
	}
}

// A knock replayed is no second stream of the same exit.
func TestTheHubRefusesAReplayedKnock(t *testing.T) {
	s := newStand(t, testExit)
	hello, err := nodeapi.NewCascadeHello(s.key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(hello)
	first, err := s.srv.Client().Post(s.srv.URL+nodeapi.CascadePath, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if first.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("the first knock: %s", first.Status)
	}
	_ = first.Body.Close()
	second, err := s.srv.Client().Post(s.srv.URL+nodeapi.CascadePath, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Body.Close()
	if second.StatusCode != http.StatusNotFound {
		t.Fatalf("a replayed knock: %s", second.Status)
	}
}
