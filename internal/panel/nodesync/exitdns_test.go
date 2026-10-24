package nodesync

import (
	"context"
	"slices"
	"testing"

	"mikan/internal/panel/exitdns"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/subs"
)

// The DNS at the exit reaches the nodes on from the start: a panel updated to it turns it
// on for every node. A change of it is a new state; off and the host's resolver leave the
// node as before it.
func TestExitDNSInState(t *testing.T) {
	s, _, st, _, _ := setup(t)
	ctx := context.Background()
	on, err := s.desired(ctx)
	if err != nil || on.DNS == nil || !on.DNS.Sniff || len(on.DNS.Servers) != 0 {
		t.Fatalf("on by default with the host's resolver: %+v %v", on.DNS, err)
	}
	set := func(c exitdns.Config) {
		t.Helper()
		if err := settings.Set(ctx, settings.New(st.Q), exitdns.KeyConfig, c); err != nil {
			t.Fatal(err)
		}
		s.m.SlotsChanged() // as the API does after saving
	}
	set(exitdns.Config{Sniff: true, Resolver: exitdns.ResolverGoogle, Servers: []string{"tls://dns.example"}})
	google, err := s.desired(ctx)
	if err != nil || google.DNS == nil || !slices.Equal(google.DNS.Servers, exitdns.Presets[exitdns.ResolverGoogle]) {
		t.Fatalf("google: %+v %v", google.DNS, err)
	}
	if stateKey(on) == stateKey(google) {
		t.Fatal("the resolver does not change the state key")
	}
	set(exitdns.Config{Resolver: exitdns.ResolverHost, Servers: []string{}})
	off, err := s.desired(ctx)
	if err != nil || off.DNS != nil {
		t.Fatalf("off: %+v %v", off.DNS, err)
	}
}

// The admin's Routing → DNS reaches the nodes on top of the exit DNS: with Exit DNS off it is
// the state itself, and a node resolves Gemini's names by it.
func TestRoutesDNSInState(t *testing.T) {
	s, _, st, _, _ := setup(t)
	ctx := context.Background()
	set := func(r subs.Routes) {
		t.Helper()
		if err := settings.Set(ctx, settings.New(st.Q), settings.KeyRoutes, r); err != nil {
			t.Fatal(err)
		}
		s.m.SlotsChanged()
	}
	set(subs.Routes{})
	if off, err := s.desired(ctx); err != nil {
		t.Fatal(err)
	} else if off.DNS != nil && (len(off.DNS.Nameserver) != 0 || len(off.DNS.Policy) != 0) {
		t.Fatalf("nothing configured carries no overlay: %+v", off.DNS)
	}
	set(subs.Routes{DNS: subs.DNS{Nameserver: []string{"https://dns.google/dns-query"}, Default: []string{"1.1.1.1"},
		Policy: []subs.DNSPolicy{{Match: "+.google.com", Servers: []string{"https://dns.google/dns-query"}}}}})
	on, err := s.desired(ctx)
	if err != nil || on.DNS == nil || !slices.Equal(on.DNS.Nameserver, []string{"https://dns.google/dns-query"}) ||
		len(on.DNS.Policy) != 1 || on.DNS.Policy[0].Match != "+.google.com" {
		t.Fatalf("the routing DNS: %+v %v", on.DNS, err)
	}
}
