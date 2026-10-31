package subs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// xcConfig is a full Xray config as INCY reads it: the fields this package writes.
type xcConfig struct {
	Remarks   string           `json:"remarks"`
	Log       map[string]any   `json:"log"`
	DNS       map[string]any   `json:"dns"`
	Inbounds  []map[string]any `json:"inbounds"`
	Outbounds []map[string]any `json:"outbounds"`
	Routing   struct {
		DomainStrategy string           `json:"domainStrategy"`
		Balancers      []map[string]any `json:"balancers"`
		Rules          []map[string]any `json:"rules"`
	} `json:"routing"`
	Observatory map[string]any `json:"observatory"`
	Stats       map[string]any `json:"stats"`
}

func xrayConfigs(t *testing.T, prof Profile, g Groups, r Routing) []xcConfig {
	t.Helper()
	raw, err := XrayConfigs(prof, g, r)
	if err != nil {
		t.Fatal(err)
	}
	var cfgs []xcConfig
	if err := json.Unmarshal(raw, &cfgs); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	if len(cfgs) == 0 {
		t.Fatalf("an empty config array: %s", raw)
	}
	return cfgs
}

func xrayProtocols(c xcConfig) []string {
	var out []string
	for _, o := range c.Outbounds {
		if p := str(o["protocol"]); p != "freedom" && p != "blackhole" {
			out = append(out, p)
		}
	}
	return out
}

// The body is the array INCY imports server by server: every server the profile has as
// Xray can run it, the balancer entry first, and the automatic entry named after the auto
// group.
func TestXrayConfigsShape(t *testing.T) {
	prof := profile(t, "")
	cfgs := xrayConfigs(t, prof, Groups{}, RoutingRUDirect)
	// The default presets: two VLESS, Hysteria2 and TUIC. Xray has no outbound for the
	// last two, so two server configs and the automatic one.
	if len(cfgs) != 3 {
		t.Fatalf("%d configs, want 3: %+v", len(cfgs), cfgs)
	}
	for _, c := range cfgs {
		if len(c.Inbounds) != 1 || str(c.Inbounds[0]["protocol"]) != "socks" || c.Inbounds[0]["listen"] != "127.0.0.1" {
			t.Fatalf("inbounds: %v", c.Inbounds)
		}
		if str(c.Routing.DomainStrategy) != "IPIfNonMatch" && len(c.Routing.Rules) > 0 {
			t.Fatalf("domainStrategy: %v", c.Routing.DomainStrategy)
		}
		if c.DNS["queryStrategy"] != "UseIPv4" || len(anyList(c.DNS["servers"])) == 0 {
			t.Fatalf("dns: %v", c.DNS)
		}
	}

	auto := cfgs[0]
	if got := str(auto.Outbounds[0]["tag"]); got != "Авто" {
		t.Fatalf("the automatic config is named %q, want the auto group", got)
	}
	// remarks is the name INCY shows and the key it keeps the server under. The same
	// string on two configs is one entry, which is how the list collapsed to "Авто".
	if auto.Remarks != "Авто" {
		t.Fatalf("the automatic remarks: %q", auto.Remarks)
	}
	seenRemark := map[string]bool{auto.Remarks: true}
	if auto.Observatory["probeUrl"] != xrayProbeURL || auto.Observatory["probeInterval"] != "300s" || auto.Observatory["enableConcurrency"] != true {
		t.Fatalf("observatory: %v", auto.Observatory)
	}
	if len(auto.Routing.Balancers) != 1 {
		t.Fatalf("balancers: %v", auto.Routing.Balancers)
	}
	b := auto.Routing.Balancers[0]
	if str(b["tag"]) != "auto" || str(anyMap(b["strategy"])["type"]) != "leastPing" {
		t.Fatalf("the balancer: %v", b)
	}
	if sel := anyList(b["selector"]); len(sel) != 2 || sel[0] != "server-" || sel[1] != "Авто" {
		t.Fatalf("the balancer selector: %v", sel)
	}
	last := auto.Routing.Rules[len(auto.Routing.Rules)-1]
	if str(last["balancerTag"]) != "auto" || str(last["network"]) != "tcp,udp" {
		t.Fatalf("the last rule: %v", last)
	}
	if got := xrayProtocols(auto); len(got) != 2 || strings.Contains(strings.Join(got, ","), "hysteria") {
		t.Fatalf("outbounds: %v", got)
	}
	// Every server of the list is an outbound of the automatic config too.
	tags := map[string]bool{}
	for _, o := range auto.Outbounds {
		if tags[str(o["tag"])] {
			t.Fatalf("duplicate tag %q", o["tag"])
		}
		tags[str(o["tag"])] = true
	}

	// Each server is a config of its own, named after its first outbound, and its rules
	// send the traffic of the proxy to that very server.
	for _, c := range cfgs[1:] {
		if len(c.Outbounds) != 3 {
			t.Fatalf("a server config: %v", c.Outbounds)
		}
		name := str(c.Outbounds[0]["tag"])
		if name == "" || name == "direct" || name == "block" {
			t.Fatalf("a server name: %q", name)
		}
		if c.Remarks != name || seenRemark[c.Remarks] {
			t.Fatalf("server remarks %q (tag %q)", c.Remarks, name)
		}
		seenRemark[c.Remarks] = true
		if len(c.Routing.Balancers) != 0 || c.Observatory != nil {
			t.Fatalf("a server config has no balancer: %v %v", c.Routing.Balancers, c.Observatory)
		}
		last := c.Routing.Rules[len(c.Routing.Rules)-1]
		if str(last["outboundTag"]) != name {
			t.Fatalf("the rest goes to %q, want %q", last["outboundTag"], name)
		}
	}
}

