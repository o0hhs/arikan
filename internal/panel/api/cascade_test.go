package api

import (
	"database/sql"
	"testing"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/store/db"
)

// The cascade page reads the relay's listener against the port the node runs it on: a
// busy port is named so, and a node that has not caught up with a move says nothing.
func TestRelayListener(t *testing.T) {
	inUse := nodeapi.ListenerStatus{Name: nodeapi.RelayListener, Error: "listen tcp 0.0.0.0:2053: bind: address already in use", Code: nodeapi.ListenerAddrInUse}
	denied := nodeapi.ListenerStatus{Name: nodeapi.RelayListener, Error: "listen tcp 0.0.0.0:2053: bind: permission denied"}
	up := nodeapi.ListenerStatus{Name: nodeapi.RelayListener, OK: true}
	other := nodeapi.ListenerStatus{Name: "vless", OK: true}
	view := func(ok bool, port string, ls ...nodeapi.ListenerStatus) nodesync.HealthView {
		return nodesync.HealthView{OK: ok, Ports: map[string]string{nodeapi.RelayListener: port}, Listeners: ls}
	}
	for _, c := range []struct {
		name      string
		hv        nodesync.HealthView
		port      string
		state     string
		hasReason bool
	}{
		{"up", view(true, "2053", other, up), "2053", relayUp, false},
		{"busy", view(true, "2053", other, inUse), "2053", relayBusy, true},
		{"another failure", view(true, "2053", other, denied), "2053", relayFailed, true},
		{"the node runs the old port", view(true, "2053", other, inUse), "2083", relayUnknown, false},
		{"no applied state", nodesync.HealthView{OK: true, Listeners: []nodeapi.ListenerStatus{inUse}}, "2053", relayUnknown, false},
		{"the node does not answer", view(false, "2053", inUse), "2053", relayUnknown, false},
		{"no relay listener yet", view(true, "2053", other), "2053", relayUnknown, false},
	} {
		state, why := relayListener(c.hv, c.port)
		if state != c.state || (why != "") != c.hasReason {
			t.Errorf("%s: %q %q, want %q (reason %v)", c.name, state, why, c.state, c.hasReason)
		}
	}
}

// The chain the card shows is the rows the states are built from: the relay rows decide
// where an exit's traffic goes on, and only the last one decides direct or WARP.
func TestExitChain(t *testing.T) {
	names := map[int64]string{1: "entry", 2: "exit", 3: "third", 4: "far"}
	relay := func(id int64, outbound string, exit int64) db.NodeRelay {
		return db.NodeRelay{NodeID: id, Outbound: outbound, ExitNodeID: sql.NullInt64{Int64: exit, Valid: exit != 0}}
	}
	rows := map[int64]db.NodeRelay{
		2: relay(2, "warp", 0),
		3: relay(3, "direct", 0),
		4: relay(4, "warp", 0),
	}
	if chain, end := exitChain(2, rows, names); len(chain) != 0 || end != endWarp {
		t.Fatalf("a WARP exit: %+v %q", chain, end)
	}
	if chain, end := exitChain(3, rows, names); len(chain) != 0 || end != endDirect {
		t.Fatalf("a direct exit: %+v %q", chain, end)
	}
	// An exit that goes on to another node: the chain names it and ends where it ends.
	rows[2] = relay(2, "direct", 4)
	if chain, end := exitChain(2, rows, names); len(chain) != 1 || chain[0].NodeID != 4 || chain[0].Name != "far" || end != endWarp {
		t.Fatalf("a chain to another node's WARP: %+v %q", chain, end)
	}
	if got := chainEnd(2, []CascadeHop{{NodeID: 4}}); got != 4 {
		t.Fatalf("the chain ends on %d", got)
	}
	if got := chainEnd(2, nil); got != 2 {
		t.Fatalf("a chain of one hop ends on itself, got %d", got)
	}
	// A row that points back (a hand edit; the panel never saves a loop) stops at the
	// repeat and leaves direct: nothing may be dialled round and round.
	rows[2] = relay(2, "direct", 3)
	rows[3] = relay(3, "direct", 2)
	if chain, end := exitChain(2, rows, names); len(chain) != 1 || chain[0].NodeID != 3 || end != endDirect {
		t.Fatalf("a loop: %+v %q", chain, end)
	}
	// No row at all (a node nobody routed): direct, no chain.
	delete(rows, 2)
	if chain, end := exitChain(2, rows, names); len(chain) != 0 || end != endDirect {
		t.Fatalf("a node without a relay: %+v %q", chain, end)
	}
}
