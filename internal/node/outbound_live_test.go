package node

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/tunnel"

	"mikan/internal/nodeapi"
	"mikan/internal/proto"
)

// The whole of the feature, live: a link the admin pasted becomes a proxy mihomo dials, and
// the traffic through it comes out where the link points. The socks5 server here is a real
// one (the test's own, in front of a real HTTP server), so what is proved is the way the
// node reads the link — not the server behind it.
func TestOutboundLinkCarriesTraffic(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "through the outbound")
	}))
	defer target.Close()
	host, portStr, _ := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))
	port, _ := strconv.Atoi(portStr)

	socks, asked := socks5Server(t)
	st := warpState()
	st.Exits = []nodeapi.Exit{{Name: nodeapi.OutboundName(1), Link: "socks5://" + socks, Inbounds: []string{}}}
	raw, _, err := buildConfig(st, proto.Cert{}, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := executor.ParseWithBytes(raw)
	if err != nil {
		t.Fatalf("mihomo refuses the link outbound: %v", err)
	}
	tunnel.UpdateProxies(cfg.Proxies, cfg.Providers)
	proxy, ok := tunnel.Proxies()[nodeapi.OutboundName(1)]
	if !ok {
		t.Fatalf("the outbound is not a proxy mihomo knows: %v", cfg.Proxies)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := proxy.DialContext(ctx, &C.Metadata{NetWork: C.TCP, Type: C.SOCKS5, Host: host, DstPort: uint16(port)})
	if err != nil {
		t.Fatalf("dial through the outbound: %v", err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: "+host+"\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "through the outbound" {
		t.Fatalf("through the outbound: %d %q", resp.StatusCode, body)
	}
	if got := asked(); !strings.Contains(got, net.JoinHostPort(host, strconv.Itoa(port))) {
		t.Fatalf("the outbound asked for %q", got)
	}
}

// socks5Server is a minimal SOCKS5 server (no authentication, CONNECT only) that forwards
// to where it is asked to. It reports the addresses it was asked for.
func socks5Server(t *testing.T) (addr string, asked func() string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				where, err := socks5Handshake(conn)
				if err != nil {
					return
				}
				mu.Lock()
				seen = append(seen, where)
				mu.Unlock()
				upstream, err := net.DialTimeout("tcp", where, 5*time.Second)
				if err != nil {
					_, _ = conn.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer upstream.Close()
				_, _ = conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(upstream, conn); done <- struct{}{} }()
				go func() { _, _ = io.Copy(conn, upstream); done <- struct{}{} }()
				<-done
			}()
		}
	}()
	return ln.Addr().String(), func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(seen, " ")
	}
}

// socks5Handshake reads the greeting and the CONNECT request and returns the address asked
// for ("host:port").
func socks5Handshake(conn net.Conn) (string, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil || head[0] != 5 {
		return "", io.ErrUnexpectedEOF
	}
	if _, err := io.ReadFull(conn, make([]byte, int(head[1]))); err != nil {
		return "", err
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return "", err
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil || req[1] != 1 { // CONNECT
		return "", io.ErrUnexpectedEOF
	}
	var host string
	switch req[3] {
	case 1:
		raw := make([]byte, 4)
		if _, err := io.ReadFull(conn, raw); err != nil {
			return "", err
		}
		host = net.IP(raw).String()
	case 4:
		raw := make([]byte, 16)
		if _, err := io.ReadFull(conn, raw); err != nil {
			return "", err
		}
		host = net.IP(raw).String()
	case 3:
		ln := make([]byte, 1)
		if _, err := io.ReadFull(conn, ln); err != nil {
			return "", err
		}
		raw := make([]byte, int(ln[0]))
		if _, err := io.ReadFull(conn, raw); err != nil {
			return "", err
		}
		host = string(raw)
	default:
		return "", io.ErrUnexpectedEOF
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(conn, port); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port)))), nil
}
