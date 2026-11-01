package node

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"runtime/debug"
	"testing"
	"time"

	"mikan/internal/nodeapi"
)

func TestConnLinesStayOutOfLogs(t *testing.T) {
	for _, msg := range []string{
		"[TCP] dial DIRECT (match Match/) 203.0.113.9:57314 --> [2001:db8::a]:443 error: connect: network is unreachable",
		"[UDP] dial DIRECT (match Match/) 203.0.113.9:4000 --> example.com:443 error: dns resolve failed",
	} {
		if !connLine(msg) {
			t.Errorf("per-connection line must be dropped: %q", msg)
		}
	}
	for _, msg := range []string{
		"Listener vless-vision listen err: listen tcp :443: bind: address already in use",
		"mikan-sync 00ff",
	} {
		if connLine(msg) {
			t.Errorf("line must be kept: %q", msg)
		}
	}
}

// A port another program holds reaches the panel as a code, whatever the OS calls it: the
// error here is this OS's own, from a second listen on a taken port, over TCP and UDP.
func TestBusyPortIsReportedByCode(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	_, errTCP := net.Listen("tcp", tcp.Addr().String())
	_, errUDP := net.ListenPacket("udp", udp.LocalAddr().String())
	for _, e := range []error{errTCP, errUDP} {
		if e == nil {
			t.Fatal("a second listen on a taken port succeeded")
		}
		ls := listenFailed("vless-xhttp", e.Error())
		if ls.OK || ls.Code != nodeapi.ListenerAddrInUse || !ls.Busy() || ls.Error != e.Error() {
			t.Fatalf("status of %q: %+v", e, ls)
		}
	}
	for _, msg := range []string{"listen tcp: address 99999: invalid port", "listen tcp :443: bind: permission denied", "tls: no certificate"} {
		if ls := listenFailed("tuic", msg); ls.Code != "" || ls.Busy() {
			t.Errorf("%q is not a busy port: %+v", msg, ls)
		}
	}
	// The code is new: a status without one keeps the JSON older panels read.
	raw, _ := json.Marshal(nodeapi.ListenerStatus{Name: "tuic", OK: true})
	if string(raw) != `{"name":"tuic","ok":true}` {
		t.Fatalf("JSON: %s", raw)
	}
}

// Health says where the node's own page answers. Without that, the panel treats a
// REALITY dest of 127.0.0.1:<that port> as a private address and leaves the inbound out.
func TestHealthReportsTheNodesPage(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := &Engine{
		dataDir: dir,
		log:     log,
		Reg:     NewRegistry("e", 0, time.Minute, time.Now, citrus{}),
		sys:     newSysSampler(),
		Site:    newSite(dir, "127.0.0.1:0", log),
	}
	if e.Health().Site != nil {
		t.Fatal("a page that was not started is reported")
	}
	cert, key := writeSiteCert(t, dir, false)
	e.Site.SetCert(cert, key)
	e.Site.Start(false)
	st := e.Health().Site
	if st == nil || !st.OK || st.Port == 0 {
		t.Fatalf("health has no page: %+v", st)
	}
}

// A listener Apply marked up and the core no longer has is reported down, so the panel
// sends the state again. One that was already failed keeps the reason it failed with.
func TestHealthReportsAListenerTheCoreDropped(t *testing.T) {
	gone := noteGoneListeners([]nodeapi.ListenerStatus{{Name: "not-a-real-listener", OK: true}})
	if gone[0].OK || gone[0].Code != nodeapi.ListenerGone || gone[0].Error == "" {
		t.Fatalf("a missing listener: %+v", gone[0])
	}
	kept := noteGoneListeners([]nodeapi.ListenerStatus{{Name: "not-a-real-listener", Error: "bad config"}})
	if kept[0].Code == nodeapi.ListenerGone || kept[0].Error != "bad config" {
		t.Fatalf("a failed listener was rewritten: %+v", kept[0])
	}
}

// Close cancels the background work and stops the TURN child Start left running.
func TestEngineCloseStopsTurn(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, stop := context.WithCancel(context.Background())
	e := &Engine{log: log, root: ctx, stop: stop, turn: newTurnSupervisor(log)}
	t.Cleanup(e.Close)
	bin := fakeTurnServer(t, "trap 'exit 0' TERM\nsleep 60")
	e.turn.Apply(&nodeapi.Turn{Listen: "0.0.0.0:56001", Target: "127.0.0.1:8443", Binary: bin})
	waitTurn(t, "the server", func() bool { return e.turn.Status().Running })
	e.Close()
	if e.root.Err() == nil {
		t.Fatal("background work was not cancelled")
	}
	if e.turn.Status().Running {
		t.Fatal("the TURN server is still running")
	}
	e.Close() // a second close is what a deferred Close meets after a shutdown that already called it
}

func TestSafelyKeepsTheProcess(t *testing.T) {
	e := &Engine{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	e.safely("core logs", func() { panic("boom") })
}

func TestCoreName(t *testing.T) {
	for _, c := range []struct {
		m    debug.Module
		want string
	}{
		{debug.Module{Version: "v1.19.32"}, "mihomo v1.19.32"},
		{debug.Module{Version: "v1.19.32", Replace: &debug.Module{Path: "github.com/getmikan/Citrus", Version: "v1.19.33-0.20261018104129-2cae038ea2a8"}}, "citrus 2cae038"},
		{debug.Module{Version: "v1.19.32", Replace: &debug.Module{Path: "github.com/getmikan/Citrus", Version: "v1.0.0"}}, "citrus v1.0.0"},
		{debug.Module{Version: "v1.19.32", Replace: &debug.Module{Path: "github.com/getmikan/Citrus", Version: "v1.0.0-beta.1"}}, "citrus v1.0.0-beta.1"},
	} {
		if got := coreName(&c.m); got != c.want {
			t.Errorf("coreName(%+v) = %q, want %q", c.m.Replace, got, c.want)
		}
	}
}
