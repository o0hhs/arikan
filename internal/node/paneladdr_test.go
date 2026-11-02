package node

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodetls"
)

// The node's API over TCP is for its panel alone; the addresses the panel connects from are
// written down for the installer, which keeps the port in the firewall for them.
func TestPanelAddrRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), PanelAddrFile)
	api := startPanelAPI(t, path)

	// The panel: its certificate is the pinned one, so its connection is the one recorded.
	conn := api.panelConn(t)
	if _, err := conn.Write([]byte("GET /v1/health HTTP/1.1\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	waitForPanelAddr(t, path, "127.0.0.1")
	if got, _ := ReadPanelAddrs(path); len(got.IPs) != 1 || !got.IPs[0].Last.After(time.Now().Add(-time.Minute)) {
		t.Fatalf("what was written: %+v", got)
	}
	if !api.served() {
		t.Fatal("the server never saw the panel's request")
	}
	_ = conn.Close()

	// A stranger with another certificate does not get through the pinned handshake and is
	// never written down.
	stranger, err := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ccfg, err := nodetls.ClientConfig(stranger, api.nodePin)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := tls.Dial("tcp", api.addr, ccfg); err == nil {
		_, _ = c.Write([]byte("x"))
		_ = c.Close()
	}
	if got, _ := ReadPanelAddrs(path); len(got.IPs) != 1 {
		t.Fatalf("a stranger was written down: %+v", got)
	}
}

// The file keeps the newest address first and no more than PanelAddrMax of them. The form
// is the installer's: it reads "ips", "ip" and "last" out of it by name.
func TestPanelAddrKeepsTheNewest(t *testing.T) {
	at := time.Now()
	var addrs []PanelAddr
	var isNew bool
	for _, ip := range []string{"203.0.113.1", "203.0.113.2", "203.0.113.3", "203.0.113.4", "203.0.113.5"} {
		addrs, isNew = addPanelAddr(addrs, ip, at)
		if !isNew {
			t.Fatalf("%s was not new", ip)
		}
	}
	addrs, isNew = addPanelAddr(addrs, "203.0.113.3", at)
	if isNew || len(addrs) != PanelAddrMax || addrs[0].IP != "203.0.113.3" {
		t.Fatalf("a returning address: new=%v %+v", isNew, addrs)
	}
	got := make([]string, 0, len(addrs))
	for _, a := range addrs {
		got = append(got, a.IP)
	}
	want := []string{"203.0.113.3", "203.0.113.5", "203.0.113.4", "203.0.113.2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("addresses: %v, want %v", got, want)
		}
	}
	path := filepath.Join(t.TempDir(), PanelAddrFile)
	if err := writePanelAddrs(path, addrs); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"ips":[{"ip":"203.0.113.3","last":"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("the installer reads %s: %s", key, raw)
		}
	}
}

// panelAPI is a node API over pinned TLS on loopback with the address watcher in front.
type panelAPI struct {
	addr    string
	panel   nodetls.Pair
	nodePin string
	served  func() bool
}

func startPanelAPI(t *testing.T, path string) *panelAPI {
	t.Helper()
	now := time.Now()
	panel, err := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, now)
	if err != nil {
		t.Fatal(err)
	}
	node, err := nodetls.Generate("node-2.mikan", x509.ExtKeyUsageServerAuth, now)
	if err != nil {
		t.Fatal(err)
	}
	panelPin, _ := nodetls.Fingerprint(panel.CertPEM)
	nodePin, _ := nodetls.Fingerprint(node.CertPEM)
	scfg, err := nodetls.Key{Port: 1, PanelPin: panelPin, CertPEM: node.CertPEM, KeyPEM: node.KeyPEM}.ServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := WatchPanelAddr(tls.NewListener(tcp, scfg), path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = ln.Close() })
	served := make(chan struct{}, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 1)
				if _, err := conn.Read(buf); err == nil {
					select {
					case served <- struct{}{}:
					default:
					}
				}
			}()
		}
	}()
	return &panelAPI{addr: tcp.Addr().String(), panel: panel, nodePin: nodePin, served: func() bool {
		select {
		case <-served:
			return true
		default:
			return false
		}
	}}
}

// panelConn connects the way the panel does: its pinned certificate, the node's pin.
func (p *panelAPI) panelConn(t *testing.T) net.Conn {
	t.Helper()
	ccfg, err := nodetls.ClientConfig(p.panel, p.nodePin)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := tls.Dial("tcp", p.addr, ccfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func waitForPanelAddr(t *testing.T, path, ip string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := ReadPanelAddrs(path)
		if err == nil && len(got.IPs) > 0 && got.IPs[0].IP == ip {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	raw, _ := os.ReadFile(path)
	t.Fatalf("%s does not hold %s: %s", PanelAddrFile, ip, raw)
}
