package app

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// Values that live in two places at once: a setting in the database and this server's own
// listener, files or container. A database restored from another server moves one side
// only, and the two then disagree while the panel keeps working — printing addresses that
// nobody serves. Each pair has one owner; the other side follows it at start, and what
// cannot be repaired without the admin is named in the log (Settings, Diagnostics reads
// the same lines).

// ReconcileSubPort is the port the subscription listener may open, given its setting and
// the panel's own port (which follows the listener, see the panel-port correction at
// start). A sub_port equal to the panel's would take the panel's address before the
// panel's own listener opens, and the panel would not start at all; the panel's port
// serves subscriptions anyway, so the subscription port is 0 then.
func ReconcileSubPort(subPort, panelPort int) (port int, changed bool) {
	if subPort > 0 && subPort == panelPort {
		return 0, true
	}
	return subPort, false
}

// PathProblem is one secret path that cannot work as it is stored.
type PathProblem struct {
	Key  string // the setting, e.g. "sub_path"
	Text string // what is wrong, for the log and Diagnostics
	Fix  string // what the admin can do about it
}

// PathProblems lists the secret paths the panel cannot serve as they are: an empty one, one
// that cannot be a URL path segment, or two that are the same — the router takes the first
// matching one, so the other is not served at its own printed URL at all. A database
// restored from another server can bring any of these.
func PathProblems(p settings.Paths, monitorPath string) []PathProblem {
	named := []struct {
		key   string
		value string
		fix   string
	}{
		{settings.KeyAdminPath, p.Admin, "arikan admin reset-path"},
		{settings.KeySubPath, p.Sub, "arikan admin reset-sub-path"},
		{settings.KeyAgentPath, p.Agent, "set another agent path (Payments → Agents), or clear it"},
		{"monitor.path", monitorPath, "set another monitoring path (Monitoring)"},
	}
	var out []PathProblem
	for _, n := range named {
		switch {
		case n.value == "" && n.key == settings.KeyAdminPath:
			out = append(out, PathProblem{n.key, "admin_path is empty: the panel is served at no address", "arikan admin bootstrap (a new panel) or arikan admin reset-path"})
		case n.value == "" && n.key == settings.KeySubPath:
			out = append(out, PathProblem{n.key, "sub_path is empty: subscription links have no path to go to", "arikan admin reset-sub-path"})
		case n.value == "":
		case !pathSegment(n.value):
			out = append(out, PathProblem{n.key, fmt.Sprintf("%s cannot be a path segment: nothing is served at the URL the panel prints", n.key), n.fix})
		}
	}
	for i := 0; i < len(named); i++ {
		for j := i + 1; j < len(named); j++ {
			if named[i].value == "" || named[i].value != named[j].value {
				continue
			}
			out = append(out, PathProblem{
				named[j].key,
				fmt.Sprintf("%s and %s are the same: the one the router checks first answers at both addresses", named[i].key, named[j].key),
				named[j].fix,
			})
		}
	}
	if p.Legacy != "" {
		first, _, _ := strings.Cut(p.Legacy, "/")
		bad := false
		for _, seg := range strings.Split(p.Legacy, "/") {
			if !pathSegment(seg) {
				bad = true
			}
		}
		if bad {
			out = append(out, PathProblem{
				settings.KeyLegacySubPath,
				"legacy_sub_path cannot be a path: the old panel's links reach nothing",
				"change or clear legacy_sub_path (Users, Import)",
			})
		}
		for _, n := range named {
			if n.value != "" && n.value == first {
				out = append(out, PathProblem{
					settings.KeyLegacySubPath,
					fmt.Sprintf("legacy_sub_path starts with the same segment as %s: the old panel's links never reach their handler", n.key),
					"change or clear legacy_sub_path (Users, Import)",
				})
			}
		}
	}
	return out
}

