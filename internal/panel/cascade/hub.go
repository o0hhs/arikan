// Package cascade copies the connection between the two ends of a cascade over the panel's
// tunnel. An exit node keeps a stream to the panel (a knock its certificate signs, then a
// multiplexer); an entry node's bridge — the connection the panel opened on the node's own
// API — carries one stream per client connection, and asks for the exit. The hub pairs each
// with a stream to that exit and copies the bytes.
//
// Nothing of a connection is kept: no target, no content. What the hub remembers is which
// exits have a stream and which entries have a bridge, for the Nodes page, and the byte
// counts it logs when a connection ends.
package cascade

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/metacubex/yamux"

	"mikan/internal/duplex"
	"mikan/internal/nodeapi"
)

const (
	// handshake bounds the opening of one stream and the answer about it: a client that
	// sends nothing, or an exit that does not take the stream, gives the place back. The
	// bytes after it carry no deadline of the hub's.
	handshake = 15 * time.Second
	// retryMin and retryMax pace a bridge that does not come up: the panel restarts in
	// seconds, a node that is off for longer is not worth a try every second.
	retryMin = 500 * time.Millisecond
	retryMax = 30 * time.Second
	// streams is how many streams one bridge may carry at once.
	streams = 4096
	// nonceTTL is how long an exit's knock is remembered, so a replay is no second stream.
	nonceTTL = 2 * nodeapi.CascadeHelloWindow
	// maxNonces bounds what the replay memory takes from strangers.
	maxNonces = 20000
)

// Deps are what the hub needs from the panel.
type Deps struct {
	Log *slog.Logger
	Now func() time.Time
	// NodeByPin finds the node whose certificate has this pin (nodes.cert_sha256).
	NodeByPin func(ctx context.Context, pin string) (int64, bool)
	// IsExit says whether the panel uses the node as an exit.
	IsExit func(ctx context.Context, id int64) (bool, error)
	// Dial opens the bridge to an entry node: its API connection, upgraded (nodeapi.Client.Bridge).
	Dial func(ctx context.Context, id int64) (net.Conn, error)
}

// Hub is the panel's end of the cascade.
type Hub struct {
	d   Deps
	cfg *yamux.Config

	mu      sync.Mutex
	exits   map[int64]*exitSession
	entries map[int64]*entryBridge
	nonces  map[string]time.Time
}

type exitSession struct {
	sess  *yamux.Session
	conn  net.Conn
	since time.Time
}

// entryBridge is the panel's side of one entry node's loop.
type entryBridge struct {
	cancel context.CancelFunc

	mu      sync.Mutex // the routes and the current session, for the streams and the page
	allowed map[int64]bool
	sess    *yamux.Session
	since   time.Time
}

func New(d Deps) *Hub {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 15 * time.Second
	cfg.MaxIncomingStreams = streams
	return &Hub{d: d, cfg: cfg, exits: map[int64]*exitSession{}, entries: map[int64]*entryBridge{}, nonces: map[string]time.Time{}}
}

// Endpoint serves an exit node's knock. It answers true when the request was one (whether
// the panel took it or not); anything else is left to the router, which serves the path as
// any unknown one — a stranger learns nothing about the panel from it.
func (h *Hub) Endpoint(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.Path != nodeapi.CascadePath {
		return false
	}
	now := h.d.Now()
	var hello nodeapi.CascadeHello
	if err := json.NewDecoder(io.LimitReader(r.Body, nodeapi.CascadeMaxFrame)).Decode(&hello); err != nil {
		return false
	}
	pin, err := nodeapi.VerifyCascadeHello(hello, now)
	if err != nil {
		return false
	}
	id, ok := h.d.NodeByPin(r.Context(), pin)
	if !ok || !h.fresh(hello.Nonce, now) {
		return false
	}
	if exit, err := h.d.IsExit(r.Context(), id); err != nil || !exit {
		h.d.Log.Warn("cascade: a node that is no exit knocked", "node", id)
		return false
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return false
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return true
	}
	// The stream lives as long as the cascade does: no deadline of the panel's server.
	_ = conn.SetDeadline(time.Time{})
	if _, err := conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + nodeapi.CascadeUpgrade + "\r\n\r\n")); err != nil {
		_ = conn.Close()
		return true
	}
	var c net.Conn = conn
	if buf.Reader.Buffered() > 0 {
		c = &hijackedConn{Conn: conn, r: buf.Reader}
	}
	h.serveExit(id, c)
	return true
}

