package nodesync

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

const (
	// panelAddrsTTL is how long the panel's own addresses are kept: they are looked up when a
	// node's state is built, which is seldom, and a panel that moves is let in by the node.
	panelAddrsTTL  = 10 * time.Minute
	panelAddrsWait = 3 * time.Second
)

// apiAllow is the API lock of a remote node that has it on (nodeapi.DesiredState.APIAllow):
// the panel's own addresses, the networks the admin added, and the addresses of the panel's
// other nodes (a tunnel to this node arrives from a transport's, not the panel's); nil while
// it is off. The node adds the address the panel reaches it from, so a panel whose
// connections leave from another address (NAT, a second IP) is not locked out by this guess.
func (s *Syncer) apiAllow(ctx context.Context, n db.Node, nodes []db.Node) []string {
	if n.ApiLockdown == 0 || n.Address == "" {
		return nil
	}
	out := append(slices.Clone(n.ApiAllow), s.m.panelNets(ctx)...)
	out = append(out, transportNets(nodes, n.ID)...)
	slices.Sort(out)
	return append([]string{}, slices.Compact(out)...)
}

// transportNets are the addresses of the panel's other nodes as networks of one address: the
// source a tunnel through one of them comes from, which the target's API lock must let in
// before the panel ever gets there (the lock learns the panel's addresses from the panel's
// own certificate, and a tunnel does not carry it to the transport).
func transportNets(nodes []db.Node, self int64) []string {
	var out []string
	add := func(host string) {
		host = strings.Trim(strings.TrimSpace(host), "[]")
		a, err := netip.ParseAddr(host)
		if err != nil || !a.IsValid() {
			return
		}
		a = a.Unmap()
		if a.IsLoopback() || a.IsUnspecified() {
			return
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()).String())
	}
	for _, n := range nodes {
		if n.ID == self || n.Address == "" {
			continue
		}
		if host, _, err := net.SplitHostPort(n.Address); err == nil {
			add(host)
		}
		add(n.PublicHost)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// panelNets are the panel's public address and its domain as networks of one address each:
// what they are, or resolve to. A name that does not resolve leaves nothing.
func (m *Manager) panelNets(ctx context.Context) []string {
	m.addrMu.Lock()
	defer m.addrMu.Unlock()
	now := m.now()
	if !m.addrAt.IsZero() && now.Sub(m.addrAt) < panelAddrsTTL {
		return slices.Clone(m.addrs)
	}
	lookup := m.lookup
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	var out []string
	for _, key := range []string{settings.KeyPublicHost, settings.KeyDomain} {
		host, err := m.set.String(ctx, key)
		host = strings.Trim(strings.TrimSpace(host), "[]")
		if err != nil || host == "" {
			continue
		}
		addrs := []netip.Addr{}
		if a, err := netip.ParseAddr(host); err == nil {
			addrs = append(addrs, a)
		} else {
			wait, cancel := context.WithTimeout(ctx, panelAddrsWait)
			addrs, _ = lookup(wait, host)
			cancel()
		}
		for _, a := range addrs {
			if a = a.Unmap(); a.IsValid() && a.Zone() == "" {
				out = append(out, netip.PrefixFrom(a, a.BitLen()).String())
			}
		}
	}
	slices.Sort(out)
	m.addrs, m.addrAt = slices.Compact(out), now
	return slices.Clone(m.addrs)
}
