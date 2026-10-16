package subs

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func decodeHapp(t *testing.T, link string) happProfile {
	t.Helper()
	payload, ok := strings.CutPrefix(link, "happ://routing/onadd/")
	if !ok || !ValidHappRouting(link) {
		t.Fatalf("not a routing link: %s", link)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	var p happProfile
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// A Clash profile's rules land in Happ's lists by their target, the first rule that takes
// a site deciding, as in mihomo; what Xray has no list for is left out.
func TestHappRoutingOf(t *testing.T) {
	p := decodeHapp(t, HappRoutingOf("VPN", []string{
		"IP-CIDR,203.0.113.7/32,DIRECT,no-resolve",
		"GEOIP,LAN,DIRECT,no-resolve",
		"DOMAIN-SUFFIX,sber.ru,DIRECT",
		"GEOSITE,category-ads-all,REJECT",
		"GEOSITE,youtube,📺 YouTube",
		"GEOSITE,youtube,REJECT", // a twin of the rule above: never reached
		"GEOIP,telegram,VPN,no-resolve",
		"GEOSITE,no-such-category,VPN",
		"PROCESS-NAME,Telegram.exe,VPN",
		"AND,((NETWORK,UDP),(DST-PORT,443)),REJECT",
		"RULE-SET,mikan-blocked,VPN",
		"GEOSITE,category-ru,DIRECT",
		"MATCH,DIRECT",
		"MATCH,REJECT",
	}))
	if p.GlobalProxy != "false" || p.Name != "VPN" || !strings.HasPrefix(p.GeositeURL, happRelease) {
		t.Fatalf("profile: %+v", p)
	}
	want := map[string][]string{
		"DirectSites": {"geosite:private", "domain:sber.ru", "geosite:category-ru"},
		"DirectIp":    {"203.0.113.7/32", "geoip:private"},
		"BlockSites":  {"geosite:category-ads-all"},
		"ProxySites":  {"geosite:youtube", "geosite:ru-blocked"},
		"ProxyIp":     {"geoip:telegram", "geoip:ru-blocked"},
	}
	got := map[string][]string{"DirectSites": p.DirectSites, "DirectIp": p.DirectIP, "BlockSites": p.BlockSites, "ProxySites": p.ProxySites, "ProxyIp": p.ProxyIP}
	for k, w := range want {
		if !slices.Equal(got[k], w) {
			t.Errorf("%s: %v, want %v", k, got[k], w)
		}
	}
	if len(p.BlockIP) != 0 {
		t.Errorf("BlockIp: %v", p.BlockIP)
	}
	if all := decodeHapp(t, HappRoutingOf("VPN", []string{"MATCH,VPN"})); all.GlobalProxy != "true" || !slices.Equal(all.DirectSites, []string{"geosite:private"}) {
		t.Fatalf("all through the proxy: %+v", all)
	}
}

// Every category the panel's own routing can send is in the pinned geodata: one that is
// not would fail Happ's whole routing.
func TestHappKnowsThePanelsCategories(t *testing.T) {
	names := happNames()
	for _, s := range Services {
		for _, m := range s.Rules {
			if m.Type == "GEOSITE" && !names[0][m.Value] || m.Type == "GEOIP" && !names[1][m.Value] {
				t.Errorf("%s: %s,%s", s.ID, m.Type, m.Value)
			}
		}
	}
	for list, x := range happLists {
		for _, v := range append(append([]string{}, x[0]...), x[1]...) {
			cat := strings.SplitN(v, ":", 2)
			if cat[0] == "geosite" && !names[0][cat[1]] || cat[0] == "geoip" && !names[1][cat[1]] {
				t.Errorf("%s: %s", list, v)
			}
		}
	}
	for _, c := range []string{"category-ru", "ru-blocked", "private"} {
		if !names[0][c] {
			t.Errorf("geosite %s", c)
		}
	}
}

// The rules come from the profile the settings make: the simple ones, or an own profile.
func TestProfileRules(t *testing.T) {
	cfg := Config{Brand: "Mikan", Direct: []string{"203.0.113.7"}, Routing: RoutingBlocked,
		Routes: Routes{Services: map[string]string{"youtube": TargetVPN, "ads": TargetBlock, "ai": NodeTarget(2)}},
		Nodes:  []Node{{ID: 1, Name: "🇳🇱 Нидерланды"}, {ID: 2, Name: "🇺🇸 США"}}}
	p := decodeHapp(t, happAutoRouting(cfg))
	if p.Name != "Mikan" || p.GlobalProxy != "false" || !slices.Contains(p.ProxySites, "geosite:youtube") || !slices.Contains(p.ProxySites, "geosite:category-ai-!cn") ||
		!slices.Contains(p.ProxySites, "geosite:ru-blocked") || !slices.Contains(p.BlockSites, "geosite:category-ads-all") || !slices.Contains(p.DirectIP, "203.0.113.7/32") {
		t.Fatalf("simple: %+v", p)
	}
	cfg.Template = "proxy-groups:\n  - {name: Net, type: select, include-all-proxies: true}\nrules:\n  - DOMAIN-SUFFIX,mine.example,DIRECT\n  - MATCH,Net\n"
	p = decodeHapp(t, happAutoRouting(cfg))
	if p.GlobalProxy != "true" || !slices.Contains(p.DirectSites, "domain:mine.example") || slices.Contains(p.ProxySites, "geosite:youtube") {
		t.Fatalf("own profile: %+v", p)
	}
}

// Settings → Routing reaches Happ too: its resolvers become the ones the admin chose,
// and an entry Happ cannot take (by name, or a system resolver) leaves the default.
func TestHappResolvers(t *testing.T) {
	cfg := Config{Brand: "VPN", Routes: Routes{DNS: DNS{
		Nameserver: []string{"system", "https://8.8.8.8/dns-query#Auto"},
		Default:    []string{"77.88.8.8"},
	}}}
	p := decodeHapp(t, happAutoRouting(cfg))
	if p.RemoteDNS != "8.8.8.8" || p.RemoteDNSIP != "8.8.8.8" || p.RemoteDNSDomain != "https://8.8.8.8/dns-query" || p.RemoteDNSType != "DoH" {
		t.Fatalf("remote: %+v", p)
	}
	if p.DomesticDNS != "77.88.8.8" || p.DomesticDNSDomain != "https://77.88.8.8/dns-query" {
		t.Fatalf("domestic: %+v", p)
	}
	// The Russian zone's own policy wins over the bootstrap list for the sites that go
	// direct, and a DoH server by name keeps the panel's default: Happ resolves by address.
	cfg.Routes.DNS.Default = []string{"1.1.1.1"}
	cfg.Routes.DNS.Policy = []DNSPolicy{{Match: "geosite:category-ru", Servers: []string{"https://dns.alidns.com/dns-query"}}}
	p = decodeHapp(t, happAutoRouting(cfg))
	if p.DomesticDNS != "77.88.8.8" || p.RemoteDNS != "8.8.8.8" {
		t.Fatalf("by name: %+v", p)
	}
	// A plain resolver, as the panel's presets write them, is raised to its DoH by address.
	if ns, ok := firstHappNS([]string{"77.88.8.8"}); !ok || ns.addr != "77.88.8.8" || ns.domain != "https://77.88.8.8/dns-query" || ns.ip != "77.88.8.8" {
		t.Fatalf("plain: %+v %v", ns, ok)
	}
	if _, ok := firstHappNS([]string{"system", "dns.google", "https://dns.google/dns-query"}); ok {
		t.Fatal("a resolver by name must not be taken")
	}
}
