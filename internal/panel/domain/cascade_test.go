package domain

import (
	"database/sql"
	"slices"
	"testing"

	"mikan/internal/panel/store/db"
)

// Each node gets its part: the exits its listeners leave through with their reserves, and
// whether anyone leaves through it.
func TestCascadePlans(t *testing.T) {
	remote := func(id int64, host, cert string) db.Node {
		return db.Node{ID: id, Name: host, Address: host + ":40000", CertSha256: cert, Enabled: 1}
	}
	exit := func(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: true} }
	nodes := []db.Node{
		{ID: 1, Name: "panel", Enabled: 1}, // the panel's own node: no address, no key
		remote(2, "198.51.100.2", "aa"),    // A: an entry
		remote(3, "198.51.100.3", "bb"),    // B: A's exit, leaving on through C
		remote(4, "198.51.100.4", "cc"),    // C
		remote(5, "198.51.100.5", "dd"),    // D: an entry too
		{ID: 6, Name: "no key", Address: "198.51.100.6:40000", Enabled: 1},
	}
	inbounds := []db.Inbound{
		{ID: 1, NodeID: 2, Name: "vless-1", Enabled: 1, ExitNodeID: exit(3)},
		{ID: 2, NodeID: 5, Name: "vless-2", Enabled: 1, ExitNodeID: exit(3)},
		{ID: 3, NodeID: 5, Name: "vless-3", Enabled: 1, ExitNodeID: exit(6)},
		// A disabled inbound leaves nothing through anybody.
		{ID: 4, NodeID: 2, Name: "vless-4", Enabled: 0, ExitNodeID: exit(5)},
	}
	relays := []db.NodeRelay{{NodeID: 3, ExitNodeID: exit(4)}}
	plans := CascadePlans(nodes, inbounds, relays)

	a := plans[2]
	if len(a.Routes) != 1 || a.Routes[0].Exit != 3 || a.Routes[0].Name != "NODE-3" || !a.Routes[0].Tunnel {
		t.Fatalf("A's routes: %+v", a.Routes)
	}
	// The reserves are the other exits that can carry, nearest by id: C, and not the node
	// without a key (it cannot carry) and not A itself.
	if want := []int64{4}; !slices.Equal(a.Routes[0].Reserves, want) {
		t.Fatalf("A's reserves: %v, want %v", a.Routes[0].Reserves, want)
	}
	if a.IsExit {
		t.Fatal("nobody leaves through A")
	}
	b := plans[3]
	if len(b.Routes) != 1 || b.Routes[0].Exit != 4 || !b.Routes[0].Tunnel || !b.IsExit {
		t.Fatalf("B: %+v", b)
	}
	d := plans[5]
	if len(d.Routes) != 2 || d.Routes[0].Exit != 3 || d.Routes[1].Exit != 6 || d.Routes[1].Tunnel {
		t.Fatalf("D: %+v", d.Routes)
	}
	if d.IsExit {
		t.Fatal("the disabled inbound is no exit: nobody enabled leaves through D")
	}
	if !plans[6].IsExit {
		t.Fatal("a node without a key that someone names is still an exit (and cannot carry)")
	}
	if p, ok := plans[1]; ok && (p.IsExit || len(p.Routes) > 0) {
		t.Fatalf("the panel's own node has no part: %+v", p)
	}
}

// A node that leaves through this one in turn is no reserve of it: the cascade has one
// hop, and that one would close a loop.
func TestCascadeReservesSkipNodesThatLeaveThroughThisOne(t *testing.T) {
	remote := func(id int64, host, cert string) db.Node {
		return db.Node{ID: id, Name: host, Address: host + ":40000", CertSha256: cert, Enabled: 1}
	}
	exit := func(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: true} }
	nodes := []db.Node{
		remote(2, "198.51.100.2", "aa"), // A
		remote(3, "198.51.100.3", "bb"), // B, leaving through A in turn
		remote(4, "198.51.100.4", "cc"), // C, D's exit
		remote(5, "198.51.100.5", "dd"), // D
	}
	inbounds := []db.Inbound{
		{ID: 1, NodeID: 2, Name: "a", Enabled: 1, ExitNodeID: exit(3)},
		{ID: 2, NodeID: 3, Name: "b", Enabled: 1, ExitNodeID: exit(2)},
		{ID: 3, NodeID: 5, Name: "d", Enabled: 1, ExitNodeID: exit(4)},
	}
	plans := CascadePlans(nodes, inbounds, nil)
	reserves := plans[2].Routes[0].Reserves
	if slices.Contains(reserves, 3) {
		t.Fatalf("B leaves through A and cannot be its reserve: %v", reserves)
	}
	if !slices.Equal(reserves, []int64{4}) {
		t.Fatalf("A's reserves: %v", reserves)
	}
}

// One node, two ways out: an inbound leaving through the exit directly and another through
// the same exit's WARP. The routes are separate — their names differ, so the entry's core
// sends each protocol to its own proxy — the exit runs its WARP listener, and the WARP
// route has no reserves: another exit is another address, and the address is its point.
func TestCascadeWarpRoute(t *testing.T) {
	remote := func(id int64, host, cert string) db.Node {
		return db.Node{ID: id, Name: host, Address: host + ":40000", CertSha256: cert, Enabled: 1}
	}
	exit := func(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: true} }
	nodes := []db.Node{remote(1, "198.51.100.1", "aa"), remote(2, "198.51.100.2", "bb"), remote(3, "198.51.100.3", "cc")}
	inbounds := []db.Inbound{
		{ID: 1, NodeID: 1, Name: "vless-plain", Enabled: 1, ExitNodeID: exit(2)},
		{ID: 2, NodeID: 1, Name: "vless-warp", Enabled: 1, ExitNodeID: exit(2), ExitWarp: 1},
		// Node 3 is an exit of its own, so it is the plain route's reserve; the WARP route
		// gets none, whatever else can carry.
		{ID: 3, NodeID: 1, Name: "vless-3", Enabled: 1, ExitNodeID: exit(3)},
	}
	plans := CascadePlans(nodes, inbounds, nil)

	a := plans[1]
	if len(a.Routes) != 3 {
		t.Fatalf("A's routes: %+v", a.Routes)
	}
	var plain, warp *CascadeRoute
	for i := range a.Routes {
		switch a.Routes[i].Name {
		case "NODE-2":
			plain = &a.Routes[i]
		case "NODE-2~warp":
			warp = &a.Routes[i]
		}
	}
	if plain == nil || plain.Warp || !plain.Tunnel {
		t.Fatalf("the plain route: %+v", a.Routes)
	}
	if warp == nil || !warp.Warp || !warp.Tunnel {
		t.Fatalf("the WARP route: %+v", a.Routes)
	}
	if res := warp.Reserves; len(res) != 0 {
		t.Fatalf("a WARP route has no reserves: %v", res)
	}
	if want := []int64{3}; !slices.Equal(plain.Reserves, want) {
		t.Fatalf("a plain route keeps its reserves: %v, want %v", plain.Reserves, want)
	}
	if b := plans[2]; !b.IsExit || !b.WarpExit {
		t.Fatalf("the exit: %+v", b)
	}
	// Only the WARP route asks for the exit's WARP: the plain one alone leaves WarpExit off.
	inbounds[1].ExitWarp = 0
	if b := CascadePlans(nodes, inbounds, nil)[2]; b.WarpExit {
		t.Fatalf("no route asks for the WARP: %+v", b)
	}
}
