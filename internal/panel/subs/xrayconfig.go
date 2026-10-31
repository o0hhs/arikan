package subs

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// INCY takes a whole Xray config as a subscription: an object with inbounds and outbounds
// is one server in its list, an array of them is one server per element (docs.incy.cc,
// "Subscription format" and "Full Xray configurations"). Share links cannot carry a
// balancer, so this is the only way INCY gets an autoselect entry: the body is that
// array — the config with every server, an observatory and a leastPing balancer first,
// then one config per server, so the list stays and the automatic entry joins it.
//
// Each element is named by its top-level remarks. That field is the entry INCY shows and
// the key it updates the server by; it names the whole config, not an outbound inside a
// balancer. Without remarks every element collapses into the first one, and the list
// becomes a single "Авто" entry. The first proxy outbound's tag is the same name, which
// is what INCY uses when remarks is absent.
//
// Only what Xray really runs goes in: VLESS, Trojan and Shadowsocks (its 2022 methods and
// the classic AEAD ones). Hysteria2 and the newer types stay in the link list the app gets
// with the switch off: an outbound Xray does not know fails the whole config, not one
// server. The rules of the panel's profile come over as far as Xray's routing expresses
// them: processes and the admin's own lists (downloaded Clash lists) have no counterpart
// and are left out, a geosite or geoip category only when the geodata of Happ and INCY has
// it (happNames), and a service pinned to a node gets a balancer of that node's servers.
//
// The switch is off by default and the panel alone answers it: a node's pre-rendered
// subscription record carries the link list (nodeserve.go), not this body.

// xraySocksPort is the local SOCKS port of every config. INCY patches the listens of a
// config it imports; the same port in every config is fine, one server runs at a time.
const xraySocksPort = 10808

// xrayProbeURL is what the observatory measures the servers by.
const xrayProbeURL = "https://www.gstatic.com/generate_204"

// xrayProbeInterval is the observatory's probe interval when the admin set none
// (Routes.Servers.Interval): five minutes, the auto group's own interval.
const xrayProbeInterval = 300

// xraySSCiphers are the Shadowsocks methods Xray encrypts with. A method absent here (a
// node imported with a mihomo-only one) is left out of the config rather than making Xray
// refuse it whole.
var xraySSCiphers = map[string]bool{
	"2022-blake3-aes-128-gcm": true, "2022-blake3-aes-256-gcm": true, "2022-blake3-chacha20-poly1305": true,
	"aes-128-gcm": true, "aes-256-gcm": true, "chacha20-poly1305": true, "chacha20-ietf-poly1305": true,
}

// xrayServer is one proxy of the profile as an Xray outbound, without its tag: a config
// gives it one, and the first one names the config in INCY.
type xrayServer struct {
	name string
	node int64
	auto bool
	out  map[string]any
}

func (s xrayServer) tagged(tag string) map[string]any {
	m := make(map[string]any, len(s.out)+1)
	for k, v := range s.out {
		m[k] = v
	}
	m["tag"] = tag
	return m
}

// XrayConfigs renders the whole subscription body for INCY with the autoselect switch on:
// a JSON array, the balancer config first, then one full config per server. ErrNoProxies
// when nothing of the profile is an outbound Xray has: the caller serves the link list.
func XrayConfigs(p Profile, g Groups, r Routing) ([]byte, error) {
	cfgs, err := xrayConfigList(p, g, r)
	if err != nil {
		return nil, err
	}
	return json.Marshal(cfgs)
}

