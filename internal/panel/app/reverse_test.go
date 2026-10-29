package app

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/metacubex/yamux"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/config"
	"mikan/internal/panel/store/db"
)

// The panel's side of the reverse tunnel, end to end: a node opens the connection, the panel
// binds it to the node by its pinned certificate, and the panel's own client — the one the
// syncer uses — reaches the node's API over it while the node's own port answers nothing.
func TestNodeTunnel(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// The panel's client certificate: the one nodeClient loads, and the one the node's API pins.
	if _, err := nodetls.LoadOrCreate(filepath.Join(dir, "tls", "nodes"), time.Now()); err != nil {
		t.Fatal(err)
	}
	nodePair, err := nodetls.Generate("node-test", x509.ExtKeyUsageServerAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	nodePin, _ := nodetls.Fingerprint(nodePair.CertPEM)
	panelTLS, err := nodetls.Generate("panel-https", x509.ExtKeyUsageServerAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// The node: its own API port answers nothing (127.0.0.1:1), as a node the panel cannot reach.
	row, err := h.st.Q.CreateNode(ctx, db.CreateNodeParams{Name: "eu", Address: "127.0.0.1:1", PublicHost: "203.0.113.7", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.Q.SetNodeCert(ctx, db.SetNodeCertParams{CertSha256: nodePin, UpdatedAt: 2, ID: row.ID}); err != nil {
		t.Fatal(err)
	}
	row, err = h.st.Q.GetNode(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}

	tn := newTunnels(h.st, log, time.Now)
	addr, stop := tunnelListener(t, tn, panelTLS)
	defer stop()

	// The node side stands in for a real one: it does what internal/node does — its own
	// certificate as a client certificate, the upgrade, then yamux streams served over TLS.
	nodeAPI := &standNodeAPI{}
	conn := standConnect(t, addr, nodePair, mustPin(t, cfg), nodeAPI.handler())
	defer conn.Close()
	waitConnected(t, tn, row.ID)
	if v, ok := tn.View(row.ID); !ok || !v.Connected || v.Remote == "" {
		t.Fatalf("the tunnel is not up: %+v %v", v, ok)
	}

	// The panel reaches the node over the tunnel: the direct address answers nothing.
	c, err := nodeClient(cfg, row, tn)
	if err != nil {
		t.Fatal(err)
	}
	if health, err := c.Health(ctx); err != nil || health.Version != "test" {
		t.Fatalf("health over the tunnel: %+v %v", health, err)
	}
	if _, err := c.Apply(ctx, nodeapi.DesiredState{Revision: 7}); err != nil {
		t.Fatalf("state over the tunnel: %v", err)
	}
	if got := nodeAPI.revision(); got != 7 {
		t.Fatalf("the node applied revision %d, want 7", got)
	}
	// What the panel asked went over a stream, not the node's own port.
	if got := nodeAPI.paths(); len(got) != 2 || got[0] != "/v1/health" || got[1] != "/v1/state" {
		t.Fatalf("the node served %v", got)
	}

	// A second connection of the same node takes over: one tunnel per node.
	conn2 := standConnect(t, addr, nodePair, mustPin(t, cfg), nodeAPI.handler())
	defer conn2.Close()
	select {
	case <-conn.CloseChan():
	case <-time.After(10 * time.Second):
		t.Fatal("the older tunnel is still open after a newer connection took over")
	}
	waitConnected(t, tn, row.ID)

	// The tunnel goes away: the panel's calls fall back to the direct path, which answers
	// nothing — so the node is honestly unreachable.
	_ = conn2.Close()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, _ := tn.View(row.ID); !v.Connected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the tunnel is still counted as up after it closed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v, _ := tn.View(row.ID); v.Error == "" || v.Ended.IsZero() {
		t.Fatalf("the closed tunnel says nothing: %+v", v)
	}
	if _, err := c.Health(ctx); err == nil {
		t.Fatal("the node answered with no tunnel and no port")
	} else if code := nodeapi.Classify(err); code != nodeapi.LinkRefused && code != nodeapi.LinkUnknown && code != nodeapi.LinkTLS {
		t.Fatalf("the failure after the tunnel: %q %v", code, err)
	}
}

// A certificate the panel does not know is refused; a plain request without the tunnel's ALPN
// never reaches the route and is answered like any unknown path; a matching certificate gets
// the tunnel.
func TestNodeTunnelRefusesStrangers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := nodetls.LoadOrCreate(filepath.Join(dir, "tls", "nodes"), time.Now()); err != nil {
		t.Fatal(err)
	}
	known, _ := nodetls.Generate("node-known", x509.ExtKeyUsageServerAuth, time.Now())
	pin, _ := nodetls.Fingerprint(known.CertPEM)
	row, err := h.st.Q.CreateNode(ctx, db.CreateNodeParams{Name: "known", Address: "127.0.0.1:1", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.Q.SetNodeCert(ctx, db.SetNodeCertParams{CertSha256: pin, UpdatedAt: 2, ID: row.ID}); err != nil {
		t.Fatal(err)
	}
	tn := newTunnels(h.st, log, time.Now)
	panelTLS, _ := nodetls.Generate("panel-https", x509.ExtKeyUsageServerAuth, time.Now())
	addr, stop := tunnelListener(t, tn, panelTLS)
	defer stop()

	other, _ := nodetls.Generate("node-other", x509.ExtKeyUsageServerAuth, time.Now())
	if _, err := dialTunnel(t, addr, other, true); err == nil {
		t.Fatal("a node the panel does not know got a tunnel")
	}
	if v, ok := tn.View(row.ID); ok && v.Connected {
		t.Fatalf("an unknown node is connected: %+v", v)
	}
	// Without the tunnel's ALPN no client certificate is asked at all: the route answers like
	// any unknown path, and a browser is never prompted for one.
	if status, err := plainRequest(t, addr); err != nil || status != http.StatusNotFound {
		t.Fatalf("a plain request: %d %v", status, err)
	}
	// The same ALPN without a certificate: the handshake itself fails.
	if _, err := dialTunnelNoCert(t, addr); err == nil {
		t.Fatal("the tunnel took a client without a certificate")
	}
	conn := standConnect(t, addr, known, mustPin(t, cfg), (&standNodeAPI{}).handler())
	defer conn.Close()
	waitConnected(t, tn, row.ID)
}

// The node may open no stream of its own: the panel asks, the node answers.
func TestNodeTunnelResetsTheNodesStreams(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := nodetls.LoadOrCreate(filepath.Join(dir, "tls", "nodes"), time.Now()); err != nil {
		t.Fatal(err)
	}
	nodePair, _ := nodetls.Generate("node", x509.ExtKeyUsageServerAuth, time.Now())
	pin, _ := nodetls.Fingerprint(nodePair.CertPEM)
	row, err := h.st.Q.CreateNode(ctx, db.CreateNodeParams{Name: "n", Address: "127.0.0.1:1", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.Q.SetNodeCert(ctx, db.SetNodeCertParams{CertSha256: pin, UpdatedAt: 2, ID: row.ID}); err != nil {
		t.Fatal(err)
	}
	tn := newTunnels(h.st, log, time.Now)
	panelTLS, _ := nodetls.Generate("panel-https", x509.ExtKeyUsageServerAuth, time.Now())
	addr, stop := tunnelListener(t, tn, panelTLS)
	defer stop()
	conn := standConnect(t, addr, nodePair, mustPin(t, cfg), http.NotFoundHandler())
	defer conn.Close()
	waitConnected(t, tn, row.ID)
	// The node's side of the session: a stream it opens is reset by the panel at once, and
	// reading from it ends in an error rather than an answer.
	st, err := conn.OpenStream(context.Background())
	if err != nil {
		t.Fatalf("the node's own open: %v", err)
	}
	defer st.Close()
	_ = st.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1)
	if _, err := st.Read(buf); err == nil {
		t.Fatal("a stream the node opened was served")
	}
}

// standNodeAPI is an API on the node's side of the tunnel: it records what was asked of it.
type standNodeAPI struct {
	mu   sync.Mutex
	rev  int64
	list []string
}

func (s *standNodeAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		s.note(r.URL.Path)
		_ = json.NewEncoder(w).Encode(nodeapi.Health{Version: "test", Revision: s.revision()})
	})
	mux.HandleFunc("PUT /v1/state", func(w http.ResponseWriter, r *http.Request) {
		s.note(r.URL.Path)
		var state nodeapi.DesiredState
		if err := json.NewDecoder(r.Body).Decode(&state); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.rev = state.Revision
		s.mu.Unlock()
		_, _ = w.Write([]byte("{}"))
	})
	return mux
}

func (s *standNodeAPI) note(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, path)
}

func (s *standNodeAPI) revision() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rev
}

func (s *standNodeAPI) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.list...)
}

