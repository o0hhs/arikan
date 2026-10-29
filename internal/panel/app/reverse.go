package app

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/yamux"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/store"
)

// The panel's side of the nodes' reverse tunnels. A node whose panel cannot reach it opens the
// connection itself — that way round the link works where the panel's own does not (a panel in
// one country, a node in another) — and serves its API over it. The node is authenticated by
// the certificate the panel already pins for it; nothing else, and nothing in a URL.
//
// One tunnel per node: a node that reconnects (a new connection after a network change, a
// second process on its server) takes over, and the older connection is closed with a word in
// the log. The node may open no stream itself (MaxIncomingStreams 0): the panel asks, the node
// answers.
const (
	// tunnelMaxStreams is how many streams one tunnel may carry at once: the panel's calls are
	// few and short, and a tunnel that cannot take one falls back to the direct path.
	tunnelMaxStreams = 32
	// tunnelOpen bounds opening a stream: a session that takes longer is no use to a call.
	tunnelOpen = 5 * time.Second
)

// tunnel is one connected node's session.
type tunnel struct {
	node   int64
	conn   net.Conn
	sess   *yamux.Session
	remote string // where the node connected from, for the log

	mu    sync.Mutex
	since time.Time
	live  bool
	last  time.Time // the last stream opened
	open  int       // streams open now
	err   string    // why the last session ended
	ended time.Time
}

// tunnels is the panel's registry of the nodes' reverse tunnels, one session per node.
type tunnels struct {
	st  *store.Store
	log *slog.Logger
	now func() time.Time

	mu   sync.Mutex
	byID map[int64]*tunnel
}

func newTunnels(st *store.Store, log *slog.Logger, now func() time.Time) *tunnels {
	return &tunnels{st: st, log: log, now: now, byID: map[int64]*tunnel{}}
}

// View is how a node's reverse tunnel is now; ok is false while the node never had one since
// the panel started.
func (t *tunnels) View(id int64) (nodesync.TunnelView, bool) {
	t.mu.Lock()
	c := t.byID[id]
	t.mu.Unlock()
	if c == nil {
		return nodesync.TunnelView{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return nodesync.TunnelView{Connected: c.live, Since: c.since, Last: c.last, Ended: c.ended, Streams: c.open, Remote: c.remote, Error: c.err}, true
}

// Open opens one stream of the node's tunnel: the panel's way into a node it cannot reach
// itself. ok is false when there is no tunnel now, or it cannot take another stream — the
// caller then takes the direct path.
func (t *tunnels) Open(ctx context.Context, id int64) (net.Conn, bool) {
	t.mu.Lock()
	c := t.byID[id]
	t.mu.Unlock()
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	live, sess := c.live, c.sess
	c.mu.Unlock()
	if !live || sess == nil {
		return nil, false
	}
	c.mu.Lock()
	if c.open >= tunnelMaxStreams {
		c.mu.Unlock()
		return nil, false
	}
	c.open++
	c.mu.Unlock()
	octx, cancel := context.WithTimeout(ctx, tunnelOpen)
	defer cancel()
	st, err := sess.OpenStream(octx)
	if err != nil {
		c.mu.Lock()
		c.open--
		c.mu.Unlock()
		return nil, false
	}
	if _, err := st.Write([]byte{nodeapi.StreamControl}); err != nil {
		c.mu.Lock()
		c.open--
		c.mu.Unlock()
		_ = st.Close()
		return nil, false
	}
	c.mu.Lock()
	c.last = t.now()
	c.mu.Unlock()
	return &tunnelStream{Stream: st, c: c}, true
}

// tunnelStream is one stream of a tunnel: closing it frees its place.
type tunnelStream struct {
	*yamux.Stream
	c *tunnel
}

func (s *tunnelStream) Close() error {
	s.c.mu.Lock()
	s.c.open--
	s.c.mu.Unlock()
	return s.Stream.Close()
}

// serve answers the node's request to open a tunnel and says whether it took the path. A
// request that is not one is answered like any unknown path: the route reveals nothing.
func (t *tunnels) serve(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != nodeapi.TunnelPath {
		return false
	}
	// Anything that is not a node asking for its tunnel is served like any unknown path, so the
	// route tells a scanner nothing (the node's certificate is mostly the listener's business:
	// only a client presenting it gets this far).
	if r.Method != http.MethodGet || !strings.EqualFold(r.Header.Get("Upgrade"), nodeapi.TunnelUpgrade) {
		return false
	}
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		t.log.Warn("node tunnel: a connection without a certificate", "from", remoteHost(r.RemoteAddr))
		return false
	}
	pin := nodetls.PinOfDER(r.TLS.PeerCertificates[0].Raw)
	id, err := t.nodeOfPin(r.Context(), pin)
	if err != nil {
		t.log.Error("node tunnel: the panel's nodes are unreadable", "err", err)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return true
	}
	if id == 0 {
		// A certificate the panel never issued, or one of a node that was removed: nothing of it
		// is kept, the pin included.
		t.log.Warn("node tunnel: the certificate belongs to no node", "from", remoteHost(r.RemoteAddr))
		http.Error(w, "unknown", http.StatusForbidden)
		return true
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "unsupported", http.StatusInternalServerError)
		return true
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return true
	}
	_ = conn.SetDeadline(time.Time{})
	if _, err := io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: "+nodeapi.TunnelUpgrade+"\r\n\r\n"); err != nil {
		conn.Close()
		return true
	}
	raw := net.Conn(conn)
	if buf.Reader.Buffered() > 0 {
		raw = &bufferedConn{Conn: conn, r: buf.Reader}
	}
	sess, err := yamux.Client(raw, tunnelMux(), nil)
	if err != nil {
		t.log.Warn("node tunnel: the session did not start", "node", id, "err", err)
		conn.Close()
		return true
	}
	c := &tunnel{node: id, conn: conn, sess: sess, remote: remoteHost(r.RemoteAddr), since: t.now(),
		live: true}
	t.install(c)
	t.log.Info("node tunnel: the node is connected", "node", id, "from", c.remote)
	<-sess.CloseChan()
	conn.Close()
	t.drop(c, "the connection closed")
	return true
}

