package node

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/transport/socks5"
	"github.com/metacubex/yamux"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
)

// panel is the other end of an entry's bridge: it answers the asks as the panel's hub and
// the exit do, and echoes what the client sends.
type panel struct {
	t      *testing.T
	sess   *yamux.Session
	asks   chan int64
	refuse map[int64]bool
	target chan nodeapi.CascadeTarget
}

func newPanel(t *testing.T, nodeConn net.Conn, refuse ...int64) *panel {
	t.Helper()
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	sess, err := yamux.Server(nodeConn, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	p := &panel{t: t, sess: sess, asks: make(chan int64, 16), refuse: map[int64]bool{}, target: make(chan nodeapi.CascadeTarget, 16)}
	for _, id := range refuse {
		p.refuse[id] = true
	}
	go func() {
		for {
			st, err := sess.AcceptStream()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = st.Close() }()
				_ = st.SetReadDeadline(time.Now().Add(10 * time.Second))
				kind, exit, err := nodeapi.ReadCascadeAsk(st)
				if err != nil {
					return
				}
				if kind != nodeapi.CascadeStreamData {
					return
				}
				p.asks <- exit
				if p.refuse[exit] {
					_ = nodeapi.WriteCascadeAnswer(st, "the exit is not in the tunnel")
					return
				}
				if err := nodeapi.WriteCascadeAnswer(st, ""); err != nil {
					return
				}
				target, err := nodeapi.ReadCascadeTarget(st)
				if err != nil {
					return
				}
				p.target <- target
				_ = st.SetReadDeadline(time.Time{})
				_, _ = io.Copy(st, st)
			}()
		}
	}()
	return p
}

// bridge starts an entry of the node with the given routes, its bridge to the panel being
// the other end of a pipe.
func bridge(t *testing.T, password string, routes ...nodeapi.CascadeRoute) (*cascade, *entryBridge, *panel) {
	t.Helper()
	return bridgeRefusing(t, password, nil, routes...)
}

// bridgeRefusing is a bridge whose panel refuses the exits in refuse.
func bridgeRefusing(t *testing.T, password string, refuse []int64, routes ...nodeapi.CascadeRoute) (*cascade, *entryBridge, *panel) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := newCascade(log, nodetls.Key{}, &Tunnel{})
	panelConn, nodeConn := net.Pipe()
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	sess, err := yamux.Client(nodeConn, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.bridge = &cascadeBridge{conn: nodeConn, sess: sess, since: time.Now()}
	t.Cleanup(func() { _ = sess.Close() })
	p := newPanel(t, panelConn, refuse...)
	b := c.newEntry(&nodeapi.CascadeEntry{Listen: "127.0.0.1:0", Password: password, Routes: routes})
	if b.ln == nil {
		t.Fatalf("the bridge does not listen: %s", b.err)
	}
	t.Cleanup(func() { _ = b.ln.Close() })
	return c, b, p
}

// dial speaks to the bridge as the core's SOCKS5 outbound does.
func dial(t *testing.T, b *entryBridge, user, password, target string) (net.Conn, error) {
	t.Helper()
	conn, err := net.Dial("tcp", b.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	addr := socks5.ParseAddr(target)
	if addr == nil {
		t.Fatalf("bad target %q", target)
	}
	if _, err := socks5.ClientHandshake(conn, addr, socks5.CmdConnect, &socks5.User{Username: user, Password: password}); err != nil {
		return conn, err
	}
	return conn, nil
}

// refused says the bridge took the SOCKS5 exchange and carried nothing: the core's own
// client does not look at the reply code, so a refusal shows as the connection ending.
func refused(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Write([]byte("x"))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("a refused connection carried bytes")
	}
}

func echo(t *testing.T, conn net.Conn) {
	t.Helper()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("the answer: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("the answer is %q", buf)
	}
}