// tunnelListener serves the panel's side the way the panel's own listener does (serve.go): the
// tunnel's ALPN asks a client certificate, and nothing else does.
func tunnelListener(t *testing.T, tn *tunnels, panelTLS nodetls.Pair) (string, func()) {
	t.Helper()
	cert, err := tls.X509KeyPair([]byte(panelTLS.CertPEM), []byte(panelTLS.KeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	base := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}}
	tunnel := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert},
		NextProtos: []string{nodeapi.TunnelALPN}, ClientAuth: tls.RequireAnyClientCert}
	base.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		for _, p := range hello.SupportedProtos {
			if p == nodeapi.TunnelALPN {
				return tunnel, nil
			}
		}
		return nil, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !tn.serve(w, r) {
				http.NotFound(w, r)
			}
		}),
		ReadHeaderTimeout: 5 * time.Second,
		// As the panel's own listener does: net/http serves nothing on a connection with the
		// tunnel's ALPN, the hook does.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){nodeapi.TunnelALPN: serveTunnelProto},
	}
	go func() { _ = srv.Serve(tls.NewListener(ln, base)) }()
	return ln.Addr().String(), func() {
		_ = srv.Close()
		_ = ln.Close()
	}
}

// standConnect opens a tunnel as the node does and serves api over its streams, in TLS with the
// node's own API server config.
func standConnect(t *testing.T, addr string, node nodetls.Pair, panelPin string, api http.Handler) *yamux.Session {
	t.Helper()
	conn, err := dialTunnel(t, addr, node, true)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := yamux.Server(conn, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srvCfg, err := nodetls.Key{Port: 1, PanelPin: panelPin, CertPEM: node.CertPEM, KeyPEM: node.KeyPEM}.ServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			st, err := sess.AcceptStream()
			if err != nil {
				return
			}
			go func() {
				defer st.Close()
				var kind [1]byte
				if _, err := io.ReadFull(st, kind[:]); err != nil || kind[0] != nodeapi.StreamControl {
					return
				}
				tc := tls.Server(st, srvCfg)
				if err := tc.Handshake(); err != nil {
					return
				}
				srv := &http.Server{Handler: api, ReadHeaderTimeout: 5 * time.Second}
				_ = srv.Serve(oneConn(tc))
			}()
		}
	}()
	return sess
}

