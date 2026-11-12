package app

import (
	"encoding/json"
	"net/http"
	"testing"

	"mikan/internal/panel/domain"
)

// Removal of inactive subscriptions is off until the admin turns it on, and a week is the
// soonest it may reach. The list says who would go without deleting anyone.
func TestLifecycleRemoval(t *testing.T) {
	h := newHarness(t)
	if resp, raw := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, raw)
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1"
	type rules struct {
		Removal struct {
			On   bool `json:"on"`
			Days int  `json:"days"`
		} `json:"removal"`
	}
	var got rules
	resp, raw := h.do(http.MethodGet, api+"/lifecycle", nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &got) != nil || got.Removal.On || got.Removal.Days != 30 {
		t.Fatalf("default off, a month: %d %s", resp.StatusCode, raw)
	}
	resp, raw = h.do(http.MethodPut, api+"/lifecycle", map[string]any{"removal": map[string]any{"on": true, "days": 3}}, csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("three days: %d %s", resp.StatusCode, raw)
	}
	resp, raw = h.do(http.MethodPut, api+"/lifecycle", map[string]any{"removal": map[string]any{"on": true, "days": 14}}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &got) != nil || !got.Removal.On || got.Removal.Days != 14 {
		t.Fatalf("turned on: %d %s", resp.StatusCode, raw)
	}
	resp, raw = h.do(http.MethodGet, api+"/lifecycle/inactive?days=14", nil, nil)
	var list struct {
		On   bool `json:"on"`
		Days int  `json:"days"`
		Due  int  `json:"due"`
		Kept int  `json:"kept"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &list) != nil || !list.On || list.Days != 14 {
		t.Fatalf("who it reaches: %d %s", resp.StatusCode, raw)
	}
	// The saved rules are what housekeeping reads, and off is still the default of a panel
	// that never set them.
	loaded, err := domain.LoadLifecycle(t.Context(), h.p.Settings)
	if err != nil || !loaded.Removal.On || loaded.Removal.Days != 14 {
		t.Fatalf("stored %+v %v", loaded, err)
	}
}