// One client connection of the entry becomes one stream, and the exit's bytes come back.
func TestTheEntryBridgeCarriesAClientConnection(t *testing.T) {
	_, b, p := bridge(t, "pw", nodeapi.CascadeRoute{Name: "NODE-7", Exits: []int64{7}})
	conn, err := dial(t, b, "NODE-7", "pw", "example.com:443")
	if err != nil {
		t.Fatalf("the bridge's handshake: %v", err)
	}
	echo(t, conn)
	select {
	case exit := <-p.asks:
		if exit != 7 {
			t.Fatalf("the panel was asked for exit %d", exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the panel was asked for nothing")
	}
	select {
	case target := <-p.target:
		if target.Host != "example.com" || target.Port != 443 {
			t.Fatalf("the exit got %+v", target)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the exit got no target")
	}
}

// The exit the inbound named is asked for first; a tunnel that is down costs one refused
// stream, and the reserve takes the connection.
func TestTheEntryBridgeFallsBackToAReserve(t *testing.T) {
	_, b, p := bridgeRefusing(t, "pw", []int64{7}, nodeapi.CascadeRoute{Name: "NODE-7", Exits: []int64{7, 9}})
	conn, err := dial(t, b, "NODE-7", "pw", "example.com:443")
	if err != nil {
		t.Fatalf("the bridge's handshake: %v", err)
	}
	echo(t, conn)
	got := []int64{}
	for len(got) < 2 {
		select {
		case exit := <-p.asks:
			got = append(got, exit)
		case <-time.After(5 * time.Second):
			t.Fatalf("the panel was asked for %v, not the reserve", got)
		}
	}
	if got[0] != 7 || got[1] != 9 {
		t.Fatalf("the exits asked for: %v", got)
	}
}

// The bridge is no open SOCKS proxy: a program that finds the port without the state's
// password gets nothing.
func TestTheEntryBridgeRefusesAForeignPassword(t *testing.T) {
	_, b, p := bridge(t, "pw", nodeapi.CascadeRoute{Name: "NODE-7", Exits: []int64{7}})
	if _, err := dial(t, b, "NODE-7", "not-the-password", "example.com:443"); err == nil {
		t.Fatal("a foreign password passed")
	}
	select {
	case exit := <-p.asks:
		t.Fatalf("a refused connection reached the panel: exit %d", exit)
	case <-time.After(200 * time.Millisecond):
	}
}

// A user the state does not name is no exit: the connection is refused, not guessed at.
func TestTheEntryBridgeRefusesAnUnknownExit(t *testing.T) {
	_, b, p := bridge(t, "pw", nodeapi.CascadeRoute{Name: "NODE-7", Exits: []int64{7}})
	conn, err := dial(t, b, "NODE-99", "pw", "example.com:443")
	if err != nil {
		t.Fatalf("the handshake: %v", err)
	}
	refused(t, conn)
	select {
	case exit := <-p.asks:
		t.Fatalf("an unknown exit reached the panel: exit %d", exit)
	case <-time.After(200 * time.Millisecond):
	}
}

// Without the panel's bridge the connections fail: nothing leaves directly.
func TestTheEntryBridgeRefusesWithoutATunnel(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := newCascade(log, nodetls.Key{}, &Tunnel{})
	b := c.newEntry(&nodeapi.CascadeEntry{Listen: "127.0.0.1:0", Password: "pw",
		Routes: []nodeapi.CascadeRoute{{Name: "NODE-7", Exits: []int64{7}}}})
	if b.ln == nil {
		t.Fatalf("the bridge does not listen: %s", b.err)
	}
	t.Cleanup(func() { _ = b.ln.Close() })
	conn, err := dial(t, b, "NODE-7", "pw", "example.com:443")
	if err != nil {
		t.Fatalf("the handshake: %v", err)
	}
	refused(t, conn)
}

// A bridge anywhere but loopback would be a SOCKS proxy for the server, and the world: the
// node does not make one, whatever the state says.
func TestTheEntryBridgeListensOnLoopbackOnly(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := newCascade(log, nodetls.Key{}, &Tunnel{})
	for _, addr := range []string{"0.0.0.0:47105", "192.0.2.1:47105", "[::]:47105"} {
		b := c.newEntry(&nodeapi.CascadeEntry{Listen: addr, Password: "pw"})
		if b.ln != nil {
			_ = b.ln.Close()
			t.Fatalf("the bridge listened on %s", addr)
		}
		if b.err == "" {
			t.Fatalf("%s: no reason given", addr)
		}
	}
}

// The exit's side hands the connection to the core as one of the cascade's own: no user,
// no limits of a subscriber (the entry counted its own), the exit's rules still applied.
func TestTheCascadeListenerReachesTheCore(t *testing.T) {
	inner := &fakeTunnel{seen: make(chan *C.Metadata, 1)}
	tun := &Tunnel{inner: inner}
	a, b := net.Pipe()
	defer a.Close()
	t.Cleanup(func() { _ = b.Close() })
	tun.HandleTCPConn(a, &C.Metadata{NetWork: C.TCP, Type: C.SOCKS5, InName: nodeapi.CascadeListener, Host: "example.com", DstPort: 443})
	select {
	case m := <-inner.seen:
		if m.InName != nodeapi.CascadeListener || m.Host != "example.com" {
			t.Fatalf("the core got %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("the core got no connection")
	}
}

// fakeTunnel is the core's tunnel as the node's wrapper calls it.
type fakeTunnel struct {
	seen chan *C.Metadata
}

func (f *fakeTunnel) HandleTCPConn(_ net.Conn, m *C.Metadata) { f.seen <- m }

func (*fakeTunnel) HandleUDPPacket(C.UDPPacket, *C.Metadata) {}

func (*fakeTunnel) NatTable() C.NatTable { return nil }

// A route through the exit's WARP is marked for the exit: the bridge writes the mark into
// the target of every stream of that route, and the exit's rules see the WARP listener
// instead of the cascade's own name.
func TestTheEntryBridgeMarksAWarpRoute(t *testing.T) {
	_, b, p := bridge(t, "pw", nodeapi.CascadeRoute{Name: nodeapi.ExitWarpName(7), Exits: []int64{7}, Warp: true})
	conn, err := dial(t, b, nodeapi.ExitWarpName(7), "pw", "example.com:443")
	if err != nil {
		t.Fatalf("the bridge's handshake: %v", err)
	}
	echo(t, conn)
	select {
	case target := <-p.target:
		if !target.Warp || target.Host != "example.com" || target.Port != 443 {
			t.Fatalf("the exit got %+v", target)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the exit got no target")
	}

	_, plain, pp := bridge(t, "pw", nodeapi.CascadeRoute{Name: nodeapi.ExitName(7), Exits: []int64{7}})
	conn, err = dial(t, plain, nodeapi.ExitName(7), "pw", "example.com:443")
	if err != nil {
		t.Fatalf("the plain bridge's handshake: %v", err)
	}
	echo(t, conn)
	select {
	case target := <-pp.target:
		if target.Warp {
			t.Fatalf("a plain route is marked for WARP: %+v", target)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the exit got no target for the plain route")
	}
}

// The exit's rules tell the marked streams by their listening name.
func TestCascadeInName(t *testing.T) {
	if got := cascadeInName(nodeapi.CascadeTarget{Host: "example.com", Port: 443}); got != nodeapi.CascadeListener {
		t.Fatalf("a plain stream arrives as %q", got)
	}
	if got := cascadeInName(nodeapi.CascadeTarget{Host: "example.com", Port: 443, Warp: true}); got != nodeapi.CascadeWarpListener {
		t.Fatalf("a marked stream arrives as %q", got)
	}
}
