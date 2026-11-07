package app

import (
	"context"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/metacubex/yamux"

	"mikan/internal/duplex"
	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/config"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// standForwardNode is a node that serves both halves of the tunnel: the API over a control
// stream, and a client's connection over a data stream, dialled to the port the stream names.
func standForwardNode(t *testing.T, addr string, node nodetls.Pair, panelPin string) *yamux.Session {
	t.Helper()
	conn, err := dialTunnel(t, addr, node, true)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := yamux.Server(conn, nil, nil)
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
				kind, port, err := nodeapi.ReadStreamPort(st)
				if err != nil {
					_ = st.Close()
					return
				}
				if kind != nodeapi.StreamData {
					_ = st.Close()
					return
				}
				remote, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", itoaPort(int(port))))
				if err != nil {
					_ = st.Close()
					return
				}
				defer remote.Close()
				defer st.Close()
				duplex.Copy(st, remote)
			}()
		}
	}()
	return sess
}

func itoaPort(n int) string {
	if n == 0 {
		return "0"
	}
	var b [6]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// The panel as the way in: a client connects to the panel on a port the node named, and the
// panel carries the connection to the node over its reverse tunnel — the node's protocol
// listening on loopback, not on a port a filter cuts.
func TestTunnelForwardCarriesAClient(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := nodetls.LoadOrCreate(filepath.Join(dir, "tls", "nodes"), time.Now()); err != nil {
		t.Fatal(err)
	}
	nodePair, err := nodetls.Generate("node-fwd", x509.ExtKeyUsageServerAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	nodePin, _ := nodetls.Fingerprint(nodePair.CertPEM)
	panelTLS, err := nodetls.Generate("panel-https", x509.ExtKeyUsageServerAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// The node's protocol: an echo on loopback, as the core's inbound listens.
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
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	port := echo.Addr().(*net.TCPAddr).Port

	row, err := h.st.Q.CreateNode(ctx, db.CreateNodeParams{Name: "eu", Address: "127.0.0.1:1", PublicHost: "203.0.113.7", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.Q.SetNodeCert(ctx, db.SetNodeCertParams{CertSha256: nodePin, UpdatedAt: 2, ID: row.ID}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.Q.SetNodeTunnelForward(ctx, db.SetNodeTunnelForwardParams{TunnelForward: domain.TunnelForwardJSON([]int{port}), UpdatedAt: 2, ID: row.ID}); err != nil {
		t.Fatal(err)
	}

	tn := newTunnels(h.st, log, time.Now)
	addr, stop := tunnelListener(t, tn, panelTLS)
	defer stop()
	sess := standForwardNode(t, addr, nodePair, mustPin(t, cfg))
	defer sess.Close()
	waitConnected(t, tn, row.ID)

	// The forwarder listens on a loopback port of this test's own (a real panel would bind the
	// node's port on its address), and each client connection names the node's protocol port
	// (the echo's) on the stream. A free port is taken for the listener, closed, and reopened
	// by the forwarder, so the test never holds 443 or a real protocol's port.
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fport := free.Addr().(*net.TCPAddr).Port
	_ = free.Close()
	fwd := newTunnelForward(h.st, tn, "127.0.0.1", log)
	defer fwd.Close()
	fp := fwd.open(uint16(fport), row.ID)
	if fp == nil {
		t.Fatal("the forwarder's listener did not start")
	}
	fp.port = uint16(port) // the stream names the node's protocol port, not the panel's

	// A client connects to the panel's forwarder; the bytes echo back from the node's protocol.
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", itoaPort(fport)))
	if err != nil {
		t.Fatalf("the panel's forwarder: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	got := make([]byte, 4)
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "ping" {
		t.Fatalf("a client through the panel: %q %v", got, err)
	}
}