// pathSegment is what a secret path may be to match a request: one URL path segment of
// Latin letters, digits, - and _, as bootstrap and the settings page write them.
func pathSegment(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// NodeProblems lists the remote nodes that cannot work as their rows are: a row without a
// certificate fingerprint can never accept this panel's client certificate, and the panel
// keeps nothing else of a node's key. A database-only restore leaves the other server's
// rows while this server has its own paper credentials, and rows added before a full
// restore point at nodes whose keys never came along.
func NodeProblems(nodes []db.Node) []string {
	var out []string
	for _, n := range nodes {
		if n.Address == "" {
			continue // the panel's own node, reached over the socket
		}
		if strings.TrimSpace(n.CertSha256) == "" {
			out = append(out, fmt.Sprintf("node %q (%s) has no certificate fingerprint: it cannot accept the panel's node certificate", n.Name, n.Address))
		}
	}
	return out
}

// PaymentAdaptersWithoutSettings lists the payment adapters the host runs whose settings
// this database does not have: a database restored from another server, or one whose
// settings row went, leaves the adapter running while the method is switched off — the
// payments it should take are not offered at all.
func PaymentAdaptersWithoutSettings(state addons.State, hasSettings func(id string) bool) []string {
	var out []string
	for id, a := range state.Adapters {
		if a.KindOf() != addons.KindPayment || a.Status != "running" || hasSettings(id) {
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// reconcileSubPort brings the subscription port setting to the panel's own port when a
// restored database made the two the same. It runs before the subscription listener opens,
// so the panel starts; the setting it corrects is what the links are built from.
func (p *Panel) reconcileSubPort(ctx context.Context) {
	panelPort, _, err := settings.Get[int](ctx, p.Settings, settings.KeyPanelPort)
	if err != nil || panelPort <= 0 {
		return
	}
	subPort, _, err := settings.Get[int](ctx, p.Settings, settings.KeySubPort)
	if err != nil {
		return
	}
	port, changed := ReconcileSubPort(subPort, panelPort)
	if !changed {
		return
	}
	if err := settings.Set(ctx, p.Settings, settings.KeySubPort, port); err != nil {
		p.logf().Warn("settings: the subscription port is the panel's own port and the setting cannot be corrected", "sub_port", subPort, "panel_port", panelPort, "err", err)
		return
	}
	p.logf().Warn("settings: the subscription port was the panel's own port (a database from another server?); the panel's port serves subscriptions anyway", "sub_port", subPort, "panel_port", panelPort)
}

// checkDivergence names the pairs that disagree and cannot be repaired here. It runs once,
// at the panel's first Apply: the log it writes is the Diagnostics page's, and a repair by
// the admin (a new key, the adapter's settings) is seen at the next start, not needed here.
func (p *Panel) checkDivergence(ctx context.Context, paths settings.Paths, monitorPath string) {
	p.diverged.Do(func() {
		log := p.logf()
		for _, prob := range PathProblems(paths, monitorPath) {
			log.Warn("settings: "+prob.Text, "fix", prob.Fix)
		}
		nodes, err := p.st.Q.ListNodes(ctx)
		if err != nil {
			log.Warn("nodes: the node rows were not read", "err", err)
		}
		for _, text := range NodeProblems(nodes) {
			log.Warn("nodes: "+text, "fix", "issue a new join key on the node's page and run arikan join on the node")
		}
		state, err := p.Addons.State()
		if err != nil {
			log.Warn("addons: the host's state.json was not read", "err", err)
			return
		}
		ids := PaymentAdaptersWithoutSettings(state, func(id string) bool {
			_, found, err := p.Settings.Raw(ctx, "addon."+id) // billing.addonKey
			return err == nil && found
		})
		for _, id := range ids {
			log.Warn("addons: the payment adapter runs but this database has no settings for it: its payment method is not offered", "addon", id, "fix", "set it up in Payments, payment methods, or restore the database it belongs to")
		}
	})
}

// logf is the panel's logger; a test panel may run without one.
func (p *Panel) logf() *slog.Logger {
	if p.log == nil {
		return slog.Default()
	}
	return p.log
}
