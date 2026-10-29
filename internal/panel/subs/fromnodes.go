package subs

import (
	"errors"
	"slices"
)

// FromNodes is the admin's setting for subscriptions answered by nodes (settings.
// KeySubsFromNodes): the feature on, and the nodes that answer. The link's host stays the
// subscription domain's (settings.KeySubDomain) — the admin points that name at these nodes
// — and the path stays the subscriber's token.
type FromNodes struct {
	Enabled bool    `json:"enabled"`
	Nodes   []int64 `json:"nodes,omitempty"`
}

// MaxSubNodes bounds the chosen nodes: each keeps every rendered subscription, and a
// subscription domain resolves to one address in the end.
const MaxSubNodes = 8

// Serves says whether node id is one of the chosen nodes.
func (f FromNodes) Serves(id int64) bool { return f.Enabled && slices.Contains(f.Nodes, id) }

// Check refuses what the node state would choke on: too many nodes, one id twice, an id that
// is no node. The error is a code for the admin panel's field.
func (f FromNodes) Check(nodeExists func(int64) bool) error {
	if len(f.Nodes) > MaxSubNodes {
		return errors.New("subs_nodes_many")
	}
	seen := make(map[int64]bool, len(f.Nodes))
	for _, id := range f.Nodes {
		if id <= 0 || seen[id] || !nodeExists(id) {
			return errors.New("subs_node_unknown")
		}
		seen[id] = true
	}
	return nil
}
