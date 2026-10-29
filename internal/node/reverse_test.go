package node

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/metacubex/yamux"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
)

// reflectPanel stands in for the panel's side of the tunnel: the ALPN that asks a client
// certificate, the upgrade, then yamux streams the test drives.
type reflectPanel struct {
	addr     string
	stop     func()
	sessions chan *yamux.Session
}

func newReflectPanel(t *testing.T, panelTLSPair nodetls.Pair) *reflectPanel {
	t.Helper()
	return listenOn(t, "127.0.0.1:0", panelTLSPair)
}

// session is the next connection the node opened.
func (p *reflectPanel) session(t *testing.T) *yamux.Session {
	t.Helper()
	select {
	case s := <-p.sessions:
		return s
	case <-time.After(15 * time.Second):
		t.Fatal("the node did not open a tunnel")
		return nil
	}
}

// api asks the node's API over one stream of the tunnel, in TLS as the panel does.
func (p *reflectPanel) api(t *testing.T, sess *yamux.Session, req *http.Request, panel nodetls.Pair, nodePin string) *http.Response {
	t.Helper()
	conn := p.stream(t, sess, nodeapi.StreamControl)
	cfg, err := nodetls.ClientConfig(panel, nodePin)
	if err != nil {
		t.Fatal(err)
	}
	tc := tls.Client(conn, cfg)
	if err := tc.Handshake(); err != nil {
		t.Fatalf("the node's API handshake: %v", err)
	}
	if err := req.Write(tc); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(tc), req)
	if err != nil {
		t.Fatalf("the API's answer: %v", err)
	}
	return resp
}

// stream opens a stream and writes its type byte.
func (p *reflectPanel) stream(t *testing.T, sess *yamux.Session, kind byte) net.Conn {
	t.Helper()
	st, err := sess.OpenStream(context.Background())
	if err != nil {
		t.Fatalf("open a stream: %v", err)
	}
	if _, err := st.Write([]byte{kind}); err != nil {
		t.Fatalf("the stream's type: %v", err)
	}
	return st
}

// The node serves its API over the tunnel it opens to the panel: the state arrives, the node
// applies it, and health comes back — with its own API port closed the whole time.
func TestReverseTunnelServesTheAPI(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	e, err := Start(Options{DataDir: dir, Version: "test", Log: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.Apply(nodeapi.DesiredState{Revision: 99}) })
	panelTLSPair, _ := nodetls.Generate("panel-https", x509.ExtKeyUsageServerAuth, time.Now())
	nodePair, err := nodetls.Generate("node", x509.ExtKeyUsageServerAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	nodePin, _ := nodetls.Fingerprint(nodePair.CertPEM)
	panelClient, _ := nodetls.Generate("panel", x509.ExtKeyUsageClientAuth, time.Now())
	panelPin, _ := nodetls.Fingerprint(panelClient.CertPEM)
	nodeSrv, err := nodetls.Key{Port: 1, PanelPin: panelPin, CertPEM: nodePair.CertPEM, KeyPEM: nodePair.KeyPEM}.ServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	nodeCert, _ := tls.X509KeyPair([]byte(nodePair.CertPEM), []byte(nodePair.KeyPEM))

	panel := newReflectPanel(t, panelTLSPair)
	defer panel.stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunReverseTunnel(ctx, ReverseTunnel{
		URL:     "https://" + panel.addr,
		Client:  &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{nodeCert}, InsecureSkipVerify: true, NextProtos: []string{nodeapi.TunnelALPN}},
		Server:  nodeSrv,
		Handler: Handler(e, log),
		Log:     log,
	})
	sess := panel.session(t)

	// The state arrives over the tunnel and the node applies it.
	body, _ := json.Marshal(nodeapi.DesiredState{Revision: 7})
	req, _ := http.NewRequest(http.MethodPut, "https://node/v1/state", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := panel.api(t, sess, req, panelClient, nodePin)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the state: %d", resp.StatusCode)
	}
	if got := e.Health().Revision; got != 7 {
		t.Fatalf("the node runs revision %d, want 7", got)
	}

	// And health comes back through the same tunnel.
	req, _ = http.NewRequest(http.MethodGet, "https://node/v1/health", nil)
	resp = panel.api(t, sess, req, panelClient, nodePin)
	var health nodeapi.Health
	err = json.NewDecoder(resp.Body).Decode(&health)
	resp.Body.Close()
	if err != nil || health.Revision != 7 || health.Version != "test" {
		t.Fatalf("health over the tunnel: %+v %v", health, err)
	}

	// A tunnel is the API, not a proxy: a CONNECT to anything else is refused.
	req, _ = http.NewRequest(http.MethodConnect, "https://node", nil)
	req.Host = "example.com:443"
	resp = panel.api(t, sess, req, panelClient, nodePin)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a CONNECT over the reverse tunnel: %d", resp.StatusCode)
	}

	// ...but the bot's Telegram path is the one exception: the panel's "Telegram through a
	// node" sends CONNECT to a TunnelHosts address, and it must reach the API's relay.
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echo.Close() })
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	saved := nodeapi.TunnelHosts
	nodeapi.TunnelHosts = []string{echo.Addr().String()}
	t.Cleanup(func() { nodeapi.TunnelHosts = saved })
	req, _ = http.NewRequest(http.MethodConnect, "https://node", nil)
	req.Host = echo.Addr().String()
	resp = panel.api(t, sess, req, panelClient, nodePin)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the bot's CONNECT over the reverse tunnel: %d", resp.StatusCode)
	}

	// A stream that is not the API's is closed without an answer.
	st := panel.stream(t, sess, nodeapi.StreamData)
	_ = st.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := st.Read(make([]byte, 1)); err == nil {
		t.Fatal("a data stream was served")
	}
	_ = st.Close()
}

