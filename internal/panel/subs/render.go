// Package subs renders subscriptions: share links for Xray/sing-box based clients and a
// mihomo profile for Clash-family clients.
package subs

import (
	"cmp"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/turn"
	"mikan/internal/proto"
	"mikan/internal/textutil"
)

// Endpoint describes how clients reach one node.
type Endpoint struct {
	Host      string // IP or domain the client connects to
	SNI       string // TLS server name for Hysteria2/TUIC; empty for IP-only nodes
	PinSHA256 string // hex SHA-256 of the node's self-signed Hysteria2/TUIC certificate
	// SNIRouter: the node shares its 443 between the inbounds marked for it, which clients
	// reach on 443.
	SNIRouter bool
}

// Node is one server in the subscription.
type Node struct {
	ID int64
	// Name is the node's country group, e.g. "🇳🇱 Нидерланды"; with several nodes its
	// leading flag (or the whole name) prefixes proxy names.
	Name     string
	Endpoint Endpoint
	// Emergency marks the node that keeps Telegram reachable for subscriptions that have
	// ended: their profiles list its inbounds alone. EmergencySpeed is the Mbit/s they are
	// shaped to there (0: none said).
	Emergency      bool
	EmergencySpeed int
	// SSOneKey: the node is older than the Shadowsocks-2022 key per user (proto.ClientInput).
	SSOneKey bool
	// AutoInclude: the node's servers take part in the auto group and the fastest-pick;
	// off, they stay in the plain list and in their country's group.
	AutoInclude bool
}

// Groups are the proxy-group names Clash-family apps show; the admin can rename them.
type Groups struct {
	Main string // selector, "VPN" by default
	Auto string // url-test, "Авто" by default, "Auto" on a panel in English
}

// AliasGroup is the name Clash apps assume for the main group in the rules they inject
// themselves (Koala Clash per-app routing: "PROCESS-NAME,app.exe,PROXY").
const AliasGroup = "PROXY"

// Routing is how Clash-family apps split traffic between the tunnel and the direct path.
type Routing string

const (
	// RoutingRUDirect sends Russian sites and IPs past the tunnel by mihomo's own geodata
	// (MetaCubeX geosite category-ru and GeoIP ru): banks and state services refuse
	// foreign IPs, and the node does not carry traffic that needs no VPN.
	RoutingRUDirect Routing = "ru_direct"
	// RoutingAll sends everything but the LAN through the tunnel and needs no geodata.
	RoutingAll Routing = "all"
	// RoutingBlocked sends only what is blocked or slowed down in Russia through the
	// tunnel (blockedLists, and the services sent to it), the rest goes direct. Apps that
	// cannot take rule lists get RoutingRUDirect.
	RoutingBlocked Routing = "blocked"
	DefaultRouting         = RoutingRUDirect
)

// ParseRouting maps a stored setting to a mode; empty and unknown values get the default.
func ParseRouting(s string) Routing {
	if r := Routing(s); r == RoutingRUDirect || r == RoutingAll || r == RoutingBlocked {
		return r
	}
	return DefaultRouting
}

// geoxURL is the geodata Koala Clash ships (MetaCubeX meta-rules-dat, mihomo's own
// default). Apps without the files download them from here before the profile starts.
var geoxURL = map[string]string{
	"geoip":   "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip-lite.dat",
	"geosite": "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat",
	"mmdb":    "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.metadb",
	"asn":     "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/GeoLite2-ASN.mmdb",
}

// WithDefaults fills in the names the admin left empty, in the panel's default language
// ("en", Russian otherwise).
func (g Groups) WithDefaults(lang string) Groups {
	if g.Main == "" {
		g.Main = "VPN"
	}
	if g.Auto == "" {
		g.Auto = "Авто"
		if textutil.Lang(lang).En() {
			g.Auto = "Auto"
		}
	}
	return g
}

// reserved names are mihomo's built-in policies; a group or proxy with such a name
// breaks the profile.
var reserved = []string{"DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE", "GLOBAL"}

