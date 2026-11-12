package subs

import (
	"testing"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
)

// Every app the panel knows, and the format it is served. ?format= wins over the
// User-Agent. The node's pick is the same one: a node that answers the link while the
// panel is down must not hand Hiddify a Clash profile or an old sing-box a config it
// cannot load.
func TestAppFormats(t *testing.T) {
	base := mixedProfile()
	cases := []struct {
		ua, accept, query, format string
	}{
		{"clash-verge/v2.2.3", "", "", "clash"},
		{"mihomo/1.19.31", "", "", "clash"},
		{"FlClash/v0.8.80 clash-meta", "", "", "clash"},
		{"Koala-Clash/1.4.0", "", "", "clash"},
		{"kabosu/1.0.0", "", "", "clash"},
		{"Prizrak-Box/1.4.2 (windows)", "", "", "clash"},
		{"Flowvy/0.9 mihomo/1.19.31", "", "", "clash"},
		{"Murge/2.0", "", "", "clash"},
		{"Rabbit/1.1 (macos)", "", "", "clash"},
		{"Stash/3.1.1 Clash/1.9.0", "", "", "clash"},
		{"Happ/3.4.1", "", "", "uri"},
		{"v2rayNG/1.10.2", "", "", "uri"},
		{"v2RayTun/Android", "", "", "uri"},
		{"INCY/2.0.8", "", "", "uri"},
		{"v2rayN/6.31", "", "", "uri"},
		{"HiddifyNext/2.5.7 (android) like ClashMeta v2ray sing-box", "", "", "uri"},
		{"HiddifyNext/4.1.1 (android) like ClashMeta v2ray sing-box", "", "", "uri"},
		{"Karing/1.2.26 platform/tvos sing-box 1.14.0", "", "", "uri"},
		{"NekoBox/1.3.7", "", "", "uri"},
		{"sing-box 1.11.0", "", "", "uri"},
		{"SFA (sing-box 1.14.2; language en_US)", "", "", "singbox"},
		{"SFM (sing-box 1.12.20; language ru_RU)", "", "", "singbox"},
		{"SFI (sing-box 1.11.4; language en_US)", "", "", "uri"},
		{"SFA/1.12.4", "", "", "singbox"},
		{"SFA/1.10.0", "", "", "uri"},
		{"SFT/1.13.0", "", "", "singbox"},
		{"Streisand/1.6", "", "", "uri"},
		{"Shadowrocket/2.2.60", "", "", "uri"},
		{"foxray/1.0", "text/html", "", "uri"},
		{"curl/8.9", "", "", "uri"},
		{"Mozilla/5.0 (iPhone)", "text/html,application/xhtml+xml", "", "html"},
		{"Happ/3.4.1", "text/html", "", "uri"},
		{"Happ/3", "", "clash", "clash"},
		{"Happ/3", "", "xray", "xray"},
		{"INCY/2.1.0", "", "json", "xray"},
		{"INCY/2.1.0", "", "singbox", "singbox"},
		{"clash-verge/v2", "", " uri ", "uri"},
		{"", "Text/HTML", "", "html"},
		{"", "", "", "uri"},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		name := c.ua
		if name == "" {
			name = "empty"
		}
		if c.query != "" {
			name += " ?" + c.query
		}
		t.Run(name, func(t *testing.T) {
			if got := Format(c.ua, c.accept, c.query); got != c.format {
				t.Fatalf("Format = %s, want %s", got, c.format)
			}
			if got := nodeapi.SubFormat(c.query, c.ua, c.accept); got != c.format {
				t.Fatalf("the node would serve %s, the panel %s", got, c.format)
			}
			if c.format == "html" || c.format == "turn" || seen[c.format+"/"+c.ua] {
				return
			}
			seen[c.format+"/"+c.ua] = true
			p := base
			p.App = DetectApp(c.ua)
			p.Inbounds = forApp(base.Inbounds, p.App, domain.StateActive)
			assertSubscription(t, p)
		})
	}
}
