package node

import (
	"context"
	"net"
	"time"

	"mikan/internal/nodeapi"
)

// egressEvery is how often the node asks whether its own way to the internet works: the
// panel reaches it over the same uplink, so a check every half minute is enough to tell a
// dead uplink from a dead node, and cheap enough to leave running.
const egressEvery = 30 * time.Second

// egressProbe is one check: a direct TCP connect to a public address, then the host's
// resolver for a public name. No core, no proxy: a failure here is the server's own network.
func egressProbe(ctx context.Context) *nodeapi.EgressStatus {
	st := &nodeapi.EgressStatus{CheckedAt: time.Now().UTC()}
	start := time.Now()
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", "1.1.1.1:443")
	if err != nil {
		st.Error = "tcp: " + err.Error()
		return st
	}
	_ = conn.Close()
	st.LatencyMS = time.Since(start).Milliseconds()
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := net.DefaultResolver.LookupHost(rctx, "www.google.com"); err != nil {
		st.Error = "dns: " + err.Error()
		return st
	}
	st.OK = true
	return st
}

// runEgress checks the way out until ctx is done.
func (e *Engine) runEgress(ctx context.Context) {
	t := time.NewTicker(egressEvery)
	defer t.Stop()
	e.egress.Store(egressProbe(ctx))
	for {
		select {
		case <-t.C:
			e.egress.Store(egressProbe(ctx))
		case <-ctx.Done():
			return
		}
	}
}