func xrayConfigList(p Profile, g Groups, r Routing) ([]map[string]any, error) {
	// The rules and the service groups as a current mihomo would make them: the same
	// routing the Clash profile gets, as far as Xray can express it.
	p.App = App{}
	g = g.WithDefaults(p.Lang)
	ps, err := build(p)
	if err != nil {
		return nil, err
	}
	servers := make([]xrayServer, 0, len(ps))
	for _, x := range ps {
		if x.uri == "" {
			continue
		}
		out, ok := xrayOutbound(x.uri)
		if !ok {
			continue // a protocol Xray has no outbound for
		}
		servers = append(servers, xrayServer{name: x.name, node: x.node, auto: x.auto, out: out})
	}
	if len(servers) == 0 {
		return nil, ErrNoProxies
	}
	nodeOfName := make(map[string]int64, len(servers))
	names := make([]string, len(servers))
	for i, s := range servers {
		names[i], nodeOfName[s.name] = s.name, s.node
	}
	taken := map[string]bool{}
	for _, n := range append(append([]string{}, names...), g.Main, g.Auto, AliasGroup) {
		taken[strings.ToLower(n)] = true
	}
	if len(p.Nodes) > 1 {
		for _, n := range p.Nodes {
			if ValidName(n.Name) == nil {
				taken[strings.ToLower(n.Name)] = true
			}
		}
	}
	rt := p.route(g, ps, taken, r)
	texts := xrayRuleTexts(p, rt, r)

	// The servers of the auto group: those the admin left in it, all of them when none is.
	member := make([]bool, len(servers))
	anyAuto := false
	for i, s := range servers {
		member[i] = s.auto
		anyAuto = anyAuto || s.auto
	}
	if !anyAuto {
		for i := range member {
			member[i] = true
		}
	}
	// INCY names a config after its first proxy outbound: the first one is a server of the
	// auto group, tagged with the group's name.
	first := 0
	for i := range servers {
		if member[i] {
			first = i
			break
		}
	}

	// A tag may not repeat within the config's outbounds; the balancer's "auto" lives in a
	// namespace of its own, so an outbound named "auto" is fine.
	used := map[string]bool{"direct": true, "block": true}
	tags := make([]string, len(servers))
	for i, s := range servers {
		var tag string
		switch {
		case i == first:
			tag = xrayTag(g.Auto, used)
		case member[i]:
			tag = xrayTag(fmt.Sprintf("server-%d-%d", s.node, i+1), used)
		default:
			tag = xrayTag(fmt.Sprintf("other-%d-%d", s.node, i+1), used)
		}
		tags[i], used[tag] = tag, true
	}

	// A service the admin pinned to a node gets a balancer of that node's servers, as the
	// Clash profile gives it a group. A node with nothing Xray can run falls back to the
	// automatic entry.
	groupBalancer := map[string]string{}
	selector := dedup([]string{"server-", tags[first]})
	balancers := []map[string]any{
		{"tag": "auto", "selector": selector, "strategy": map[string]any{"type": "leastPing"}},
	}
	for _, gr := range rt.groups {
		ids := map[int64]bool{}
		for _, x := range anyList(gr["proxies"]) {
			if id, ok := nodeOfName[str(x)]; ok {
				ids[id] = true
			}
		}
		if len(ids) != 1 {
			continue
		}
		var id int64
		for x := range ids {
			id = x
		}
		tag := "service-" + strconv.Itoa(len(balancers))
		sel := []string{fmt.Sprintf("server-%d-", id), fmt.Sprintf("other-%d-", id)}
		if servers[first].node == id {
			sel = append(sel, tags[first])
		}
		balancers = append(balancers, map[string]any{"tag": tag, "selector": sel, "strategy": map[string]any{"type": "leastPing"}})
		selector = append(selector, sel...)
		groupBalancer[str(gr["name"])] = tag
	}
	selector = dedup(selector)

	pickAuto := func(target string) (string, string) {
		switch {
		case target == "DIRECT":
			return "direct", ""
		case strings.HasPrefix(target, "REJECT"):
			return "block", ""
		case target == g.Main || target == g.Auto || target == AliasGroup:
			return "", "auto"
		}
		if b, ok := groupBalancer[target]; ok {
			return "", b
		}
		return "", "auto"
	}
	probe := p.Routes.Servers.Interval
	if probe <= 0 {
		probe = xrayProbeInterval
	}
	// remarks is the name INCY shows for this config and the key it keeps the server
	// under. The same string is the first outbound's tag, so a client that names a
	// config from that tag shows the same entry.
	autoRemark := tags[first]
	auto := map[string]any{
		"remarks":  autoRemark,
		"log":      map[string]any{"loglevel": "warning"},
		"dns":      xrayDNS(p.Routes.DNS, r),
		"inbounds": xrayInbounds(),
		"outbounds": func() []any {
			out := make([]any, 0, len(servers)+2)
			out = append(out, servers[first].tagged(tags[first]))
			for i, s := range servers {
				if i != first {
					out = append(out, s.tagged(tags[i]))
				}
			}
			return append(out, xrayDirect(), xrayBlock())
		}(),
		"routing": map[string]any{
			"domainStrategy": "IPIfNonMatch",
			"balancers":      balancers,
			"rules":          append(xrayRules(texts, pickAuto), xrayFallback(r)),
		},
		"observatory": map[string]any{
			"subjectSelector":   selector,
			"probeUrl":          xrayProbeURL,
			"probeInterval":     strconv.Itoa(probe) + "s",
			"enableConcurrency": true,
		},
		"stats": map[string]any{},
	}
	cfgs := []map[string]any{auto}

	// One config per server: the same rules, with every proxy target on this very server.
	// remarks stays unique across the list, including the automatic entry: two configs
	// with one remarks are one server in INCY, and the others disappear.
	seenRemark := map[string]bool{"direct": true, "block": true, autoRemark: true}
	for _, s := range servers {
		tag := xrayTag(s.name, seenRemark)
		seenRemark[tag] = true
		pick := func(target string) (string, string) {
			switch {
			case target == "DIRECT":
				return "direct", ""
			case strings.HasPrefix(target, "REJECT"):
				return "block", ""
			}
			return tag, ""
		}
		cfgs = append(cfgs, map[string]any{
			"remarks":   tag,
			"log":       map[string]any{"loglevel": "warning"},
			"dns":       xrayDNS(p.Routes.DNS, r),
			"inbounds":  xrayInbounds(),
			"outbounds": []any{s.tagged(tag), xrayDirect(), xrayBlock()},
			"routing": map[string]any{
				"domainStrategy": "IPIfNonMatch",
				"rules":          append(xrayRules(texts, pick), xrayFallbackOutbound(tag, r)),
			},
		})
	}
	return cfgs, nil
}

