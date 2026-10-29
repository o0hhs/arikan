package app

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"mikan/internal/panel/tlscert"
)

// A browser that negotiates h2 must be answered in HTTP/2. With TLSNextProto set for the
// node tunnel's own ALPN, net/http does not enable HTTP/2 by itself; the panel's TLS config
// still advertises h2, so a browser picked it and was read as HTTP/1.1 — Safari said "the
// server unexpectedly disconnected", while doctor and curl (HTTP/1.1) were happy.
func TestThePanelServesHTTP2(t *testing.T) {
	cert, err := tlscert.LoadOrCreateSelfSigned(t.TempDir(), "panel.test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	srv := httpServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("a browser on h2 was served over %s", r.Proto)
		}
		_, _ = io.WriteString(w, "ok")
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = srv.Serve(tls.NewListener(ln, &tls.Config{
			Certificates: []tls.Certificate{*cert},
			NextProtos:   []string{"h2", "http/1.1"},
		}))
	}()
	t.Cleanup(func() { _ = srv.Close() })

	client := &http.Client{Transport: &http2.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := client.Get("https://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("HTTP/2 through the panel's server: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || resp.ProtoMajor != 2 || string(body) != "ok" {
		t.Fatalf("%d %s %q", resp.StatusCode, resp.Proto, body)
	}
}
