package nodeapi

import (
	"bytes"
	"crypto/x509"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodetls"
)

// The frames of one cascade stream: the ask, the answer, the target.
func TestCascadeFramesRoundTrip(t *testing.T) {
	var b bytes.Buffer
	if err := WriteCascadeAsk(&b, CascadeStreamData, 7); err != nil {
		t.Fatal(err)
	}
	kind, exit, err := ReadCascadeAsk(&b)
	if err != nil || kind != CascadeStreamData || exit != 7 {
		t.Fatalf("ask: kind %d exit %d err %v", kind, exit, err)
	}
	if _, _, err := ReadCascadeAsk(bytes.NewReader([]byte{1, 2})); err == nil {
		t.Fatal("a truncated ask must not read")
	}

	b.Reset()
	if err := WriteCascadeAnswer(&b, ""); err != nil {
		t.Fatal(err)
	}
	if err := ReadCascadeAnswer(&b); err != nil {
		t.Fatalf("an ok answer: %v", err)
	}

	b.Reset()
	if err := WriteCascadeAnswer(&b, "the exit is not in the tunnel"); err != nil {
		t.Fatal(err)
	}
	var refused *CascadeRefused
	if err := ReadCascadeAnswer(&b); !errors.As(err, &refused) || refused.Reason != "the exit is not in the tunnel" {
		t.Fatalf("a refused answer: %v", err)
	}

	b.Reset()
	want := CascadeTarget{Host: "example.com", Port: 443}
	if err := WriteCascadeTarget(&b, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCascadeTarget(&b)
	if err != nil || got != want {
		t.Fatalf("target: %+v, %v", got, err)
	}
	for _, bad := range []string{
		`{"host":"","port":443}`,
		`{"host":"example.com","port":0}`,
		`{"host":"example.com","port":70000}`,
		`not json`,
	} {
		var frame bytes.Buffer
		var hdr [2]byte
		hdr[0], hdr[1] = byte(len(bad)>>8), byte(len(bad))
		frame.Write(hdr[:])
		frame.WriteString(bad)
		if _, err := ReadCascadeTarget(&frame); err == nil {
			t.Fatalf("a target %s must not read", bad)
		}
	}
	// The frame is exactly as long as it says: the client's first bytes stay for the
	// connection.
	b.Reset()
	_ = WriteCascadeTarget(&b, want)
	b.WriteString("GET")
	if _, err := io.ReadFull(&b, make([]byte, 3)); err != nil || b.Len() != 0 {
		t.Fatalf("the target frame took the connection's bytes: %v, %d left", err, b.Len())
	}
}

// The exit's knock: the node's own certificate proves who knocks, and nothing else does.
func TestCascadeHello(t *testing.T) {
	now := time.Now()
	key, err := nodetls.Generate("node-2.mikan", x509.ExtKeyUsageServerAuth, now)
	if err != nil {
		t.Fatal(err)
	}
	other, err := nodetls.Generate("node-9.mikan", x509.ExtKeyUsageServerAuth, now)
	if err != nil {
		t.Fatal(err)
	}
	hello, err := NewCascadeHello(key, now)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := VerifyCascadeHello(hello, now)
	if err != nil {
		t.Fatalf("a fresh knock: %v", err)
	}
	want, err := nodetls.Fingerprint(key.CertPEM)
	if err != nil || pin != want {
		t.Fatalf("the pin of the knocks: %q, want %q", pin, want)
	}
	if _, err := VerifyCascadeHello(hello, now.Add(CascadeHelloWindow+time.Minute)); !errors.Is(err, ErrCascadeHelloTime) {
		t.Fatalf("a stale knock: %v", err)
	}
	if _, err := VerifyCascadeHello(hello, now.Add(-CascadeHelloWindow-time.Minute)); !errors.Is(err, ErrCascadeHelloTime) {
		t.Fatalf("a knock from the future: %v", err)
	}
	// Another node's certificate cannot carry this knock's signature.
	borrowed := hello
	borrowed.Cert = other.CertPEM
	if _, err := VerifyCascadeHello(borrowed, now); !errors.Is(err, ErrCascadeHelloSig) {
		t.Fatalf("a borrowed certificate: %v", err)
	}
	// Neither can another nonce.
	moved := hello
	moved.Nonce = strings.Repeat("0", 32)
	if _, err := VerifyCascadeHello(moved, now); !errors.Is(err, ErrCascadeHelloSig) {
		t.Fatalf("a changed nonce: %v", err)
	}
	// A nonce is 32 hex digits, no more.
	short := hello
	short.Nonce = "ab"
	if _, err := VerifyCascadeHello(short, now); !errors.Is(err, ErrCascadeHelloForm) {
		t.Fatalf("a short nonce: %v", err)
	}
}
