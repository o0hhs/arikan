package webproxy

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

// v2Header is a PROXY protocol v2 header for tests: the PROXY command over TCP with the
// given addresses, as our node writes it.
func v2Header(t *testing.T, src, dst string) []byte {
	t.Helper()
	s, d := netip.MustParseAddrPort(src), netip.MustParseAddrPort(dst)
	var b bytes.Buffer
	b.Write(signature)
	b.WriteByte(0x21) // version 2, PROXY
	s4, d4 := s.Addr().Unmap(), d.Addr().Unmap()
	if s4.Is4() && d4.Is4() {
		b.WriteByte(0x11)
		_ = binary.Write(&b, binary.BigEndian, uint16(12))
		b.Write(s4.AsSlice())
		b.Write(d4.AsSlice())
	} else {
		b.WriteByte(0x21)
		_ = binary.Write(&b, binary.BigEndian, uint16(36))
		s16, d16 := s4.As16(), d4.As16()
		b.Write(s16[:])
		b.Write(d16[:])
	}
	_ = binary.Write(&b, binary.BigEndian, s.Port())
	_ = binary.Write(&b, binary.BigEndian, d.Port())
	return b.Bytes()
}

// The reader takes a PROXY v2 header and nothing else: v1, LOCAL, UDP, UNIX, garbage, a
// truncated header and an empty stream are all refused.
func TestReadV2(t *testing.T) {
	good := []struct{ src, dst string }{{"203.0.113.7:51234", "198.51.100.1:443"}, {"[2001:db8::7]:51234", "[2001:db8::1]:443"}}
	for _, c := range good {
		src, err := ReadV2(bytes.NewReader(v2Header(t, c.src, c.dst)))
		if err != nil || src.String() != c.src {
			t.Fatalf("%s: %v %v", c.src, src, err)
		}
	}
	// TLVs after the addresses are part of the header: read and dropped.
	withTLV := append(v2Header(t, good[0].src, good[0].dst), 0x04, 0x00, 0x04, 0xde, 0xad, 0xbe, 0xef)
	binary.BigEndian.PutUint16(withTLV[14:16], uint16(len(withTLV)-16))
	if src, err := ReadV2(bytes.NewReader(withTLV)); err != nil || src.String() != good[0].src {
		t.Fatalf("a TLV: %v %v", src, err)
	}

	v1 := []byte("PROXY TCP4 203.0.113.7 198.51.100.1 51234 443\r\n")
	local := append(append([]byte{}, signature...), 0x20, 0x11, 0x00, 0x00)
	udp := append(append([]byte{}, signature...), 0x21, 0x12, 0x00, 0x0c)
	unix := append(append([]byte{}, signature...), 0x21, 0x31, 0x00, 0x00)
	garbage := []byte("GET / HTTP/1.1\r\nHost: sub.example.com\r\n\r\n")
	shortPayload := append(append([]byte{}, signature...), 0x21, 0x11, 0x00, 0x06, 1, 2, 3, 4, 5, 6)
	for name, raw := range map[string][]byte{
		"v1":            v1,
		"local":         local,
		"udp":           udp,
		"unix":          unix,
		"garbage":       garbage,
		"short payload": shortPayload,
		"empty":         nil,
		"truncated":     v2Header(t, good[0].src, good[0].dst)[:20],
	} {
		if src, err := ReadV2(bytes.NewReader(raw)); err == nil {
			t.Errorf("%s: taken for a header: %v", name, src)
		}
	}
	// A stream that is cut right inside the signature is no header, not a malformed one.
	if _, err := ReadV2(bytes.NewReader(signature[:8])); !errors.Is(err, ErrNoHeader) {
		t.Fatalf("a cut signature: %v", err)
	}
}

// testCert is a self-signed certificate for name and its key.
func testCert(t *testing.T, name string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// serve starts an HTTPS server over a proxy listener, answering each request with the
// client's address as the panel sees it and the name it was asked for.
func serve(t *testing.T, hits *atomic.Int64) *Listener {
	t.Helper()
	ln, err := Listen("127.0.0.1:0", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	cert := testCert(t, "sub.example.com")
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = fmt.Fprintf(w, "%s|%s", r.RemoteAddr, r.TLS.ServerName)
	})}
	go func() { _ = srv.Serve(tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}})) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln
}

// fakeConn is a connection from an address of the test's choosing.
type fakeConn struct {
	net.Conn
	remote net.Addr
}

func (c fakeConn) RemoteAddr() net.Addr { return c.remote }

// The panel behind the node's router: a forwarded connection is served with the client's
// address, not the router's; one that came without a header, or from off loopback, is not
// served at all.
func TestListener(t *testing.T) {
	var hits atomic.Int64
	ln := serve(t, &hits)

	// A forwarded connection: the header, then TLS as any client sends it.
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(v2Header(t, "203.0.113.7:51234", "198.51.100.1:443")); err != nil {
		t.Fatal(err)
	}
	tc := tls.Client(c, &tls.Config{ServerName: "sub.example.com", InsecureSkipVerify: true})
	_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(tc, "GET / HTTP/1.1\r\nHost: sub.example.com\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(tc), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if got, want := string(body), "203.0.113.7:51234|sub.example.com"; got != want {
		t.Fatalf("the panel saw %q, want %q", got, want)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits %d", hits.Load())
	}

	// No header: nothing is served, the connection is closed.
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := io.WriteString(raw, "GET / HTTP/1.1\r\nHost: sub.example.com\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	_ = raw.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := raw.Read(make([]byte, 1)); err == nil || n != 0 {
		t.Fatalf("an unforwarded connection: read %d, %v", n, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("an unforwarded connection reached the server: %d", hits.Load())
	}

	// A header from a peer off loopback is refused even when it is well formed: only the
	// node on this host may say who the client is.
	pr, pw := net.Pipe()
	defer pw.Close()
	l := &Listener{}
	if _, ok := l.admit(fakeConn{Conn: pr, remote: &net.TCPAddr{IP: net.IPv4(203, 0, 113, 9), Port: 4444}}); ok {
		t.Fatal("a peer off loopback was let in")
	}
}

// The listener is the node's router's alone: a public bind is refused before a socket exists.
func TestListenRefusesANonLoopbackAddress(t *testing.T) {
	if ln, err := Listen("0.0.0.0:0", slog.New(slog.DiscardHandler)); err == nil {
		ln.Close()
		t.Fatal("the web listener took a public address")
	}
}
