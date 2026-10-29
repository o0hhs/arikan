package node

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/yamux"

	"mikan/internal/duplex"
	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
)

// The cascade over the panel's tunnel (nodeapi.Cascade): an entry node's users leave
// through an exit node, and both connections are opened by the side the filters between
// the countries let through. The exit keeps a connection to the panel; the panel opens the
// entry's bridge on its API; every client connection of the entry becomes one stream from
// the bridge to the exit, and the exit hands what it reads to its own core — its rules,
// its ExitDNS and its way out are the same as for an inbound of its own.
//
// No address of an exit is ever dialled here: the exits of the core are SOCKS5 proxies on
// the node's loopback (nodeapi.CascadeListen), and a name without a tunnel behind it fails
// instead of leaving directly.

const (
	// cascadeHandshake bounds the SOCKS5 exchange of one connection and the opening of its
	// stream: a client that sends nothing, or a panel that answers nothing, gives the place
	// back. The bytes after the handshake have no deadline of ours.
	cascadeHandshake = 15 * time.Second
	// cascadeRetry is the longest pause between attempts at a tunnel that does not come up,
	// and cascadeRetryMin the first: a panel that restarts is back within seconds.
	cascadeRetryMin = time.Second
	cascadeRetryMax = 30 * time.Second
	// cascadeStreams is how many streams one bridge may carry at once (the clients of one
	// entry node); a burst beyond it waits rather than being refused by yamux.
	cascadeStreams = 4096
)

// cascade is the node's part in the panel's cascade: the exit's stream to the panel, and
// the entry's loopback SOCKS5 whose connections ride the bridge the panel opened to this
// node.
type cascade struct {
	log  *slog.Logger
	join nodetls.Key // this node's identity to the panel: its join key's certificate
	tun  *Tunnel     // the core, where an exit's stream is handed

	mu     sync.Mutex
	state  *nodeapi.Cascade
	run    *cascadeRun
	bridge *cascadeBridge
}

// cascadeRun is one applied state's roles: the entry's listener and the exit's loop. What
// a new state does not change it keeps: a new exit for an inbound is no reason to break
// the connections in flight, and a stream to the panel is no reason to reconnect.
type cascadeRun struct {
	entry   *entryBridge
	exit    *exitRun
	exitURL string
	stop    context.CancelFunc
}

func newCascade(log *slog.Logger, join nodetls.Key, tun *Tunnel) *cascade {
	return &cascade{log: log, join: join, tun: tun}
}

// safely runs a goroutine of the cascade: a panic in one connection ends that connection,
// not the node.
func (c *cascade) safely(job string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("cascade: a job panicked", "job", job, "panic", r)
		}
	}()
	fn()
}

// apply takes the state's cascade: nil stops the roles, a new one replaces them. The same
// state twice changes nothing (the listeners of the core already follow their own keys).
// What the new state keeps — the bridge at the same address, the stream to the same panel —
// stays open, so a new exit for an inbound costs the clients in flight nothing.
func (c *cascade) apply(st *nodeapi.Cascade) {
	c.mu.Lock()
	if sameCascade(c.state, st) {
		c.mu.Unlock()
		return
	}
	old := c.run
	c.state, c.run = st, nil
	noEntry := st == nil || st.Entry == nil
	c.mu.Unlock()
	if noEntry {
		c.dropBridge()
	}
	var keepEntry *entryBridge
	var keepExit *exitRun
	var keepStop context.CancelFunc
	if st != nil && old != nil {
		if st.Entry != nil && old.entry != nil && old.entry.ln != nil && old.entry.same(st.Entry) {
			keepEntry = old.entry
		}
		if st.Exit != nil && old.exit != nil && old.stop != nil && old.exitURL == st.Exit.URL {
			keepExit, keepStop = old.exit, old.stop
		}
	}
	if old != nil {
		if old.stop != nil && old.exit != keepExit {
			old.stop()
		}
		if old.entry != nil && old.entry != keepEntry && old.entry.ln != nil {
			_ = old.entry.ln.Close()
		}
	}
	if st == nil {
		return
	}
	run := &cascadeRun{entry: keepEntry, exit: keepExit, stop: keepStop}
	if st.Exit != nil {
		run.exitURL = st.Exit.URL
	}
	if st.Exit != nil && keepExit == nil {
		run.exit = &exitRun{}
		if c.join.CertPEM == "" || c.join.KeyPEM == "" {
			run.exit.down(errors.New("the node has no join key: it cannot prove itself to the panel"))
		} else {
			ctx, cancel := context.WithCancel(context.Background())
			run.stop = cancel
			go c.safely("cascade exit", func() { c.exitLoop(ctx, st.Exit.URL, run.exit) })
		}
	}
	if st.Entry != nil {
		if keepEntry != nil {
			keepEntry.setRoutes(st.Entry)
		} else {
			run.entry = c.newEntry(st.Entry)
		}
	}
	c.mu.Lock()
	c.run = run
	c.mu.Unlock()
}