// The link's protocol, transport and security as an Xray outbound: REALITY with the
// node's public key, XHTTP with the link's extra, Vision's flow, gRPC's service, a pinned
// self-signed certificate, Trojan and Shadowsocks-2022.
func TestXrayOutboundFromLinks(t *testing.T) {
	cases := []struct {
		name   string
		link   string
		want   func(*testing.T, map[string]any)
		xrayOK bool
	}{
		{
			name:   "reality-xhttp",
			link:   "vless://0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1@203.0.113.7:443?encryption=none&security=reality&sni=www.example.com&fp=chrome&pbk=abc&sid=12&flow=&type=xhttp&path=%2Fx&mode=auto&host=cdn.example.com&extra=%7B%22xPaddingBytes%22%3A%22100-1000%22%7D#n",
			xrayOK: true,
			want: func(t *testing.T, o map[string]any) {
				if o["protocol"] != "vless" {
					t.Fatalf("protocol: %v", o["protocol"])
				}
				stream := anyMap(o["streamSettings"])
				if stream["network"] != "xhttp" || stream["security"] != "reality" {
					t.Fatalf("stream: %v", stream)
				}
				if anyMap(stream["xhttpSettings"])["xPaddingBytes"] != "100-1000" {
					t.Fatalf("the extra: %v", stream["xhttpSettings"])
				}
				reality := anyMap(stream["realitySettings"])
				if reality["publicKey"] != "abc" || reality["shortId"] != "12" || reality["serverName"] != "www.example.com" || reality["fingerprint"] != "chrome" {
					t.Fatalf("reality: %v", reality)
				}
			},
		},
		{
			name:   "vision",
			link:   "vless://0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1@203.0.113.7:8443?encryption=none&flow=xtls-rprx-vision&security=tls&sni=node.example.com&fp=chrome&type=tcp#n",
			xrayOK: true,
			want: func(t *testing.T, o map[string]any) {
				vnext := anyList(anyMap(o["settings"])["vnext"])
				user := anyMap(anyList(anyMap(vnext[0])["users"])[0])
				if user["flow"] != "xtls-rprx-vision" {
					t.Fatalf("flow: %v", user)
				}
				tls := anyMap(anyMap(o["streamSettings"])["tlsSettings"])
				if tls["serverName"] != "node.example.com" || tls["allowInsecure"] != nil {
					t.Fatalf("tls: %v", tls)
				}
			},
		},
		{
			name:   "pinned-certificate",
			link:   "vless://0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1@203.0.113.7:2087?encryption=none&security=tls&sni=203.0.113.7&allowInsecure=1&pcs=AB12CD&type=tcp#n",
			xrayOK: true,
			want: func(t *testing.T, o map[string]any) {
				tls := anyMap(anyMap(o["streamSettings"])["tlsSettings"])
				if tls["pinnedPeerCertSha256"] != "ab12cd" {
					t.Fatalf("the pin: %v", tls)
				}
			},
		},
		{
			name:   "trojan-grpc",
			link:   "trojan://s3cr%2Ft@203.0.113.7:2053?security=reality&sni=www.example.com&fp=firefox&pbk=abc&sid=&type=grpc&serviceName=gun#n",
			xrayOK: true,
			want: func(t *testing.T, o map[string]any) {
				if o["protocol"] != "trojan" {
					t.Fatalf("protocol: %v", o["protocol"])
				}
				server := anyMap(anyList(anyMap(o["settings"])["servers"])[0])
				if server["password"] != "s3cr/t" {
					t.Fatalf("password: %v", server)
				}
				stream := anyMap(o["streamSettings"])
				if anyMap(stream["grpcSettings"])["serviceName"] != "gun" || stream["security"] != "reality" {
					t.Fatalf("stream: %v", stream)
				}
			},
		},
		{
			name:   "shadowsocks-2022",
			link:   "ss://2022-blake3-aes-128-gcm:serverKey%2BuserKey%3D@203.0.113.7:6443#n",
			xrayOK: true,
			want: func(t *testing.T, o map[string]any) {
				server := anyMap(anyList(anyMap(o["settings"])["servers"])[0])
				if o["protocol"] != "shadowsocks" || server["method"] != "2022-blake3-aes-128-gcm" || server["password"] != "serverKey+userKey=" {
					t.Fatalf("shadowsocks: %v", o)
				}
			},
		},
		{
			name: "shadowsocks-mihomo-only",
			link: "ss://aes-128-cfb:cGFzcw%3D%3D@203.0.113.7:6443#n",
		},
		{
			name: "hysteria2",
			link: "hysteria2://pass@203.0.113.7:443/?sni=node.example.com&insecure=1&pinSHA256=ab12#n",
		},
		{
			name: "tuic",
			link: "tuic://uuid:pass@203.0.113.7:8443?congestion_control=bbr&alpn=h3#n",
		},
		{
			name: "unknown-transport",
			link: "vless://uuid@203.0.113.7:443?encryption=none&security=none&type=kcp#n",
		},
	}
	for _, c := range cases {
		out, ok := xrayOutbound(c.link)
		if ok != c.xrayOK {
			t.Errorf("%s: ok=%v, want %v (%v)", c.name, ok, c.xrayOK, out)
			continue
		}
		if ok && c.want != nil {
			c.want(t, out)
		}
	}
}

