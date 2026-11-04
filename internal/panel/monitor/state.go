package monitor

import (
	"context"
	"database/sql"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// State is what the page shows: the service's own numbers. Nothing here names a
// subscriber, a device, a link or an address of one.
type State struct {
	GeneratedAt time.Time `json:"generated_at"`
	Brand       string    `json:"brand"`
	// Blocks: which of them the page draws, and the period the chart covers.
	Blocks []string `json:"blocks"`
	Range  string   `json:"range"`
	// DetailOn: the nodes are summing what the subscriptions go to, so the sites block has
	// something to show; off, it says so instead.
	DetailOn bool `json:"detail_on"`

	Summary  Summary   `json:"summary"`
	Traffic  []Point   `json:"traffic"`
	Nodes    []Node    `json:"nodes"`
	Sites    []Site    `json:"sites"`
	Payments []Payment `json:"payments"`
}

// Summary is the service at a glance.
type Summary struct {
	Online        int `json:"online"`
	Subscriptions int `json:"subscriptions"`
	Active        int `json:"active"`
	// Traffic of the day (UTC), of the month, and of all time.
	TrafficToday int64 `json:"traffic_today"`
	TrafficMonth int64 `json:"traffic_month"`
	TrafficTotal int64 `json:"traffic_total"`
	NodesUp      int   `json:"nodes_up"`
	NodesTotal   int   `json:"nodes_total"`
}

// Point is one bucket of the chart.
type Point struct {
	T    time.Time `json:"t"`
	Up   int64     `json:"up"`
	Down int64     `json:"down"`
}

// Node is one server of the service. Name is the admin's own label: it may say what the
// server is for, never who is on it.
type Node struct {
	Name  string `json:"name"`
	Local bool   `json:"local"`
	OK    bool   `json:"ok"`
	Conns int    `json:"conns"`
	Up    int64  `json:"up"`
	Down  int64  `json:"down"`
	// LastSeen is when the node last answered, absent while it has not since the panel
	// started (the page then says it never answered rather than showing year 1).
	LastSeen *time.Time `json:"last_seen,omitempty"`
}

// Site is what the subscriptions went to over the period, summed by the nodes.
type Site struct {
	Site string `json:"site"`
	Up   int64  `json:"up"`
	Down int64  `json:"down"`
}

// Payment is what came in over the last 30 days, by currency, in hundredths.
type Payment struct {
	Currency string `json:"currency"`
	Count    int64  `json:"count"`
	Amount   int64  `json:"amount"`
}

// ranges are the periods the chart may cover; the page opens on the last day.
var ranges = map[string]bool{"24h": true, "7d": true, "30d": true}

// Range says which period a request asked for, and refuses anything else.
func Range(rng string) string {
	if ranges[rng] {
		return rng
	}
	return "24h"
}

// state gathers the page's numbers.
func (h *Handler) state(ctx context.Context, cfg Config, rng string) (State, error) {
	now := h.d.Now()
	// Every list is an array, never null: the page draws them without asking whether they
	// are there.
	out := State{GeneratedAt: now.UTC(), Range: rng, Blocks: cfg.Blocks, Sites: []Site{}, Payments: []Payment{}}
	out.Brand = h.d.Brand(ctx)

	counts, err := domain.CountStates(ctx, h.d.Store.Q, now)
	if err != nil {
		return out, err
	}
	out.Summary.Subscriptions, out.Summary.Active = int(counts.Total), int(counts.Active+counts.Expiring)
	if out.Summary.Online, err = h.online(ctx); err != nil {
		return out, err
	}
	// The day and the month come out of the daily table (UTC days, the panel's own
	// accounting): one query for both, and the same numbers the panel shows.
	monthStart := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	days, err := h.d.Store.Q.TotalTrafficDaily(ctx, monthStart.Unix()/86400)
	if err != nil {
		return out, err
	}
	today := now.UTC().Unix() / 86400
	for _, d := range days {
		out.Summary.TrafficMonth += d.Up + d.Down
		if d.Day == today {
			out.Summary.TrafficToday = d.Up + d.Down
		}
	}
	total, err := h.d.Store.Q.SumUserTraffic(ctx)
	if err != nil {
		return out, err
	}
	out.Summary.TrafficTotal = total.Up + total.Down

	if out.Traffic, err = h.traffic(ctx, rng); err != nil {
		return out, err
	}
	if out.Nodes, err = h.nodes(ctx, rng); err != nil {
		return out, err
	}
	for _, n := range out.Nodes {
		if n.OK {
			out.Summary.NodesUp++
		}
	}
	out.Summary.NodesTotal = len(out.Nodes)

	if cfg.Show("sites") {
		if out.DetailOn, err = h.d.Set.On(ctx, settings.TrafficDetail); err != nil {
			return out, err
		}
		if out.DetailOn {
			since := domain.SiteDay(periodStart(now, rng))
			rows, err := h.d.Store.Q.TopUserSites(ctx, db.TopUserSitesParams{Since: since, Lim: 12})
			if err != nil {
				return out, err
			}
			out.Sites = make([]Site, 0, len(rows))
			for _, r := range rows {
				out.Sites = append(out.Sites, Site{Site: r.Site, Up: r.Up, Down: r.Down})
			}
		}
	}
	if cfg.Show("payments") {
		rows, err := h.d.Store.Q.PaymentTotals(ctx, sql.NullInt64{Int64: now.Add(-30 * 24 * time.Hour).Unix(), Valid: true})
		if err != nil {
			return out, err
		}
		out.Payments = make([]Payment, 0, len(rows))
		for _, r := range rows {
			out.Payments = append(out.Payments, Payment{Currency: r.Currency, Count: r.N, Amount: r.Total})
		}
	}
	return out, nil
}

// traffic is the chart's points, by the day for 30 days and by the hour otherwise.
func (h *Handler) traffic(ctx context.Context, rng string) ([]Point, error) {
	daily, since := trafficSince(h.d.Now(), rng)
	out := []Point{}
	if daily {
		rows, err := h.d.Store.Q.TotalTrafficDaily(ctx, since)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, Point{T: time.Unix(r.Day*86400, 0).UTC(), Up: r.Up, Down: r.Down})
		}
		return out, nil
	}
	rows, err := h.d.Store.Q.TotalTrafficHourly(ctx, since)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, Point{T: time.Unix(r.Hour*3600, 0).UTC(), Up: r.Up, Down: r.Down})
	}
	return out, nil
}