// Status is the cascade as the panel's health check reports it; nil when the node has no
// part in it.
func (c *cascade) Status() *nodeapi.CascadeStatus {
	c.mu.Lock()
	run, bridge := c.run, c.bridge
	c.mu.Unlock()
	if run == nil {
		return nil
	}
	st := &nodeapi.CascadeStatus{}
	if e := run.entry; e != nil {
		es := &nodeapi.CascadeEntryStatus{Listening: e.ln != nil, Error: e.err}
		if bridge != nil {
			es.Bridge, es.Since = true, bridge.since
		}
		st.Entry = es
	}
	if x := run.exit; x != nil {
		res := x.result()
		st.Exit = &res
	}
	return st
}

// EntryWanted says whether the node's state has an entry: the panel's bridge is taken only
// then, and a panel that asks anyway is answered no.
func (c *cascade) EntryWanted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state != nil && c.state.Entry != nil
}

// setBridge takes the panel's connection: the upgrade the panel opened on the Node API, now
// carrying the streams of this node's clients. A new one replaces the old — the panel
// reconnects after its own restart, and the old connection is dead anyway.
func (c *cascade) setBridge(conn net.Conn) error {
	sess, err := yamux.Client(conn, cascadeMux(), nil)
	if err != nil {
		_ = conn.Close()
		return err
	}
	b := &cascadeBridge{conn: conn, sess: sess, since: time.Now()}
	c.mu.Lock()
	old := c.bridge
	c.bridge = b
	c.mu.Unlock()
	if old != nil {
		_ = old.sess.Close()
		_ = old.conn.Close()
	}
	go c.safely("cascade bridge", func() {
		<-sess.CloseChan()
		c.mu.Lock()
		if c.bridge == b {
			c.bridge = nil
		}
		c.mu.Unlock()
		_ = conn.Close()
		c.log.Info("cascade: the panel's bridge is gone; new connections wait for it")
	})
	c.log.Info("cascade: the panel's bridge is up")
	return nil
}

func (c *cascade) dropBridge() {
	c.mu.Lock()
	b := c.bridge
	c.bridge = nil
	c.mu.Unlock()
	if b != nil {
		_ = b.sess.Close()
		_ = b.conn.Close()
	}
}

// open asks the panel for a stream to exit.
func (c *cascade) open(ctx context.Context, exit int64) (*yamux.Stream, error) {
	c.mu.Lock()
	b := c.bridge
	c.mu.Unlock()
	if b == nil {
		return nil, errors.New("the panel's bridge is not up")
	}
	st, err := b.sess.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	_ = st.SetDeadline(time.Now().Add(cascadeHandshake))
	if err := nodeapi.WriteCascadeAsk(st, nodeapi.CascadeStreamData, exit); err != nil {
		_ = st.Close()
		return nil, err
	}
	if err := nodeapi.ReadCascadeAnswer(st); err != nil {
		_ = st.Close()
		return nil, err
	}
	_ = st.SetDeadline(time.Time{})
	return st, nil
}

// cascadeBridge is the panel's connection to this entry node and its multiplexer.
type cascadeBridge struct {
	conn  net.Conn
	sess  *yamux.Session
	since time.Time
}

// entryBridge is the entry's loopback SOCKS5: the core points its exits here, and every
// connection it takes becomes a stream to the exit the SOCKS5 user names.
type entryBridge struct {
	c      *cascade
	ln     net.Listener
	listen string // the address it listens on: a state that names it again keeps the bridge
	err    string

	mu     sync.Mutex // the password and the routes; a new state replaces them under it
	pass   string
	routes map[string][]int64
}

func routesOf(st *nodeapi.CascadeEntry) map[string][]int64 {
	routes := make(map[string][]int64, len(st.Routes))
	for _, r := range st.Routes {
		if r.Name != "" && len(r.Exits) > 0 {
			routes[r.Name] = r.Exits
		}
	}
	return routes
}

