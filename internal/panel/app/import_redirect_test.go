package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

// The one-tap import hands an app this panel's own link, never an address the caller named:
// a link under the panel's domain must not vouch for somebody else's host, which would serve
// the app a profile of its own.
func TestImportHandsOnlyOwnLinks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(h.st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355, settings.KeyDeviceBinding: false} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	clock := func() time.Time { return h.now }
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	// Somebody else's host with a real token: the answer points at this panel.
	evil := url.QueryEscape("https://evil.example/" + u.SubToken)
	resp, _ := h.do(http.MethodGet, "/"+subPath+"/import?client=happ&sub_url="+evil, nil, nil)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("a token under another host: %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if strings.Contains(loc, "evil.example") || !strings.HasPrefix(loc, "happ://add/") || !strings.Contains(loc, u.SubToken) {
		t.Fatalf("the redirect vouched for another host: %q", loc)
	}
	// A token nobody has, and an app that takes no link: nothing to hand out.
	for _, query := range []string{
		"/" + subPath + "/import?client=happ&sub_url=" + url.QueryEscape("https://evil.example/AAAAAAAAAAAAAAAAAAAAAAAA"),
		"/" + subPath + "/import?client=nope&sub_url=" + url.QueryEscape("https://evil.example/"+u.SubToken),
	} {
		if resp, _ := h.do(http.MethodGet, query, nil, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d", query, resp.StatusCode)
		}
	}
}
