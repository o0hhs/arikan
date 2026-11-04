package node

import (
	"context"
	"errors"
	"net"
	"time"

	"mikan/internal/nodeapi"
)

// egressEvery is how often the node asks whether its own way to the internet works: the
// panel reaches it over the same uplink, so a check every half minute is enough to tell a
// dead uplink from a dead node, and cheap enough to leave running.
const egressEvery = 30 * time.Second

// tcpDNS is the same server over TCP: UDP is the packet a filter or a corner of the
// network drops first, so one lost UDP answer must not call the uplink dead.
var tcpDNS = &net.Resolver{
	PreferGo: true,
	Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		d := net.Dialer{Timeout: 3 * time.Second}
		return d.DialContext(ctx, "tcp", "1.1.1.1:53")
	},
}

// lookupHost asks the host's resolver for a public name, and one retry over TCP to the
// same server when the first answer does not come; the server's own UDP DNS being dropped
// is not the same as the server having no internet.
func lookupHost(ctx context.Context) ([]string, error) {
	hosts, err := net.DefaultResolver.LookupHost(ctx, "www.google.com")
	if err == nil {
		return hosts, nil
	}
	rc, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	hosts, err2 := tcpDNS.LookupHost(rc, "www.google.com")
	if err2 == nil {
		return hosts, nil
	}
	// Both failed: err is the first, plain one.
	return nil, errors.Join(err, err2)
}

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
	if _, err := lookupHost(rctx); err != nil {
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