// same says whether a new state asks for the same listener. The password and the routes
// may differ: they are the bridge's own check, and setRoutes replaces them — the
// connections in flight keep theirs, the new ones take the new.
func (b *entryBridge) same(st *nodeapi.CascadeEntry) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.listen == st.Listen
}

// setRoutes gives the bridge a new state's routes and password, under the connections'
// feet: what is open stays open.
func (b *entryBridge) setRoutes(st *nodeapi.CascadeEntry) {
	b.mu.Lock()
	b.pass, b.routes = st.Password, routesOf(st)
	b.mu.Unlock()
}

// password is what the bridge asks now.
func (b *entryBridge) password() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pass
}

// route is the exits of one name as the bridge knows them now.
func (b *entryBridge) route(name string) []int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.routes[name])
}

func (c *cascade) newEntry(st *nodeapi.CascadeEntry) *entryBridge {
	b := &entryBridge{c: c, listen: st.Listen, pass: st.Password, routes: routesOf(st)}
	host, _, err := net.SplitHostPort(st.Listen)
	if err != nil || !loopbackHost(host) {
		// A listener anywhere but loopback would be a SOCKS proxy for the whole server (and
		// the world): the panel never asks for one, and the node does not make one.
		b.err = "the bridge listens on loopback only: " + st.Listen
		return b
	}
	if b.pass == "" {
		b.err = "the bridge has no password"
		return b
	}
	ln, err := net.Listen("tcp", st.Listen)
	if err != nil {
		b.err = err.Error()
		c.log.Warn("cascade: the entry's bridge does not listen", "listen", st.Listen, "err", err)
		return b
	}
	b.ln = ln
	c.log.Info("cascade: the entry's bridge listens", "listen", st.Listen, "routes", len(b.routes))
	go c.safely("cascade entry", b.accept)
	return b
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && ip.IsLoopback()
}

func (b *entryBridge) accept() {
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return
		}
		go b.c.safely("cascade connection", func() { b.serve(conn) })
	}
}

// serve is one client connection: the SOCKS5 the core speaks, then the tunnel.
func (b *entryBridge) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(cascadeHandshake))
	target, name, rep, err := socksRequest(conn, b.password())
	if err != nil {
		if rep != socksReplied {
			_ = socksReply(conn, rep)
		}
		return
	}
	_ = conn.SetDeadline(time.Time{})
	ids := b.route(name)
	if len(ids) == 0 {
		// The core named an exit the panel did not put in the state: refuse, never guess.
		_ = socksReply(conn, socksRepHostUnreachable)
		return
	}
	// The exit the inbound named first, then the panel's reserves: a tunnel that is down
	// costs one refused stream, not the client's connection.
	var why error
	for _, id := range ids {
		ctx, cancel := context.WithTimeout(context.Background(), cascadeHandshake)
		stream, err := b.c.open(ctx, id)
		cancel()
		if err != nil {
			why = err
			continue
		}
		if err := nodeapi.WriteCascadeTarget(stream, target); err != nil {
			_ = stream.Close()
			why = err
			continue
		}
		if err := socksReply(conn, socksRepOK); err != nil {
			_ = stream.Close()
			return
		}
		duplex.Copy(conn, stream)
		return
	}
	if why != nil {
		b.c.log.Info("cascade: no exit took the connection", "exit", name, "err", why)
	}
	_ = socksReply(conn, socksRepHostUnreachable)
}

// SOCKS5: what the core's own outbound speaks, and nothing else. The user names the exit,
// the password is the state's: a program on the server that finds the port is not the core.
const (
	socksRepOK              = 0
	socksRepGeneral         = 1
	socksRepHostUnreachable = 4
	socksRepCmdNotSupported = 7
	socksRepAddrNotSupport  = 8
	// socksReplied is no reply code: socksRequest already wrote one (the auth stage).
	socksReplied = 0xff
)

