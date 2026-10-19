package app

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// Where clients go, from the admin's side: off by default, the switch and its days saved
// together, what a subscription went to shown for the period asked, and everything the
// nodes summed gone in one call.
func TestTrafficDetailAPI(t *testing.T) {
	h := newHarness(t)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	path := "/" + adminPath + "/api/v1/traffic-detail"
	var view struct {
		Enabled bool `json:"enabled"`
		Days    int  `json:"days"`
	}
	resp, body := h.do(http.MethodGet, path, nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &view) != nil || view.Enabled || view.Days != domain.DefaultDetailDays {
		t.Fatalf("off by default, kept seven days: %d %s", resp.StatusCode, body)
	}

	resp, body = h.do(http.MethodPut, path, map[string]any{"enabled": true, "days": 30}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &view) != nil || !view.Enabled || view.Days != 30 {
		t.Fatalf("the switch saved with its days: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPut, path, map[string]any{"enabled": true, "days": 400}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("days past the bound: %d %s", resp.StatusCode, body)
	}

	// What the nodes summed for one subscription: the two days add up per site, busiest
	// first, and what no admin is looking at (another slot's user) stays out of it.
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	tariffs, err := h.st.Q.ListTariffs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return h.now }
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	day := domain.SiteDay(h.now)
	for _, s := range []struct {
		site      string
		up, down  int64
		dayOffset int64
	}{{"example.com", 10, 100, 0}, {"example.com", 5, 50, -86400}, {"googlevideo.com", 0, 200, 0}} {
		if err := h.st.Q.AddUserSite(ctx, db.AddUserSiteParams{Day: day + s.dayOffset, UserID: u.ID, Site: s.site, Up: s.up, Down: s.down}); err != nil {
			t.Fatal(err)
		}
	}
	var sites struct {
		Enabled  bool `json:"enabled"`
		KeepDays int  `json:"keep_days"`
		Items    []struct {
			Site string `json:"site"`
			Up   int64  `json:"up"`
			Down int64  `json:"down"`
		} `json:"items"`
	}
	resp, body = h.do(http.MethodGet, "/"+adminPath+"/api/v1/users/"+strconv.FormatInt(u.ID, 10)+"/sites?days=7", nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &sites) != nil || !sites.Enabled || sites.KeepDays != 30 {
		t.Fatalf("the sites of a subscription: %d %s", resp.StatusCode, body)
	}
	if len(sites.Items) != 2 || sites.Items[0].Site != "googlevideo.com" || sites.Items[1].Site != "example.com" ||
		sites.Items[1].Up != 15 || sites.Items[1].Down != 150 {
		t.Fatalf("busiest first, the days added up: %+v", sites.Items)
	}
	// A period that reaches past what was written: the older day is left out.
	resp, body = h.do(http.MethodGet, "/"+adminPath+"/api/v1/users/"+strconv.FormatInt(u.ID, 10)+"/sites?days=1", nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &sites) != nil || len(sites.Items) != 2 || sites.Items[1].Up != 10 {
		t.Fatalf("the period asked: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.do(http.MethodGet, "/"+adminPath+"/api/v1/users/999999/sites", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a subscription nobody has: %d", resp.StatusCode)
	}

	// The clear: everything gone, the number of rows said back, and the detail still on.
	var cleared struct {
		Rows int64 `json:"rows"`
	}
	resp, body = h.do(http.MethodDelete, path+"/sites", nil, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &cleared) != nil || cleared.Rows != 3 {
		t.Fatalf("cleared: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodGet, "/"+adminPath+"/api/v1/users/"+strconv.FormatInt(u.ID, 10)+"/sites?days=7", nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &sites) != nil || len(sites.Items) != 0 || !sites.Enabled {
		t.Fatalf("nothing kept, counting on: %d %s", resp.StatusCode, body)
	}
	// Clearing without a session of a browser is refused, and the audit names both calls.
	if resp, _ := h.do(http.MethodDelete, path+"/sites", nil, nil); resp.StatusCode == http.StatusOK {
		t.Fatalf("cleared without the CSRF token: %d", resp.StatusCode)
	}
	var details []string
	rows, err := h.st.DB.Query(`SELECT details FROM audit_log WHERE action LIKE 'traffic_detail.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		details = append(details, d)
	}
	if len(details) != 2 || !strings.Contains(details[0], `"days":30`) || !strings.Contains(details[1], `"rows":3`) {
		t.Fatalf("the audit log: %v", details)
	}
	if slices.Contains(details, "") {
		t.Fatalf("empty audit entries: %v", details)
	}
}
