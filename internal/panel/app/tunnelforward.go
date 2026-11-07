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
	st     *store.Store
	tun    *tunnels
	log    *slog.Logger
	host   string
	mu     sync.Mutex
	by     map[uint16]*forwardPort
	reload *time.Timer
}

// forwardPort is one port the panel listens on for a node's clients.
type forwardPort struct {
	node int64
	port uint16
	ln   net.Listener
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
	want := map[uint16]int64{}
	for _, n := range nodes {
		if n.Enabled == 0 || n.Address == "" {
			continue
		}
		for _, p := range domain.ParseTunnelForward(n.TunnelForward) {
			port := uint16(p)
			if other, ok := want[port]; ok {
				f.log.Warn("tunnel forward: two nodes name one port, the first keeps it", "port", port, "node", n.ID, "first", other)
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
}

// open starts the listener for a port, serving the node's clients.
func (f *tunnelForward) open(port uint16, node int64) *forwardPort {
	ln, err := net.Listen("tcp", net.JoinHostPort(f.host, strconv.Itoa(int(port))))
	if err != nil {
		f.log.Warn("tunnel forward: the port does not open", "port", port, "node", node, "err", err)
		return nil
	}
	fp := &forwardPort{node: node, port: port, ln: ln}
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