func (h *Hub) serveExit(id int64, conn net.Conn) {
	// The panel opens the streams of an exit (one per client connection): it is the client
	// of this multiplexer, whatever direction the connection was dialled in.
	sess, err := yamux.Client(conn, h.cfg, nil)
	if err != nil {
		_ = conn.Close()
		h.d.Log.Warn("cascade: an exit's stream did not open", "node", id, "err", err)
		return
	}
	es := &exitSession{sess: sess, conn: conn, since: h.d.Now()}
	h.mu.Lock()
	old := h.exits[id]
	h.exits[id] = es
	h.mu.Unlock()
	if old != nil {
		// One stream per exit: a new connection of the same node replaces the old, which is
		// dead or duplicated.
		_ = old.sess.Close()
		_ = old.conn.Close()
	}
	h.d.Log.Info("cascade: an exit is in the tunnel", "node", id)
	for {
		st, err := sess.AcceptStream()
		if err != nil {
			break
		}
		// The panel opens the streams of an exit; one the exit opens is not a thing.
		_ = st.Close()
	}
	h.mu.Lock()
	if h.exits[id] == es {
		delete(h.exits, id)
	}
	h.mu.Unlock()
	_ = sess.Close()
	_ = conn.Close()
	h.d.Log.Info("cascade: an exit left the tunnel", "node", id)
}

// SetRoutes tells the hub which exits an entry node's listeners may use (the exit of each
// route and its reserves): a node without routes has no bridge, and one whose routes
// changed gets the new set without dropping the connections in flight.
func (h *Hub) SetRoutes(entry int64, allowed []int64) {
	set := make(map[int64]bool, len(allowed))
	for _, id := range allowed {
		set[id] = true
	}
	h.mu.Lock()
	b := h.entries[entry]
	if len(set) == 0 {
		delete(h.entries, entry)
		h.mu.Unlock()
		if b != nil {
			b.stop()
		}
		return
	}
	if b != nil {
		b.setAllowed(set)
		h.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	b = &entryBridge{allowed: set, cancel: cancel}
	h.entries[entry] = b
	h.mu.Unlock()
	go h.keepBridge(ctx, entry, b)
}

// keepBridge holds the panel's bridge to an entry node: a connection of its own, retried
// until the routes go away.
func (h *Hub) keepBridge(ctx context.Context, entry int64, b *entryBridge) {
	wait := retryMin
	for ctx.Err() == nil {
		conn, err := h.d.Dial(ctx, entry)
		if err != nil {
			h.d.Log.Info("cascade: no bridge to the entry", "node", entry, "err", err)
			if !sleep(ctx, wait) {
				return
			}
			wait = min(wait*2, retryMax)
			continue
		}
		wait = retryMin
		sess, err := yamux.Server(conn, h.cfg, nil)
		if err != nil {
			_ = conn.Close()
			h.d.Log.Warn("cascade: the entry's bridge did not open", "node", entry, "err", err)
			continue
		}
		b.mu.Lock()
		b.sess, b.since = sess, h.d.Now()
		b.mu.Unlock()
		h.d.Log.Info("cascade: the entry's bridge is up", "node", entry)
		for {
			st, err := sess.AcceptStream()
			if err != nil {
				break
			}
			go h.pair(st, entry, b)
		}
		b.mu.Lock()
		if b.sess == sess {
			b.sess = nil
		}
		b.mu.Unlock()
		_ = sess.Close()
		_ = conn.Close()
		h.d.Log.Info("cascade: the entry's bridge is down", "node", entry)
		if ctx.Err() != nil {
			return
		}
		if !sleep(ctx, time.Second) {
			return
		}
	}
}

// pair serves one stream of an entry's bridge: the ask, the exit's stream, and the bytes.
func (h *Hub) pair(st *yamux.Stream, entry int64, b *entryBridge) {
	defer func() { _ = st.Close() }()
	_ = st.SetDeadline(h.d.Now().Add(handshake))
	kind, exit, err := nodeapi.ReadCascadeAsk(st)
	if err != nil {
		return
	}
	if kind != nodeapi.CascadeStreamData {
		_ = nodeapi.WriteCascadeAnswer(st, "not a data stream")
		return
	}
	if !b.allows(exit) {
		_ = nodeapi.WriteCascadeAnswer(st, "the exit is not one of this node's")
		return
	}
	es := h.exitSession(exit)
	if es == nil {
		_ = nodeapi.WriteCascadeAnswer(st, "the exit is not in the tunnel")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), handshake)
	xs, err := es.sess.OpenStream(ctx)
	cancel()
	if err != nil {
		_ = nodeapi.WriteCascadeAnswer(st, "the exit is not in the tunnel")
		return
	}
	defer func() { _ = xs.Close() }()
	if err := nodeapi.WriteCascadeAnswer(st, ""); err != nil {
		return
	}
	_ = st.SetDeadline(time.Time{})
	up, down := duplex.Copy(st, xs)
	// Who asked, where it went and how much passed: never the target, never a byte of it.
	h.d.Log.Debug("cascade: a connection went through", "entry", entry, "exit", exit, "up", up, "down", down)
}