// install puts a new tunnel in place and closes the node's previous one, if any: one tunnel
// per node.
func (t *tunnels) install(c *tunnel) {
	t.mu.Lock()
	old := t.byID[c.node]
	t.byID[c.node] = c
	t.mu.Unlock()
	if old != nil {
		old.mu.Lock()
		live, sess := old.live, old.sess
		old.mu.Unlock()
		if live && sess != nil {
			t.log.Info("node tunnel: a newer connection of the node took over", "node", c.node, "from", c.remote)
			_ = sess.Close()
			t.drop(old, "a newer connection took over")
		}
	}
}

// drop marks a session over, unless a newer one is in its place already.
func (t *tunnels) drop(c *tunnel, reason string) {
	c.mu.Lock()
	if c.live {
		c.live = false
		c.err = reason
		c.ended = t.now()
	}
	c.mu.Unlock()
	t.mu.Lock()
	current := t.byID[c.node] == c
	t.mu.Unlock()
	if !current {
		return
	}
	t.log.Info("node tunnel: the tunnel is over", "node", c.node, "reason", reason)
}

// nodeOfPin is the node whose certificate pin is pin; 0 when the panel has no such node.
func (t *tunnels) nodeOfPin(ctx context.Context, pin string) (int64, error) {
	nodes, err := t.st.Q.ListNodes(ctx)
	if err != nil {
		return 0, err
	}
	for _, n := range nodes {
		if n.CertSha256 != "" && nodetls.SamePin(n.CertSha256, pin) {
			return n.ID, nil
		}
	}
	return 0, nil
}

// tunnelMux is the panel's yamux configuration: the node may open no stream at all, and a
// session that cannot write for a while is closed rather than trusted.
func tunnelMux() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 20 * time.Second
	// The panel asks, the node answers: a stream the node opens is reset at once.
	cfg.MaxIncomingStreams = 0
	cfg.AcceptBacklog = 1
	cfg.ConnectionWriteTimeout = 15 * time.Second
	return cfg
}

// serveTunnelProto serves HTTP/1.1 on a connection that negotiated the tunnel's ALPN. net/http
// serves nothing itself on such a connection: it hands any protocol but HTTP's own to a
// TLSNextProto hook, and this is that hook.
func serveTunnelProto(_ *http.Server, conn *tls.Conn, h http.Handler) {
	_ = http.Serve(oneConn(conn), h)
}

// oneConn is a listener of one connection: the tunnel's HTTP/1.1 serves exactly the connection
// net/http handed over, and returns once it is over.
type oneConnListener struct {
	conn net.Conn
	used bool
	done chan struct{}
}

func oneConn(c net.Conn) net.Listener {
	return &oneConnListener{conn: &notifyClose{Conn: c, done: make(chan struct{})}, done: make(chan struct{})}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	if l.used {
		<-l.conn.(*notifyClose).done
		return nil, net.ErrClosed
	}
	l.used = true
	return l.conn, nil
}

func (l *oneConnListener) Close() error   { return nil }
func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// notifyClose says when a connection is closed, so the listener knows its one connection is
// over.
type notifyClose struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func (c *notifyClose) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}

// bufferedConn reads what the HTTP answer left buffered before the connection itself.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// remoteHost is the source of a connection without its port.
func remoteHost(remote string) string {
	if h, _, err := net.SplitHostPort(remote); err == nil {
		return h
	}
	return remote
}
