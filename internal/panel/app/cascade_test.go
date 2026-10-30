package app

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodetls"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

// Cascades over HTTP: an inbound leaves through another node, a node's relay goes on
// to a third; self, loops, unknown nodes and the relay's port are refused.
func TestCascadeOverHTTP(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, settings.New(h.st.Q), settings.KeyPanelPort, 21355); err != nil {
		t.Fatal(err)
	}
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	b, _, err := domain.AddNode(ctx, h.st, panel, domain.NodeInput{Name: "B", Host: "198.51.100.20", APIPort: 40000}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := domain.AddNode(ctx, h.st, panel, domain.NodeInput{Name: "C", Host: "198.51.100.30", APIPort: 40000}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1"
	ins, _ := h.st.Q.ListInbounds(ctx)
	inbound := api + "/inbounds/" + strconv.FormatInt(ins[0].ID, 10)
	id := func(n int64) string { return strconv.FormatInt(n, 10) }

	for name, c := range map[string]struct {
		body map[string]any
		code string
	}{
		"itself":      {map[string]any{"outbound": "node", "exit_node_id": 1}, "exit_self"},
		"no node":     {map[string]any{"outbound": "node", "exit_node_id": 99}, "exit_not_found"},
		"no exit set": {map[string]any{"outbound": "node"}, "exit_not_found"},
	} {
		if resp, body := h.do(http.MethodPatch, inbound, c.body, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), c.code) {
			t.Fatalf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	resp, body := h.do(http.MethodPatch, inbound, map[string]any{"outbound": "node", "exit_node_id": b.ID}, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"outbound":"node"`) || !strings.Contains(string(body), `"exit_node_id":`+id(b.ID)) {
		t.Fatalf("via B: %d %s", resp.StatusCode, body)
	}
	// B now has a relay with node 1 as its source.
	resp, body = h.do(http.MethodGet, api+"/nodes/"+id(b.ID)+"/cascade", nil, nil)
	var bv struct {
		Relay struct {
			Port    string `json:"port"`
			Sources []struct {
				NodeID int64 `json:"node_id"`
			} `json:"sources"`
			Outbound string `json:"outbound"`
		} `json:"relay"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &bv) != nil || bv.Relay.Port == "" || len(bv.Relay.Sources) != 1 || bv.Relay.Sources[0].NodeID != 1 || bv.Relay.Outbound != "direct" {
		t.Fatalf("B's relay: %d %s", resp.StatusCode, body)
	}
	if _, body := h.do(http.MethodGet, api+"/nodes/1/cascade", nil, nil); !strings.Contains(string(body), `"inbounds":["`+ins[0].Name+`"]`) {
		t.Fatalf("node 1 exits: %s", body)
	}
	// B → C, then C → B would loop.
	if resp, body := h.do(http.MethodPatch, api+"/nodes/"+id(b.ID)+"/cascade", map[string]any{"outbound": "node", "exit_node_id": c.ID}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("B → C: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, api+"/nodes/"+id(c.ID)+"/cascade", map[string]any{"outbound": "node", "exit_node_id": b.ID}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "exit_cycle") {
		t.Fatalf("loop: %d %s", resp.StatusCode, body)
	}
	// C → WARP: the panel will not save the route while C has no WARP — the state would
	// carry none, and other nodes' traffic would leave from C's own address.
	cWarp := api + "/nodes/" + id(c.ID) + "/warp"
	if resp, body := h.do(http.MethodPatch, api+"/nodes/"+id(c.ID)+"/cascade", map[string]any{"outbound": "warp"}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "warp_not_configured") {
		t.Fatalf("C → WARP without a WARP: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPost, cWarp+"/import", map[string]any{"config": warpConf}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("C's WARP: %d %s", resp.StatusCode, body)
	}
	// A WARP that is off is no WARP for this route either.
	if resp, body := h.do(http.MethodPatch, cWarp, map[string]any{"enabled": false}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("C's WARP off: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, api+"/nodes/"+id(c.ID)+"/cascade", map[string]any{"outbound": "warp"}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "warp_off") {
		t.Fatalf("C → WARP with WARP off: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, cWarp, map[string]any{"enabled": true}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("C's WARP on: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, api+"/nodes/"+id(c.ID)+"/cascade", map[string]any{"outbound": "warp"}, csrf); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"outbound":"warp"`) {
		t.Fatalf("C → WARP: %d %s", resp.StatusCode, body)
	}
	// The entry's card shows the real chain: exit B, then C, then WARP.
	resp, body = h.do(http.MethodGet, api+"/nodes/1/cascade", nil, nil)
	var view struct {
		Exits []struct {
			NodeID int64 `json:"node_id"`
			Chain  []struct {
				NodeID int64 `json:"node_id"`
			} `json:"chain"`
			End  string `json:"end"`
			Warp *struct {
				Configured bool `json:"configured"`
			} `json:"warp"`
		} `json:"exits"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &view) != nil {
		t.Fatalf("the entry's card: %d %s", resp.StatusCode, body)
	}
	var chainOK bool
	for _, e := range view.Exits {
		if e.NodeID == b.ID && e.End == "warp" && len(e.Chain) == 1 && e.Chain[0].NodeID == c.ID && e.Warp != nil && e.Warp.Configured {
			chainOK = true
		}
	}
	if !chainOK {
		t.Fatalf("the entry does not show B → C → WARP: %s", body)
	}
	// WARP is not taken away from other nodes' traffic: without it that traffic would leave
	// from C's own address.
	for name, do := range map[string]func() (*http.Response, []byte){
		"delete": func() (*http.Response, []byte) { return h.do(http.MethodDelete, cWarp, nil, csrf) },
		"disable": func() (*http.Response, []byte) {
			return h.do(http.MethodPatch, cWarp, map[string]any{"enabled": false}, csrf)
		},
	} {
		resp, body := do()
		if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "warp_in_use_cascade") {
			t.Fatalf("%s while the relay goes through WARP: %d %s", name, resp.StatusCode, body)
		}
	}
	// An inbound on B may not take the relay's port.
	bIns, _ := h.st.Q.ListInbounds(ctx)
	for _, in := range bIns {
		if in.NodeID == b.ID && domain.InboundNetwork(in) == "tcp" {
			if resp, body := h.do(http.MethodPatch, api+"/inbounds/"+id(in.ID), map[string]any{"port": bv.Relay.Port}, csrf); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "port_relay") {
				t.Fatalf("relay port: %d %s", resp.StatusCode, body)
			}
			break
		}
	}
	// Back to direct: the exit is gone.
	resp, body = h.do(http.MethodPatch, inbound, map[string]any{"outbound": "direct"}, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"outbound":"direct"`) || strings.Contains(string(body), "exit_node_id") {
		t.Fatalf("direct: %d %s", resp.StatusCode, body)
	}
}

// Two ways out through one exit: a protocol leaves by the exit's own way, another through
// its WARP. The choice is the inbound's, refused while the exit has no WARP, and the
// entry's card names both routes.
func TestInboundExitWarp(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	b, _, err := domain.AddNode(ctx, h.st, panel, domain.NodeInput{Name: "B", Host: "198.51.100.20", APIPort: 40000}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1"
	ins, _ := h.st.Q.ListInbounds(ctx)
	id := func(n int64) string { return strconv.FormatInt(n, 10) }
	plain, warp := api+"/inbounds/"+id(ins[0].ID), api+"/inbounds/"+id(ins[1].ID)

	// The exit has no WARP yet: the route is refused, not saved as one that would leave
	// from the exit's own address.
	if resp, body := h.do(http.MethodPatch, warp, map[string]any{"outbound": "node", "exit_node_id": b.ID, "exit_warp": true}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "warp_not_configured") || !strings.Contains(string(body), "body.exit_warp") {
		t.Fatalf("a route through WARP without one: %d %s", resp.StatusCode, body)
	}
	// The mark without an exit is no route at all.
	if resp, body := h.do(http.MethodPatch, warp, map[string]any{"outbound": "direct", "exit_warp": true}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "warp_route_needs_exit") {
		t.Fatalf("the mark without an exit: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPost, api+"/nodes/"+id(b.ID)+"/warp/import", map[string]any{"config": warpConf}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("B's WARP: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, warp, map[string]any{"outbound": "node", "exit_node_id": b.ID, "exit_warp": true}, csrf); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"exit_warp":true`) {
		t.Fatalf("the WARP route: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, plain, map[string]any{"outbound": "node", "exit_node_id": b.ID}, csrf); resp.StatusCode != http.StatusOK || strings.Contains(string(body), `"exit_warp":true`) {
		t.Fatalf("the plain route: %d %s", resp.StatusCode, body)
	}
	// The entry's card: the same exit twice, one route through its WARP and one not.
	resp, body := h.do(http.MethodGet, api+"/nodes/1/cascade", nil, nil)
	var view struct {
		Exits []struct {
			NodeID int64  `json:"node_id"`
			Via    string `json:"via"`
			End    string `json:"end"`
			Warp   *struct {
				Configured bool `json:"configured"`
			} `json:"warp"`
		} `json:"exits"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &view) != nil {
		t.Fatalf("the entry's card: %d %s", resp.StatusCode, body)
	}
	via := map[string][]string{}
	for _, e := range view.Exits {
		if e.NodeID != b.ID {
			continue
		}
		via[e.Via] = append(via[e.Via], e.End)
		if e.Via == "warp" && (e.End != "warp" || e.Warp == nil || !e.Warp.Configured) {
			t.Fatalf("a WARP route says where it ends: %+v", e)
		}
		if e.Via == "direct" && e.End != "direct" {
			t.Fatalf("a plain route ends directly: %+v", e)
		}
	}
	if len(via["warp"]) != 1 || len(via["direct"]) != 1 {
		t.Fatalf("the two routes of B: %v", via)
	}
	// Back to direct clears the mark with it.
	if resp, body := h.do(http.MethodPatch, warp, map[string]any{"outbound": "direct"}, csrf); resp.StatusCode != http.StatusOK || strings.Contains(string(body), "exit_warp") {
		t.Fatalf("back to direct: %d %s", resp.StatusCode, body)
	}
}