func (h *Hub) exitSession(id int64) *exitSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	es := h.exits[id]
	if es == nil || es.sess.IsClosed() {
		return nil
	}
	return es
}

// ExitOnline says whether the panel has the node's stream now, and since when.
func (h *Hub) ExitOnline(id int64) (time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	es := h.exits[id]
	if es == nil || es.sess.IsClosed() {
		return time.Time{}, false
	}
	return es.since, true
}

// BridgeOnline says whether the panel's bridge to an entry node is up, and since when.
func (h *Hub) BridgeOnline(id int64) (time.Time, bool) {
	h.mu.Lock()
	b := h.entries[id]
	h.mu.Unlock()
	if b == nil {
		return time.Time{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sess == nil {
		return time.Time{}, false
	}
	return b.since, true
}

// stop ends the loop and its connection: the node has no routes, and the panel holds no
// bridge to it.
func (b *entryBridge) stop() {
	b.cancel()
	b.mu.Lock()
	s := b.sess
	b.mu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}

// An entry's routes are read by every stream, from the last state the panel built.
func (b *entryBridge) allows(id int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.allowed[id]
}

func (b *entryBridge) setAllowed(set map[int64]bool) {
	b.mu.Lock()
	b.allowed = set
	b.mu.Unlock()
}

// fresh says whether a knock's nonce is new, and remembers it while a replay could count.
func (h *Hub) fresh(nonce string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, seen := h.nonces[nonce]; seen {
		return false
	}
	if len(h.nonces) >= maxNonces {
		for k, until := range h.nonces {
			if now.After(until) {
				delete(h.nonces, k)
			}
		}
		if len(h.nonces) >= maxNonces {
			return false
		}
	}
	h.nonces[nonce] = now.Add(nonceTTL)
	return true
}

// Close ends every bridge and stream: the panel is going away.
func (h *Hub) Close() {
	h.mu.Lock()
	exits, entries := h.exits, h.entries
	h.exits, h.entries = map[int64]*exitSession{}, map[int64]*entryBridge{}
	h.mu.Unlock()
	for _, b := range entries {
		b.stop()
	}
	for _, es := range exits {
		_ = es.sess.Close()
		_ = es.conn.Close()
	}
}

// hijackedConn reads what the 101 left in the server's buffer before the connection itself.
type hijackedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *hijackedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