// socksRequest reads the greeting, the user and password, and the request of one client
// connection. A reply other than 0 is the code the caller must send before closing;
// socksReplied says one already went out.
func socksRequest(conn net.Conn, pass string) (t nodeapi.CascadeTarget, name string, rep byte, err error) {
	var b [512]byte
	read := func(n int) error {
		_, e := io.ReadFull(conn, b[:n])
		return e
	}
	if err = read(2); err != nil {
		return t, "", socksReplied, err
	}
	if b[0] != 5 {
		return t, "", socksReplied, errors.New("not SOCKS5")
	}
	nmethods := int(b[1])
	if nmethods == 0 || nmethods > len(b) {
		return t, "", socksReplied, errors.New("bad SOCKS5 greeting")
	}
	if err = read(nmethods); err != nil {
		return t, "", socksReplied, err
	}
	auth := false
	for _, m := range b[:nmethods] {
		if m == 2 { // username and password: the only way this listener takes anyone
			auth = true
		}
	}
	if !auth {
		_, _ = conn.Write([]byte{5, 0xff})
		return t, "", socksReplied, errors.New("the bridge takes a user and a password only")
	}
	if _, err = conn.Write([]byte{5, 2}); err != nil {
		return t, "", socksReplied, err
	}
	if err = read(2); err != nil {
		return t, "", socksReplied, err
	}
	if b[0] != 1 {
		_, _ = conn.Write([]byte{1, 1})
		return t, "", socksReplied, errors.New("bad auth version")
	}
	ulen := int(b[1])
	if ulen == 0 || read(ulen) != nil {
		_, _ = conn.Write([]byte{1, 1})
		return t, "", socksReplied, errors.New("empty user")
	}
	name = string(b[:ulen])
	if err = read(1); err != nil {
		return t, "", socksReplied, err
	}
	plen := int(b[0])
	if plen == 0 || read(plen) != nil {
		_, _ = conn.Write([]byte{1, 1})
		return t, "", socksReplied, errors.New("empty password")
	}
	if pass == "" || subtle.ConstantTimeCompare(b[:plen], []byte(pass)) != 1 {
		_, _ = conn.Write([]byte{1, 1})
		return t, "", socksReplied, errors.New("wrong password")
	}
	if _, err = conn.Write([]byte{1, 0}); err != nil {
		return t, "", socksReplied, err
	}
	// VER CMD RSV ATYP, then the address and the port.
	if err = read(4); err != nil {
		return t, "", socksReplied, err
	}
	if b[0] != 5 {
		return t, "", socksRepGeneral, errors.New("bad request version")
	}
	cmd, atyp := b[1], b[3]
	var host string
	switch atyp {
	case 1:
		if err = read(4); err != nil {
			return t, "", socksReplied, err
		}
		host = net.IP(b[:4]).String()
	case 4:
		if err = read(16); err != nil {
			return t, "", socksReplied, err
		}
		host = net.IP(b[:16]).String()
	case 3:
		if err = read(1); err != nil {
			return t, "", socksReplied, err
		}
		l := int(b[0])
		if l == 0 || read(l) != nil {
			return t, "", socksRepAddrNotSupport, errors.New("empty name")
		}
		host = string(b[:l])
	default:
		return t, "", socksRepAddrNotSupport, fmt.Errorf("address type %d", atyp)
	}
	if err = read(2); err != nil {
		return t, "", socksReplied, err
	}
	port := int(binary.BigEndian.Uint16(b[:2]))
	if port == 0 {
		return t, "", socksRepGeneral, errors.New("port 0")
	}
	if cmd != 1 { // CONNECT only: the tunnel carries no datagrams (see the docs)
		return t, "", socksRepCmdNotSupported, fmt.Errorf("command %d", cmd)
	}
	return nodeapi.CascadeTarget{Host: host, Port: port}, name, socksRepOK, nil
}

// socksReply answers the core's request: 0, or why not (RFC 1928's codes).
func socksReply(w io.Writer, rep byte) error {
	_, err := w.Write([]byte{5, rep, 0, 1, 0, 0, 0, 0, 0, 0})
	return err
}

// exitRun is the exit's stream to the panel as the health check reports it.
type exitRun struct {
	mu        sync.Mutex
	connected bool
	since     time.Time
	err       string
	sess      *yamux.Session
}

func (x *exitRun) up(sess *yamux.Session) {
	x.mu.Lock()
	x.connected, x.since, x.err, x.sess = true, time.Now(), "", sess
	x.mu.Unlock()
}

func (x *exitRun) down(err error) {
	x.mu.Lock()
	x.connected, x.since, x.sess = false, time.Time{}, nil
	x.err = ""
	if err != nil {
		x.err = err.Error()
	}
	x.mu.Unlock()
}

// stop closes the session under a run that is going away: its accept loop ends, and the
// panel sees the exit leave.
func (x *exitRun) stop() {
	x.mu.Lock()
	s := x.sess
	x.sess = nil
	x.mu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}