// nodes is every server of the service with what it carried and how it answers.
func (h *Handler) nodes(ctx context.Context, rng string) ([]Node, error) {
	rows, err := h.d.Store.Q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	daily, since := trafficSince(h.d.Now(), rng)
	carried := map[int64]domain.Bytes{}
	if daily {
		sums, err := h.d.Store.Q.SumNodesTrafficDaily(ctx, since)
		if err != nil {
			return nil, err
		}
		for _, s := range sums {
			carried[s.NodeID] = domain.Bytes{Up: s.Up, Down: s.Down}
		}
	} else {
		sums, err := h.d.Store.Q.SumNodesTrafficHourly(ctx, since)
		if err != nil {
			return nil, err
		}
		for _, s := range sums {
			carried[s.NodeID] = domain.Bytes{Up: s.Up, Down: s.Down}
		}
	}
	out := make([]Node, 0, len(rows))
	for _, n := range rows {
		t := carried[n.ID]
		v := Node{Name: n.Name, Local: n.Address == "", Up: t.Up, Down: t.Down}
		// The label of a server nobody named: the panel's own is "this server", and the
		// page should not show its address.
		if v.Name == "" {
			if v.Local {
				v.Name = "panel"
			} else {
				v.Name = "node"
			}
		}
		if h.d.Nodes != nil {
			if hv, ok := h.d.Nodes.Health(n.ID); ok {
				v.OK, v.Conns = hv.OK, hv.Health.Conns
				if !hv.LastOK.IsZero() {
					seen := hv.LastOK
					v.LastSeen = &seen
				}
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// online is how many subscriptions have a connection now: the same count the panel's own
// dashboard shows (one user with several devices counts once).
func (h *Handler) online(ctx context.Context) (int, error) {
	if h.d.Online == nil {
		return 0, nil
	}
	live := h.d.Online()
	if len(live) == 0 {
		return 0, nil
	}
	names := make([]string, 0, len(live))
	for name := range live {
		names = append(names, name)
	}
	owners, err := h.d.Store.Q.SlotOwners(ctx, names)
	if err != nil {
		return 0, err
	}
	seen := map[int64]bool{}
	for _, o := range owners {
		seen[o.UserID] = true
	}
	return len(seen), nil
}

// trafficSince is where a range starts: a day number for 30 days (the chart then goes by
// the day), an hour number otherwise.
func trafficSince(now time.Time, rng string) (daily bool, since int64) {
	switch rng {
	case "30d":
		return true, now.Add(-30*24*time.Hour).Unix() / 86400
	case "7d":
		return false, now.Add(-7*24*time.Hour).Unix() / 3600
	}
	return false, now.Add(-24*time.Hour).Unix() / 3600
}

// periodStart is the first moment trafficSince counts for rng.
func periodStart(now time.Time, rng string) time.Time {
	daily, since := trafficSince(now, rng)
	if daily {
		return time.Unix(since*86400, 0).UTC()
	}
	return time.Unix(since*3600, 0).UTC()
}