// The panel's routing as Xray's: the panel's hosts and the LAN out of the tunnel, the
// admin's rules, a service and its block, the Russian sites direct, the QUIC twin, and a
// rule Xray cannot match left out rather than broken.
func TestXrayRules(t *testing.T) {
	prof := profile(t, "")
	prof.Direct = []string{"203.0.113.7", "vpn.example.com"}
	prof.Rules = []string{
		"DOMAIN-SUFFIX,sber.ru,DIRECT",
		"DOMAIN-WILDCARD,*.example.org,DIRECT",
		"PROCESS-NAME,curl.exe,DIRECT",
		"IP-CIDR,198.51.100.0/24,DIRECT,no-resolve",
		"DST-PORT,8443,DIRECT",
	}
	prof.Routes = Routes{
		Services: map[string]string{"youtube": TargetVPN, "ads": TargetBlock},
		Tune:     Tune{BlockQUIC: true},
	}
	cfgs := xrayConfigs(t, prof, Groups{}, RoutingRUDirect)
	rules := cfgs[0].Routing.Rules

	find := func(match func(map[string]any) bool) map[string]any {
		for _, r := range rules {
			if match(r) {
				return r
			}
		}
		return nil
	}
	has := func(field, value string) bool {
		return find(func(r map[string]any) bool {
			for _, x := range anyList(r[field]) {
				if str(x) == value {
					return true
				}
			}
			return false
		}) != nil
	}
	for _, value := range []string{"full:vpn.example.com", "domain:sber.ru", "domain:example.org", "geosite:youtube", "geosite:category-ru"} {
		if !has("domain", value) {
			t.Errorf("no domain rule %q: %v", value, rules)
		}
	}
	for _, value := range []string{"geoip:private", "203.0.113.7/32", "198.51.100.0/24", "geoip:ru"} {
		if !has("ip", value) {
			t.Errorf("no ip rule %q: %v", value, rules)
		}
	}
	if r := find(func(r map[string]any) bool { return str(r["port"]) == "8443" }); r == nil || str(r["outboundTag"]) != "direct" {
		t.Errorf("no port rule: %v", rules)
	}
	if r := find(func(r map[string]any) bool {
		return str(r["network"]) == "udp" && str(r["port"]) == "443" && str(r["outboundTag"]) == "block"
	}); r == nil {
		t.Errorf("no QUIC twin: %v", rules)
	}
	if r := find(func(r map[string]any) bool { return str(r["balancerTag"]) == "auto" && anyList(r["domain"]) != nil }); r == nil {
		t.Errorf("the service does not go to the auto group: %v", rules)
	}
	if r := find(func(r map[string]any) bool { return str(r["outboundTag"]) == "block" && anyList(r["domain"]) != nil }); r == nil {
		t.Errorf("the ads are not blocked: %v", rules)
	}
	// A process rule has no Xray counterpart: nothing of it reaches the config.
	for _, r := range rules {
		if strings.Contains(strings.Join([]string{str(r["domain"]), str(r["ip"]), str(r["process"])}, " "), "curl") {
			t.Fatalf("a process rule got into the config: %v", r)
		}
	}
	if last := rules[len(rules)-1]; str(last["balancerTag"]) != "auto" {
		t.Fatalf("the last rule: %v", last)
	}
}

