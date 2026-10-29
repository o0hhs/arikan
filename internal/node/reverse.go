package node

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/yamux"

	"mikan/internal/nodeapi"
	"mikan/internal/pace"
)

// The node's outgoing tunnel to its panel. The link that does not work is the panel's own
// (a panel in one country, a node in another: the panel's calls never arrive), while the
// other way round always does — so the node opens the connection and serves its API over it.
// The panel is a yamux client and asks over the streams; the node accepts and answers each
// with the API, over TLS with the pins both ends already have, so a node whose API port is
// closed is reached just the same.
const (
	tunnelRetryFirst = 2 * time.Second
	tunnelRetryMax   = 5 * time.Minute
	// tunnelStreamSays is how long a stream may take to say what it carries: the panel writes
	// the type byte at once, and a stream that says nothing is not the panel's.
	tunnelStreamSays = 15 * time.Second
	// tunnelDial bounds a connection to the panel: a panel that does not answer is told apart
	// from one that refuses the node.
	tunnelDial = 15 * time.Second
)

// ReverseTunnel is how the node reaches its panel when the panel cannot reach it: the address
// from the join key, the certificates both sides pin, and the API each stream serves.
type ReverseTunnel struct {
	// URL is the panel's base address from the join key ("https://host:port"); "": no tunnel.
	URL string
	// Client is the TLS the node connects out with: its own certificate as a client
	// certificate (the panel accepts it by the pin it holds) and the tunnel's ALPN.
	Client *tls.Config
	// Server is the node's API TLS, for the TLS inside each stream: the node's certificate, the
	// panel's client certificate pinned by it — the same as on the API port.
	Server *tls.Config
	// Handler is the API each stream serves (node.Handler).
	Handler http.Handler
	Log     *slog.Logger
	// Now is the clock, for tests; nil: time.Now.
	Now func() time.Time
}

func (o ReverseTunnel) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// RunReverseTunnel keeps a connection to the panel open until ctx ends: it reconnects with a
// growing pause and a jitter (a panel that comes back is not met by every node at once), and
// the streams of each connection are served while it lasts.
func RunReverseTunnel(ctx context.Context, o ReverseTunnel) {
	if o.URL == "" {
		o.Log.Info("no reverse tunnel: the join key has no panel address")
		return
	}
	api := o.Handler
	// A tunnel carries the API, not a proxy: nothing asked of the node here is forwarded to a
	// third address (a nested tunnel would only loop).
	o.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			writeJSON(w, http.StatusForbidden, nodeapi.Error{Code: "tunnel_forbidden", Message: "the reverse tunnel serves the API alone"})
			return
		}
		api.ServeHTTP(w, r)
	})
	backoff := pace.Backoff{Base: tunnelRetryFirst, Max: tunnelRetryMax}
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		start := o.now()
		err := serveReverseTunnel(ctx, o)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			o.Log.Warn("reverse tunnel: the panel does not take it", "url", o.URL, "err", err)
		}
		// A tunnel that carried for a while starts the pause over: a reconnect after an hour is
		// not a failing node.
		if o.now().Sub(start) > time.Minute {
			attempt = 0
		}
		pause := backoff.Next(attempt)
		if !pace.Sleep(ctx, pause+jitter(pause/4)) {
			return
		}
		attempt++
	}
}

// serveReverseTunnel opens one connection to the panel and serves its streams until the
// connection ends.
func serveReverseTunnel(ctx context.Context, o ReverseTunnel) error {
	u, err := url.Parse(o.URL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("the panel address %q is no address", o.URL)
	}
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: tunnelDial, KeepAlive: 30 * time.Second}, Config: o.Client}
	conn, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return err
	}
	defer conn.Close()
	req := &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: u.Host, Path: nodeapi.TunnelPath},
		Host: u.Host, Header: http.Header{"Connection": {"Upgrade"}, "Upgrade": {nodeapi.TunnelUpgrade}}}
	_ = conn.SetDeadline(time.Now().Add(tunnelDial))
	if err := req.Write(conn); err != nil {
		return err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Time{})
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var stream net.Conn = conn
	if br.Buffered() > 0 {
		stream = &bufferedConn{Conn: conn, r: br}
	}
	sess, err := yamux.Server(stream, tunnelMux(), nil)
	if err != nil {
		return err
	}
	defer sess.Close()
	o.Log.Info("reverse tunnel: the panel took the connection", "panel", u.Host)
	for {
		st, err := sess.AcceptStream()
		if err != nil {
			if errors.Is(err, yamux.ErrSessionShutdown) || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go serveTunnelStream(ctx, st, o)
	}
}

// serveTunnelStream serves the API over one stream of the tunnel, in TLS: the panel checks the
// node's certificate in it and the node checks the panel's, as on a direct connection.
func serveTunnelStream(ctx context.Context, st *yamux.Stream, o ReverseTunnel) {
	defer st.Close()
	_ = st.SetReadDeadline(time.Now().Add(tunnelStreamSays))
	var kind [1]byte
	if _, err := io.ReadFull(st, kind[:]); err != nil {
		return
	}
	_ = st.SetReadDeadline(time.Time{})
	if kind[0] != nodeapi.StreamControl {
		// A stream of another kind (user traffic, a later round) is not served here.
		return
	}
	hsctx, cancel := context.WithTimeout(ctx, tunnelDial)
	tc := tls.Server(st, o.Server)
	err := tc.HandshakeContext(hsctx)
	cancel()
	if err != nil {
		return
	}
	srv := &http.Server{Handler: o.Handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	// One connection per stream: Serve returns when it is over.
	_ = srv.Serve(oneConn(tc))
}

// tunnelMux is the node's yamux configuration: its streams are the panel's calls, and a
// connection that cannot be written for a while is closed rather than trusted.
func tunnelMux() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 20 * time.Second
	cfg.AcceptBacklog = 64
	cfg.ConnectionWriteTimeout = 15 * time.Second
	return cfg
}

// jitter is how far a reconnect may move: a share of the pause, so a fleet of nodes does not
// knock on a returning panel in one instant.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(d)))
}

// oneConn is a listener of one connection: the API's server serves exactly the stream it is
// given, and Serve returns once that stream is over.
type oneConnListener struct {
	conn *closedConn
	used bool
}

func oneConn(c net.Conn) net.Listener {
	return &oneConnListener{conn: &closedConn{Conn: c, done: make(chan struct{})}}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	if l.used {
		// The connection's work is done (its close fired): the server has nothing more to
		// accept, and Serve returns instead of waiting for ever.
		<-l.conn.done
		return nil, net.ErrClosed
	}
	l.used = true
	return l.conn, nil
}

func (l *oneConnListener) Close() error   { return nil }
func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// closedConn says when it is closed, so the listener knows the one connection is over.
type closedConn struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func (c *closedConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}

// bufferedConn reads what the HTTP answer left buffered before the connection itself.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