// ValidName checks a group or proxy name the admin typed. Commas are refused because
// rules reference groups as "MATCH,<name>".
func ValidName(s string) error {
	switch {
	case s == "" || strings.TrimSpace(s) != s:
		return errors.New("name_blank")
	case utf8.RuneCountInString(s) > 48:
		return errors.New("name_too_long")
	case strings.ContainsAny(s, ",\"'\\"):
		return errors.New("name_bad_char")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return errors.New("name_bad_char")
		}
	}
	for _, x := range reserved {
		if strings.EqualFold(s, x) {
			return errors.New("name_reserved")
		}
	}
	return nil
}

// ErrNoProxies: nothing of the profile is left for the app, or for any app.
var ErrNoProxies = errors.New("no proxies")

type Profile struct {
	// Skip, when set, hears of an inbound that is left out because it cannot be rendered
	// (a port that is no number, a template that no longer parses): one broken inbound must
	// not empty or fail the subscription of everyone.
	Skip     func(in db.Inbound, err error)
	Slot     db.Slot
	Inbounds []db.Inbound // enabled and allowed for this user, in display order
	// What profile read on the way, for the subscription's page (info) to build on rather than
	// read again: every inbound, the user's access and grants left.
	all    []db.Inbound
	access domain.Access
	grants domain.GrantsLeft
	Nodes  []Node   // enabled nodes in display order; inbounds of other nodes are skipped
	Direct []string // hosts that bypass the tunnel: the panel and every node
	// Notice, when set, is the banner of an ended subscription: dead server entries that
	// say why nothing works and where to renew (see Notice).
	Notice *Notice
	// ProtocolOrder is the admin's order of protocol families (settings.KeyProtocolOrder);
	// within a node the inbounds follow it, the unnamed families after them as they were.
	ProtocolOrder []string
	// Fingerprint is the panel's default uTLS profile (proto.ClientInput.Fingerprint).
	Fingerprint string
	// Rules are the admin's own Clash rules (ServedRules), before the built-in routing.
	Rules []string
	// Turn is the VK-call way in of each node, by node id: its inbound is listed a second
	// time as a server of its own (the WL entry), at the client's own address.
	Turn map[int64]turn.Config
	// UserID picks the user's share of a node's VK call links.
	UserID int64
	// Routes go after the admin's rules, for the apps that take rule lists (App); Lang
	// names the groups they add.
	Routes Routes
	App    App
	Lang   string
}

// Notice is the banner a profile carries when the subscription cannot be used: each line
// is a server entry of its own (the apps show the name where the servers are), and together
// they say why nothing works and where to fix it. With a working emergency server the
// banner follows it; without one it is the first entries, so they are what the app picks.
type Notice struct {
	Lines        []string
	AfterEntries bool
}

type proxy struct {
	name string
	node int64
	pool int64 // the inbound's server group, 0: none (the main servers)
	auto bool  // in the auto group: both the node and the protocol are
	uri  string
	yaml map[string]any
}

// NodePrefix is what precedes proxy names of a node when a subscription has several:
// the flag a name starts with, or the whole name.
func NodePrefix(name string) string {
	rs := []rune(strings.TrimSpace(name))
	if len(rs) >= 2 && isRegional(rs[0]) && isRegional(rs[1]) {
		return string(rs[:2])
	}
	return string(rs)
}