// The node reconnects when the panel comes back: the tunnel is not a one-shot.
func TestReverseTunnelReconnects(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e, err := Start(Options{DataDir: t.TempDir(), Version: "test", Log: log})
	if err != nil {
		t.Fatal(err)
	}
	panelTLSPair, _ := nodetls.Generate("panel-https", x509.ExtKeyUsageServerAuth, time.Now())
	nodePair, _ := nodetls.Generate("node", x509.ExtKeyUsageServerAuth, time.Now())
	panelClient, _ := nodetls.Generate("panel", x509.ExtKeyUsageClientAuth, time.Now())
	panelPin, _ := nodetls.Fingerprint(panelClient.CertPEM)
	nodeSrv, err := nodetls.Key{Port: 1, PanelPin: panelPin, CertPEM: nodePair.CertPEM, KeyPEM: nodePair.KeyPEM}.ServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	nodeCert, _ := tls.X509KeyPair([]byte(nodePair.CertPEM), []byte(nodePair.KeyPEM))
	first := newReflectPanel(t, panelTLSPair)
	addr := first.addr
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunReverseTunnel(ctx, ReverseTunnel{
		URL:     "https://" + addr,
		Client:  &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{nodeCert}, InsecureSkipVerify: true, NextProtos: []string{nodeapi.TunnelALPN}},
		Server:  nodeSrv,
		Handler: Handler(e, log),
		Log:     log,
	})
	first.session(t).Close()
	first.stop()

	// A panel that answers again on the same address: the node's pause is a couple of seconds,
	// so this waits it out.
	second := listenOn(t, addr, panelTLSPair)
	defer second.stop()
	second.session(t)
}

// listenOn is newReflectPanel on a given address (the node reconnects to the one it knows).
func listenOn(t *testing.T, addr string, panelTLSPair nodetls.Pair) *reflectPanel {
	t.Helper()
	cert, err := tls.X509KeyPair([]byte(panelTLSPair.CertPEM), []byte(panelTLSPair.KeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert},
		NextProtos: []string{nodeapi.TunnelALPN}, ClientAuth: tls.RequireAnyClientCert}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	p := &reflectPanel{addr: ln.Addr().String(), sessions: make(chan *yamux.Session, 4)}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != nodeapi.TunnelPath {
			http.NotFound(w, r)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		_ = conn.SetDeadline(time.Time{})
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: "+nodeapi.TunnelUpgrade+"\r\n\r\n")
		raw := net.Conn(conn)
		if buf.Reader.Buffered() > 0 {
			raw = &bufferedConn{Conn: conn, r: buf.Reader}
		}
		sess, err := yamux.Client(raw, nil, nil)
		if err != nil {
			conn.Close()
			return
		}
		select {
		case p.sessions <- sess:
		default:
		}
		<-sess.CloseChan()
		conn.Close()
	}), ReadHeaderTimeout: 5 * time.Second,
		// net/http serves nothing on a connection with the tunnel's ALPN; it hands it to a
		// TLSNextProto hook, as the panel's listener does.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){
			nodeapi.TunnelALPN: func(_ *http.Server, c *tls.Conn, h http.Handler) { _ = http.Serve(oneConn(c), h) },
		},
	}
	go func() { _ = srv.Serve(tls.NewListener(ln, cfg)) }()
	p.stop = func() { _ = srv.Close(); _ = ln.Close() }
	return p
}
