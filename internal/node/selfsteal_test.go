package node

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter"
	C "github.com/metacubex/mihomo/constant"

	"mikan/internal/nodeapi"
	"mikan/internal/proto"
)

// The REALITY inbound is the panel's own HTTPS on loopback as its target (self-steal): the
// installer applies it on any install with a domain, and the node in production runs with
// MIKAN_ALLOW_PRIVATE=false. A client must still complete the handshake: the mirror dial to
// the target is what every connection of every user starts with.
func TestSelfStealRealityClient(t *testing.T) {
	// The panel's HTTPS: a TLS server with a certificate for the domain clients send as SNI.
	target := startTLSTarget(t)
	targetAddr := target.Addr().String()
	_, targetPort, err := net.SplitHostPort(targetAddr)
	if err != nil {
		t.Fatal(err)
	}

	eng, err := Start(Options{
		DataDir: t.TempDir(), Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		DeviceRelease: time.Second, AllowPrivate: false, ACMEListen: freeAddr(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Challenge.Close)

	priv, err := randomRealityKey()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := proto.RealityPublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	uuid := "00000000-0000-4000-8000-000000000001"
	// First a public site as the target, as any install starts out; the self-steal dest
	// replaces it with the next state, which is the change the panel makes after install.
	reality := func(dest string, names []any) proto.Template {
		return proto.Template{
			"type": "vless",
			"reality-config": map[string]any{
				"dest": dest, "private-key": priv,
				"short-id": []any{"0123456789abcdef"}, "server-names": names,
			},
			"mikan": map[string]any{"flow": "xtls-rprx-vision"},
		}
	}
	listenPort := freePort(t)
	in := func(tpl proto.Template, selfPort int) nodeapi.DesiredState {
		return nodeapi.DesiredState{
			Revision: 1,
			Inbounds: []nodeapi.Inbound{{Name: "reality", Port: strconv.Itoa(listenPort), Config: tpl.JSON()}},
			Slots:    []nodeapi.Slot{{Name: "s1", UUID: uuid, Secret: "x"}},
			Policies: []nodeapi.Policy{{Slot: "s1", Allowed: true, QuotaRemaining: -1}},
			// The panel's own HTTPS port: the one address the REALITY dest may name.
			SelfStealPort: selfPort,
		}
	}
	public := in(reality("www.example.org:443", []any{"www.example.org"}), 0)
	if _, err := eng.Apply(public); err != nil {
		t.Fatal(err)
	}
	st := in(reality("127.0.0.1:"+targetPort, []any{"example.com"}), mustAtoi(t, targetPort))
	res, err := eng.Apply(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Recreated) != 1 || res.Recreated[0] != "reality" {
		t.Fatalf("turning self-steal on must rebuild the listener with the new dest: %v", res.Recreated)
	}
	for _, l := range res.Listeners {
		if !l.OK {
			t.Fatalf("listener %s did not start: %s", l.Name, l.Error)
		}
	}

	client, err := adapter.ParseProxy(map[string]any{
		"name": "self-steal", "type": "vless", "server": "127.0.0.1", "port": listenPort, "uuid": uuid,
		"network": "tcp", "tls": true, "flow": "xtls-rprx-vision", "servername": "example.com", "client-fingerprint": "chrome",
		"reality-opts": map[string]any{"public-key": pub, "short-id": "0123456789abcdef"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	meta := &C.Metadata{NetWork: C.TCP, Type: C.VLESS, DstPort: 80, Host: "example.com"}
	conn, err := client.DialContext(ctx, meta)
	if err != nil {
		t.Fatalf("a client cannot connect through a REALITY inbound with a self-steal dest: %v", err)
	}
	conn.Close()
	if got := target.count.Load(); got == 0 {
		t.Fatal("the node never dialed the REALITY dest: the handshake cannot have mirrored the target")
	}
}

// startTLSTarget is the panel's own HTTPS in the test: a TLS server for example.com that
// counts the connections it served (the REALITY server dials it to mirror every handshake).
func startTLSTarget(t *testing.T) *connCounter {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := &connCounter{Listener: ln}
	srv := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "panel") }),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{selfSigned(t, "example.com")}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: time.Second,
	}
	go func() { _ = srv.ServeTLS(c, "", "") }()
	t.Cleanup(func() { _ = srv.Close() })
	return c
}

// connCounter counts the TLS connections accepted: the mirror dial of a REALITY handshake.
type connCounter struct {
	net.Listener
	count atomic.Int64
}

func (c *connCounter) Accept() (net.Conn, error) {
	conn, err := c.Listener.Accept()
	if err == nil {
		c.count.Add(1)
	}
	return conn, err
}

func selfSigned(t *testing.T, name string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}))
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// randomRealityKey is the X25519 private key format mihomo and the panel share: a bare
// 32-byte scalar, base64url, without padding.
func randomRealityKey() (string, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(k.Bytes()), nil
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func freeAddr(t *testing.T) string {
	t.Helper()
	return "127.0.0.1:" + strconv.Itoa(freePort(t))
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The rules of a node without MIKAN_ALLOW_PRIVATE keep every private range rejected, the
// loopback included: nothing a self-steal setup needs may widen that (the node's own dial
// to its REALITY dest is not a user connection and never goes through these rules).
func TestSelfStealKeepsPrivateRules(t *testing.T) {
	st := nodeapi.DesiredState{SelfStealPort: 21355}
	raw, _, err := buildConfig(st, proto.Cert{}, false)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Rules []string `json:"rules"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	rejects := map[string]bool{}
	for _, r := range cfg.Rules {
		if "IP-CIDR,127.0.0.0/8,REJECT" == r || "IP-CIDR,127.0.0.1/32,DIRECT" == r {
			rejects[r] = true
		}
	}
	if !rejects["IP-CIDR,127.0.0.0/8,REJECT"] {
		t.Fatalf("loopback is not rejected for users: %v", cfg.Rules)
	}
	for _, r := range cfg.Rules {
		if r == "IP-CIDR,127.0.0.1/32,DIRECT" || r == "IP-CIDR,127.0.0.0/8,DIRECT" {
			t.Fatalf("a self-steal setup widened the loopback rules: %s", r)
		}
	}
}
