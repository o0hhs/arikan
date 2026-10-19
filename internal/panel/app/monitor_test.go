package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/monitor"
	"mikan/internal/panel/settings"
)

// monitorPassword is the page's own password in these tests: long enough for
// auth.MinPassword, and not the panel's.
const monitorPassword = "monitor-page-password"

// The public monitoring page end to end: off until the admin sets a password, its own
// address on the panel's host, its own password (the panel's ones mean nothing there), and
// everything it answers disappearing the moment the password or the switch goes.
func TestMonitorPage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	// One subscription, so the summary the page shows has something true in it.
	tariffs, err := h.st.Q.ListTariffs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return h.now }
	if _, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID}); err != nil {
		t.Fatal(err)
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	path := "/" + adminPath + "/api/v1/monitor"

	type view struct {
		Enabled     bool     `json:"enabled"`
		Path        string   `json:"path"`
		HasPassword bool     `json:"has_password"`
		Blocks      []string `json:"blocks"`
		Available   []string `json:"available"`
		MinPassword int      `json:"min_password"`
	}
	var v view
	resp, body := h.do(http.MethodGet, path, nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || v.Enabled || v.HasPassword || v.Path != "" {
		t.Fatalf("off by default, no password: %d %s", resp.StatusCode, body)
	}
	if len(v.Blocks) != 3 || v.Blocks[0] != "summary" || v.MinPassword < 12 {
		t.Fatalf("what a page nobody set up shows: %s", body)
	}

	// A password short of the panel's own is refused before anything is stored (the tag
	// repeats auth.MinPassword; TestMonitorPasswordTagIsMinPassword keeps them in step).
	resp, body = h.do(http.MethodPost, path+"/password", map[string]any{"password": "short"}, csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "body.password") {
		t.Fatalf("a short password: %d %s", resp.StatusCode, body)
	}
	if v, err := monitor.Load(ctx, settings.New(h.st.Q)); err != nil || v.Hash != "" {
		t.Fatalf("a refused password was stored: %+v %v", v, err)
	}
	resp, body = h.do(http.MethodPost, path+"/password", map[string]any{"password": monitorPassword}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || !v.HasPassword || len(v.Path) != monitor.PathLen {
		t.Fatalf("the password set, the link made: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPut, path, map[string]any{"enabled": true, "blocks": []string{"summary", "traffic", "nodes", "sites", "payments"}}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || !v.Enabled || len(v.Blocks) != 5 {
		t.Fatalf("the page turned on: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPut, path, map[string]any{"enabled": true, "blocks": []string{"summary", "nonsense"}}, csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "monitor_block_unknown") {
		t.Fatalf("a block this panel does not have: %d %s", resp.StatusCode, body)
	}

	// The router learns the page's own address from the settings, as it does every five
	// seconds while the panel runs.
	if _, err := h.p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	base := "/" + v.Path
	resp, body = h.do(http.MethodGet, base+"/", nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `<base href="/`+v.Path+`/">`) {
		t.Fatalf("the page's own entry with its own base: %d %s", resp.StatusCode, body)
	}
	// The bundle's own files are served from the page's address: without them the entry
	// loads and draws nothing.
	resp, body = h.do(http.MethodGet, base+"/assets/app-1.js", nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "console.log(1)") {
		t.Fatalf("the bundle's asset: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.do(http.MethodGet, base+"/favicon.png", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the page's icon: %d", resp.StatusCode)
	}
	// Nothing of the page answers anything but a read: the two POSTs above are its own.
	if resp, _ := h.do(http.MethodPost, base+"/assets/app-1.js", nil, nil); resp.StatusCode == http.StatusOK {
		t.Fatalf("a write to the bundle: %d", resp.StatusCode)
	}
	// The panel's own password opens nothing here, and the state is not given without the
	// page's session.
	resp, body = h.do(http.MethodGet, base+"/api/state", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the state without a session: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPost, base+"/api/login", map[string]any{"password": password}, map[string]string{"Sec-Fetch-Site": "same-origin"})
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "bad_password") {
		t.Fatalf("the panel's own password: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPost, base+"/api/login", map[string]any{"password": monitorPassword}, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a login from another site: %d %s", resp.StatusCode, body)
	}

	resp, body = h.do(http.MethodPost, base+"/api/login", map[string]any{"password": monitorPassword}, map[string]string{"Sec-Fetch-Site": "same-origin"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the page's own password: %d %s", resp.StatusCode, body)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == monitor.CookieName {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value == "" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("the session cookie: %+v", resp.Cookies())
	}

	resp, body = h.do(http.MethodGet, base+"/api/state?range=24h", nil, map[string]string{"Cookie": cookie.Name + "=" + cookie.Value})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the state with the session: %d %s", resp.StatusCode, body)
	}
	var state struct {
		Brand   string   `json:"brand"`
		Blocks  []string `json:"blocks"`
		Range   string   `json:"range"`
		Summary struct {
			Subscriptions int `json:"subscriptions"`
			NodesTotal    int `json:"nodes_total"`
		} `json:"summary"`
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	}
	if json.Unmarshal(body, &state) != nil || state.Range != "24h" || len(state.Blocks) != 5 || state.Summary.Subscriptions != 1 || state.Summary.NodesTotal == 0 {
		t.Fatalf("what the page shows: %s", body)
	}
	// Nothing of a subscriber is in it: no link, no token, no name of anyone.
	for _, secret := range []string{"sub_token", "token", "user", "link"} {
		if strings.Contains(strings.ToLower(string(body)), secret) {
			t.Fatalf("the state carries %q: %s", secret, body)
		}
	}

	// A new address: the old link is a 404 at once (the router is told right away, as the
	// panel's settings page does after a change).
	var rotated view
	resp, body = h.do(http.MethodPost, path+"/path", map[string]any{"path": "monitor-link-1234"}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &rotated) != nil || rotated.Path != "monitor-link-1234" {
		t.Fatalf("a new address: %d %s", resp.StatusCode, body)
	}
	if _, err := h.p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.do(http.MethodGet, base+"/api/state", nil, map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the old address after a rotation: %d", resp.StatusCode)
	}
	// The password stayed, and its sessions are signed with it: the same cookie opens the
	// page at its new address.
	if resp, body := h.do(http.MethodGet, "/monitor-link-1234/api/state", nil, map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}); resp.StatusCode != http.StatusOK {
		t.Fatalf("the same session at the new address: %d %s", resp.StatusCode, body)
	}

	// Upping the switch closes the page even with the password kept.
	if resp, body := h.do(http.MethodPut, path, map[string]any{"enabled": false, "blocks": []string{"summary"}}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("turned off: %d %s", resp.StatusCode, body)
	}
	if _, err := h.p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.do(http.MethodGet, "/monitor-link-1234/api/state", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a page that is off: %d", resp.StatusCode)
	}
	// And a password put back closes it just the same: no password, no page.
	if resp, body := h.do(http.MethodPut, path, map[string]any{"enabled": true, "blocks": []string{"summary"}}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("on again: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodDelete, path+"/password", nil, csrf); resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || v.HasPassword {
		t.Fatalf("the password cleared: %d %s", resp.StatusCode, body)
	}
	if _, err := h.p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.do(http.MethodGet, "/monitor-link-1234/", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a page without a password: %d", resp.StatusCode)
	}
	// The panel's own login is untouched by all of this.
	if resp, _ := h.do(http.MethodGet, "/"+adminPath+"/api/v1/monitor", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the admin's own session still works: %d", resp.StatusCode)
	}

	// Every accepted call is in the audit log: the password set and cleared, three saves of
	// the switch, the address changed. The refused ones (the short password, the block this
	// panel does not have, the panel's own password as this page's) are not.
	var entries int
	if err := h.st.DB.QueryRow(`SELECT count(*) FROM audit_log WHERE action LIKE 'monitor.%'`).Scan(&entries); err != nil || entries != 6 {
		t.Fatalf("the audit log: %d %v", entries, err)
	}
}

// The page's password is the only thing in front of a public address: five wrong ones from
// one client and the sixth is refused before the hash is even run.
func TestMonitorLoginIsRateLimited(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	if _, err := h.p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	path := "/" + adminPath + "/api/v1/monitor"
	if resp, body := h.do(http.MethodPost, path+"/password", map[string]any{"password": monitorPassword}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("password: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPut, path, map[string]any{"enabled": true, "blocks": []string{"summary"}}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("enable: %d %s", resp.StatusCode, body)
	}
	var v struct {
		Path string `json:"path"`
	}
	if _, body := h.do(http.MethodGet, path, nil, nil); json.Unmarshal(body, &v) != nil {
		t.Fatal("view")
	}
	if _, err := h.p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	var last int
	for i := 0; i < 5; i++ {
		resp, _ := h.do(http.MethodPost, "/"+v.Path+"/api/login", map[string]any{"password": "wrong-password-000"}, same)
		last = resp.StatusCode
	}
	if last != http.StatusUnauthorized {
		t.Fatalf("the fifth wrong password: %d", last)
	}
	resp, body := h.do(http.MethodPost, "/"+v.Path+"/api/login", map[string]any{"password": "wrong-password-000"}, same)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" || !strings.Contains(string(body), "rate_limited") {
		t.Fatalf("the sixth: %d %s %v", resp.StatusCode, body, resp.Header)
	}
	// Even the right password waits out the block.
	if resp, _ := h.do(http.MethodPost, "/"+v.Path+"/api/login", map[string]any{"password": monitorPassword}, same); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the right password inside the block: %d", resp.StatusCode)
	}
}

// The page's address and password round-trip through the settings document: what the admin
// set is what the next request reads, and the hash never leaves the panel.
func TestMonitorPasswordIsOnlyStoredHashed(t *testing.T) {
	h := newHarness(t)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	path := "/" + adminPath + "/api/v1/monitor"
	if resp, body := h.do(http.MethodPost, path+"/password", map[string]any{"password": monitorPassword}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("password: %d %s", resp.StatusCode, body)
	}
	if _, body := h.do(http.MethodGet, path, nil, nil); strings.Contains(string(body), monitorPassword) || strings.Contains(string(body), "argon2") {
		t.Fatalf("the view carries the password or its hash: %s", body)
	}
	c, err := monitor.Load(context.Background(), settings.New(h.st.Q))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.Hash, "$argon2id$") || strings.Contains(c.Hash, monitorPassword) {
		t.Fatalf("the hash as stored: %q", c.Hash)
	}
}
