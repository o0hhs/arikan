package nodesync

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// panelPublic makes the panel answer at 203.0.113.10:8443 and leaves the local node's first
// working inbound, the entry of the tests below.
func panelPublic(t *testing.T) (*Syncer, *store.Store, db.Inbound) {
	t.Helper()
	a, _, st, _, _ := setup(t)
	ctx := context.Background()
	if err := settings.Set(ctx, settings.New(st.Q), settings.KeyPublicHost, "203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, settings.New(st.Q), settings.KeyPanelPort, 8443); err != nil {
		t.Fatal(err)
	}
	ins, err := st.Q.ListInbounds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range ins {
		if in.NodeID == a.id && in.Enabled != 0 {
			return a, st, in
		}
	}
	t.Fatal("the seed left the local node no inbound")
	return nil, nil, db.Inbound{}
}

// addNode adds a remote node with its own join key and attaches a syncer to it.
func addNode(t *testing.T, a *Syncer, st *store.Store, name, host string) (db.Node, *Syncer) {
	t.Helper()
	ctx := context.Background()
	panel, err := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	n, _, err := domain.AddNode(ctx, st, panel, domain.NodeInput{Name: name, Host: host, APIPort: 40000}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return n, a.m.attach(t, n.ID, Target{Node: &fakeNode{}, TLS: fakeTLS})
}

// useExit makes src leave through exit: the exit's relay and the source's key, as the API
// does when an exit is chosen.
func useExit(t *testing.T, st *store.Store, src, exit int64) {
	t.Helper()
	ctx := context.Background()
	if err := domain.CheckExit(ctx, st.Q, src, exit); err != nil {
		t.Fatal(err)
	}
	x, err := st.Q.GetNode(ctx, exit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := domain.EnsureRelay(ctx, st.Q, x, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := domain.RelayUser(ctx, st.Q, exit, src); err != nil {
		t.Fatal(err)
	}
}

// routeInbound sends one inbound of a node to an exit.
func routeInbound(t *testing.T, st *store.Store, in db.Inbound, exit int64) {
	t.Helper()
	if err := st.Q.SetInboundRoute(context.Background(), db.SetInboundRouteParams{ExitNodeID: nullID(exit), Outbound: "direct", ID: in.ID}); err != nil {
		t.Fatal(err)
	}
}

// The entry's state names the exit by its name and its own bridge: nothing in it makes the
// node dial the exit's address. The exit is told where the panel answers.
func TestCascadeGoesOverTheTunnel(t *testing.T) {
	a, st, in := panelPublic(t)
	ctx := context.Background()
	nb, b := addNode(t, a, st, "B", "198.51.100.20")
	useExit(t, st, a.id, nb.ID)
	routeInbound(t, st, in, nb.ID)

	da, err := a.desired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if da.Cascade == nil || da.Cascade.Entry == nil {
		t.Fatal("the entry has no bridge in its state")
	}
	if da.Cascade.Entry.Listen != nodeapi.CascadeListen || da.Cascade.Entry.Password == "" {
		t.Fatalf("the bridge: %+v", da.Cascade.Entry)
	}
	if len(da.Cascade.Entry.Routes) != 1 || da.Cascade.Entry.Routes[0].Name != nodeapi.ExitName(nb.ID) {
		t.Fatalf("the routes: %+v", da.Cascade.Entry.Routes)
	}
	if got := da.Cascade.Entry.Routes[0].Exits; len(got) == 0 || got[0] != nb.ID {
		t.Fatalf("the exit and its reserves: %v", got)
	}
	if len(da.Exits) != 1 {
		t.Fatalf("the exits: %+v", da.Exits)
	}
	var proxy map[string]any
	if err := json.Unmarshal(da.Exits[0].Proxy, &proxy); err != nil {
		t.Fatal(err)
	}
	if proxy["type"] != "socks5" || proxy["server"] != "127.0.0.1" ||
		proxy["username"] != nodeapi.ExitName(nb.ID) || proxy["password"] != da.Cascade.Entry.Password {
		t.Fatalf("the exit's proxy: %v", proxy)
	}
	if proxy["udp"] != false {
		t.Fatalf("the exit's proxy carries datagrams: %v", proxy)
	}
	// The one thing the cascade must not do: the entry never learns the exit's address.
	raw, err := json.Marshal(da)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "198.51.100.20") {
		t.Fatalf("the state carries the exit's address: %s", raw)
	}

	db_, err := b.desired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if db_.Cascade == nil || db_.Cascade.Exit == nil {
		t.Fatal("the exit has no stream in its state")
	}
	if want := "https://203.0.113.10:8443" + nodeapi.CascadePath; db_.Cascade.Exit.URL != want {
		t.Fatalf("the exit's endpoint: %q, want %q", db_.Cascade.Exit.URL, want)
	}
	if db_.Cascade.Entry != nil {
		t.Fatal("the exit has a bridge of its own: nothing leaves through it")
	}
}

// An exit without a join key cannot prove itself to the panel: the state names it with a
// way to nowhere, and the inbounds behind it fail rather than leave directly.
func TestAnExitWithoutAKeyIsNotInTheTunnel(t *testing.T) {
	a, st, in := panelPublic(t)
	ctx := context.Background()
	nb, b := addNode(t, a, st, "B", "198.51.100.20")
	useExit(t, st, a.id, nb.ID)
	routeInbound(t, st, in, nb.ID)
	if err := st.Q.SetNodeCert(ctx, db.SetNodeCertParams{CertSha256: "", UpdatedAt: time.Now().Unix(), ID: nb.ID}); err != nil {
		t.Fatal(err)
	}
	a.m.changes.Add(1) // a new snapshot, as the API's change would ask for

	da, err := a.desired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if da.Cascade != nil && da.Cascade.Entry != nil {
		t.Fatalf("a cascade with nothing to carry it: %+v", da.Cascade.Entry)
	}
	if len(da.Exits) != 1 {
		t.Fatalf("the exits: %+v", da.Exits)
	}
	var proxy map[string]any
	if err := json.Unmarshal(da.Exits[0].Proxy, &proxy); err != nil {
		t.Fatal(err)
	}
	if proxy["type"] != "vless" || proxy["server"] != "192.0.2.1" {
		t.Fatalf("the exit must fail, not be dialled: %v", proxy)
	}
	db_, err := b.desired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if db_.Cascade != nil && db_.Cascade.Exit != nil {
		t.Fatal("a node without a key cannot be an exit")
	}
}

// Two exits, one named by the inbound: the other is a reserve the entry tries when the
// first tunnel is down, and the reserve is told to keep its stream too.
func TestCascadeReservesReachTheExit(t *testing.T) {
	a, st, in := panelPublic(t)
	ctx := context.Background()
	nb, _ := addNode(t, a, st, "B", "198.51.100.20")
	nc, c := addNode(t, a, st, "C", "198.51.100.30")
	useExit(t, st, a.id, nb.ID)
	useExit(t, st, nb.ID, nc.ID)
	routeInbound(t, st, in, nb.ID)
	// B's relay goes on through C: C is an exit of the panel, and the reserve of A's route.
	if err := st.Q.SetNodeRelayRoute(ctx, db.SetNodeRelayRouteParams{Outbound: "direct", ExitNodeID: nullID(nc.ID), NodeID: nb.ID}); err != nil {
		t.Fatal(err)
	}

	da, err := a.desired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if da.Cascade == nil || da.Cascade.Entry == nil || len(da.Cascade.Entry.Routes) != 1 {
		t.Fatalf("the entry's routes: %+v", da.Cascade)
	}
	got := da.Cascade.Entry.Routes[0].Exits
	if len(got) != 2 || got[0] != nb.ID || got[1] != nc.ID {
		t.Fatalf("the exit and its reserve: %v", got)
	}
	// The core's proxy is the exit the inbound named; the reserve is no second one.
	if len(da.Exits) != 1 || da.Exits[0].Name != nodeapi.ExitName(nb.ID) {
		t.Fatalf("the exits: %+v", da.Exits)
	}
	dc, err := c.desired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dc.Cascade == nil || dc.Cascade.Exit == nil {
		t.Fatal("a reserve must keep its stream: the entry may need it any moment")
	}
}
