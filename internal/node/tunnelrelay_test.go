package node

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"mikan/internal/nodeapi"
)

func quietRelay(t *testing.T) *tunnelRelay {
	t.Helper()
	return newTunnelRelay(slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
}

// The state's targets are ip:port; a name is dropped (the panel sends the addresses of its
// nodes, and a name would be resolved by this node's resolver).
func TestTunnelRelaySet(t *testing.T) {
	r := quietRelay(t)
	r.set([]string{"203.0.113.7:8443", "[2001:db8::1]:8443", "node.example:443", "203.0.113.8", ""})
	if len(r.to) != 2 {
		t.Fatalf("targets: %v, want the two ip:port", r.to)
	}
	for _, want := range []string{"203.0.113.7:8443", "[2001:db8::1]:8443"} {
		ap, err := netip.ParseAddrPort(want)
		if err != nil {
			t.Fatal(err)
		}
		if !r.to[ap] {
			t.Errorf("%s is not in %v", want, r.to)
		}
	}
	r.set(nil)
	if r.to != nil {
		t.Fatalf("an empty state is no targets, not an empty map: %v", r.to)
	}
}

// What the relay lets through: the panel's node addresses and the bot's fixed hosts; never a
// private or loopback address, never the node's own API port, never anything else.
func TestTunnelRelayAllow(t *testing.T) {
	r := quietRelay(t)
	r.setAPIPort(7000)
	r.set([]string{"203.0.113.7:8443", "10.0.0.5:8443", "127.0.0.1:9000", "203.0.113.9:7000", "169.254.169.254:80", "[::1]:8443", "0.0.0.0:8443"})
	for _, c := range []struct{ target, why string }{
		{"203.0.113.7:8443", ""},
		{"203.0.113.7:8444", "forbidden"},
		{"10.0.0.5:8443", "private"},
		{"127.0.0.1:9000", "private"},
		{"203.0.113.9:7000", "self"},
		{"node.example:443", "forbidden"},
		{"169.254.169.254:80", "private"},
		{"[::1]:8443", "private"},
		{"0.0.0.0:8443", "private"},
	} {
		if _, why := r.allow(c.target); why != c.why {
			t.Errorf("allow(%s) = %q, want %q", c.target, why, c.why)
		}
	}
	// The bot's hosts are the code's own, not the panel's: they are allowed as written.
	saved := nodeapi.TunnelHosts
	nodeapi.TunnelHosts = []string{"api.telegram.org:443", "127.0.0.1:9100"}
	t.Cleanup(func() { nodeapi.TunnelHosts = saved })
	for _, host := range []string{"api.telegram.org:443", "127.0.0.1:9100"} {
		if _, why := r.allow(host); why != "" {
			t.Errorf("allow(%s) = %q, want it through", host, why)
		}
	}
}

// A tunnel carries the bytes both ways; a target the state does not name is refused with its
// code, before anything is dialled.
func TestTunnelRelayServe(t *testing.T) {
	echo := echoListener(t)
	echoAddr := echo.Addr().String()
	// The relay refuses loopback targets of the state; the bot's list is what lets a test
	// reach a listener in this process (a node's real targets are public addresses).
	saved := nodeapi.TunnelHosts
	nodeapi.TunnelHosts = []string{echoAddr}
	t.Cleanup(func() { nodeapi.TunnelHosts = saved })

	r := quietRelay(t)
	srv := httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "CONNECT "+echoAddr+" HTTP/1.1\r\nHost: "+echoAddr+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT: %d", resp.StatusCode)
	}
	if _, err := io.WriteString(conn, "hello"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("echo through the tunnel: %q %v", buf, err)
	}

	// A target that is not in the state: refused with the code the panel shows.
	bad, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	if _, err := io.WriteString(bad, "CONNECT 203.0.113.44:8443 HTTP/1.1\r\nHost: 203.0.113.44:8443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err = http.ReadResponse(bufio.NewReader(bad), nil)
	if err != nil {
		t.Fatal(err)
	}
	var e nodeapi.Error
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil || resp.StatusCode != http.StatusForbidden || e.Code != "tunnel_forbidden" {
		t.Fatalf("an unlisted target: %d %+v %v", resp.StatusCode, e, err)
	}
}

// The relay takes a place per open tunnel and gives it back when the stream ends: a node is no
// open proxy even for the panel's own certificate.
func TestTunnelRelayLimit(t *testing.T) {
	r := quietRelay(t)
	for i := 0; i < tunnelAtOnce; i++ {
		if !r.claim() {
			t.Fatalf("tunnel %d refused under the limit", i)
		}
	}
	if r.claim() {
		t.Fatal("one tunnel past the limit")
	}
	r.release()
	if !r.claim() {
		t.Fatal("a place given back is not taken again")
	}
}

// A tunnel nobody uses is closed: a stream would otherwise hold its socket for ever.
func TestTunnelRelayIdle(t *testing.T) {
	echo := echoListener(t)
	echoAddr := echo.Addr().String()
	saved := nodeapi.TunnelHosts
	nodeapi.TunnelHosts = []string{echoAddr}
	t.Cleanup(func() { nodeapi.TunnelHosts = saved })

	r := quietRelay(t)
	r.idle, r.check = 40*time.Millisecond, 10*time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(srv.Close)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "CONNECT "+echoAddr+" HTTP/1.1\r\nHost: "+echoAddr+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	if resp, err := http.ReadResponse(br, nil); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT: %v %v", resp, err)
	}
	// Nothing goes through it: the relay closes both ends about idle later.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	start := time.Now()
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("the tunnel carried a byte nobody sent")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the idle tunnel stayed open %v", took)
	}
}

// The node's state carries what it may tunnel to, and its API handler serves the panel from
// it, refusing a tunnel to its own API port even when the state names it.
func TestTunnelToFromState(t *testing.T) {
	e, err := Start(Options{DataDir: t.TempDir(), Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.Apply(nodeapi.DesiredState{Revision: 99}) })
	if _, err := e.Apply(nodeapi.DesiredState{Revision: 1, TunnelTo: []string{"203.0.113.7:8443"}}); err != nil {
		t.Fatal(err)
	}
	if _, why := e.tunnelRelay().allow("203.0.113.7:8443"); why != "" {
		t.Fatalf("the state's target: %q", why)
	}
	if _, why := e.tunnelRelay().allow("203.0.113.8:8443"); why != "forbidden" {
		t.Fatalf("a target out of the state: %q", why)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: Handler(e, e.log), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	own := fmt.Sprintf("203.0.113.7:%d", port)
	if _, err := e.Apply(nodeapi.DesiredState{Revision: 2, TunnelTo: []string{own}}); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "CONNECT "+own+" HTTP/1.1\r\nHost: "+own+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	var ne nodeapi.Error
	if err := json.NewDecoder(resp.Body).Decode(&ne); err != nil || ne.Code != "tunnel_self" {
		t.Fatalf("a tunnel to the node's own API: %d %+v %v", resp.StatusCode, ne, err)
	}
}

// echoListener accepts connections and sends back what it gets.
func echoListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return ln
}
