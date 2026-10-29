package nodesync

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/store/db"
)

// The panel sometimes cannot reach a node's API itself: the panel is in one country and the
// node in another, or a hoster's route blackholes the panel's address. Then another node of
// the panel, one the panel does reach (usually its own, next to it), carries the stream: the
// panel asks it for HTTP CONNECT to the node's address (nodeapi.Client.Tunnel) and does its
// own TLS handshake to the node inside that stream. End to end the node still sees the
// panel's pinned certificate, not the transport's: the transport only moves bytes it cannot
// read.

const (
	// directBudget bounds the direct attempt when a transport could carry it: a node's API
	// answers in well under a second, and the tunnel needs the rest of the call's time
	// (the health check has five seconds).
	directBudget = 2500 * time.Millisecond
	// directRetry is how long a failed direct path is trusted to stay failed. While it holds,
	// the transport goes first and the direct path is tried after it, so a node behind a
	// blackholed route does not pay the budget on every call.
	directRetry = time.Minute
	// directTimeout is the direct attempt without a transport: the dialer's own, as before.
	directTimeout = 5 * time.Second
)

// tunnelNode is a node's client that carries a stream to another node (nodeapi.Client).
type tunnelNode interface {
	Tunnel(ctx context.Context, addr string) (net.Conn, error)
}

// routeState is how the panel last reached one node's API.
type routeState struct {
	// directOK: the last direct dial answered. directDown is when it last failed, directCode
	// why (nodeapi.Classify) and directText its words.
	directOK   bool
	directDown time.Time
	directCode string
	directText string
	// viaID is the transport of the last attempt that went through one (0: none), viaErr
	// why it failed ("" when it carried), carried whether it carries the node now.
	viaID   int64
	viaErr  string
	carried bool
}

// directFailed says whether the direct path failed lately and, if so, why.
func (r routeState) directFailed(now time.Time) bool {
	return !r.directDown.IsZero() && now.Sub(r.directDown) < directRetry
}

// RouteView is how the panel reached a node: directly, or through another node when its own
// path does not answer. It is the last connection's, which is the health check's (its idle
// connections are dropped before every check).
type RouteView struct {
	ViaID       int64  // the node that carried the stream; 0: the direct path
	DirectCode  string // why the direct path failed; "" while it answers
	DirectError string
	ViaError    string // why the transport failed; "" when it carried or was not needed
}

// route is what the panel's client for one node opens its connections with: the direct path,
// or a tunnel through another node (nodeapi.Route). A node addressed by a name has none: a
// transport's whitelist holds the panel's nodes as ip:port (a name would be resolved by the
// transport's resolver, not the panel's), so there is nothing to ask it for.
func (m *Manager) route(n db.Node) nodeapi.Route {
	if host, _, err := net.SplitHostPort(n.Address); err != nil {
		return nil
	} else if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err != nil {
		return nil
	}
	return func(ctx context.Context, addr string) (net.Conn, error) {
		return m.walk(ctx, n, addr)
	}
}

// direct opens one connection to addr, the direct path.
func (m *Manager) direct(ctx context.Context, addr string, budget time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: directTimeout, KeepAlive: 30 * time.Second}
	if budget <= 0 {
		return d.DialContext(ctx, "tcp", addr)
	}
	dctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return d.DialContext(dctx, "tcp", addr)
}

// walk opens one connection to a node: the transport first when the direct path failed
// lately, the direct path first otherwise; the other way is tried when one does not answer.
func (m *Manager) walk(ctx context.Context, n db.Node, addr string) (net.Conn, error) {
	now := m.now()
	st := m.routeOf(n.ID)
	transport, via, hasTransport := m.transportFor(n)
	// The transport carries a direct path that failed lately; the direct path is tried after
	// it (with the full budget) so a recovered route is noticed within directRetry.
	if hasTransport && st.directFailed(now) {
		conn, err := m.through(ctx, transport, via, addr)
		if err == nil {
			m.noteVia(n.ID, via, nil)
			return conn, nil
		}
		m.noteVia(n.ID, via, err)
		conn, directErr := m.direct(ctx, addr, 0)
		if directErr == nil {
			m.noteDirect(n.ID, now, nil)
			return conn, nil
		}
		m.noteDirect(n.ID, now, directErr)
		return nil, directErr
	}
	budget := time.Duration(0)
	if hasTransport {
		budget = directBudget
	}
	conn, directErr := m.direct(ctx, addr, budget)
	if directErr == nil {
		m.noteDirect(n.ID, now, nil)
		return conn, nil
	}
	m.noteDirect(n.ID, now, directErr)
	// Only a path that does not answer is carried. A node that answered with a TLS, pin or
	// status failure is reachable, and its answer is the truth about it.
	if !hasTransport || ctx.Err() != nil || !carriable(directErr) {
		return nil, directErr
	}
	conn, viaErr := m.through(ctx, transport, via, addr)
	if viaErr != nil {
		m.noteVia(n.ID, via, viaErr)
		// The direct path's failure is the one the panel explains: it is about the node's own
		// address; the transport's words stay beside it in the route view.
		return nil, directErr
	}
	m.noteVia(n.ID, via, nil)
	return conn, nil
}