// Every key of the body is a key Xray reads. Xray's own validator is not in the repository,
// and a misspelt field is either ignored or fails the config on the phone, where nobody
// sees it: the shapes are pinned here instead.
func TestXrayConfigsKnownFields(t *testing.T) {
	prof := profile(t, "")
	prof.Direct = []string{"203.0.113.7"}
	prof.Rules = []string{"DOMAIN-SUFFIX,sber.ru,DIRECT", "IP-CIDR,198.51.100.0/24,DIRECT,no-resolve"}
	prof.Routes = Routes{Services: map[string]string{"youtube": TargetVPN, "ads": TargetBlock}, Tune: Tune{BlockQUIC: true}}
	cfgs := xrayConfigs(t, prof, Groups{}, RoutingRUDirect)

	allowed := func(where string, m map[string]any, keys ...string) []string {
		ok := make(map[string]bool, len(keys))
		for _, k := range keys {
			ok[k] = true
		}
		var bad []string
		for k := range m {
			if !ok[k] {
				bad = append(bad, where+"."+k)
			}
		}
		return bad
	}
	for i, c := range cfgs {
		raw := map[string]any{}
		b, _ := json.Marshal(c)
		_ = json.Unmarshal(b, &raw)
		// remarks is INCY's name for the entry (docs.incy.cc). Xray's decoder skips a key it
		// does not know, and INCY reads it before the core does; without it the app keeps
		// one server for the whole array.
		bad := allowed("config", raw, "remarks", "log", "dns", "inbounds", "outbounds", "routing", "observatory", "stats")
		bad = append(bad, allowed("log", anyMap(c.Log), "loglevel")...)
		bad = append(bad, allowed("dns", c.DNS, "servers", "queryStrategy")...)
		for _, s := range anyList(c.DNS["servers"]) {
			bad = append(bad, allowed("dns.server", anyMap(s), "address", "domains")...)
		}
		for _, in := range c.Inbounds {
			bad = append(bad, allowed("inbound", in, "tag", "protocol", "listen", "port", "settings", "sniffing")...)
			bad = append(bad, allowed("inbound.sniffing", anyMap(in["sniffing"]), "enabled", "destOverride")...)
		}
		for _, o := range c.Outbounds {
			bad = append(bad, allowed("outbound", o, "tag", "protocol", "settings", "streamSettings")...)
			if stream := anyMap(o["streamSettings"]); stream != nil {
				bad = append(bad, allowed("stream", stream, "network", "security", "tlsSettings", "realitySettings",
					"wsSettings", "xhttpSettings", "grpcSettings", "httpupgradeSettings")...)
				bad = append(bad, allowed("stream.tls", anyMap(stream["tlsSettings"]),
					"serverName", "fingerprint", "allowInsecure", "pinnedPeerCertSha256", "alpn")...)
				bad = append(bad, allowed("stream.reality", anyMap(stream["realitySettings"]),
					"serverName", "fingerprint", "publicKey", "shortId", "spiderX")...)
				bad = append(bad, allowed("stream.ws", anyMap(stream["wsSettings"]), "path", "headers")...)
				bad = append(bad, allowed("stream.grpc", anyMap(stream["grpcSettings"]), "serviceName", "authority")...)
				bad = append(bad, allowed("stream.httpupgrade", anyMap(stream["httpupgradeSettings"]), "path", "host")...)
				// The xhttp settings are the link's own extra, which is Xray's: their keys are
				// whatever the node's config asks for, and they are passed through as they are.
			}
			switch str(o["protocol"]) {
			case "vless":
				bad = append(bad, allowed("settings", anyMap(o["settings"]), "vnext")...)
				for _, v := range anyList(anyMap(o["settings"])["vnext"]) {
					bad = append(bad, allowed("vnext", anyMap(v), "address", "port", "users")...)
					for _, u := range anyList(anyMap(v)["users"]) {
						bad = append(bad, allowed("user", anyMap(u), "id", "encryption", "flow")...)
					}
				}
			case "trojan":
				bad = append(bad, allowed("settings", anyMap(o["settings"]), "servers")...)
				for _, s := range anyList(anyMap(o["settings"])["servers"]) {
					bad = append(bad, allowed("server", anyMap(s), "address", "port", "password", "flow")...)
				}
			case "shadowsocks":
				bad = append(bad, allowed("settings", anyMap(o["settings"]), "servers")...)
				for _, s := range anyList(anyMap(o["settings"])["servers"]) {
					bad = append(bad, allowed("server", anyMap(s), "address", "port", "method", "password")...)
				}
			}
		}
		bad = append(bad, allowed("routing", map[string]any{
			"domainStrategy": c.Routing.DomainStrategy, "balancers": c.Routing.Balancers, "rules": c.Routing.Rules,
		}, "domainStrategy", "balancers", "rules")...)
		for _, b := range c.Routing.Balancers {
			bad = append(bad, allowed("balancer", b, "tag", "selector", "strategy")...)
			bad = append(bad, allowed("balancer.strategy", anyMap(b["strategy"]), "type", "settings")...)
		}
		for _, r := range c.Routing.Rules {
			bad = append(bad, allowed("rule", r, "type", "domain", "ip", "sourceIP", "port", "sourcePort",
				"network", "outboundTag", "balancerTag")...)
		}
		if len(bad) > 0 {
			t.Fatalf("config %d has keys Xray may not read: %v", i, bad)
		}
	}
	// The observatory and stats of the automatic config.
	auto := map[string]any{}
	b, _ := json.Marshal(cfgs[0])
	_ = json.Unmarshal(b, &auto)
	bad := allowed("observatory", anyMap(auto["observatory"]), "subjectSelector", "probeUrl", "probeInterval", "enableConcurrency")
	bad = append(bad, allowed("stats", anyMap(auto["stats"]))...)
	if len(bad) > 0 {
		t.Fatalf("keys Xray may not read: %v", bad)
	}
}

