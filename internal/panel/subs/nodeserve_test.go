package subs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/storetest"
)

type subChanges struct{}

func (subChanges) PoliciesChanged() {}
func (subChanges) SlotsChanged()    {}

// A subscription the panel renders for a node carries every body an app may ask for, the
// headers with the traffic, and its expiry — the same rendering the panel serves itself.
func TestNodeRecordHasEveryFormat(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	// Seed gives the panel's own node an inbound of every default preset: something to put
	// in a profile.
	if err := domain.Seed(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	pool := domain.NewPool(st, clock)
	users := domain.NewUsers(st, pool, subChanges{}, clock)
	tariffs, err := st.Q.ListTariffs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u, err := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Brand: "Test VPN", Nodes: []Node{{ID: 1, Endpoint: Endpoint{Host: "203.0.113.7"}}}}
	h := NewHandler(st, func(context.Context) (Config, error) { return cfg, nil }, nil, clock, nil, false)

	rec, err := h.NodeRecord(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Path != u.SubToken || rec.Expires.IsZero() {
		t.Fatalf("the record: %+v", rec)
	}
	if rec.Expires.After(now.Add(SubCacheMaxAge + time.Second)) {
		t.Fatalf("a subscription without a term expires in %v", rec.Expires.Sub(now))
	}
	hd := rec.Headers
	if !strings.HasPrefix(hd["Subscription-Userinfo"], "upload=0; download=0;") || hd["Profile-Update-Interval"] != "1" {
		t.Fatalf("headers: %v", hd)
	}
	title, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(hd["Profile-Title"], "base64:"))
	if err != nil || !strings.Contains(string(title), "Test VPN") {
		t.Fatalf("the title: %q %v", title, err)
	}
	byFormat := map[string]nodeapi.SubBody{}
	for _, b := range rec.Bodies {
		byFormat[b.Format] = b
	}
	for _, f := range []string{nodeapi.SubFormatURI, nodeapi.SubFormatClash, nodeapi.SubFormatSingBox, nodeapi.SubFormatHTML} {
		if len(byFormat[f].Body) == 0 {
			t.Fatalf("no %s body: %v", f, byFormat)
		}
	}
	links, err := base64.StdEncoding.DecodeString(string(byFormat[nodeapi.SubFormatURI].Body))
	if err != nil || !strings.Contains(string(links), "vless://") || !strings.HasPrefix(byFormat[nodeapi.SubFormatURI].Type, "text/plain") {
		t.Fatalf("the links: %q %v", links, err)
	}
	if clash := string(byFormat[nodeapi.SubFormatClash].Body); !strings.Contains(clash, "proxies:") || !strings.Contains(byFormat[nodeapi.SubFormatClash].Name, ".yaml") {
		t.Fatalf("clash: %q", clash)
	}
	var sb map[string]any
	if err := json.Unmarshal(byFormat[nodeapi.SubFormatSingBox].Body, &sb); err != nil || sb["outbounds"] == nil {
		t.Fatalf("sing-box: %v", err)
	}
	if page := string(byFormat[nodeapi.SubFormatHTML].Body); !strings.Contains(page, "Test VPN") || !strings.HasPrefix(byFormat[nodeapi.SubFormatHTML].Type, "text/html") {
		t.Fatalf("the page: %q", page)
	}
}

// A subscription that cannot connect carries the notice in place of its servers, in the app
// formats and on the page — and it is still a record: the link keeps answering, and renewing
// works because the subscriber can read what happened.
func TestNodeRecordOfAnEndedSubscription(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := domain.Seed(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	pool := domain.NewPool(st, clock)
	users := domain.NewUsers(st, pool, subChanges{}, clock)
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, err := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	// The term ran out: a subscription that already ended.
	past := now.Add(-time.Hour)
	if u, err = users.Update(ctx, u.ID, domain.Patch{ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Brand: "Test VPN", InactiveServer: true, Nodes: []Node{{ID: 1, Endpoint: Endpoint{Host: "203.0.113.7"}}}}
	h := NewHandler(st, func(context.Context) (Config, error) { return cfg, nil }, nil, clock, nil, false)
	rec, err := h.NodeRecord(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	// Without an inbound the ended subscription still has a record: the notice, not a failure.
	if len(rec.Bodies) < 4 {
		t.Fatalf("the bodies: %v", len(rec.Bodies))
	}
	var page string
	for _, b := range rec.Bodies {
		if b.Format == nodeapi.SubFormatHTML {
			page = string(b.Body)
		}
	}
	if !strings.Contains(page, "закончилась") && !strings.Contains(page, "ended") {
		t.Fatalf("the page says nothing about the end: %q", page)
	}
	// An ended subscription's record lives the cache's time, so the notice stays readable.
	if !rec.Expires.After(now.Add(29 * 24 * time.Hour)) {
		t.Fatalf("an ended subscription expires at %v", rec.Expires)
	}
}