// carriable says whether a failure of the direct path is one a transport may fix: a path
// that does not answer. A refusal by the node itself is the node's answer.
func carriable(err error) bool {
	switch nodeapi.Classify(err) {
	case nodeapi.LinkTimeout, nodeapi.LinkRefused, nodeapi.LinkUnreachable, nodeapi.LinkDNS, nodeapi.LinkUnknown:
		return true
	}
	return false
}

// through asks the transport node for a stream to addr.
func (m *Manager) through(ctx context.Context, transport tunnelNode, via int64, addr string) (net.Conn, error) {
	// NoFallback: a node that carries a stream dials its own target directly; two nodes that
	// name each other can never tunnel in a circle.
	return transport.Tunnel(nodeapi.NoFallback(ctx), addr)
}

// transportFor picks the node that carries this node's calls when its direct path does not
// answer: the node the admin named, or the first healthy node the panel reaches directly. A
// transport is never itself carried by one, so the chains stop at one hop.
func (m *Manager) transportFor(n db.Node) (tunnelNode, int64, bool) {
	if n.TransportID > 0 {
		if n.TransportID == n.ID {
			return nil, 0, false
		}
		s, ok := m.Syncer(n.TransportID)
		if !ok {
			return nil, 0, false
		}
		tn, ok := s.node.(tunnelNode)
		if !ok {
			return nil, 0, false
		}
		return tn, n.TransportID, true
	}
	for _, s := range m.Syncers() {
		if s.id == n.ID {
			continue
		}
		if st := m.routeOf(s.id); st.carried {
			continue // itself carried by another node: one hop only
		}
		if hv, ok := m.Health(s.id); ok && !hv.CheckedAt.IsZero() && !hv.OK {
			continue
		}
		tn, ok := s.node.(tunnelNode)
		if !ok {
			continue
		}
		return tn, s.id, true
	}
	return nil, 0, false
}

// routeOf is one node's route state.
func (m *Manager) routeOf(id int64) routeState {
	m.routeMu.Lock()
	defer m.routeMu.Unlock()
	return m.routes[id]
}

// RouteOf is how the panel last reached one node, for the Nodes page.
func (m *Manager) RouteOf(id int64) RouteView {
	st := m.routeOf(id)
	if st.viaID == 0 && st.directCode == "" {
		return RouteView{}
	}
	return RouteView{ViaID: st.viaID, DirectCode: st.directCode, DirectError: st.directText, ViaError: st.viaErr}
}

func (m *Manager) noteDirect(id int64, now time.Time, err error) {
	m.routeMu.Lock()
	defer m.routeMu.Unlock()
	st := m.routes[id]
	if err == nil {
		// The direct path answers: the transport, if it carried, carried nothing after.
		m.routes[id] = routeState{directOK: true}
		return
	}
	st.directOK, st.directDown, st.directCode, st.directText = false, now, nodeapi.Classify(err), err.Error()
	m.routes[id] = st
}

func (m *Manager) noteVia(id, via int64, err error) {
	m.routeMu.Lock()
	defer m.routeMu.Unlock()
	st := m.routes[id]
	st.viaID, st.viaErr, st.carried = via, "", err == nil
	if err != nil {
		st.viaErr = err.Error()
	}
	m.routes[id] = st
}

// forgetRoute drops what a removed node's route says.
func (m *Manager) forgetRoute(id int64) {
	m.routeMu.Lock()
	defer m.routeMu.Unlock()
	delete(m.routes, id)
}

// tunnelTo is what a node may carry a tunnel to for the panel: the API of every other node, so
// a panel that cannot reach one of them reaches it through this one. The panel's own node has
// no address (its API is a socket) and needs nothing carried; a node addressed by a name is
// left out, its address is no ip:port the transport could match.
func tunnelTo(nodes []db.Node, self int64) []string {
	var out []string
	for _, n := range nodes {
		if n.ID == self || n.Address == "" || !ipAddress(n.Address) {
			continue
		}
		out = append(out, n.Address)
	}
	return out
}

// ipAddress says whether addr is host:port with an IP host.
func ipAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	_, err = netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil
}
