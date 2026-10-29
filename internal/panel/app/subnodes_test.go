package app

import (
	"net/http"
	"strings"
	"testing"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

// A subscription served by nodes is a link on their public 443: enabling the feature without
// a subscription domain of its own, or with another port, would advertise an address the
// nodes never answer. The settings API refuses both and says which one is missing.
func TestSubsFromNodesNeedsTheDomainAnd443(t *testing.T) {
	h := newHarness(t)
	if err := domain.Seed(t.Context(), h.st, h.now); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	enable := func() []byte {
		t.Helper()
		resp, out := h.do(http.MethodPatch, api+"/settings", map[string]any{
			"subs_from_nodes": map[string]any{"enabled": true, "nodes": []int64{1}},
		}, csrf)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("enabled without the domain and 443: %d %s", resp.StatusCode, out)
		}
		return out
	}
	// Neither the domain nor 443 yet.
	out := enable()
	if !strings.Contains(string(out), "subs_nodes_domain") || !strings.Contains(string(out), "subs_nodes_port") {
		t.Fatalf("the refusal does not name both: %s", out)
	}
	// The domain is there; the panel's own port is not the nodes' 443.
	if err := settings.Set(t.Context(), h.p.Settings, settings.KeySubDomain, "sub.example.com"); err != nil {
		t.Fatal(err)
	}
	out = enable()
	if strings.Contains(string(out), "subs_nodes_domain") || !strings.Contains(string(out), "subs_nodes_port") {
		t.Fatalf("the panel's port is not named as the problem: %s", out)
	}
}