// xrayFallback is the last rule of the balancer config: what matched nothing goes through
// the auto group; in the "blocked" mode, as in the Clash profile, it goes direct.
func xrayFallback(r Routing) map[string]any {
	if r == RoutingBlocked {
		return map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": "direct"}
	}
	return map[string]any{"type": "field", "network": "tcp,udp", "balancerTag": "auto"}
}

// xrayFallbackOutbound is the last rule of a single-server config: the rest goes through
// that server, or direct in the "blocked" mode.
func xrayFallbackOutbound(tag string, r Routing) map[string]any {
	if r == RoutingBlocked {
		return map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": "direct"}
	}
	return map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": tag}
}

// xrayRuleTexts are the Clash rules of p in the order the profile applies them, before
// the fallback: keep the panel's hosts and the LAN out of the tunnel, the admin's own
// rules, the services and lists, then the Russian sites in the ru_direct mode.
func xrayRuleTexts(p Profile, rt routed, r Routing) []string {
	rules := directRules(p.Direct)
	rules = append(rules, "GEOIP,LAN,DIRECT,no-resolve")
	if p.Routes.Tune.BlockQUIC {
		rules = append(rules, "AND,((NETWORK,UDP),(DST-PORT,443)),REJECT")
	}
	rules = append(rules, p.Rules...)
	rules = append(rules, rt.rules...)
	if r == RoutingRUDirect {
		rules = append(rules, "GEOSITE,category-ru,DIRECT", "GEOIP,ru,DIRECT")
	}
	return rules
}

// xrayPick says where a rule's target goes: an outbound, or a balancer.
type xrayPick func(target string) (outbound, balancer string)

