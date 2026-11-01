package subs

import (
	"context"
	"net"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/turn"
)

// The "YouTube without ads" offer is a cascade of the admin's own: the user enters the
// Russian node (the one their ISP reaches), that node carries the traffic out through the
// network and, for what needs a Russian address, leaves with its own one — where nfqws on
// its host (installer/src/dpi.rs) gets YouTube and Discord past the DPI. The Russian
// YouTube has no ads; the foreign one has them. The cascade, then, is
// user → the entry node → the node it exits through.
//
// INCY cannot follow the cascade: a full Xray config carries its own routing and no state
// of the node's connections, and INCY's rules cannot leave the phone. So the config names
// the entry by its address — the address the entry is reached by in the cascade, i.e. the
// node it exits through — and ends its rules there. The address cannot be a node's own
// public one: from the user's ISP it may be the very one that is filtered.

// YouTubeExitHost is the address the INCY configs end on: the node the "YouTube without
// ads" entry node exits through ("host:port" of its SOCKS), or "" when the offer is off or
// the entry leaves directly. A direct exit is the node's own address, which INCY cannot
// reach as an outbound, so the configs end on the auto group instead.
func YouTubeExitHost(ctx context.Context, q *db.Queries, r Routes) (string, error) {
	entry, ok := r.YouTubeEntry()
	if !ok || r.YouTubeExitDirect() {
		return "", nil
	}
	exit, ok := r.YouTubeExitNode()
	if !ok || exit == entry {
		return "", nil
	}
	nodes, err := q.ListNodes(ctx)
	if err != nil {
		return "", err
	}
	turns, err := q.ListNodeTurns(ctx)
	if err != nil {
		return "", err
	}
	byID := make(map[int64]db.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	for _, t := range turns {
		if t.NodeID == exit && t.Enabled != 0 {
			tc := turn.FromRow(t)
			return net.JoinHostPort(nodes[exit].PublicHost, tc.ClientPort), nil
		}
	}
	n, ok := byID[exit]
	if !ok {
		return "", nil
	}
	return net.JoinHostPort(domain.NodeHost(n), "443"), nil
}