// dialTunnel asks for the tunnel over TLS, as the node does; with cert false no client
// certificate is presented.
func dialTunnel(t *testing.T, addr string, node nodetls.Pair, withCert bool) (net.Conn, error) {
	t.Helper()
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, NextProtos: []string{nodeapi.TunnelALPN}}
	if withCert {
		cert, err := tls.X509KeyPair([]byte(node.CertPEM), []byte(node.KeyPEM))
		if err != nil {
			t.Fatal(err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: cfg}
	conn, err := d.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequest(http.MethodGet, "https://"+addr+nodeapi.TunnelPath, nil)
	req.Host = addr
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", nodeapi.TunnelUpgrade)
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, errors.New("status " + strconv.Itoa(resp.StatusCode))
	}
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

func dialTunnelNoCert(t *testing.T, addr string) (net.Conn, error) {
	t.Helper()
	return dialTunnel(t, addr, nodetls.Pair{}, false)
}

// plainRequest asks for the tunnel without the tunnel's ALPN: no certificate is asked, and the
// route answers like any unknown path.
func plainRequest(t *testing.T, addr string) (int, error) {
	t.Helper()
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}}
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: cfg}
	conn, err := d.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	req, _ := http.NewRequest(http.MethodGet, "https://"+addr+nodeapi.TunnelPath, nil)
	req.Host = addr
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", nodeapi.TunnelUpgrade)
	if err := req.Write(conn); err != nil {
		return 0, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return 0, err
	}
	return resp.StatusCode, nil
}

func waitConnected(t *testing.T, tn *tunnels, id int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, ok := tn.View(id); ok && v.Connected {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the tunnel did not come up")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func mustPin(t *testing.T, cfg config.Config) string {
	t.Helper()
	pair, err := nodetls.LoadOrCreate(filepath.Join(cfg.DataDir, "tls", "nodes"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pin, _ := nodetls.Fingerprint(pair.CertPEM)
	return pin
}