// xrayRules turns the Clash rules into Xray field rules. A rule Xray cannot match becomes
// no rule at all (a process, a list the panel has no geodata tag for), a rule that names
// both sites and IPs becomes one rule of each.
func xrayRules(rules []string, pick xrayPick) []map[string]any {
	out := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		typ, target, set := ruleParts(rule)
		if typ == "" {
			continue
		}
		outbound, balancer := pick(target)
		out = append(out, xrayConditions(rule, typ, set, outbound, balancer)...)
	}
	return out
}

func xrayConditions(rule, typ, set, outbound, balancer string) []map[string]any {
	f := splitRule(rule)
	if len(f) < 3 {
		return nil
	}
	value := strings.TrimSpace(f[1])
	one := func(field string, v any) []map[string]any {
		r := map[string]any{"type": "field", field: v}
		if outbound != "" {
			r["outboundTag"] = outbound
		} else {
			r["balancerTag"] = balancer
		}
		return []map[string]any{r}
	}
	switch typ {
	case "DOMAIN":
		return one("domain", []string{"full:" + value})
	case "DOMAIN-SUFFIX":
		return one("domain", []string{"domain:" + value})
	case "DOMAIN-KEYWORD":
		return one("domain", []string{"keyword:" + value})
	case "DOMAIN-REGEX":
		return one("domain", []string{"regexp:" + value})
	case "DOMAIN-WILDCARD":
		if d, ok := xrayWildcard(value); ok {
			return one("domain", []string{"domain:" + d})
		}
	case "GEOSITE":
		// Only what the geodata of Happ and INCY carries (happ/geosite.txt): a category one
		// of them lacks would fail the whole config. Xray has no negated geosite either.
		if v := strings.ToLower(value); !strings.HasPrefix(v, "!") && happNames()[0][v] {
			return one("domain", []string{"geosite:" + v})
		}
	case "GEOIP":
		switch {
		case strings.EqualFold(value, "lan"):
			return one("ip", []string{"geoip:private"})
		case !strings.HasPrefix(value, "!"):
			if v := strings.ToLower(value); happNames()[1][v] {
				return one("ip", []string{"geoip:" + v})
			}
		}
	case "IP-CIDR", "IP-CIDR6":
		if _, err := netip.ParsePrefix(value); err == nil {
			return one("ip", []string{value})
		}
	case "SRC-IP-CIDR":
		if _, err := netip.ParsePrefix(value); err == nil {
			return one("sourceIP", []string{value})
		}
	case "DST-PORT":
		if p := xrayPorts(value); p != "" {
			return one("port", p)
		}
	case "SRC-PORT":
		if p := xrayPorts(value); p != "" {
			return one("sourcePort", p)
		}
	case "NETWORK":
		switch v := strings.ToLower(value); v {
		case "tcp", "udp":
			return one("network", v)
		}
	case "RULE-SET":
		// The panel's own lists by the geodata categories nearest to them, as Happ's routing
		// gets them (happLists); an admin's own list is a download Xray cannot read.
		var out []map[string]any
		if sites := happLists[set][0]; len(sites) > 0 {
			out = append(out, one("domain", sites)...)
		}
		if ips := happLists[set][1]; len(ips) > 0 {
			out = append(out, one("ip", ips)...)
		}
		return out
	case "AND":
		// Only the QUIC twin the panel makes: UDP to port 443, kept from leaving the tunnel.
		if network, port, ok := xrayAnd(value); ok {
			r := map[string]any{"type": "field", "network": network}
			if port != "" {
				r["port"] = port
			}
			if outbound != "" {
				r["outboundTag"] = outbound
			} else {
				r["balancerTag"] = balancer
			}
			return []map[string]any{r}
		}
	}
	return nil
}

// xrayWildcard is a Clash wildcard as an Xray domain rule: "*.example.com" is what Xray's
// "domain:" already matches; the rest (a "?" or a "*" in the middle) is left out.
func xrayWildcard(v string) (string, bool) {
	d, ok := strings.CutPrefix(v, "*.")
	if !ok || d == "" || strings.ContainsAny(d, "*?") {
		return "", false
	}
	return d, true
}

