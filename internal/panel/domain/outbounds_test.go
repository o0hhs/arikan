package domain

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// The link checks are the panel's own: one line, a protocol the node reads, no spaces, and
// a name and a host read out of it. What the node can actually dial is decided there, by
// mihomo's own parser.
func TestCheckOutboundLink(t *testing.T) {
	own := []string{"203.0.113.5", "vpn.example.com", "example.org:8443"}
	cases := []struct {
		name       string
		link       string
		kind, host string
		remark     string
		err        error
	}{
		{name: "vless", link: "vless://11111111-2222-3333-4444-555555555555@198.51.100.30:443?security=tls#NL%20Amsterdam", kind: "vless", host: "198.51.100.30", remark: "NL Amsterdam"},
		{name: "vless without a name", link: "vless://11111111-2222-3333-4444-555555555555@[2001:db8::1]:443", kind: "vless", host: "2001:db8::1"},
		{name: "trojan", link: "trojan://secret@198.51.100.32:443?sni=x.example", kind: "trojan", host: "198.51.100.32"},
		{name: "ss", link: "ss://YWVzLTI1Ni1nY206c2VjcmV0@198.51.100.31:8388#SS", kind: "ss", host: "198.51.100.31", remark: "SS"},
		{name: "ss base64", link: "ss://YWVzLTI1Ni1nY206c2VjcmV0QDE5OC41MS4xMDAuMzE6ODM4OA", kind: "ss", host: "198.51.100.31"},
		{name: "vmess", link: "vmess://eyJ2IjoiMiIsInBzIjoiVk0iLCJhZGQiOiIxOTguNTEuMTAwLjMzIiwicG9ydCI6IjQ0MyJ9", kind: "vmess", host: "198.51.100.33", remark: "VM"},
		{name: "hysteria2", link: "hysteria2://pass@198.51.100.34:8443?sni=x.example", kind: "hysteria2", host: "198.51.100.34"},
		{name: "own address", link: "vless://11111111-2222-3333-4444-555555555555@203.0.113.5:443", err: ErrOutboundSelf},
		{name: "own domain", link: "trojan://secret@vpn.example.com:443", err: ErrOutboundSelf},
		{name: "own node with a port", link: "vless://11111111-2222-3333-4444-555555555555@example.org:8443", err: ErrOutboundSelf},
		{name: "not a link", link: "hello", err: ErrOutboundLink},
		{name: "unknown protocol", link: "foo://x@203.0.113.9:1", err: ErrOutboundLink},
		{name: "two lines", link: "vless://a@203.0.113.9:1\nvless://b@203.0.113.9:2", err: ErrOutboundLink},
		{name: "a space inside", link: "vless://a@203.0.113.9:1 x", err: ErrOutboundLink},
		{name: "empty", link: "", err: ErrOutboundLink},
		{name: "too long", link: "vless://" + strings.Repeat("a", MaxOutboundLink), err: ErrOutboundLink},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CheckOutboundLink(c.link, own)
			if !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if c.err != nil {
				return
			}
			if got.Kind != c.kind || got.Host != c.host || got.Name != c.remark {
				t.Fatalf("read as %+v", got)
			}
		})
	}
	// The name is a name and nothing else: control characters go, and it stays short.
	if n := cleanOutboundName("  A\x00B\nC  "); n != "ABC" {
		t.Fatalf("cleaned to %q", n)
	}
	if n := cleanOutboundName(strings.Repeat("я", 100)); len([]rune(n)) != maxOutboundName {
		t.Fatalf("not cut: %d", len([]rune(n)))
	}
}

// isNameInUse tells the panel's own refusal: another outbound already shows under the name.
func isNameInUse(err error) bool {
	var e *NameInUseError
	return errors.As(err, &e)
}

// An outbound of its own name, kept and edited, and gone only when nothing leaves through
// it: an inbound pointing at it would quietly go out the direct way.
func TestOutboundLife(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	q := st.Q

	o, err := AddOutbound(ctx, q, "vless://11111111-2222-3333-4444-555555555555@198.51.100.30:443#Frakfurt", "", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if o.Name != "Frakfurt" || o.Enabled != 1 {
		t.Fatalf("created as %+v", o)
	}
	// A second one with the same name is refused: the list shows names, not ids.
	if _, err := AddOutbound(ctx, q, "trojan://secret@198.51.100.32:443#frakfurt", "", nil, now); !isNameInUse(err) {
		t.Fatalf("a twin name was accepted: %v", err)
	}
	if _, err := AddOutbound(ctx, q, "trojan://secret@198.51.100.32:443", "Trojan", nil, now); err != nil {
		t.Fatal(err)
	}
	// Replacing the link keeps the name unless it is renamed in the same patch.
	two, err := UpdateOutbound(ctx, q, o.ID, OutboundPatch{Link: ptr("vless://11111111-2222-3333-4444-555555555555@198.51.100.99:443#Berlin")}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if two.Name != "Berlin" || two.Link != "vless://11111111-2222-3333-4444-555555555555@198.51.100.99:443#Berlin" {
		t.Fatalf("relinked as %+v", two)
	}
	if _, err := UpdateOutbound(ctx, q, o.ID, OutboundPatch{Link: ptr("vless://11111111-2222-3333-4444-555555555555@198.51.100.98:443#Neu"), Name: ptr("My own")}, nil, now); err != nil {
		t.Fatal(err)
	}
	if two, _ := q.GetOutbound(ctx, o.ID); two.Name != "My own" {
		t.Fatalf("renamed as %+v", two)
	}
	off, err := UpdateOutbound(ctx, q, o.ID, OutboundPatch{Enabled: ptr(false)}, nil, now)
	if err != nil || off.Enabled != 0 {
		t.Fatalf("switched off as %+v %v", off, err)
	}
	// A bad link never replaces a working one.
	if _, err := UpdateOutbound(ctx, q, o.ID, OutboundPatch{Link: ptr("nonsense")}, nil, now); !errors.Is(err, ErrOutboundLink) {
		t.Fatalf("a bad link was accepted: %v", err)
	}
	if _, err := UpdateOutbound(ctx, q, o.ID, OutboundPatch{Name: ptr("Trojan")}, nil, now); !isNameInUse(err) {
		t.Fatalf("a twin name was accepted: %v", err)
	}
	// One inbound leaves through it: it cannot be deleted while that holds.
	if err := Seed(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	ins, err := q.ListInbounds(ctx)
	if err != nil || len(ins) == 0 {
		t.Fatalf("no inbounds to route: %v", err)
	}
	in := ins[0]
	route := func(outboundID int64) error {
		return q.SetInboundRoute(ctx, db.SetInboundRouteParams{Outbound: "direct", ID: in.ID,
			OutboundID: sql.NullInt64{Int64: outboundID, Valid: outboundID != 0}})
	}
	if err := route(o.ID); err != nil {
		t.Fatal(err)
	}
	uses, err := OutboundUsesOf(ctx, q, o.ID)
	if err != nil || len(uses) != 1 || uses[0].ID != in.ID {
		t.Fatalf("uses = %+v %v", uses, err)
	}
	if err := DeleteOutbound(ctx, q, o.ID); !errors.Is(err, ErrOutboundUsed) {
		t.Fatalf("deleted while used: %v", err)
	}
	if err := route(0); err != nil {
		t.Fatal(err)
	}
	if err := DeleteOutbound(ctx, q, o.ID); err != nil {
		t.Fatal(err)
	}
	if err := DeleteOutbound(ctx, q, o.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
}