// A service the admin pinned to a node gets a balancer of that node's servers, as the
// Clash profile gives it a group of its own.
func TestXrayServiceOnANode(t *testing.T) {
	prof := twoNodes(t)
	prof.Routes = Routes{Services: map[string]string{"youtube": "node:2"}}
	cfgs := xrayConfigs(t, prof, Groups{}, RoutingRUDirect)
	auto := cfgs[0]
	if len(auto.Routing.Balancers) != 2 {
		t.Fatalf("balancers: %v", auto.Routing.Balancers)
	}
	service := auto.Routing.Balancers[1]
	if str(service["tag"]) != "service-1" || str(anyMap(service["strategy"])["type"]) != "leastPing" {
		t.Fatalf("the service balancer: %v", service)
	}
	found := false
	for _, sel := range anyList(service["selector"]) {
		if str(sel) == "server-2-" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the service balancer selects no server of node 2: %v", service["selector"])
	}
	if r := func() map[string]any {
		for _, r := range auto.Routing.Rules {
			for _, d := range anyList(r["domain"]) {
				if str(d) == "geosite:youtube" {
					return r
				}
			}
		}
		return nil
	}(); r == nil || str(r["balancerTag"]) != "service-1" {
		t.Fatalf("the service rule: %v", r)
	}
	// The observatory covers the service balancer's servers too, otherwise leastPing would
	// exclude them.
	covered := false
	for _, sel := range anyList(auto.Observatory["subjectSelector"]) {
		if str(sel) == "server-2-" {
			covered = true
		}
	}
	if !covered {
		t.Fatalf("observatory: %v", auto.Observatory["subjectSelector"])
	}
}

