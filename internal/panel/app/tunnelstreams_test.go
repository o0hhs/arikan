package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/metacubex/yamux"
)

// A node's clients never take the places of the panel's own calls: with more clients' data
// streams open than the API has places, the API still opens its stream — before, both shared
// one bound of 32, and a node with a few dozen connections lost its API over the tunnel.
func TestTunnelDataStreamsLeaveTheAPIRoom(t *testing.T) {
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	panel, err := yamux.Client(a, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	node, err := yamux.Server(b, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = panel.Close(); _ = node.Close() })
	go func() {
		for {
			st, err := node.AcceptStream()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(io.Discard, st) }()
		}
	}()

	tn := newTunnels(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	tn.byID[7] = &tunnel{node: 7, sess: panel, live: true}
	ctx := context.Background()

	var data []net.Conn
	for i := 0; i < tunnelMaxStreams+8; i++ {
		st, ok := tn.OpenData(ctx, 7, 443)
		if !ok {
			t.Fatalf("data stream %d did not open", i)
		}
		data = append(data, st)
	}
	ctl, ok := tn.Open(ctx, 7)
	if !ok {
		t.Fatal("the API's stream did not open while clients' streams were open")
	}
	_ = ctl.Close()

	view, _ := tn.View(7)
	if view.Streams != len(data) {
		t.Fatalf("streams open: %d, want %d", view.Streams, len(data))
	}
	for _, st := range data {
		_ = st.Close()
	}
	if view, _ = tn.View(7); view.Streams != 0 {
		t.Fatalf("streams open after close: %d", view.Streams)
	}
}