// xrayPorts is a Clash port value ("443", "8000-9000", "443/8443") as Xray's own.
func xrayPorts(v string) string {
	var out []string
	for _, part := range strings.Split(v, "/") {
		part = strings.TrimSpace(part)
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil || a < 1 || a > 65535 {
			return ""
		}
		if isRange {
			b, err := strconv.Atoi(hi)
			if err != nil || b < a || b > 65535 {
				return ""
			}
		}
		out = append(out, part)
	}
	return strings.Join(out, ",")
}

// xrayAnd reads the one AND rule the panel makes, NETWORK and DST-PORT: Xray's routing
// has no logic rules of its own.
func xrayAnd(v string) (network, port string, ok bool) {
	v = strings.TrimSpace(v)
	// One pair of brackets around the whole group: "((NETWORK,UDP),(DST-PORT,443))".
	if strings.HasPrefix(v, "(") && strings.HasSuffix(v, ")") {
		v = strings.TrimSpace(v[1 : len(v)-1])
	}
	parts := splitRule(v)
	if len(parts) != 2 {
		return "", "", false
	}
	for _, part := range parts {
		p := strings.TrimSpace(part)
		p = strings.TrimPrefix(p, "(")
		p = strings.TrimSuffix(p, ")")
		f := splitRule(p)
		if len(f) != 2 {
			return "", "", false
		}
		switch strings.ToUpper(strings.TrimSpace(f[0])) {
		case "NETWORK":
			network = strings.ToLower(strings.TrimSpace(f[1]))
			if network != "tcp" && network != "udp" {
				return "", "", false
			}
		case "DST-PORT":
			if port = xrayPorts(strings.TrimSpace(f[1])); port == "" {
				return "", "", false
			}
		default:
			return "", "", false
		}
	}
	return network, port, network != ""
}

// xrayInbounds is the local SOCKS entry INCY runs the config through. Sniffing lets the
// domain rules see the site of an app that connects by address.
func xrayInbounds() []any {
	return []any{map[string]any{
		"tag": "socks-in", "protocol": "socks", "listen": "127.0.0.1", "port": xraySocksPort,
		"settings": map[string]any{"auth": "noauth", "udp": true},
		"sniffing": map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}},
	}}
}

func xrayDirect() map[string]any { return map[string]any{"tag": "direct", "protocol": "freedom"} }

func xrayBlock() map[string]any { return map[string]any{"tag": "block", "protocol": "blackhole"} }

// xrayDNS is the panel's Settings → Routing DNS as Xray's own, with the same resolvers:
// the profile's nameservers, and a domestic one for the Russian names the profile sends
// direct. What Xray has no form for goes (DoT, DoQ, a group suffix); the panel's own pair
// applies where the admin set none.
func xrayDNS(d DNS, r Routing) map[string]any {
	var servers []any
	add := func(address string, domains []string) {
		s := map[string]any{"address": address}
		if len(domains) > 0 {
			s["domains"] = domains
		}
		servers = append(servers, s)
	}
	names := xrayNameservers(d.Nameserver)
	if len(names) == 0 {
		names = []string{"https://1.1.1.1/dns-query", "https://8.8.8.8/dns-query"}
	}
	for _, n := range names {
		add(n, nil)
	}
	if r != RoutingAll && !xrayHasDNS(d, "ru") && !xrayHasDNS(d, ".cn") {
		if ns := xrayNameservers([]string{"77.88.8.8"}); len(ns) > 0 {
			add(ns[0], []string{"geosite:category-ru"})
		}
	}
	for _, p := range d.Policy {
		domains, ns := xrayDNSDomains(p.Match), xrayNameservers(p.Servers)
		if len(domains) == 0 || len(ns) == 0 {
			continue
		}
		add(ns[0], domains)
	}
	return map[string]any{"servers": servers, "queryStrategy": "UseIPv4"}
}

