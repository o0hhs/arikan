package app

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"mikan/internal/duplex"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// The panel as the way in: a client that cannot reach a node's protocol directly (the filters
// between the countries cut every port of the node) connects to the panel on the protocol's
// port, and the panel carries the connection to the node over the reverse tunnel the node
// opened. The node names its ports (nodes.tunnel_forward); the panel listens on each of them,
// and every client connection becomes a data stream on that node's tunnel, carried to the
// node's own loopback where its core counts and limits the user.
//
// One listener per port per node. Two nodes naming the same port is a mistake the panel
// reports: the first keeps it.
type tunnelForward struct {
	st   *store.Store
	tun  *tunnels
	log  *slog.Logger
	host string
	mu   sync.Mutex
	by   map[uint16]*forwardPort

	warnMu sync.Mutex
	warned map[uint16]string // the last warning each port had, so a roll does not repeat it
}

// forwardPort is one port the panel listens on for a node's clients.
type forwardPort struct {
	node int64
	// port is the node's protocol port named on the data stream; bindPort is where the panel
	// listens. They are the same in life; a test listens on a free port of its own.
	port     uint16
	bindPort uint16
	ln       net.Listener
}

func newTunnelForward(st *store.Store, tun *tunnels, host string, log *slog.Logger) *tunnelForward {
	return &tunnelForward{st: st, tun: tun, host: host, log: log, by: map[uint16]*forwardPort{}}
}

// Roll brings the listeners in line with the nodes' tunnel_forward: a port a node stops naming
// closes, a new one opens. Called when a node's setting changes and on start.
func (f *tunnelForward) Roll(ctx context.Context) {
	nodes, err := f.st.Q.ListNodes(ctx)
	if err != nil {
		f.log.Warn("tunnel forward: the nodes are unreadable", "err", err)
		return
	}
	own, err := f.ownPorts(ctx, nodes)
	if err != nil {
		// Without the map of the panel's own server, no port is opened: one taken from the
		// panel's own node would cut every client of it.
		f.log.Warn("tunnel forward: the panel's own ports are unreadable", "err", err)
		return
	}
	want := map[uint16]int64{}
	refused := map[uint16]bool{}
	for _, n := range nodes {
		if n.Enabled == 0 || n.Address == "" {
			continue
		}
		for _, p := range domain.ParseTunnelForward(n.TunnelForward) {
			port := uint16(p)
			if holder, busy := own.holder(port); busy {
				refused[port] = true
				f.once(port, "own:"+holder, "tunnel forward: the port is the panel's own server's, it is not taken", "port", port, "node", n.ID, "holder", holder)
				continue
			}
			if other, ok := want[port]; ok {
				refused[port] = true
				f.once(port, "two", "tunnel forward: two nodes name one port, the first keeps it", "port", port, "node", n.ID, "first", other)
				continue
			}
			want[port] = n.ID
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for port, fp := range f.by {
		if id, ok := want[port]; !ok || id != fp.node {
			_ = fp.ln.Close()
			delete(f.by, port)
		}
	}
	for port, id := range want {
		if _, ok := f.by[port]; ok {
			continue
		}
		if fp := f.open(port, id); fp != nil {
			f.by[port] = fp
		}
	}
	// A port that is fine again is reported anew when it next goes wrong.
	f.warnMu.Lock()
	for port := range f.warned {
		if _, open := f.by[port]; !refused[port] && (open || want[port] == 0) {
			delete(f.warned, port)
		}
	}
	f.warnMu.Unlock()
}

// ownPorts is what the panel's own server needs over TCP: the panel's and the subscription
// port, the inbounds and the relay of the panel's own node (a disabled inbound included: it
// comes back on), and 443 under its SNI router. The forwarder never listens there. It used to
// take such a port in the moment the node's core restarted and let it go: the core then could
// not listen, every client of the panel's own node was cut, and the panel carried their
// connections to another node over its tunnel, spending its CPU on traffic it could only
// fail.
func (f *tunnelForward) ownPorts(ctx context.Context, nodes []db.Node) (ownServer, error) {
	var own ownServer
	for _, n := range nodes {
		if n.Address != "" {
			continue
		}
		m, err := domain.NodePorts(ctx, f.st.Q, n)
		if err != nil {
			return ownServer{}, err
		}
		own.maps = append(own.maps, m)
		own.sni = own.sni || n.SniRouter != 0
	}
	return own, nil
}

// ownServer is what listens on the panel's own server: its node's port maps.
type ownServer struct {
	maps []domain.PortMap
	sni  bool
}

// holder names what on the panel's own server needs port; ok is false when nothing does.
func (o ownServer) holder(port uint16) (string, bool) {
	if o.sni && port == 443 {
		return "sni_router", true
	}
	spec := strconv.Itoa(int(port))
	for _, m := range o.maps {
		if h, busy := m.Busy(spec, "tcp", domain.PortHolder{Kind: "tunnel_forward"}); busy {
			if h.Name != "" {
				return h.Kind + " " + h.Name, true
			}
			return h.Kind, true
		}
		// Free also keeps off SSH and an inbound that is off now but may come back on.
		if !m.Free(int(port), "tcp") {
			return "reserved", true
		}
	}
	return "", false
}

// once logs a warning about port the first time why is the reason, not every roll: the
// rolls run every few seconds.
func (f *tunnelForward) once(port uint16, why, msg string, args ...any) {
	f.warnMu.Lock()
	defer f.warnMu.Unlock()
	if f.warned == nil {
		f.warned = map[uint16]string{}
	}
	if f.warned[port] == why {
		return
	}
	f.warned[port] = why
	f.log.Warn(msg, args...)
}

// open starts the listener for a port, serving the node's clients.
func (f *tunnelForward) open(port uint16, node int64) *forwardPort {
	ln, err := net.Listen("tcp", net.JoinHostPort(f.host, strconv.Itoa(int(port))))
	if err != nil {
		f.once(port, "listen", "tunnel forward: the port does not open", "port", port, "node", node, "err", err)
		return nil
	}
	fp := &forwardPort{node: node, port: port, bindPort: port, ln: ln}
	go f.serve(fp)
	f.log.Info("tunnel forward: the port is open", "port", port, "node", node)
	return fp
}

func (f *tunnelForward) serve(fp *forwardPort) {
	for {
		conn, err := fp.ln.Accept()
		if err != nil {
			return
		}
		go f.one(fp, conn)
	}
}

// one carries one client's connection to the node over its tunnel: the first bytes name the
// node's port, and the bytes after them are the client's, untouched.
func (f *tunnelForward) one(fp *forwardPort, conn net.Conn) {
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	st, ok := f.tun.OpenData(ctx, fp.node, fp.port)
	cancel()
	if !ok {
		f.log.Debug("tunnel forward: the node's tunnel is not up", "port", fp.port, "node", fp.node)
		return
	}
	defer st.Close()
	duplex.Copy(conn, st)
}

// Close ends every listener: the panel is going away.
func (f *tunnelForward) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for port, fp := range f.by {
		_ = fp.ln.Close()
		delete(f.by, port)
	}
}