// A server named like the auto group, and two servers with one name, still come out as
// their own entries: INCY treats equal remarks as one server.
func TestXrayConfigsRemarksStayDistinct(t *testing.T) {
	c, err := presets.NewConfig("vless_reality_vision", "www.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	in := func(id int64, port, display string) db.Inbound {
		return db.Inbound{ID: id, NodeID: 1, Name: "vless", DisplayName: display, Preset: "vless_reality_vision", Port: port, Enabled: 1, Config: c}
	}
	prof := Profile{
		Slot: db.Slot{Name: "s000001", Uuid: "0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1", Secret: "S3cr3t"},
		Inbounds: []db.Inbound{
			in(1, "8443", "Авто"),
			in(2, "8444", "Авто"),
			in(3, "8445", "Германия"),
		},
		Nodes: []Node{{ID: 1, Endpoint: Endpoint{Host: "203.0.113.7"}}},
	}
	cfgs := xrayConfigs(t, prof, Groups{}, RoutingAll)
	if len(cfgs) != 4 {
		t.Fatalf("%d configs, want the auto entry and three servers", len(cfgs))
	}
	seen := map[string]bool{}
	for _, cfg := range cfgs {
		if cfg.Remarks == "" || seen[cfg.Remarks] {
			t.Fatalf("remarks %q in %v", cfg.Remarks, seen)
		}
		seen[cfg.Remarks] = true
		if str(cfg.Outbounds[0]["tag"]) != cfg.Remarks {
			t.Fatalf("remarks %q, tag %q", cfg.Remarks, cfg.Outbounds[0]["tag"])
		}
	}
	if !seen["Авто"] || !seen["Германия"] {
		t.Fatalf("remarks: %v", seen)
	}
}