func isRegional(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

// noticeProxy is one line of the banner as a proxy entry: an address that answers nothing
// (127.0.0.1:1, the port reserved for nothing), named with the whole line. Every format
// carries it — mihomo and sing-box take a SOCKS server with no keys, the share links a
// vless one — so an ended subscription looks the same in every app.
func noticeProxy(line string) proxy {
	return proxy{
		name: line,
		yaml: map[string]any{"name": line, "type": "socks5", "server": "127.0.0.1", "port": 1},
		uri:  "vless://00000000-0000-0000-0000-000000000000@127.0.0.1:1?encryption=none&type=tcp#" + url.PathEscape(line),
	}
}

// build renders the user's proxies node by node, in the nodes' order, the protocols within
// a node in the admin's order (settings.KeyProtocolOrder; the unnamed families keep their
// own). The banner of an ended subscription goes first — it is what tells why nothing
// works, and the apps pick the first server — unless a working emergency server stands
// before it, so Telegram stays reachable while the buyer reads.
func build(p Profile) ([]proxy, error) {
	var out, banner []proxy
	used := map[string]bool{}
	if p.Notice != nil {
		for _, line := range p.Notice.Lines {
			banner = append(banner, noticeProxy(line))
			used[line] = true
		}
	}
	slot := proto.Slot{Name: p.Slot.Name, UUID: p.Slot.Uuid, Secret: p.Slot.Secret}
	multi := len(p.Nodes) > 1
	inbounds := orderInbounds(p.Inbounds, p.ProtocolOrder)
	for _, n := range p.Nodes {
		for _, in := range inbounds {
			if in.NodeID != n.ID {
				continue
			}
			port := domain.FirstPort(in.Port)
			if port == 0 {
				p.skip(in, fmt.Errorf("bad port %q", in.Port))
				continue
			}
			t, err := parseTemplate(in.Config)
			if err != nil {
				// Saved configs are validated; one broken inbound must not empty the subscription.
				p.skip(in, err)
				continue
			}
			spec := in.Port
			if n.Endpoint.SNIRouter && proto.SNI443(t) {
				port, spec = 443, "443"
			}
			base := domain.ProxyName(in)
			// A name the admin typed is used as is; preset names get the node's flag.
			if prefix := NodePrefix(n.Name); multi && in.DisplayName == "" && prefix != "" {
				base = prefix + " " + base
			}
			// mihomo refuses a profile with two proxies of the same name.
			name := base
			for i := 2; used[name]; i++ {
				name = base + " " + strconv.Itoa(i)
			}
			c, err := proto.ClientConfig(t, proto.ClientInput{Name: name, Host: n.Endpoint.Host, Port: port, PortSpec: spec,
				SNI: n.Endpoint.SNI, PinSHA256: n.Endpoint.PinSHA256, Fingerprint: p.Fingerprint, Slot: slot, SSOneKey: n.SSOneKey})
			if err != nil {
				p.skip(in, err)
				continue
			}
			used[name] = true
			out = append(out, proxy{name: name, node: n.ID, pool: in.PoolID.Int64, auto: n.AutoInclude && in.AutoInclude != 0, uri: c.URI, yaml: c.Mihomo})
			out = append(out, turnCopies(p, n, in, t, base, used, slot)...)
		}
	}
	if banner == nil {
		return out, nil
	}
	if p.Notice.AfterEntries {
		return append(out, banner...), nil
	}
	return append(banner, out...), nil
}

// orderInbounds sorts the inbounds by the admin's order of protocol families: the families
// named come first, in that order, the rest after them as they were (their nodes' order and
// their own). A template that does not read keeps its place.
func orderInbounds(ins []db.Inbound, families []string) []db.Inbound {
	if len(families) == 0 {
		return ins
	}
	rank := make(map[string]int, len(families))
	for i, f := range families {
		rank[strings.ToLower(strings.TrimSpace(f))] = i
	}
	weight := func(in db.Inbound) int {
		t, err := parseTemplate(in.Config)
		if err != nil {
			return len(families)
		}
		if r, ok := rank[t.Type()]; ok {
			return r
		}
		return len(families)
	}
	out := slices.Clone(ins)
	slices.SortStableFunc(out, func(a, b db.Inbound) int { return cmp.Compare(weight(a), weight(b)) })
	return out
}

// emergencyOnly leaves the emergency nodes' inbounds and nodes alone in the profile: what an
// ended subscription may still use, so Telegram keeps working while the buyer finds the bot.
// Without an emergency node the profile keeps everything it had — the node turns the
// subscription away anyway, and the banner says the rest.
func (p *Profile) emergencyOnly() {
	keep := map[int64]bool{}
	for _, n := range p.Nodes {
		if n.Emergency {
			keep[n.ID] = true
		}
	}
	if len(keep) == 0 {
		return
	}
	ins := make([]db.Inbound, 0, len(p.Inbounds))
	for _, in := range p.Inbounds {
		if keep[in.NodeID] {
			ins = append(ins, in)
		}
	}
	nodes := make([]Node, 0, len(p.Nodes))
	for _, n := range p.Nodes {
		if n.Emergency {
			nodes = append(nodes, n)
		}
	}
	p.Inbounds, p.Nodes = ins, nodes
	p.Notice.AfterEntries = true
}

// turnCopies are the WL entries of an inbound: the same server, reached through the TURN
// servers of a VK call by the client program running next to the user's app, so it stands
// in the list at the client's own address and looks like one more server. Only the
// inbound the node's server hands traffic to gets one, and only when the node has TURN on.
func turnCopies(p Profile, n Node, in db.Inbound, t proto.Template, base string, used map[string]bool, slot proto.Slot) []proxy {
	tc, ok := p.Turn[n.ID]
	if !ok || tc.InboundID != in.ID || tc.Port() == 0 {
		return nil
	}
	// The copy reaches this very node: whatever stands in front of it for the usual
	// clients (a CDN address) must not apply.
	direct, err := turn.Direct(t)
	if err != nil {
		p.skip(in, err)
		return nil
	}
	name := tc.Name(base)
	for i := 2; used[name]; i++ {
		name = tc.Name(base) + " " + strconv.Itoa(i)
	}
	c, err := proto.ClientConfig(direct, proto.ClientInput{Name: name, Host: tc.ClientHost, Port: tc.Port(),
		PortSpec: strconv.Itoa(tc.Port()), SNI: n.Endpoint.SNI, PinSHA256: n.Endpoint.PinSHA256,
		Fingerprint: p.Fingerprint, Slot: slot})
	if err != nil {
		p.skip(in, err)
		return nil
	}
	used[name] = true
	return []proxy{{name: name, node: n.ID, uri: c.URI, yaml: c.Mihomo}}
}

func (p Profile) skip(in db.Inbound, err error) {
	if p.Skip != nil {
		p.Skip(in, err)
	}
}

// URIs renders one share link per inbound.
func URIs(p Profile) (string, error) {
	links, _, err := uris(p)
	return links, err
}

// uris are the share links of p and the proxies they are of.
func uris(p Profile) (string, []proxy, error) {
	ps, err := build(p)
	if err != nil {
		return "", nil, err
	}
	lines := make([]string, 0, len(ps))
	in := make([]proxy, 0, len(ps))
	for _, x := range ps {
		if x.uri != "" { // the mihomo-only types have no share link
			lines = append(lines, x.uri)
			in = append(in, x)
		}
	}
	if len(lines) == 0 {
		return "", nil, ErrNoProxies
	}
	return strings.Join(lines, "\n"), in, nil
}

// Mihomo renders a complete client profile.
func Mihomo(p Profile, g Groups, r Routing) ([]byte, error) {
	cfg, _, err := mihomoConfig(p, g, r)
	if err != nil {
		return nil, err
	}
	return marshalYAML(cfg)
}

// mihomoConfig is the profile Mihomo renders, and the proxies in it.
func mihomoConfig(p Profile, g Groups, r Routing) (map[string]any, []proxy, error) {
	ps, err := build(p)
	if err != nil {
		return nil, nil, err
	}
	if len(ps) == 0 {
		// Groups with no proxies in them are a profile mihomo may refuse whole.
		return nil, nil, ErrNoProxies
	}
	return mihomoConfigOf(p, g, r, ps), ps, nil
}

// defaultNameservers are the DNS-over-HTTPS servers a profile resolves with, and the ones that
// resolve the proxy servers' own names.
var defaultNameservers = []string{"https://1.1.1.1/dns-query", "https://dns.google/dns-query"}

// mihomoConfigOf is the profile of ps.
func mihomoConfigOf(p Profile, g Groups, r Routing, ps []proxy) map[string]any {
	g = g.WithDefaults("")
	proxies := make([]map[string]any, len(ps))
	names := make([]string, len(ps))
	for i, x := range ps {
		proxies[i], names[i] = x.yaml, x.name
	}
	var countries []map[string]any
	if !p.Routes.Servers.NoCountries {
		countries = countryGroups(p.Nodes, ps, g, names)
	}
	selector := []string{g.Auto}
	for _, c := range countries {
		selector = append(selector, c["name"].(string))
	}
	// The auto group takes only what the admin left in it: a node or a protocol marked
	// «В автовыборе» off stays in the plain list and in its country's group. All off: the
	// group takes every server, as it did before the switch existed.
	autoNames := make([]string, 0, len(ps))
	for _, x := range ps {
		if x.auto {
			autoNames = append(autoNames, x.name)
		}
	}
	if len(autoNames) == 0 {
		autoNames = names
	}
	auto := urlTest(g.Auto, autoNames)
	switch p.Routes.Servers.Auto {
	case AutoFallback:
		auto["type"] = "fallback" // the first that works, in the order of the nodes
		delete(auto, "tolerance")
	case AutoLoadBalance:
		// mihomo spreads the working servers; the strategy is left out on purpose, so the
		// core's own default (consistent-hashing: a destination keeps one server) applies.
		auto["type"] = "load-balance"
		delete(auto, "tolerance")
	}
	groups := []map[string]any{
		{"name": g.Main, "type": "select", "proxies": append(selector, names...)},
		auto,
	}
	groups = append(groups, countries...)
	if n := p.Routes.Servers.Interval; n > 0 {
		for _, gr := range groups[1:] {
			gr["interval"] = n
		}
	}
	taken := map[string]bool{}
	for _, n := range append(append([]string{}, names...), g.Main, g.Auto, AliasGroup) {
		taken[strings.ToLower(n)] = true
	}
	for _, c := range countries {
		taken[strings.ToLower(c["name"].(string))] = true
	}
	if !p.routable() && r == RoutingBlocked {
		r = RoutingRUDirect
	}
	rt := p.route(g, ps, taken, r)
	groups = append(groups, rt.groups...)
	if g.Main != AliasGroup {
		groups = append(groups, map[string]any{"name": AliasGroup, "type": "select", "proxies": []string{g.Main}, "hidden": true})
	}
	dns := map[string]any{
		"enable": true, "ipv6": false, "enhanced-mode": "fake-ip", "fake-ip-range": "198.18.0.1/16",
		"default-nameserver": []string{"1.1.1.1", "8.8.8.8"},
		"nameserver":         slices.Clone(defaultNameservers),
	}
	// The panel and the nodes stay out of the tunnel whatever the admin's rules say.
	rules := append(directRules(p.Direct), "GEOIP,LAN,DIRECT,no-resolve")
	tune := p.Routes.Tune
	if tune.BlockQUIC {
		rules = append(rules, "AND,((NETWORK,UDP),(DST-PORT,443)),REJECT")
	}
	rules = append(rules, p.Rules...)
	rules = append(rules, rt.rules...)
	cfg := map[string]any{
		"mixed-port": 7890, "allow-lan": false, "mode": "rule", "log-level": "warning",
		// The node has no IPv6 on most VPS: with it on, apps first try IPv6 through the
		// tunnel and wait for the node's "network unreachable" before falling back.
		"ipv6": false, "unified-delay": true, "tcp-concurrent": true,
		"dns":          dns,
		"proxies":      proxies,
		"proxy-groups": groups,
	}
	if len(rt.providers) > 0 {
		cfg["rule-providers"] = rt.providers
	}
	if rt.geodata || r != RoutingAll {
		cfg["geodata-mode"], cfg["geox-url"] = false, geoxURL
	}
	if r == RoutingRUDirect {
		rules = append(rules, "GEOSITE,category-ru,DIRECT", "GEOIP,ru,DIRECT")
	}
	if r != RoutingAll {
		// GEOIP,ru makes the app resolve every domain itself. DoH straight from Russia
		// stalls under TSPU throttling, so it goes through the tunnel (the alias group has
		// a fixed name: "&" or "=" in a renamed group would break the "#group" suffix).
		// Russian domains resolve with Yandex DNS directly and keep working without the VPN.
		dns["nameserver"] = []string{"https://1.1.1.1/dns-query#" + AliasGroup, "https://8.8.8.8/dns-query#" + AliasGroup}
		dns["proxy-server-nameserver"] = slices.Clone(defaultNameservers)
		dns["nameserver-policy"] = map[string]any{"geosite:category-ru": []string{"77.88.8.8", "77.88.8.1"}}
	}
	p.Routes.DNS.apply(dns)
	if tune.RealIP {
		dns["enhanced-mode"] = "redir-host"
		delete(dns, "fake-ip-range")
	}
	if tune.Sniffer {
		cfg["sniffer"] = map[string]any{"enable": true, "parse-pure-ip": true,
			"sniff": map[string]any{"HTTP": map[string]any{"ports": []any{80, "8080-8880"}}, "TLS": map[string]any{"ports": []any{443, 8443}},
				"QUIC": map[string]any{"ports": []any{443, 8443}}}}
	}
	// mihomo skips a rule whose group can't carry UDP (a proxy with udp off, picked by hand
	// or by url-test) and sends what falls through to DIRECT: past the tunnel, from the real
	// address. REJECT makes such apps fall back to TCP instead. In the "blocked" mode the
	// rest goes direct by design; its tunnel rules get their own REJECT twins (blockedRules).
	last := []string{"MATCH," + g.Main, "MATCH,REJECT"}
	if r == RoutingBlocked {
		last = []string{"MATCH,DIRECT"}
	}
	cfg["rules"] = append(rules, last...)
	return cfg
}

func urlTest(name string, proxies []string) map[string]any {
	return map[string]any{"name": name, "type": "url-test", "proxies": proxies, "url": "https://www.gstatic.com/generate_204", "interval": 300, "tolerance": 50}
}

// countryGroups picks the fastest proxy of each node when there are several nodes. A
// node whose name would clash with another group or a proxy gets no group: the profile
// must load whatever the admin typed.
func countryGroups(nodes []Node, ps []proxy, g Groups, names []string) []map[string]any {
	if len(nodes) < 2 {
		return nil
	}
	taken := map[string]bool{strings.ToLower(g.Main): true, strings.ToLower(g.Auto): true, strings.ToLower(AliasGroup): true}
	for _, n := range names {
		taken[strings.ToLower(n)] = true
	}
	var out []map[string]any
	for _, n := range nodes {
		var own []string
		for _, x := range ps {
			if x.node == n.ID {
				own = append(own, x.name)
			}
		}
		key := strings.ToLower(n.Name)
		if len(own) == 0 || ValidName(n.Name) != nil || taken[key] {
			continue
		}
		taken[key] = true
		out = append(out, urlTest(n.Name, own))
	}
	return out
}

// directRules keep traffic to the server itself (panel, SSH, subscription updates) out
// of the tunnel: in TUN mode it would otherwise loop through the node and die with it.
func directRules(hosts []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, h := range hosts {
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		switch ip, err := netip.ParseAddr(h); {
		case err != nil:
			out = append(out, "DOMAIN,"+h+",DIRECT")
		case ip.Is4():
			out = append(out, "IP-CIDR,"+ip.String()+"/32,DIRECT,no-resolve")
		default:
			out = append(out, "IP-CIDR6,"+ip.String()+"/128,DIRECT,no-resolve")
		}
	}
	return out
}
