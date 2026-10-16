package node

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The node's own page: https on its port, the files of www/, 404.html for what is not there,
// the panel's headers off, and HSTS only once the certificate is one browsers trust.
func TestSiteServesItsFiles(t *testing.T) {
	dir := t.TempDir()
	site := newSite(dir, "127.0.0.1:0", slog.New(slog.NewTextHandler(io.Discard, nil)))
	site.SetCert(writeSiteCert(t, dir, false))
	site.Start()
	st := site.Status()
	if st == nil || !st.OK || st.Port == 0 {
		t.Fatalf("the page did not start: %+v", st)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(st.Port))

	// A neutral page is written when there is none, and the admin's files are left alone.
	if page := getSite(t, addr, "/"); page.code != 200 || !strings.Contains(page.body, "Nothing here yet") {
		t.Fatalf("the default page: %d %q", page.code, page.body)
	}
	if err := os.WriteFile(filepath.Join(dir, SiteDir, "index.html"), []byte("<h1>my own</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if page := getSite(t, addr, "/index.html"); page.code != 200 || page.body != "<h1>my own</h1>" {
		t.Fatalf("the admin's page: %d %q", page.code, page.body)
	}
	if page := getSite(t, addr, "/index.html"); page.hsts != "" || page.server != "" {
		t.Fatalf("a self-signed certificate: HSTS %q, Server %q", page.hsts, page.server)
	}
	if page := getSite(t, addr, "/nope"); page.code != 404 || !strings.Contains(page.body, "Not found.") {
		t.Fatalf("404.html: %d %q", page.code, page.body)
	}
	if page := getSite(t, addr, "/.env"); page.code != 404 {
		t.Fatalf("a hidden file: %d", page.code)
	}
	// TLS 1.2 at least: an older client is refused.
	if c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS11}); err == nil {
		_ = c.Close()
		t.Fatal("TLS 1.1 was accepted")
	}
	if c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12}); err != nil {
		t.Fatalf("TLS 1.2: %v", err)
	} else {
		_ = c.Close()
	}

	// A certificate a browser trusts (not signed by itself) turns HSTS on: the page is not
	// offered to a visitor as https-only while it would only warn them.
	site.SetCert(writeSiteCert(t, dir, true))
	if page := getSite(t, addr, "/index.html"); page.hsts != hsts {
		t.Fatalf("HSTS with a trusted certificate: %q", page.hsts)
	}
}

// An inbound on 443 is what holds the port on a real node: with the default address taken,
// the page moves to the loopback port a REALITY dest may name.
func TestSiteMovesOffATakenPort(t *testing.T) {
	// A loopback address of a test is not the default one: no fallback, and the admin sees
	// the failure where they look (the node's health, site.json, doctor).
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	site := newSite(t.TempDir(), held.Addr().String(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	site.SetCert(writeSiteCert(t, t.TempDir(), false))
	site.Start()
	if st := site.Status(); st == nil || st.OK || st.Error == "" {
		t.Fatalf("a taken port: %+v", st)
	}

	other := newSite(t.TempDir(), SiteDefaultListen, slog.New(slog.NewTextHandler(io.Discard, nil)))
	other.listenFunc = func(network, addr string) (net.Listener, error) {
		if addr == SiteDefaultListen {
			return nil, &net.OpError{Op: "listen", Net: "tcp", Err: os.ErrPermission}
		}
		return net.Listen(network, "127.0.0.1:0")
	}
	other.SetCert(writeSiteCert(t, t.TempDir(), false))
	other.Start()
	st := other.Status()
	if st == nil || !st.OK || st.Port == 0 {
		t.Fatalf("the fallback: %+v", st)
	}
	if !strings.HasPrefix(st.Listen, "127.0.0.1:") {
		t.Fatalf("the fallback is not on loopback: %+v", st)
	}
}

// off: no page at all (MIKAN_SITE_LISTEN=off), and nothing is reported.
func TestSiteOff(t *testing.T) {
	site := newSite(t.TempDir(), "off", slog.New(slog.NewTextHandler(io.Discard, nil)))
	site.Start()
	if site.Status() != nil {
		t.Fatal("a page that is off")
	}
	if site.Start(); site.Status() != nil {
		t.Fatal("a second Start of a page that is off")
	}
}

type sitePage struct {
	code   int
	body   string
	hsts   string
	server string
}

func getSite(t *testing.T, addr, path string) sitePage {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, Timeout: 5 * time.Second}
	resp, err := client.Get("https://" + addr + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return sitePage{
		code: resp.StatusCode, body: string(raw),
		hsts: resp.Header.Get("Strict-Transport-Security"), server: resp.Header.Get("Server"),
	}
}

// writeSiteCert writes the certificate the page presents into dir: signed by itself (a node
// before its public certificate), or by another one (a certificate a browser trusts).
func writeSiteCert(t *testing.T, dir string, public bool) (string, string) {
	t.Helper()
	cert := selfSigned(t, "example.com")
	if public {
		cert = signedByAnother(t, "example.com")
	}
	key, err := x509.MarshalECPrivateKey(cert.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "site.crt")
	keyPath := filepath.Join(dir, "site.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: key}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

// signedByAnother is a leaf certificate whose issuer is not itself, as a public one is.
func signedByAnother(t *testing.T, name string) tls.Certificate {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
