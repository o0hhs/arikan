package node

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"mikan/internal/nodeapi"
)

// The panel's way through a node it can reach to one it cannot: the panel asks the node over
// its API (HTTP CONNECT, authenticated by the panel's pinned certificate or the socket's
// permissions) to open a stream to another node's API, and the node dials it with its own
// outgoing connection. Two things keep a node from being an open proxy: the targets are what
// the panel put in its state (DesiredState.TunnelTo, the panel's other nodes) plus the bot's
// fixed TunnelHosts, and loopback, private addresses and the node's own API port are refused
// whatever the list says.
const (
	// tunnelAtOnce is how many tunnels one node carries at once: the panel keeps a few
	// connections to each node it reaches, and a node that carries for several of them needs a
	// few each; a burst beyond this is refused rather than left to eat the node.
	tunnelAtOnce = 64
	// tunnelIdle is how long a tunnel may carry nothing before both ends are closed: a stream
	// nobody uses any more would hold its socket and a place in tunnelAtOnce for ever.
	tunnelIdle  = 10 * time.Minute
	tunnelDial  = 10 * time.Second
	tunnelCheck = 30 * time.Second
)

// tunnelRelay is the node's CONNECT endpoint's state.
type tunnelRelay struct {
	log *slog.Logger
	now func() time.Time
	// idle is how long a tunnel may carry nothing, check how often that is looked at; a test
	// shortens both.
	idle  time.Duration
	check time.Duration

	mu   sync.Mutex
	to   map[netip.AddrPort]bool // what the panel's state allows
	open int                     // tunnels carrying bytes now

	// apiPort is the port of the node's own API: a tunnel to it would loop. 0: unknown
	// (the panel's own node is reached over its socket, which has no port).
	apiPort int
}

// tunnelRelay is the engine's relay, made on first use (both the API handler and Apply
// reach it).
func (e *Engine) tunnelRelay() *tunnelRelay {
	e.relayOnce.Do(func() { e.relay = newTunnelRelay(e.log, time.Now) })
	return e.relay
}

func newTunnelRelay(log *slog.Logger, now func() time.Time) *tunnelRelay {
	if log == nil {
		log = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &tunnelRelay{log: log, now: now, idle: tunnelIdle, check: tunnelCheck}
}

// set takes the addresses of a new state: the panel's other nodes, as the panel knows them.
// A target that is not an "ip:port" is dropped: a name would be resolved by this node's
// resolver, not the panel's, and the panel never sends one.
func (t *tunnelRelay) set(addrs []string) {
	to := map[netip.AddrPort]bool{}
	for _, a := range addrs {
		ap, err := netip.ParseAddrPort(a)
		if err != nil {
			t.log.Warn("node tunnel: not an ip:port, dropped", "target", a)
			continue
		}
		to[netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())] = true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(to) == 0 {
		to = nil
	}
	t.to = to
}

// setAPIPort is the port of the node's own API listener, refused as a target.
func (t *tunnelRelay) setAPIPort(port int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.apiPort = port
}

// allow is why target may not be dialled ("" when it may): forbidden (not in the state),
// private (loopback or a private address), self (the node's own API). The state comes first, so
// a stranger learns nothing about the ranges from the answer.
func (t *tunnelRelay) allow(target string) (target2 string, why string) {
	// The bot's tunnel is fixed in the code, not sent by the panel, and may name a host
	// instead of an address.
	for _, h := range nodeapi.TunnelHosts {
		if h == target {
			return target, ""
		}
	}
	ap, err := netip.ParseAddrPort(target)
	if err != nil {
		return "", "forbidden"
	}
	addr := ap.Addr().Unmap()
	t.mu.Lock()
	listed, apiPort := t.to[netip.AddrPortFrom(addr, ap.Port())], t.apiPort
	t.mu.Unlock()
	if !listed {
		return "", "forbidden"
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsUnspecified() || !addr.IsGlobalUnicast() {
		return "", "private"
	}
	if apiPort != 0 && int(ap.Port()) == apiPort {
		return "", "self"
	}
	return ap.String(), ""
}

// claim takes a place in tunnelAtOnce; a refusal says the node is carrying enough already.
func (t *tunnelRelay) claim() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.open >= tunnelAtOnce {
		return false
	}
	t.open++
	return true
}

func (t *tunnelRelay) release() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.open--
}

// serve answers one HTTP CONNECT. The node dials the target itself and copies both ways;
// what is logged is who asked, where it went and how much passed, never the content.
func (t *tunnelRelay) serve(w http.ResponseWriter, r *http.Request) {
	log := t.log
	target, why := t.allow(r.Host)
	if why != "" {
		log.Warn("node tunnel refused", "target", r.Host, "reason", why, "from", r.RemoteAddr)
		writeJSON(w, http.StatusForbidden, nodeapi.Error{Code: "tunnel_" + why, Message: r.Host + " is not a tunnel target (" + why + ")"})
		return
	}
	if !t.claim() {
		log.Warn("node tunnel refused", "target", target, "reason", "busy", "from", r.RemoteAddr)
		writeJSON(w, http.StatusServiceUnavailable, nodeapi.Error{Code: "tunnel_busy", Message: "the node carries as many tunnels as it takes"})
		return
	}
	defer t.release()
	ctx, cancel := context.WithTimeout(r.Context(), tunnelDial)
	up, err := (&net.Dialer{Timeout: tunnelDial}).DialContext(ctx, "tcp", target)
	cancel()
	if err != nil {
		log.Warn("node tunnel: the target does not answer", "target", target, "err", err)
		writeJSON(w, http.StatusBadGateway, nodeapi.Error{Code: "tunnel_unreachable", Message: err.Error()})
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		writeJSON(w, http.StatusInternalServerError, nodeapi.Error{Code: "tunnel_unsupported"})
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	// The stream belongs to the caller from here: no deadline of the Node API's.
	_ = conn.SetDeadline(time.Time{})
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		conn.Close()
		up.Close()
		return
	}
	start := t.now()
	var toTarget, fromTarget atomic.Int64
	last := atomic.Int64{}
	last.Store(start.UnixNano())
	client := &watchedConn{Conn: conn, n: &toTarget, last: &last, now: t.now}
	remote := &watchedConn{Conn: up, n: &fromTarget, last: &last, now: t.now}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, buf.Reader); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, remote); done <- struct{}{} }()
	// A tunnel that carries nothing for idle is closed: nobody is using it any more.
	stop := make(chan struct{})
	go func() {
		tick := time.NewTicker(t.check)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				if t.now().UnixNano()-last.Load() >= int64(t.idle) {
					t.log.Info("node tunnel idle: closing", "target", target, "idle", t.idle)
					_ = conn.Close()
					_ = up.Close()
					return
				}
			}
		}
	}()
	<-done
	close(stop)
	conn.Close()
	up.Close()
	<-done
	log.Info("node tunnel", "target", target, "from", r.RemoteAddr, "to_target", toTarget.Load(), "from_target", fromTarget.Load(), "took", t.now().Sub(start).String())
}

// watchedConn counts what goes through it and says when it last moved.
type watchedConn struct {
	net.Conn
	n    *atomic.Int64
	last *atomic.Int64
	now  func() time.Time
}

func (c *watchedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.n.Add(int64(n))
		c.last.Store(c.now().UnixNano())
	}
	return n, err
}

func (c *watchedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.n.Add(int64(n))
		c.last.Store(c.now().UnixNano())
	}
	return n, err
}