func (x *exitRun) result() nodeapi.CascadeExitStatus {
	x.mu.Lock()
	defer x.mu.Unlock()
	return nodeapi.CascadeExitStatus{Connected: x.connected, Since: x.since, Error: x.err}
}

// exitLoop keeps the exit's stream to the panel while the state asks for one: a knock, the
// multiplexer, and the streams the panel opens on it. A lost connection is retried with a
// growing pause; the panel replaces whatever it had for this node, so a duplicate is never
// a second source of traffic.
func (c *cascade) exitLoop(ctx context.Context, rawURL string, x *exitRun) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		x.down(fmt.Errorf("the panel's cascade address is not https: %q", rawURL))
		return
	}
	wait := cascadeRetryMin
	for ctx.Err() == nil {
		conn, err := c.knock(ctx, u)
		if err != nil {
			x.down(err)
			c.log.Info("cascade: the panel does not take the exit's stream", "err", err)
			if !sleepCtx(ctx, wait) {
				return
			}
			wait = min(wait*2, cascadeRetryMax)
			continue
		}
		wait = cascadeRetryMin
		sess, err := yamux.Server(conn, cascadeMux(), nil)
		if err != nil {
			_ = conn.Close()
			x.down(err)
			continue
		}
		x.up(sess)
		c.log.Info("cascade: the exit's stream to the panel is up")
		for {
			st, err := sess.AcceptStream()
			if err != nil {
				break
			}
			go c.safely("cascade stream", func() { c.serveExit(st) })
		}
		_ = sess.Close()
		_ = conn.Close()
		x.down(errors.New("the panel closed the stream"))
		c.log.Info("cascade: the exit's stream to the panel is down")
		if ctx.Err() != nil {
			return
		}
		if !sleepCtx(ctx, time.Second) {
			return
		}
	}
}

// knock asks the panel for the exit's stream: the node's certificate signs it, and the
// panel answers 101 with the connection taken over.
func (c *cascade) knock(ctx context.Context, u *url.URL) (net.Conn, error) {
	hello, err := nodeapi.NewCascadeHello(c.join, time.Now())
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(hello)
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	td := tls.Dialer{NetDialer: d, Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}}
	conn, err := td.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(cascadeHandshake))
	req := &http.Request{
		Method: http.MethodPost,
		URL:    u,
		Host:   u.Host,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   io.NopCloser(bytes.NewReader(raw)),
	}
	req.ContentLength = int64(len(raw))
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("the panel did not take the tunnel: %s", resp.Status)
	}
	_ = conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		// The same wrapper the reverse tunnel uses: what the 101 left buffered before the
		// connection itself (reverse.go).
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// serveExit is one stream the panel opened: the target, then the client's bytes, which the
// core takes the connection over with. The exit's own rules, its ExitDNS and its ways out
// apply exactly as they do for a client of its own.
func (c *cascade) serveExit(st *yamux.Stream) {
	defer func() { _ = st.Close() }()
	_ = st.SetReadDeadline(time.Now().Add(cascadeHandshake))
	target, err := nodeapi.ReadCascadeTarget(st)
	if err != nil {
		return
	}
	_ = st.SetReadDeadline(time.Time{})
	m := &C.Metadata{NetWork: C.TCP, Type: C.SOCKS5, InName: nodeapi.CascadeListener, DstPort: uint16(target.Port)}
	if ip, err := netip.ParseAddr(target.Host); err == nil {
		m.DstIP = ip.Unmap()
	} else {
		m.Host = target.Host
	}
	if ap, err := netip.ParseAddrPort(st.RemoteAddr().String()); err == nil {
		m.SrcIP, m.SrcPort = ap.Addr().Unmap(), ap.Port()
	}
	// The core owns the stream from here: it counts it, applies the rules (private ranges
	// and the filters included) and closes it.
	c.tun.HandleTCPConn(st, m)
}

// cascadeMux is the multiplexer of both roles: keepalives so a silently dead connection is
// noticed, and a stream count that fits a busy entry node.
func cascadeMux() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 15 * time.Second
	cfg.MaxIncomingStreams = cascadeStreams
	return cfg
}

// sleepCtx pauses for d, false when the context ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// sameCascade says whether two states ask for the same thing.
func sameCascade(a, b *nodeapi.Cascade) bool {
	if a == nil || b == nil {
		return a == b
	}
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return bytes.Equal(ra, rb)
}
