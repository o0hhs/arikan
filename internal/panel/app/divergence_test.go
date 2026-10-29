package app

import (
	"strings"
	"testing"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

func TestReconcileSubPort(t *testing.T) {
	for _, c := range []struct {
		sub, panel, want int
		changed          bool
	}{
		{0, 21355, 0, false},       // off stays off
		{9443, 21355, 9443, false}, // a port of its own
		{21355, 21355, 0, true},    // the panel's own: the panel would not start
		{9053, 0, 9053, false},     // no panel port known (development): nothing to compare
		{9053, -1, 9053, false},    // a broken value is not the listener's
		{443, 443, 0, true},        // the same rule on the default port
	} {
		got, changed := ReconcileSubPort(c.sub, c.panel)
		if got != c.want || changed != c.changed {
			t.Errorf("ReconcileSubPort(%d, %d) = %d, %v; want %d, %v", c.sub, c.panel, got, changed, c.want, c.changed)
		}
	}
}

// Apply corrects the subscription port a restored database made equal to the panel's own;
// the listener opens right after Apply, and a correction after that would be too late.
func TestApplyCorrectsASubPortOfThePanelsOwn(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if err := settings.Set(ctx, h.p.Settings, settings.KeyPanelPort, 21355); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, h.p.Settings, settings.KeySubPort, 21355); err != nil {
		t.Fatal(err)
	}
	if _, err := h.p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if sub, _, err := settings.Get[int](ctx, h.p.Settings, settings.KeySubPort); err != nil || sub != 0 {
		t.Fatalf("the subscription port must fall back to the panel's: %d %v", sub, err)
	}
	// A port of its own is the admin's choice and stays.
	if err := settings.Set(ctx, h.p.Settings, settings.KeySubPort, 9443); err != nil {
		t.Fatal(err)
	}
	if _, err := h.p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if sub, _, err := settings.Get[int](ctx, h.p.Settings, settings.KeySubPort); err != nil || sub != 9443 {
		t.Fatalf("a distinct subscription port: %d %v", sub, err)
	}
}

func TestPathProblems(t *testing.T) {
	ok := settings.Paths{Admin: "Kq7vN2xTg4mRz8pLw3YbC5dE", Sub: "s9Hj2Kd8Lm4P"}
	if got := PathProblems(ok, ""); len(got) != 0 {
		t.Fatalf("a healthy pair: %+v", got)
	}
	keys := func(ps []PathProblem) string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Key)
		}
		return strings.Join(out, ",")
	}

	// The same segment for the admin and the subscription links: the router checks the
	// admin first, so subscription links open the panel's login page.
	both := ok
	both.Sub = both.Admin
	if got := PathProblems(both, ""); keys(got) != settings.KeySubPath {
		t.Fatalf("admin and sub equal: %+v", got)
	}
	// A restored database can bring a path no request can match.
	for _, c := range []struct{ admin, sub, want string }{
		{"", "s9Hj2Kd8Lm4P", settings.KeyAdminPath},
		{"Kq7vN2xTg4mRz8pLw3YbC5dE", "", settings.KeySubPath},
		{"a/b", "s9Hj2Kd8Lm4P", settings.KeyAdminPath},
		{"Kq7vN2xTg4mRz8pLw3YbC5dE", "sub path", settings.KeySubPath},
		{"Kq7vN2xTg4mRz8pLw3YbC5dE", "s9Hj2Kd8Lm4P.", settings.KeySubPath},
	} {
		got := PathProblems(settings.Paths{Admin: c.admin, Sub: c.sub}, "")
		if keys(got) != c.want {
			t.Errorf("admin %q, sub %q: %+v; want %s", c.admin, c.sub, got, c.want)
		}
	}
	// The agent panel and the monitoring page share the same switch as the admin and sub.
	agent := ok
	agent.Agent = ok.Admin
	if got := PathProblems(agent, ""); keys(got) != settings.KeyAgentPath {
		t.Fatalf("agent equal to admin: %+v", got)
	}
	if got := PathProblems(ok, ok.Sub); keys(got) != "monitor.path" {
		t.Fatalf("monitor equal to sub: %+v", got)
	}
	// An imported panel's old links go by the first segment.
	legacy := ok
	legacy.Legacy = "s9Hj2Kd8Lm4P/api/sub"
	if got := PathProblems(legacy, ""); keys(got) != settings.KeyLegacySubPath {
		t.Fatalf("legacy under the subscription path: %+v", got)
	}
	badLegacy := ok
	badLegacy.Legacy = "sub/x y"
	if got := PathProblems(badLegacy, ""); keys(got) != settings.KeyLegacySubPath {
		t.Fatalf("a legacy path that cannot match: %+v", got)
	}
	if got := PathProblems(settings.Paths{Admin: ok.Admin, Sub: ok.Sub, Legacy: "sub/api/sub"}, ""); len(got) != 0 {
		t.Fatalf("a legacy path of its own: %+v", got)
	}
}

func TestNodeProblems(t *testing.T) {
	nodes := []db.Node{
		{Name: "local", Address: ""}, // the panel's own node, reached over the socket
		{Name: "n1", Address: "203.0.113.7:25305"},
		{Name: "n2", Address: "203.0.113.8:25305", CertSha256: "ab12"},
	}
	got := NodeProblems(nodes)
	if len(got) != 1 || !strings.Contains(got[0], `"n1"`) {
		t.Fatalf("nodes without a pin: %+v", got)
	}
}

func TestPaymentAdaptersWithoutSettings(t *testing.T) {
	state := addons.State{Adapters: map[string]addons.Installed{
		"yoomoney":   {Status: "running"},                           // payment, runs, no settings here
		"cryptobot":  {Status: "running", Kind: addons.KindPayment}, // the same, named explicitly
		"stopped":    {Status: "stopped"},                           // not running: nothing is offered anyway
		"telegram":   {Status: "running", Kind: addons.KindBot},     // not a payment adapter
		"configured": {Status: "running"},                           // has its settings
	}}
	configured := map[string]bool{"configured": true}
	got := PaymentAdaptersWithoutSettings(state, func(id string) bool { return configured[id] })
	if strings.Join(got, ",") != "cryptobot,yoomoney" {
		t.Fatalf("adapters without settings: %v", got)
	}
}
