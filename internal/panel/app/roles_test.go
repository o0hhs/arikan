package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// as logs in as username in a client of its own: the roles are checked per session.
func (h *harness) as(t *testing.T, username, pw string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := *h.ts.Client() // a client of its own: the jars must not replace one another
	c.Jar = jar
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

func (h *harness) loginAs(t *testing.T, c *http.Client, username, pw string) string {
	t.Helper()
	body := `{"username":"` + username + `","password":"` + pw + `"}`
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/"+adminPath+"/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("login %s: %v %v", username, err, resp)
	}
	var me struct {
		CSRFToken string `json:"csrf_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&me)
	resp.Body.Close()
	return me.CSRFToken
}

func (h *harness) asRole(t *testing.T, username, pw string) (*http.Client, string) {
	t.Helper()
	c := h.as(t, username, pw)
	return c, h.loginAs(t, c, username, pw)
}

func (h *harness) doAs(t *testing.T, c *http.Client, csrf, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, h.ts.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

// The panel's roles: a superadmin keeps everything and names the others; support sees the
// subscriber's card alone; tech keeps the nodes and the logs.
func TestPanelRoles(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	mk := func(name, role string) {
		hash, err := auth.HashPassword(password)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.st.Q.CreateAdminWithRole(ctx, db.CreateAdminWithRoleParams{Username: name, PasswordHash: hash, Role: role, CreatedAt: time.Now().Unix()}); err != nil {
			t.Fatal(err)
		}
	}
	mk("olga", "support")
	mk("tech", "tech")
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, func() time.Time { return h.now }), noChanges{}, func() time.Time { return h.now }).
		Create(ctx, domain.CreateInput{Name: "Иван", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}

	// The superadmin: everything, and naming admins.
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("superadmin login")
	}
	if resp, body := h.do(http.MethodGet, "/"+adminPath+"/api/v1/admins", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("admins: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodGet, "/"+adminPath+"/api/v1/users", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("superadmin users: %d %s", resp.StatusCode, body)
	}

	// Support: the card and its three actions; nothing else.
	sup, supCSRF := h.asRole(t, "olga", password)
	if resp, _ := h.doAs(t, sup, "", http.MethodGet, "/"+adminPath+"/api/v1/users", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("support users: %d", resp.StatusCode)
	}
	if resp, _ := h.doAs(t, sup, "", http.MethodGet, "/"+adminPath+"/api/v1/nodes", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("support nodes: %d", resp.StatusCode)
	}
	id := strconv.FormatInt(u.ID, 10)
	if resp, body := h.doAs(t, sup, "", http.MethodGet, "/"+adminPath+"/api/v1/support/card?q="+id, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("support card: %d %s", resp.StatusCode, body)
	}
	before, _ := h.st.Q.GetUser(ctx, u.ID)
	if resp, body := h.doAs(t, sup, supCSRF, http.MethodPost, "/"+adminPath+"/api/v1/support/card/"+id+"/days", map[string]int{"days": 3}); resp.StatusCode != http.StatusOK {
		t.Fatalf("support days: %d %s", resp.StatusCode, body)
	}
	after, _ := h.st.Q.GetUser(ctx, u.ID)
	if after.ExpiresAt.Int64-before.ExpiresAt.Int64 != 3*86400 {
		t.Fatalf("the gift: %d → %d", before.ExpiresAt.Int64, after.ExpiresAt.Int64)
	}
	if resp, body := h.doAs(t, sup, supCSRF, http.MethodPost, "/"+adminPath+"/api/v1/support/card/"+id+"/sessions", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("support sessions: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.doAs(t, sup, supCSRF, http.MethodPost, "/"+adminPath+"/api/v1/support/card/"+id+"/blocked", map[string]bool{"blocked": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("support block: %d %s", resp.StatusCode, body)
	}
	if u, _ := h.st.Q.GetUser(ctx, u.ID); u.Status != "disabled" {
		t.Fatalf("blocked status %q", u.Status)
	}

	// Tech: the nodes and the log; not the subscribers.
	tech, _ := h.asRole(t, "tech", password)
	if resp, body := h.doAs(t, tech, "", http.MethodGet, "/"+adminPath+"/api/v1/nodes", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("tech nodes: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.doAs(t, tech, "", http.MethodGet, "/"+adminPath+"/api/v1/audit", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("tech audit: %d", resp.StatusCode)
	}
	if resp, _ := h.doAs(t, tech, "", http.MethodGet, "/"+adminPath+"/api/v1/users", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tech users: %d", resp.StatusCode)
	}
	if resp, _ := h.doAs(t, tech, "", http.MethodGet, "/"+adminPath+"/api/v1/support/card?q="+id, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tech card: %d", resp.StatusCode)
	}

	// The last superadmin cannot be demoted; the support admin cannot name admins.
	if n, err := h.st.Q.CountAdminsWithRole(ctx, "superadmin"); err != nil || n != 1 {
		t.Fatalf("superadmins: %d %v", n, err)
	}
	me, _ := h.st.Q.GetAdminByUsername(ctx, "admin")
	if resp, body := h.do(http.MethodPut, "/"+adminPath+"/api/v1/admins/"+strconv.FormatInt(me.ID, 10), map[string]string{"role": "support"}, map[string]string{"X-CSRF-Token": h.csrf}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("the last superadmin: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.doAs(t, sup, "", http.MethodGet, "/"+adminPath+"/api/v1/admins", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("support admins: %d", resp.StatusCode)
	}

	// The panel still knows who it talks to: the card's next request after a reset works.
	if resp, _ := h.doAs(t, sup, supCSRF, http.MethodPost, "/"+adminPath+"/api/v1/support/card/"+id+"/blocked", map[string]bool{"blocked": false}); resp.StatusCode != http.StatusOK {
		t.Fatalf("unblock: %d", resp.StatusCode)
	}
	_ = httptest.NewServer
	_ = sql.ErrNoRows
}
