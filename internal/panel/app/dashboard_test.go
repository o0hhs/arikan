package app

import (
	"encoding/json"
	"net/http"
	"testing"

	"mikan/internal/panel/dashboard"
)

// The dashboard's settings from the admin's side: every card and the compact date by
// default, a card or a date this panel does not know refused, the rest saved whole, and a
// card the request said nothing about left as it was.
func TestDashboardAPI(t *testing.T) {
	h := newHarness(t)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	path := "/" + adminPath + "/api/v1/dashboard"
	type layout struct {
		Cards   map[string]bool `json:"cards"`
		Date    string          `json:"date"`
		Seconds bool            `json:"seconds"`
	}
	var v layout
	resp, body := h.do(http.MethodGet, path, nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || v.Date != dashboard.DefaultDate || !v.Seconds {
		t.Fatalf("the dashboard as it always was: %d %s", resp.StatusCode, body)
	}
	for _, card := range dashboard.Cards {
		if !v.Cards[card] {
			t.Fatalf("card %q is off by default: %s", card, body)
		}
	}

	resp, body = h.do(http.MethodPut, path, map[string]any{"cards": map[string]bool{"kpis": false, "traffic": true}, "date": "large", "seconds": false}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || v.Cards["kpis"] || !v.Cards["traffic"] || v.Date != "large" || v.Seconds {
		t.Fatalf("saved whole: %d %s", resp.StatusCode, body)
	}
	// A card nobody named is shown: a card a later version adds appears without the admin
	// setting the dashboard up again.
	for _, card := range []string{"attention", "top", "server", "nodes"} {
		if !v.Cards[card] {
			t.Fatalf("card %q was switched off by saying nothing about it: %s", card, body)
		}
	}
	if resp, body := h.do(http.MethodGet, path, nil, nil); json.Unmarshal(body, &v) != nil || v.Cards["kpis"] || v.Date != "large" {
		t.Fatalf("kept: %d %s", resp.StatusCode, body)
	}

	resp, body = h.do(http.MethodPut, path, map[string]any{"cards": map[string]bool{"nonsense": true}, "date": "large", "seconds": true}, csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !json.Valid(body) {
		t.Fatalf("a card this panel does not have: %d %s", resp.StatusCode, body)
	}
	var errs struct {
		Errors []struct {
			Location string `json:"location"`
			Message  string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &errs) != nil || len(errs.Errors) != 1 || errs.Errors[0].Location != "body.cards.nonsense" {
		t.Fatalf("the refused card is named: %s", body)
	}
	if resp, body := h.do(http.MethodPut, path, map[string]any{"cards": map[string]bool{}, "date": "tomorrow", "seconds": true}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a date this panel does not have: %d %s", resp.StatusCode, body)
	}
	// Nothing of the refused calls was stored.
	if resp, body := h.do(http.MethodGet, path, nil, nil); json.Unmarshal(body, &v) != nil || v.Cards["kpis"] || v.Date != "large" || v.Seconds {
		t.Fatalf("a refused call changed the dashboard: %d %s", resp.StatusCode, body)
	}
	var n int
	if err := h.st.DB.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'dashboard.update'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the audit log: %d %v", n, err)
	}
}