// Nothing of the profile is an outbound Xray has: the caller serves the link list instead
// of a config INCY would fail to start.
func TestXrayConfigsWithoutXrayServers(t *testing.T) {
	var ins []db.Inbound
	c, err := presets.NewConfig("hysteria2", "www.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	ins = append(ins, db.Inbound{ID: 1, NodeID: 1, Name: "hysteria2", Preset: "hysteria2", Port: "443", Enabled: 1, Config: c})
	prof := Profile{
		Slot:     db.Slot{Name: "s000001", Uuid: "0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1", Secret: "S3cr3t"},
		Inbounds: ins,
		Nodes:    []Node{{ID: 1, Endpoint: Endpoint{Host: "203.0.113.7"}}},
	}
	if _, err := XrayConfigs(prof, Groups{}, RoutingRUDirect); !errors.Is(err, ErrNoProxies) {
		t.Fatalf("err: %v", err)
	}
}

// Fifty servers: one config each and the automatic one, every tag unique, the balancer
// over all of them.
func TestXrayConfigsManyServers(t *testing.T) {
	prof := profile(t, "")
	prof.Inbounds, prof.Nodes = nil, nil
	for i := 1; i <= 50; i++ {
		c, err := presets.NewConfig("vless_reality_vision", "www.example.com:443")
		if err != nil {
			t.Fatal(err)
		}
		prof.Inbounds = append(prof.Inbounds, db.Inbound{ID: int64(i), NodeID: int64(i), Name: "vless", Preset: "vless_reality_vision", Port: "8443", Enabled: 1, Config: c})
		prof.Nodes = append(prof.Nodes, Node{ID: int64(i), Name: "🇳🇱 NL " + strconv.Itoa(i), Endpoint: Endpoint{Host: "203.0.113.7"}})
	}
	cfgs := xrayConfigs(t, prof, Groups{Auto: "Auto"}, RoutingAll)
	if len(cfgs) != 51 {
		t.Fatalf("%d configs, want 51", len(cfgs))
	}
	// Fifty configs of the same rules stay small enough for an app to fetch: the body is
	// compact JSON, not an indented one.
	if raw, err := XrayConfigs(prof, Groups{Auto: "Auto"}, RoutingAll); err != nil {
		t.Fatal(err)
	} else if len(raw) > 1<<20 {
		t.Fatalf("the body is %d bytes", len(raw))
	}
	auto := cfgs[0]
	if got := str(auto.Outbounds[0]["tag"]); got != "Auto" || auto.Remarks != "Auto" {
		t.Fatalf("the first outbound: %q remarks %q", got, auto.Remarks)
	}
	remarks := map[string]bool{auto.Remarks: true}
	seen := map[string]bool{}
	for _, o := range auto.Outbounds {
		tag := str(o["tag"])
		if seen[tag] {
			t.Fatalf("duplicate tag %q", tag)
		}
		seen[tag] = true
	}
	for _, c := range cfgs[1:] {
		if c.Remarks == "" || remarks[c.Remarks] || str(c.Outbounds[0]["tag"]) != c.Remarks {
			t.Fatalf("server remarks %q", c.Remarks)
		}
		remarks[c.Remarks] = true
	}
	if len(remarks) != 51 {
		t.Fatalf("%d distinct remarks", len(remarks))
	}
	// The auto group is off for every server: all of them are in the balancer, as the
	// Clash profile does when the switches are all off.
	sel := anyList(auto.Routing.Balancers[0]["selector"])
	if len(sel) != 2 || sel[0] != "server-" || sel[1] != "Auto" {
		t.Fatalf("selector: %v", sel)
	}
	if n := len(auto.Routing.Rules); n == 0 {
		t.Fatal("no rules")
	}
}

// The panel serves the config only to INCY with the switch on, only in the link format,
// and without the routing header — the config carries the routing itself. With the switch
// off INCY gets the base64 list and the header, as before.
func TestINCYFullConfigDelivery(t *testing.T) {
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
	tariffs, err := st.Q.ListTariffs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u, err := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Brand: "Test VPN", INCYAuto: true, Happ: Happ{Routing: HappRoutingAuto},
		Nodes: []Node{{ID: 1, Endpoint: Endpoint{Host: "203.0.113.7"}}},
	}
	h := NewHandler(st, func(context.Context) (Config, error) { return cfg, nil }, nil, clock, nil, false)
	serve := func(ua string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "https://sub.example.com/"+u.SubToken, nil)
		req.Header.Set("User-Agent", ua)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	w := serve("INCY/2.1.0")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type %q", ct)
	}
	if w.Header().Get("routing") != "" {
		t.Fatal("the full config must not carry the routing header")
	}
	if c := w.Header().Get("Cache-Control"); c != "no-store" {
		t.Fatalf("the config is cached: %q", c)
	}
	var cfgs []xcConfig
	if err := json.Unmarshal(w.Body.Bytes(), &cfgs); err != nil {
		t.Fatalf("%v\n%s", err, w.Body)
	}
	if len(cfgs) < 2 || len(cfgs[0].Routing.Balancers) == 0 || cfgs[0].Observatory == nil {
		t.Fatalf("not the balancer array: %d configs", len(cfgs))
	}
	shown := map[string]bool{}
	for _, c := range cfgs {
		if c.Remarks == "" || shown[c.Remarks] {
			t.Fatalf("INCY would collapse remarks %q", c.Remarks)
		}
		shown[c.Remarks] = true
	}
	// The seeded Hysteria2 inbound is not in the configs: the VLESS and Trojan ones are.
	for _, c := range cfgs {
		for _, p := range xrayProtocols(c) {
			if p != "vless" && p != "trojan" && p != "shadowsocks" {
				t.Fatalf("an outbound Xray has not: %q", p)
			}
		}
	}

	// Every other app keeps the link list, header and all.
	w = serve("Happ/3.4.1")
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Happ got %q", ct)
	}
	if w.Header().Get("routing") == "" {
		t.Fatal("Happ must keep the routing header")
	}
	if _, err := base64.StdEncoding.DecodeString(strings.TrimSpace(w.Body.String())); err != nil {
		t.Fatalf("Happ got no base64: %v", err)
	}

	// The switch off: INCY gets the list and the header again.
	cfg.INCYAuto = false
	w = serve("INCY/2.1.0")
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("INCY with the switch off got %q", ct)
	}
	if w.Header().Get("routing") == "" {
		t.Fatal("INCY with the switch off must get the routing header")
	}
	if _, err := base64.StdEncoding.DecodeString(strings.TrimSpace(w.Body.String())); err != nil {
		t.Fatalf("INCY with the switch off got no base64: %v", err)
	}
}
