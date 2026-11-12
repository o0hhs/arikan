package subs

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// What every rendered subscription has to keep, whichever format an app asks for.
// The INCY autoselect bug shipped a body in which every server had collapsed into the
// first entry: these checks are the ones that would have failed on it.

func TestSubscriptionInvariants(t *testing.T) {
	t.Run("mixed", func(t *testing.T) {
		p := mixedProfile()
		assertSubscription(t, p)
		// Hysteria2 is in the JSON. TUIC and AnyTLS are not, and that does not refuse the
		// config: the autoselect entry and the servers Xray can run are still served.
		raw, err := XrayConfigs(p, Groups{}, RoutingRUDirect)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"protocol":"hysteria"`) {
			t.Fatal("Hysteria2 is missing from the Xray configs")
		}
		if strings.Contains(string(raw), `"protocol":"tuic"`) {
			t.Fatal("TUIC has no Xray outbound and must stay out of the configs")
		}
	})

	t.Run("names", func(t *testing.T) {
		p := namedProfile()
		ps := mustBuild(t, p)
		got := map[string]bool{}
		for _, x := range ps {
			got[x.name] = true
		}
		for _, name := range []string{"🇳🇱 Нидерланды", "😀 сервер", strings.Repeat("я", 300), "true", "null", "yes", `say "hi"`, "a: b", "DIRECT 2", "PROXY 2"} {
			if !got[name] {
				t.Errorf("missing %q in %v", name, got)
			}
		}
		if got["DIRECT"] || got["PROXY"] || got[""] {
			t.Fatalf("a blank or reserved name reached the profile: %v", got)
		}
		assertSubscription(t, p)
	})

	t.Run("ports and sni", func(t *testing.T) {
		p := portProfile()
		ps := mustBuild(t, p)
		byPort := map[string]bool{}
		for _, x := range ps {
			u, err := url.Parse(x.uri)
			if err != nil {
				t.Fatal(err)
			}
			byPort[u.Port()] = true
			if strings.ContainsAny(x.uri, "\n\r") || strings.Contains(x.name, "\n") {
				t.Fatalf("a broken link: %q", x.uri)
			}
		}
		if !byPort["1"] || !byPort["65535"] || !byPort["20000"] {
			t.Fatalf("ports: %v", byPort)
		}
		if byPort["0"] || byPort["99999"] {
			t.Fatalf("a refused port was served: %v", byPort)
		}
		// The hopping range is the hysteria link's mport, and the odd SNI is ASCII.
		var hy2 string
		for _, x := range ps {
			if strings.HasPrefix(x.uri, "hysteria2://") {
				hy2 = x.uri
			}
		}
		u, err := url.Parse(hy2)
		if err != nil {
			t.Fatal(err)
		}
		if u.Query().Get("mport") != "20000-20010" {
			t.Fatalf("mport: %q", hy2)
		}
		if sni := u.Query().Get("sni"); sni != "xn--e1afmkfd.xn--p1ai" {
			t.Fatalf("sni: %q", sni)
		}
		assertSubscription(t, p)
	})

	t.Run("bad values are left out", func(t *testing.T) {
		p := mixedProfile()
		badHost := vlessIn(50, 9, "gone", "8443", "SHOULD-NOT-APPEAR")
		p.Inbounds = append(p.Inbounds, badHost)
		p.Nodes = append(p.Nodes, Node{ID: 9, Endpoint: Endpoint{Host: "not a host"}})
		p.Direct = append(p.Direct, "bad,host", "ok.example.com", "has\nnewline")
		var skipped []string
		p.Skip = func(in db.Inbound, err error) { skipped = append(skipped, in.Name) }
		assertSubscription(t, p)
		if len(skipped) == 0 || skipped[0] != "gone" {
			t.Fatalf("skipped: %v", skipped)
		}
		raw, err := Mihomo(p, Groups{}, RoutingAll)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "SHOULD-NOT-APPEAR") || strings.Contains(string(raw), "bad,host") || strings.Contains(string(raw), "has\nnewline,") {
			t.Fatalf("a bad value reached the profile:\n%s", raw)
		}
		if !strings.Contains(string(raw), "DOMAIN,ok.example.com,DIRECT") {
			t.Fatal("the good direct host was dropped")
		}
	})

	t.Run("disabled", func(t *testing.T) {
		p := mixedProfile()
		off := vlessIn(60, 1, "off", "9443", "DISABLED-MARKER")
		off.Enabled = 0
		p.Inbounds = append(p.Inbounds, off)
		assertSubscription(t, p)
		raw, err := URIs(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, "DISABLED-MARKER") || strings.Contains(raw, ":9443") {
			t.Fatalf("a disabled server was served:\n%s", raw)
		}
	})

	t.Run("percent host", func(t *testing.T) {
		// "%" starts a percent-escape, so a link with it as the address does not parse
		// and would fail every app. The server is left out; the profile does not error.
		p := Profile{
			Slot:     goldenSlot,
			Inbounds: []db.Inbound{vlessIn(1, 1, "v", "443", "v"), hy2In(2, 1, "h", "443", "h")},
			Nodes:    []Node{{ID: 1, Endpoint: Endpoint{Host: "%"}}},
		}
		if _, err := URIs(p); !errors.Is(err, ErrNoProxies) {
			t.Fatalf("links: %v", err)
		}
		if _, err := Mihomo(p, Groups{}, RoutingAll); !errors.Is(err, ErrNoProxies) {
			t.Fatalf("clash: %v", err)
		}
	})

	t.Run("ipv6 and idn", func(t *testing.T) {
		in := vlessIn(1, 1, "vless", "443", "сервер")
		p := Profile{
			Slot: goldenSlot, Inbounds: []db.Inbound{in},
			Nodes: []Node{{ID: 1, Name: "🇩🇪", Endpoint: Endpoint{Host: "2001:db8::1"}}},
		}
		assertSubscription(t, p)
		links, err := URIs(p)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(links)
		if err != nil {
			t.Fatal(err)
		}
		if u.Hostname() != "2001:db8::1" || u.Fragment != "сервер" {
			t.Fatalf("link: %q", links)
		}
		idn := hy2In(2, 2, "hy2", "443", "Hysteria2")
		p.Inbounds = append(p.Inbounds, idn)
		p.Nodes = append(p.Nodes, Node{ID: 2, Endpoint: Endpoint{Host: "пример.рф", SNI: "пример.рф"}})
		assertSubscription(t, p)
		links, err = URIs(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(links, "xn--e1afmkfd.xn--p1ai") {
			t.Fatalf("the domain was not written in ASCII:\n%s", links)
		}
	})
}

// Hysteria2-only is still a full config: the autoselect entry and the server, both JSON.
// Clash and sing-box keep the server too.
func TestHysteria2Only(t *testing.T) {
	p := Profile{
		Slot:     goldenSlot,
		Inbounds: []db.Inbound{hy2In(1, 1, "hysteria2", "443", "Hysteria2")},
		Nodes:    []Node{{ID: 1, Endpoint: Endpoint{Host: "203.0.113.7", SNI: "node.example.com"}}},
	}
	raw, err := XrayConfigs(p, Groups{}, RoutingRUDirect)
	if err != nil || !strings.Contains(string(raw), `"protocol":"hysteria"`) || !strings.Contains(string(raw), `"remarks":"Авто"`) {
		t.Fatalf("xray: %v %s", err, raw)
	}
	links, err := URIs(p)
	if err != nil || !strings.HasPrefix(links, "hysteria2://") || strings.Contains(links, "\n") {
		t.Fatalf("links: %q %v", links, err)
	}
	raw, err = Mihomo(p, Groups{}, RoutingAll)
	if err != nil || !strings.Contains(string(raw), "hysteria2") {
		t.Fatalf("clash: %v\n%s", err, raw)
	}
	sb, err := SingBox(t.Context(), p, Groups{}, RoutingAll, "")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(sb, &doc); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(sb) {
		t.Fatal("sing-box is not JSON")
	}
	found := false
	for _, o := range anyList(doc["outbounds"]) {
		if str(anyMap(o)["type"]) == "hysteria2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sing-box dropped the only server: %s", sb)
	}
}

// The switch gives Happ and INCY the full config in place of the links they ask for.
// An explicit ?format=uri stays the list; ?format=xray is the configs even with the
// switch off. On, the array keeps every Xray server and the automatic entries, each
// with its own name.
func TestINCYAutoselectToggle(t *testing.T) {
	p := mixedProfile()
	cases := []struct {
		on            bool
		format, query string
		ua            string
		full          bool
	}{
		{true, "uri", "", "INCY/2.1.0", true},
		{true, "uri", "", "incy/1", true},
		{true, "uri", "", "Happ/3.4.1", true},
		{true, "uri", "uri", "INCY/2.1.0", false},
		{true, "uri", "uri", "Happ/3.4.1", false},
		{false, "xray", "xray", "v2rayNG/1.10.2", true},
		{false, "uri", "", "INCY/2.1.0", false},
		{true, "clash", "", "INCY/2.1.0", false},
		{true, "singbox", "", "INCY/2.1.0", false},
		{true, "uri", "", "v2RayTun/Android", false},
		{true, "html", "", "INCY/2.1.0", false},
	}
	for _, c := range cases {
		if got := xrayFullConfig(c.on, c.format, c.query, c.ua); got != c.full {
			t.Errorf("on=%v format=%s query=%s ua=%s: %v, want %v", c.on, c.format, c.query, c.ua, got, c.full)
		}
	}
	raw, err := XrayConfigs(xrayOnlyProfile(t), Groups{}, RoutingAll)
	if err != nil {
		t.Fatal(err)
	}
	cfgs := decodeXray(t, raw)
	remarks := map[string]bool{}
	for _, c := range cfgs {
		if c.Remarks == "" || remarks[c.Remarks] {
			t.Fatalf("remarks %q would collapse the list", c.Remarks)
		}
		remarks[c.Remarks] = true
		if str(c.Outbounds[0]["tag"]) != c.Remarks {
			t.Fatalf("remarks %q, tag %q", c.Remarks, c.Outbounds[0]["tag"])
		}
	}
	if !strings.Contains(string(raw), `"type":"leastPing"`) && !strings.Contains(string(raw), `"type": "leastPing"`) {
		t.Fatalf("no leastPing balancer: %s", raw)
	}
	links, err := URIs(p)
	if err != nil || !strings.Contains(links, "hysteria2://") || !strings.Contains(links, "vless://") {
		t.Fatalf("the link list with the switch off: %v\n%s", err, links)
	}
}

func mixedProfile() Profile {
	return Profile{
		Slot: goldenSlot,
		Inbounds: []db.Inbound{
			vlessIn(1, 1, "vision", "8443", "VLESS Vision"),
			xhttpIn(2, 1, "xhttp", "443", "VLESS XHTTP"),
			trojanIn(3, 1, "trojan", "2087", "Trojan"),
			hy2In(4, 1, "hysteria2", "2443", "Hysteria2"),
			tuicIn(5, 1, "tuic", "8444", "TUIC"),
			anytlsIn(6, 1, "anytls", "2083", "AnyTLS"),
			ssIn(7, 1, "ss", "6443", "Shadowsocks"),
		},
		Nodes: []Node{{ID: 1, Endpoint: Endpoint{Host: "203.0.113.7", SNI: "node.example.com"}}},
	}
}

func namedProfile() Profile {
	names := []string{
		"🇳🇱 Нидерланды", "😀 сервер", strings.Repeat("я", 300),
		"true", "null", "yes", `say "hi"`, "a: b", "DIRECT", "PROXY", "", "   ",
	}
	p := Profile{Slot: goldenSlot, Nodes: []Node{{ID: 1, Endpoint: Endpoint{Host: "203.0.113.7"}}}}
	for i, name := range names {
		p.Inbounds = append(p.Inbounds, vlessIn(int64(i+1), 1, fmt.Sprintf("v%d", i), fmt.Sprintf("%d", 2000+i), name))
	}
	return p
}

func portProfile() Profile {
	return Profile{
		Slot: goldenSlot,
		Inbounds: []db.Inbound{
			vlessIn(1, 1, "low", "1", "low"),
			vlessIn(2, 1, "high", "65535", "high"),
			vlessIn(3, 1, "zero", "0", "zero"),
			vlessIn(4, 1, "huge", "99999", "huge"),
			vlessIn(5, 1, "word", "abc", "word"),
			hy2In(6, 1, "hop", "20000-20010", "hop"),
		},
		Nodes: []Node{{ID: 1, Endpoint: Endpoint{Host: "203.0.113.7", SNI: "пример.рф"}}},
	}
}

func vlessIn(id, node int64, name, port, display string) db.Inbound {
	in := goldenInbound(id, node, "vless_reality_vision", name, port, proto.Template{
		"type": "vless", "mikan": map[string]any{"flow": "xtls-rprx-vision"}, "reality-config": reality("www.example.com"),
	})
	in.DisplayName = display
	return in
}

func xhttpIn(id, node int64, name, port, display string) db.Inbound {
	in := goldenInbound(id, node, "vless_reality_xhttp", name, port, proto.Template{
		"type": "vless", "xhttp-config": map[string]any{"path": "/golden-path", "mode": "stream-one"}, "reality-config": reality("www.example.com"),
	})
	in.DisplayName = display
	return in
}

func trojanIn(id, node int64, name, port, display string) db.Inbound {
	in := goldenInbound(id, node, "trojan_reality", name, port, proto.Template{"type": "trojan", "reality-config": reality("www.example.com")})
	in.DisplayName = display
	return in
}

func hy2In(id, node int64, name, port, display string) db.Inbound {
	in := goldenInbound(id, node, "hysteria2", name, port, proto.Template{"type": "hysteria2", "alpn": []any{"h3"}, "obfs": proto.ObfsSalamander, "obfs-password": "golden-obfs"})
	in.DisplayName = display
	return in
}

func tuicIn(id, node int64, name, port, display string) db.Inbound {
	in := goldenInbound(id, node, "tuic_v5", name, port, proto.Template{"type": "tuic", "alpn": []any{"h3"}, "congestion-controller": "bbr"})
	in.DisplayName = display
	return in
}

func anytlsIn(id, node int64, name, port, display string) db.Inbound {
	in := goldenInbound(id, node, "anytls", name, port, proto.Template{"type": "anytls"})
	in.DisplayName = display
	return in
}

func ssIn(id, node int64, name, port, display string) db.Inbound {
	key := base64.StdEncoding.EncodeToString([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	in := goldenInbound(id, node, "shadowsocks_2022", name, port, proto.Template{
		"type": "shadowsocks", "cipher": "2022-blake3-aes-128-gcm", "password": key,
	})
	in.DisplayName = display
	return in
}

func mustBuild(t *testing.T, p Profile) []proxy {
	t.Helper()
	ps, err := build(p)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

// assertSubscription checks every format of p: each server the format can carry appears
// once, names are unique and non-empty, and the body parses as what it claims to be.
func assertSubscription(t *testing.T, p Profile) {
	t.Helper()
	ps := mustBuild(t, p)
	seen := map[string]bool{}
	var withURI int
	for _, x := range ps {
		if x.name == "" || strings.TrimSpace(x.name) == "" || seen[strings.ToLower(x.name)] {
			t.Fatalf("name %q", x.name)
		}
		seen[strings.ToLower(x.name)] = true
		if reservedName(x.name) {
			t.Fatalf("reserved name %q", x.name)
		}
		if x.uri == "" {
			continue
		}
		withURI++
		u, err := url.Parse(x.uri)
		if err != nil || u.Hostname() == "" || u.Port() == "" {
			t.Fatalf("link %q: %v", x.uri, err)
		}
		if u.Fragment != x.name {
			t.Fatalf("fragment %q, name %q", u.Fragment, x.name)
		}
		if out, ok := xrayOutbound(x.uri); ok {
			if got := outboundSig(out); got.host != u.Hostname() || got.port != u.Port() {
				t.Fatalf("xray outbound %v of %q", got, x.uri)
			}
		}
	}

	links, err := URIs(p)
	if withURI == 0 {
		if !errors.Is(err, ErrNoProxies) {
			t.Fatalf("links: %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	} else if n := len(strings.Split(links, "\n")); n != withURI {
		t.Fatalf("%d links, want %d\n%s", n, withURI, links)
	}

	raw, err := Mihomo(p, Groups{}.WithDefaults("en"), RoutingRUDirect)
	if len(ps) == 0 {
		if !errors.Is(err, ErrNoProxies) {
			t.Fatalf("clash: %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	} else {
		assertClash(t, raw, ps)
	}

	sb, err := SingBox(t.Context(), p, Groups{}, RoutingRUDirect, "")
	var wantSB int
	for _, x := range ps {
		if _, ok := singboxOutbound(x.yaml); ok {
			wantSB++
		}
	}
	if wantSB == 0 {
		if !errors.Is(err, ErrNoProxies) {
			t.Fatalf("sing-box: %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	} else {
		assertSingBox(t, sb, ps)
	}

	xr, err := XrayConfigs(p, Groups{}, RoutingRUDirect)
	var wantX int
	for _, x := range ps {
		if _, ok := xrayOutbound(x.uri); ok {
			wantX++
		}
	}
	if wantX == 0 {
		if !errors.Is(err, ErrNoProxies) {
			t.Fatalf("xray: %v", err)
		}
		return
	}
	// A profile with servers Xray cannot run is served as the link list (ErrXrayPartial):
	// nothing is checked of the config that is not made.
	if errors.Is(err, ErrXrayPartial) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	assertXray(t, xr, wantX)
}

func assertClash(t *testing.T, raw []byte, ps []proxy) {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("clash yaml: %v\n%s", err, raw)
	}
	proxies, _ := doc["proxies"].([]any)
	if len(proxies) != len(ps) {
		t.Fatalf("%d proxies, want %d", len(proxies), len(ps))
	}
	seen := map[string]bool{}
	for i, item := range proxies {
		m, _ := item.(map[string]any)
		name, ok := m["name"].(string)
		if !ok || name != ps[i].name {
			t.Fatalf("proxy %d: %#v, want %q", i, m["name"], ps[i].name)
		}
		if seen[name] {
			t.Fatalf("duplicate %q", name)
		}
		seen[name] = true
		if _, ok := m["port"].(int); !ok {
			t.Fatalf("port of %q: %#v", name, m["port"])
		}
	}
	groups, _ := doc["proxy-groups"].([]any)
	if len(groups) == 0 {
		t.Fatal("no proxy groups")
	}
}

func assertSingBox(t *testing.T, raw []byte, ps []proxy) {
	t.Helper()
	if !json.Valid(raw) {
		t.Fatalf("not JSON: %s", raw)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, x := range ps {
		if _, ok := singboxOutbound(x.yaml); ok {
			want[x.name] = true
		}
	}
	got := map[string]bool{}
	for _, o := range anyList(doc["outbounds"]) {
		m := anyMap(o)
		typ := str(m["type"])
		switch typ {
		case "vless", "trojan", "hysteria2", "tuic", "anytls", "shadowsocks", "socks":
			tag := str(m["tag"])
			if tag == "" || got[tag] {
				t.Fatalf("tag %q", tag)
			}
			got[tag] = true
			if !want[tag] {
				t.Fatalf("unexpected %q", tag)
			}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("sing-box tags %v, want %v", got, want)
	}
}

type xrayDoc struct {
	Remarks   string           `json:"remarks"`
	Inbounds  []map[string]any `json:"inbounds"`
	Outbounds []map[string]any `json:"outbounds"`
	Routing   struct {
		DomainStrategy string           `json:"domainStrategy"`
		Balancers      []map[string]any `json:"balancers"`
		Rules          []map[string]any `json:"rules"`
	} `json:"routing"`
	Observatory map[string]any `json:"observatory"`
}

func decodeXray(t *testing.T, raw []byte) []xrayDoc {
	t.Helper()
	if !json.Valid(raw) {
		t.Fatalf("not JSON: %s", raw)
	}
	var cfgs []xrayDoc
	if err := json.Unmarshal(raw, &cfgs); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	return cfgs
}

func xrayProxyProtocols(c xrayDoc) []string {
	var out []string
	for _, o := range c.Outbounds {
		if p := str(o["protocol"]); p != "" && p != "freedom" && p != "blackhole" {
			out = append(out, p)
		}
	}
	return out
}

func assertXray(t *testing.T, raw []byte, servers int) {
	t.Helper()
	cfgs := decodeXray(t, raw)
	// The auto group, then one config per country when there are several nodes, then one per server.
	if len(cfgs) < servers+1 || len(cfgs) > servers*2+1 {
		t.Fatalf("%d configs, want %d servers, the auto entry and at most one country each", len(cfgs), servers)
	}
	remarks := map[string]bool{}
	singles := 0
	for i, c := range cfgs {
		if c.Remarks == "" || remarks[c.Remarks] {
			t.Fatalf("remarks %q", c.Remarks)
		}
		remarks[c.Remarks] = true
		if len(c.Inbounds) != 1 || str(c.Inbounds[0]["protocol"]) != "socks" || str(c.Inbounds[0]["listen"]) != "127.0.0.1" {
			t.Fatalf("inbounds: %v", c.Inbounds)
		}
		if len(c.Outbounds) < 3 {
			t.Fatalf("outbounds: %v", c.Outbounds)
		}
		if str(c.Outbounds[0]["tag"]) != c.Remarks {
			t.Fatalf("tag %q, remarks %q", c.Outbounds[0]["tag"], c.Remarks)
		}
		tags := map[string]bool{}
		for _, o := range c.Outbounds {
			tag := str(o["tag"])
			if tag == "" || tags[tag] {
				t.Fatalf("tag %q", tag)
			}
			tags[tag] = true
			p := str(o["protocol"])
			switch p {
			case "vless", "trojan", "shadowsocks", "hysteria", "freedom", "blackhole":
			default:
				t.Fatalf("protocol %q", p)
			}
		}
		if len(c.Routing.Rules) == 0 || c.Routing.DomainStrategy == "" {
			t.Fatalf("routing: %+v", c.Routing)
		}
		if len(c.Routing.Balancers) > 0 {
			if c.Observatory["probeUrl"] == "" {
				t.Fatalf("a balancer config has no observatory: %q", c.Remarks)
			}
			b := c.Routing.Balancers[0]
			if str(anyMap(b["strategy"])["type"]) != "leastPing" {
				t.Fatalf("balancer: %v", b)
			}
			if i == 0 {
				if n := len(xrayProxyProtocols(c)); n != servers {
					t.Fatalf("the auto config carries %d servers, want %d", n, servers)
				}
			}
			continue
		}
		singles++
		if n := len(xrayProxyProtocols(c)); n != 1 {
			t.Fatalf("a server config carries %d proxies", n)
		}
	}
	if singles != servers {
		t.Fatalf("%d server configs, want %d", singles, servers)
	}
}

type endpointSig struct{ host, port string }

func outboundSig(o map[string]any) endpointSig {
	s := anyMap(o["settings"])
	switch str(o["protocol"]) {
	case "vless":
		list := anyList(s["vnext"])
		if len(list) == 0 {
			return endpointSig{}
		}
		m := anyMap(list[0])
		return endpointSig{str(m["address"]), str(m["port"])}
	case "trojan", "shadowsocks":
		list := anyList(s["servers"])
		if len(list) == 0 {
			return endpointSig{}
		}
		m := anyMap(list[0])
		return endpointSig{str(m["address"]), str(m["port"])}
	case "hysteria":
		return endpointSig{str(s["address"]), str(s["port"])}
	}
	return endpointSig{}
}