// xrayHasDNS says whether the admin already sent the names matched by a marker to a
// resolver of their own.
func xrayHasDNS(d DNS, marker string) bool {
	for _, p := range d.Policy {
		if strings.Contains(strings.ToLower(p.Match), marker) {
			return true
		}
	}
	return false
}

// xrayDNSDomains is a mihomo nameserver-policy key as Xray's domains list. A geosite
// category is only used when the geodata carries it (happNames).
func xrayDNSDomains(match string) []string {
	m := strings.TrimSpace(match)
	switch {
	case strings.HasPrefix(m, "geosite:"):
		if happNames()[0][strings.TrimPrefix(m, "geosite:")] {
			return []string{m}
		}
	case strings.HasPrefix(m, "+."):
		return []string{"domain:" + strings.TrimPrefix(m, "+.")}
	case m != "" && !strings.ContainsAny(m, ":*"):
		return []string{"full:" + m}
	}
	return nil
}

// xrayNameservers are the resolvers of a list Xray can ask: an address, a DoH URL or
// "localhost" for the system one. A "#group" suffix names a Clash group, which Xray's DNS
// has no idea of, so it goes.
func xrayNameservers(list []string) []string {
	var out []string
	for _, s := range list {
		s, _, _ = strings.Cut(s, "#")
		s = strings.TrimSpace(s)
		switch {
		case s == "":
			continue
		case s == "system" || s == "dhcp":
			out = append(out, "localhost")
		case strings.HasPrefix(s, "https://"), strings.HasPrefix(s, "tcp://"):
			out = append(out, s)
		case strings.HasPrefix(s, "udp://"):
			out = append(out, strings.TrimPrefix(s, "udp://"))
		case strings.HasPrefix(s, "tls://"), strings.HasPrefix(s, "quic://"), strings.HasPrefix(s, "h3://"):
			// DoT and DoQ have no form in Xray's DNS.
		default:
			if _, err := netip.ParseAddr(s); err == nil {
				out = append(out, s)
			} else if _, err := netip.ParseAddrPort(s); err == nil {
				out = append(out, s)
			}
		}
	}
	return out
}

// xrayOutbound turns a share link into an Xray outbound: the very link INCY would import,
// in the config form. false for a protocol or a transport Xray has no outbound for.
func xrayOutbound(link string) (map[string]any, bool) {
	u, err := url.Parse(link)
	if err != nil || u.Hostname() == "" {
		return nil, false
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, false
	}
	if u.User == nil {
		return nil, false
	}
	q := u.Query()
	switch strings.ToLower(u.Scheme) {
	case "vless":
		id := u.User.Username()
		if id == "" {
			return nil, false
		}
		enc := q.Get("encryption")
		if enc == "" {
			enc = "none"
		}
		user := map[string]any{"id": id, "encryption": enc}
		if flow := q.Get("flow"); flow != "" {
			user["flow"] = flow
		}
		return xrayOutboundOf("vless", map[string]any{"vnext": []any{map[string]any{
			"address": u.Hostname(), "port": port, "users": []any{user},
		}}}, q)
	case "trojan":
		secret := u.User.Username()
		if secret == "" {
			return nil, false
		}
		server := map[string]any{"address": u.Hostname(), "port": port, "password": secret}
		if flow := q.Get("flow"); flow != "" {
			server["flow"] = flow
		}
		return xrayOutboundOf("trojan", map[string]any{"servers": []any{server}}, q)
	case "ss":
		method := u.User.Username()
		key, _ := u.User.Password()
		if !xraySSCiphers[strings.ToLower(method)] || key == "" {
			return nil, false
		}
		return map[string]any{
			"protocol": "shadowsocks",
			"settings": map[string]any{"servers": []any{map[string]any{
				"address": u.Hostname(), "port": port, "method": method, "password": key,
			}}},
		}, true
	}
	return nil, false
}

