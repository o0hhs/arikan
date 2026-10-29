package nodesync

import (
	"context"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/pace"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subs"
)

// Subscriptions from the nodes: the panel renders every subscriber's profile once per sync
// and hands each chosen node the bytes, so a link keeps working when the panel's own server
// is unreachable (internal/node answers them, nodesubs). The panel stays the only place that
// renders or knows a subscriber: a node answers a path it was given, or says 404.
//
// The path is a bearer token. It goes over the node API inside a record, and nowhere else:
// no log line here names one — the statuses count records, they never list them.

const (
	// subsEvery is how often a node's whole set is sent again: the traffic in the headers,
	// a term or a quota that moved while nobody changed a row.
	subsEvery = 15 * time.Minute
	// subsMinGap is the least between two pushes a burst of changes causes: a panel that
	// creates users in a loop syncs them in one go, and a block still reaches the node
	// within a minute.
	subsMinGap = time.Minute
)

// SubsRenderer renders one user's subscription for a node (subs.Handler.NodeRecord).
type SubsRenderer func(ctx context.Context, u db.User) (nodeapi.SubRecord, error)

// SubsPush is one node's last subscription sync and how it went.
type SubsPush struct {
	// At is when the panel last sent the node its records, Err why it could not ("" when it
	// did). Records and Bytes are what the node keeps, as of that push.
	At      time.Time
	Err     string
	Records int
	Bytes   int64
	// Generation is the panel's sync number of that push: the node drops everything older
	// than it when the push completes.
	Generation int64
}

// SetSubsRenderer gives the manager the subscription renderer; without one (a panel built
// for a test) the feature is off however the setting reads.
func (m *Manager) SetSubsRenderer(r SubsRenderer) {
	m.subsMu.Lock()
	m.subsRender = r
	m.subsMu.Unlock()
}

// SubsChanged asks for a push of the subscriptions: a user changed, a node was added, the
// setting moved. Signals are coalesced, and never closer together than subsMinGap.
func (m *Manager) SubsChanged() { signal(m.subsDirty) }

// SubsStatus is every chosen node's last push to show the admin.
func (m *Manager) SubsStatus() map[int64]SubsPush {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	out := make(map[int64]SubsPush, len(m.subsPushed))
	for id, p := range m.subsPushed {
		out[id] = p
	}
	return out
}

// runSubs pushes the subscriptions until ctx ends: at once, then every subsEvery, and
// sooner when something changed (SubsChanged).
func (m *Manager) runSubs(ctx context.Context) {
	m.pushSubs(ctx)
	t := time.NewTicker(subsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.subsDirty:
			// A burst of changes (a bulk block, an import) becomes one push.
			if !pace.Sleep(ctx, 2*time.Second) {
				return
			}
			m.pushSubsThrottled(ctx)
		case <-t.C:
			m.pushSubs(ctx)
		}
	}
}

// pushSubsThrottled is a push a change asked for: a change that came right after a push
// waits out subsMinGap, so a block still reaches the nodes within a minute rather than at
// the next full reconcile.
func (m *Manager) pushSubsThrottled(ctx context.Context) {
	m.subsMu.Lock()
	last := m.subsLast
	m.subsMu.Unlock()
	// A change right after a push waits out the gap rather than the full reconcile: a block
	// must reach the nodes within a minute even when the last push has just gone.
	if wait := subsMinGap - m.now().Sub(last); !last.IsZero() && wait > 0 {
		if !pace.Sleep(ctx, wait) {
			return
		}
	}
	m.pushSubs(ctx)
}

// pushSubs sends every chosen node its whole set: the records by chunks, then the prune of
// that generation. A node that failed halfway keeps its record of the old generation until
// the next successful push — it keeps answering, which is the point.
func (m *Manager) pushSubs(ctx context.Context) {
	cfg, err := m.subsConfig(ctx)
	if err != nil {
		m.log.Warn("subscriptions: the setting is not read", "err", err)
		return
	}
	m.subsMu.Lock()
	render := m.subsRender
	m.subsMu.Unlock()
	if !cfg.Enabled || len(cfg.Nodes) == 0 || render == nil {
		return
	}
	m.subsMu.Lock()
	m.subsLast = m.now()
	// The generation only ever grows: a clock that stands still (a test's) or steps back
	// must not let the prune keep records of an older sync.
	gen := max(m.now().UnixMilli(), m.subsGen+1)
	m.subsGen = gen
	m.subsMu.Unlock()
	for _, id := range cfg.Nodes {
		if err := ctx.Err(); err != nil {
			return
		}
		s, ok := m.Syncer(id)
		if !ok {
			continue
		}
		c, ok := s.node.(subsNode)
		if !ok {
			continue // a node client that predates the feature
		}
		p := m.pushSubsTo(ctx, id, c, render, gen)
		m.subsMu.Lock()
		m.subsPushed[id] = p
		m.subsMu.Unlock()
	}
}

// subsNode is the part of the node API a push uses.
type subsNode interface {
	PutSubs(ctx context.Context, gen int64, records []nodeapi.SubRecord) (nodeapi.SubsResult, error)
	PruneSubs(ctx context.Context, gen int64) (nodeapi.SubsResult, error)
}

// pushSubsTo renders and sends one node's whole set.
func (m *Manager) pushSubsTo(ctx context.Context, id int64, c subsNode, render SubsRenderer, gen int64) SubsPush {
	p := SubsPush{At: m.now(), Generation: gen}
	users, err := m.st.Q.ListUsers(ctx)
	if err != nil {
		p.Err = err.Error()
		m.log.Warn("subscriptions: the users are not read", "node", id, "err", err)
		return p
	}
	var chunk []nodeapi.SubRecord
	var skipped int
	var firstErr string
	flush := func() bool {
		if len(chunk) == 0 {
			return true
		}
		res, err := c.PutSubs(ctx, gen, chunk)
		if err != nil {
			p.Err = err.Error()
			return false
		}
		p.Records += res.Stored
		p.Bytes = res.Bytes
		if res.Rejected > 0 && firstErr == "" {
			firstErr = res.Reason
		}
		chunk = chunk[:0]
		return true
	}
	for _, u := range users {
		rec, err := render(ctx, u)
		if err != nil {
			skipped++
			continue
		}
		chunk = append(chunk, rec)
		if len(chunk) >= nodeapi.SubSyncChunk && !flush() {
			m.log.Warn("subscriptions: a push broke off", "node", id, "err", p.Err)
			return p
		}
	}
	if !flush() {
		m.log.Warn("subscriptions: a push broke off", "node", id, "err", p.Err)
		return p
	}
	res, err := c.PruneSubs(ctx, gen)
	if err != nil {
		p.Err = err.Error()
		m.log.Warn("subscriptions: the prune", "node", id, "err", err)
		return p
	}
	p.Bytes = res.Bytes
	p.At = m.now()
	if firstErr != "" {
		m.log.Warn("subscriptions: records the node refused", "node", id, "n", skipped, "reason", firstErr)
	}
	m.log.Info("subscriptions pushed", "node", id, "records", p.Records, "bytes", p.Bytes, "skipped", skipped)
	return p
}

// subsConfig is the admin's setting: the feature on, and which nodes answer.
func (m *Manager) subsConfig(ctx context.Context) (subs.FromNodes, error) {
	cfg, ok, err := settings.Get[subs.FromNodes](ctx, m.set, settings.KeySubsFromNodes)
	if err != nil || !ok {
		return cfg, err
	}
	return cfg, nil
}
