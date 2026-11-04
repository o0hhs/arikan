package node

import (
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"mikan/internal/nodeapi"
)

// InboundAPI is the node's own TCP listener of the API: the panel's direct way in. A node may
// keep the port closed — the panel said its reverse tunnel is enough (DesiredState.InboundAPI),
// or MIKAN_NODE_INBOUND_API=off / MIKAN_NODE_API_LISTEN=off was set on its server — and then
// the tunnel is the only way in. The port is opened and closed with the states; a port another
// program holds is tried again at the next one.
type InboundAPI struct {
	addr    string // host:port from the join key; "": there is no direct way in
	cfg     *tls.Config
	handler http.Handler
	wrap    func(net.Listener) net.Listener // the panel lock in front of the listener; nil: none
	log     *slog.Logger

	mu  sync.Mutex
	on  bool   // a listener is up now
	err string // why it is not, when it should be
	ln  net.Listener
	srv *http.Server
}

// NewInboundAPI is the listener the node's own port serves the API on. wrap is the panel
// lock (Engine.APIListener), if any.
func NewInboundAPI(addr string, cfg *tls.Config, h http.Handler, wrap func(net.Listener) net.Listener, log *slog.Logger) *InboundAPI {
	return &InboundAPI{addr: addr, cfg: cfg, handler: h, wrap: wrap, log: log}
}

// Set turns the listener on or off: on a failure to listen it says so and tries again next time.
func (a *InboundAPI) Set(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !on {
		a.close()
		return
	}
	if a.on || a.addr == "" {
		return
	}
	ln, err := net.Listen("tcp", a.addr)
	if err != nil {
		if a.err != err.Error() {
			a.log.Warn("the API port is not open: the panel has the reverse tunnel alone", "addr", a.addr, "err", err)
		}
		a.err = err.Error()
		return
	}
	srv := &http.Server{Handler: a.handler, ReadHeaderTimeout: 10 * time.Second}
	if a.wrap != nil {
		ln = a.wrap(ln)
	}
	go func() { _ = srv.Serve(tls.NewListener(ln, a.cfg)) }()
	a.ln, a.srv, a.on, a.err = ln, srv, true, ""
	a.log.Info("the API port is open", "addr", a.addr)
}

// close shuts the listener down; call it with mu held.
func (a *InboundAPI) close() {
	if !a.on {
		return
	}
	_ = a.srv.Close()
	_ = a.ln.Close()
	a.ln, a.srv, a.on = nil, nil, false
	a.log.Info("the API port is closed: the panel has the reverse tunnel alone")
}

// Close shuts the listener down for good (the node is stopping).
func (a *InboundAPI) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.close()
}

// Status is how the listener is, for Health: nil while it listens; otherwise whether it is
// off (the panel said so, or its server did) and why a wanted one could not listen.
func (a *InboundAPI) Status() *nodeapi.InboundAPIStatus {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return &nodeapi.InboundAPIStatus{On: a.on, Error: a.err}
}