// xrayOutboundOf is an outbound with its stream settings: the transport and the TLS or
// REALITY the link carries.
func xrayOutboundOf(protocol string, settings map[string]any, q url.Values) (map[string]any, bool) {
	stream, ok := xrayStream(q)
	if !ok {
		return nil, false
	}
	out := map[string]any{"protocol": protocol, "settings": settings}
	if len(stream) > 0 {
		out["streamSettings"] = stream
	}
	return out, true
}

// xrayStream is the link's transport and security as streamSettings. The values are the
// ones Xray reads: "tcp", "ws", "xhttp", "grpc", "httpupgrade"; TLS with the pin that lets
// a self-signed node certificate through, or REALITY with the node's public key.
func xrayStream(q url.Values) (map[string]any, bool) {
	network := strings.ToLower(q.Get("type"))
	if network == "" || network == "raw" {
		network = "tcp"
	}
	ss := map[string]any{"network": network}
	switch network {
	case "tcp":
	case "ws":
		ws := map[string]any{}
		if path := q.Get("path"); path != "" {
			ws["path"] = path
		}
		if host := q.Get("host"); host != "" {
			ws["headers"] = map[string]any{"Host": host}
		}
		ss["wsSettings"] = ws
	case "xhttp":
		x := map[string]any{}
		for _, k := range []string{"path", "host", "mode"} {
			if v := q.Get(k); v != "" {
				x[k] = v
			}
		}
		// The link's extra is Xray's own XHTTP settings: padding, session and uplink options
		// that must match the node's.
		if raw := q.Get("extra"); raw != "" {
			var extra map[string]any
			if err := json.Unmarshal([]byte(raw), &extra); err != nil {
				return nil, false
			}
			for k, v := range extra {
				x[k] = v
			}
		}
		ss["xhttpSettings"] = x
	case "grpc":
		g := map[string]any{}
		if name := q.Get("serviceName"); name != "" {
			g["serviceName"] = name
		}
		if auth := q.Get("authority"); auth != "" {
			g["authority"] = auth
		}
		ss["grpcSettings"] = g
	case "httpupgrade":
		h := map[string]any{}
		if path := q.Get("path"); path != "" {
			h["path"] = path
		}
		if host := q.Get("host"); host != "" {
			h["host"] = host
		}
		ss["httpupgradeSettings"] = h
	default:
		return nil, false
	}
	switch sec := strings.ToLower(q.Get("security")); sec {
	case "", "none":
	case "tls":
		t := map[string]any{}
		if sni := q.Get("sni"); sni != "" {
			t["serverName"] = sni
		}
		if fp := q.Get("fp"); fp != "" {
			t["fingerprint"] = fp
		}
		// The node certificate is self-signed and pinned: the pin is the verification, and
		// allowInsecure keeps the config working on a core that does not read pins yet.
		if q.Get("allowInsecure") == "1" || q.Get("insecure") == "1" {
			t["allowInsecure"] = true
		}
		if pin := q.Get("pcs"); pin != "" {
			t["pinnedPeerCertSha256"] = strings.ToLower(pin)
		}
		ss["security"], ss["tlsSettings"] = "tls", t
	case "reality":
		pbk := q.Get("pbk")
		if pbk == "" {
			return nil, false
		}
		r := map[string]any{"publicKey": pbk, "spiderX": "/"}
		if sni := q.Get("sni"); sni != "" {
			r["serverName"] = sni
		}
		if fp := q.Get("fp"); fp != "" {
			r["fingerprint"] = fp
		}
		if sid := q.Get("sid"); sid != "" {
			r["shortId"] = sid
		}
		ss["security"], ss["realitySettings"] = "reality", r
	default:
		return nil, false
	}
	return ss, true
}

// xrayTag keeps a tag unique in its config: two outbounds of one tag, or a proxy named
// "direct", would make Xray refuse the whole config.
func xrayTag(name string, used map[string]bool) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "server"
	}
	tag := name
	for i := 2; used[tag]; i++ {
		tag = name + " " + strconv.Itoa(i)
	}
	return tag
}

func dedup(list []string) []string {
	seen := make(map[string]bool, len(list))
	out := make([]string, 0, len(list))
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
