package nodesync

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// fakeTransport is a node's client that carries streams: Tunnel hands back a connection to
// where the node really is, which a test points anywhere.
type fakeTransport struct {
	fakeNode
	real  string
	fails bool
	asked []string
}

func (f *fakeTransport) Tunnel(ctx context.Context, addr string) (net.Conn, error) {
	f.asked = append(f.asked, addr)
	if f.fails {
		return nil, &nodeapi.Error{Code: "tunnel_unreachable", Message: "the target does not answer"}
	}
	return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", f.real)
}

// transportSetup is a panel with its own node and nothing else running yet.
func transportSetup(t *testing.T) (*store.Store, *Manager, time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := domain.Seed(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now }
	m := NewManager(st, settings.New(st.Q), domain.NewPool(st, clock), func(db.Node) (Target, error) { return Target{}, ErrNoNode },
		slog.New(slog.NewTextHandler(io.Discard, nil)), clock)
	return st, m, now
}

// syncerOf is one node's syncer, which a test drives by hand.
func syncerOf(t *testing.T, m *Manager, id int64) *Syncer {
	t.Helper()
	s, ok := m.Syncer(id)
	if !ok {
		t.Fatalf("no syncer for node %d", id)
	}
	return s
}

// nodeRow makes a node and returns its row as the panel keeps it.
func nodeRow(t *testing.T, st *store.Store, name, address string) db.Node {
	t.Helper()
	n, err := st.Q.CreateNode(context.Background(), db.CreateNodeParams{Name: name, Address: address, PublicHost: "203.0.113.1", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The transport of a node: the one the admin named, or the first healthy node the panel
// reaches directly; never the node itself, never one that is itself carried, never a node that
// cannot tunnel or does not answer.
func TestTransportFor(t *testing.T) {
	st, m, now := transportSetup(t)
	a := nodeRow(t, st, "a", "203.0.113.10:8443")
	b := nodeRow(t, st, "b", "203.0.113.11:8443")
	c := nodeRow(t, st, "c", "203.0.113.12:8443")

	fa := &fakeTransport{real: a.Address}
	m.attach(t, a.ID, Target{Node: fa, TLS: fakeTLS, Address: a.Address})
	fb := &fakeTransport{real: b.Address}
	m.attach(t, b.ID, Target{Node: fb, TLS: fakeTLS, Address: b.Address})

	if _, via, ok := m.transportFor(a); !ok || via != b.ID {
		t.Fatalf("auto for a: %d %v, want b", via, ok)
	}
	if _, via, ok := m.transportFor(b); !ok || via != a.ID {
		t.Fatalf("auto for b: %d %v, want a", via, ok)
	}
	// a carried by b is no transport itself: the chain stops at one hop.
	m.noteVia(a.ID, b.ID, nil)
	if _, via, ok := m.transportFor(b); ok {
		t.Fatalf("b took a as its transport although a is carried: %d", via)
	}
	// a node named by the admin wins, even one carried by another.
	if _, via, ok := m.transportFor(db.Node{ID: b.ID, Address: b.Address, TransportID: a.ID}); !ok || via != a.ID {
		t.Fatalf("the admin's choice: %d %v, want a", via, ok)
	}
	// The node itself is never its own transport.
	if _, via, ok := m.transportFor(db.Node{ID: a.ID, Address: a.Address, TransportID: a.ID}); ok {
		t.Fatalf("a is its own transport: %d", via)
	}
	// A node with no syncer (removed, not started) cannot carry anything.
	if _, via, ok := m.transportFor(db.Node{ID: 99, Address: "203.0.113.99:8443", TransportID: c.ID}); ok {
		t.Fatalf("an unstarted transport carried: %d", via)
	}
	// A node that does not answer is not chosen while another one can.
	m.noteDirect(a.ID, now, nil) // a answers directly again
	syncerOf(t, m, b.ID).health.Store(&HealthView{CheckedAt: now, OK: false, Code: nodeapi.LinkTimeout})
	if _, via, ok := m.transportFor(db.Node{ID: c.ID, Address: c.Address}); !ok || via != a.ID {
		t.Fatalf("auto past an unhealthy node: %d %v, want a", via, ok)
	}
	// A node whose client cannot tunnel is no transport at all: b does not answer and c
	// cannot carry, so a has none.
	m.attach(t, c.ID, Target{Node: &fakeNode{}, TLS: fakeTLS, Address: c.Address})
	syncerOf(t, m, a.ID).health.Store(&HealthView{CheckedAt: now, OK: false})
	if _, via, ok := m.transportFor(db.Node{ID: a.ID, Address: a.Address}); ok {
		t.Fatalf("a node that cannot tunnel carried: %d", via)
	}
}

// What a node may carry: the API addresses of the panel's other nodes as ip:port; not its own,
// not the panel's own node (a socket), not a node addressed by a name.
func TestTunnelTo(t *testing.T) {
	nodes := []db.Node{
		{ID: 1, Name: "own"},
		{ID: 2, Name: "a", Address: "203.0.113.10:8443"},
		{ID: 3, Name: "b", Address: "node.example:8443", PublicHost: "203.0.113.1"},
		{ID: 4, Name: "c", Address: "[2001:db8::1]:8443"},
	}
	if got := tunnelTo(nodes, 1); len(got) != 2 || got[0] != "203.0.113.10:8443" || got[1] != "[2001:db8::1]:8443" {
		t.Fatalf("own node carries %v, want the two ip:port", got)
	}
	if got := tunnelTo(nodes, 2); len(got) != 1 || got[0] != "[2001:db8::1]:8443" {
		t.Fatalf("a carries %v, want c alone", got)
	}
	// The addresses of the other nodes are what a locked API must let in: a tunnel arrives
	// from the transport's address, not the panel's.
	got := transportNets(nodes, 2)
	for _, want := range []string{"203.0.113.1/32", "2001:db8::1/128"} {
		if !slices.Contains(got, want) {
			t.Errorf("transport networks %v have no %s", got, want)
		}
	}
}

// A direct path that failed lately is carried by the transport at once; the direct path is
// tried again after directRetry, and an answer from it clears the transport.
func TestRouteRemembersTheDirectPath(t *testing.T) {
	st, m, now := transportSetup(t)
	a := nodeRow(t, st, "a", "203.0.113.10:8443")
	b := nodeRow(t, st, "b", "203.0.113.11:8443")
	m.attach(t, b.ID, Target{Node: &fakeTransport{real: b.Address}, TLS: fakeTLS, Address: b.Address})

	m.noteDirect(a.ID, now, &nodeapi.StatusError{Method: "GET", Path: "/v1/health", Status: 503})
	if r := m.RouteOf(a.ID); r.DirectCode != nodeapi.LinkHTTPStatus || r.ViaID != 0 {
		t.Fatalf("a failed direct path: %+v", r)
	}
	if !m.routeOf(a.ID).directFailed(now.Add(directRetry - time.Second)) {
		t.Fatal("the failure is forgotten before directRetry")
	}
	if m.routeOf(a.ID).directFailed(now.Add(directRetry + time.Second)) {
		t.Fatal("the failure outlives directRetry")
	}
	m.noteVia(a.ID, b.ID, nil)
	if r := m.RouteOf(a.ID); r.ViaID != b.ID || r.ViaError != "" || r.DirectCode != nodeapi.LinkHTTPStatus {
		t.Fatalf("carried by b: %+v", r)
	}
	// The tunnel failed too: the node is unreachable both ways, and the page says both.
	m.noteVia(a.ID, b.ID, &nodeapi.Error{Code: "tunnel_unreachable", Message: "down"})
	if r := m.RouteOf(a.ID); r.ViaError == "" || r.ViaID != b.ID {
		t.Fatalf("a failed transport: %+v", r)
	}
	// The direct path answers again: the transport is over.
	m.noteDirect(a.ID, now, nil)
	if r := m.RouteOf(a.ID); r.ViaID != 0 || r.DirectCode != "" {
		t.Fatalf("direct again: %+v", r)
	}
}

// The state of a node reaches it through another node when the panel's own path does not
// answer: two node APIs in this process, the target's address unreachable for the panel, the
// transport's open. The panel keeps its own TLS to the target inside the tunnel, so the
// target answers the panel itself, never the transport.
func TestStateReachesThroughATransport(t *testing.T) {
	st, m, _ := transportSetup(t)
	ctx := context.Background()
	panel, err := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	panelPin, _ := nodetls.Fingerprint(panel.CertPEM)

	// The target: a node API on this process, answering its health.
	targetMux := http.NewServeMux()
	targetMux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(nodeapi.Health{Version: "test", Revision: 1})
	})
	targetSrv := nodeAPI(t, panelPin, targetMux)
	targetAddr := targetSrv.Listener.Addr().String()

	// The transport: another node API that carries a CONNECT to where the target really is.
	asked := make(chan string, 4)
	// A CONNECT is routed by the node's own handler, before its mux: ServeMux does not match
	// the authority-form request line at all.
	transportSrv := nodeAPI(t, panelPin, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.NotFound(w, r)
			return
		}
		select {
		case asked <- r.Host:
		default:
		}
		up, err := net.Dial("tcp", targetAddr)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			up.Close()
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			up.Close()
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(up, buf.Reader); done <- struct{}{} }()
		go func() { _, _ = io.Copy(conn, up); done <- struct{}{} }()
		<-done
		conn.Close()
		up.Close()
		<-done
	}))
	transportAddr := transportSrv.Listener.Addr().String()

	// The target's row: an address the panel cannot reach (nothing listens on port 1). The
	// transport's: its own, which the panel reaches directly.
	target := nodeRow(t, st, "target", "127.0.0.1:1")
	via := nodeRow(t, st, "via", transportAddr)

	targetClient := clientFor(t, panel, "127.0.0.1:1", targetSrv)
	targetClient.SetRoute(m.route(target))
	ts := m.attach(t, target.ID, Target{Node: targetClient, TLS: fakeTLS, Address: target.Address})
	viaClient := clientFor(t, panel, transportAddr, transportSrv)
	m.attach(t, via.ID, Target{Node: viaClient, TLS: fakeTLS, Address: via.Address})

	hv, ok := m.CheckNow(ctx, target.ID)
	if !ok || !hv.OK {
		t.Fatalf("the node through the transport: %+v", hv)
	}
	if hv.Route.ViaID != via.ID {
		t.Fatalf("the route says %+v, want the transport %d", hv.Route, via.ID)
	}
	if hv.Route.DirectCode != nodeapi.LinkRefused {
		t.Fatalf("the direct path's failure: %+v", hv.Route)
	}
	select {
	case host := <-asked:
		if host != target.Address {
			t.Fatalf("the transport was asked for %q, want the node's address %q", host, target.Address)
		}
	default:
		t.Fatal("the transport was asked for nothing")
	}

	// The transport goes away: the node is unreachable both ways and the page says so.
	transportSrv.Close()
	hv, _ = m.CheckNow(ctx, target.ID)
	if hv.OK {
		t.Fatalf("the node answers with its transport gone: %+v", hv)
	}
	if hv.Route.ViaID != via.ID || hv.Route.ViaError == "" {
		t.Fatalf("the route after the transport failed: %+v", hv.Route)
	}
	if hv.Code != nodeapi.LinkRefused {
		t.Fatalf("the failure code %q, want the direct path's %q", hv.Code, nodeapi.LinkRefused)
	}
	if ts.health.Load().OK {
		t.Fatal("the health view still says ok")
	}
}

// nodeAPI starts a node API over TLS that only the panel's certificate may reach, with the
// handler the test gives.
func nodeAPI(t *testing.T, panelPin string, h http.Handler) *httptest.Server {
	t.Helper()
	nodeCert, err := nodetls.Generate("node-test", x509.ExtKeyUsageServerAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	scfg, err := nodetls.Key{Port: 1, PanelPin: panelPin, CertPEM: nodeCert.CertPEM, KeyPEM: nodeCert.KeyPEM}.ServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = scfg
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// clientFor is the panel's client for a node: it pins the node's certificate, and dials the
// row's address (which may be unreachable) — the route is what gets it there.
func clientFor(t *testing.T, panel nodetls.Pair, address string, srv *httptest.Server) *nodeapi.Client {
	t.Helper()
	tc, err := nodetls.ClientConfig(panel, nodetls.PinOfDER(srv.Certificate().Raw))
	if err != nil {
		t.Fatal(err)
	}
	return nodeapi.NewTLSClient(address, tc)
}
