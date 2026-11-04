package xray

import (
	"encoding/json"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/profile"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

// The "YouTube without ads" offer with its entry leaving through another node: the list
// gets an item of the entry, and the rules of every config that take nothing else end on
// it — which is what keeps YouTube on the entry's Russian address instead of the app's.
func TestExitConfig(t *testing.T) {
	p := profileOf(subtest.CoreVariants[0].Base())
	p.Exit = &profile.Exit{Address: "203.0.113.9", Port: 1080, Name: "RU"}
	raw, err := Render(p, model.GroupNames{Auto: "Auto"}, routing.All, model.Template{})
	if err != nil {
		t.Fatal(err)
	}
	var cfgs []map[string]any
	if err := json.Unmarshal(raw, &cfgs); err != nil {
		t.Fatal(err)
	}
	first := cfgs[0]
	if first["remarks"] != "RU" {
		t.Fatalf("the offer is not the first item: %v", first["remarks"])
	}
	ob, _ := first["outbounds"].([]any)
	if len(ob) == 0 || ob[0].(map[string]any)["protocol"] != "socks" {
		t.Fatalf("the offer's first outbound: %v", ob)
	}
	socks := ob[0].(map[string]any)["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)
	if socks["address"] != "203.0.113.9" || socks["port"] != float64(1080) {
		t.Fatalf("the entry: %v", socks)
	}
	rt := first["routing"].(map[string]any)
	rules := rt["rules"].([]any)
	last := rules[len(rules)-1].(map[string]any)
	if last["outboundTag"] != "exit" {
		t.Fatalf("the offer ends on %v", last)
	}
}

// No exit: the list is the main group and the servers, as before.
func TestNoExitConfig(t *testing.T) {
	p := profileOf(subtest.CoreVariants[0].Base())
	raw, err := Render(p, model.GroupNames{Auto: "Auto"}, routing.All, model.Template{})
	if err != nil {
		t.Fatal(err)
	}
	var cfgs []map[string]any
	if err := json.Unmarshal(raw, &cfgs); err != nil {
		t.Fatal(err)
	}
	for _, c := range cfgs {
		for _, o := range c["outbounds"].([]any) {
			if o.(map[string]any)["protocol"] == "socks" {
				t.Fatalf("an exit the offer has not: %v", c["remarks"])
			}
		}
	}
}
