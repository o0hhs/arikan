package node

import (
	"encoding/json"
	"strings"

	"github.com/metacubex/mihomo/listener"

	"mikan/internal/nodeapi"
	"mikan/internal/proto"
)

// Traffic to the node's own networks is refused: without these rules any VPN user could
// reach the host's loopback services or the cloud metadata endpoint (169.254.169.254).
// IP rules without no-resolve also catch domains that resolve to private addresses.
var privateRules = []string{
	"IP-CIDR,0.0.0.0/8,REJECT",
	"IP-CIDR,10.0.0.0/8,REJECT",
	"IP-CIDR,100.64.0.0/10,REJECT",
	"IP-CIDR,127.0.0.0/8,REJECT",
	"IP-CIDR,169.254.0.0/16,REJECT",
	"IP-CIDR,172.16.0.0/12,REJECT",
	"IP-CIDR,192.168.0.0/16,REJECT",
	"IP-CIDR,224.0.0.0/3,REJECT",
	"IP-CIDR6,::/128,REJECT", // "any address": a connection to it reaches the host itself
	"IP-CIDR6,::1/128,REJECT",
	// IPv4 written as IPv6 (::ffff:127.0.0.1) does not match the IPv4 ranges above.
	"IP-CIDR6,::ffff:0:0/96,REJECT",
	"IP-CIDR6,fc00::/7,REJECT",
	"IP-CIDR6,fe80::/10,REJECT",
}

// live are the outbounds that made it into the config: a rule naming a proxy that is not
// there is refused by mihomo, and mihomo refuses the whole config over one bad rule. An
// outbound that was left out (a link that does not read) therefore gets no rule either —
// its inbounds fall through to what comes next. nil: every named outbound counts, which is
// what routesKey wants (it only has to change when the state does).
func rules(st nodeapi.DesiredState, allowPrivate bool, live map[string]bool) []string {
	var r []string
	if !allowPrivate {
		r = append(r, privateRules...)
	}
	// Outbound SMTP from a shared VPN IP gets the address blacklisted within hours.
	r = append(r, "DST-PORT,25,REJECT")
	r = append(r, egressRules(st.Filters)...)
	r = append(r, exitRules(st, live)...)
	r = append(r, warpRules(st)...)
	return append(r, "MATCH,DIRECT")
}

// outbounds are the proxies besides DIRECT: WARP and the other nodes used as exits.
//
// An outbound the admin added by link that does not read is left out and reported like a
// rejected listener: a typo in one link must not hold up every other change of the state
// (the admin sees it in the node's check). WARP and another node's relay are the panel's
// own builds: an error about those is the state's.
func outbounds(st nodeapi.DesiredState, rejected *[]nodeapi.ListenerStatus) ([]any, map[string]bool, error) {
	out := []any{}
	if st.Warp != nil {
		p, err := warpProxyConfig(st.Warp)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, p)
	}
	live := map[string]bool{}
	for _, e := range st.Exits {
		p, err := exitProxyConfig(e)
		if err != nil {
			if strings.HasPrefix(e.Name, "OUT-") {
				*rejected = append(*rejected, nodeapi.ListenerStatus{Name: e.Name, Error: err.Error()})
				continue
			}
			return nil, nil, err
		}
		live[e.Name] = true
		out = append(out, p)
	}
	return out, live, nil
}

// buildConfig renders the mihomo config as JSON, which mihomo's YAML parser accepts.
// log-level warning keeps per-connection lines out of mihomo's own output; the ones that
// still come through are dropped by pumpLogs.
//
// A listener that does not pass the checks is left out and reported in rejected (not OK,
// with the reason): one broken inbound must not stop the node from taking every other
// change of the state, new users and policies included. What the whole state needs, the
// outbounds, is an error as before.
func buildConfig(st nodeapi.DesiredState, cert proto.Cert, allowPrivate bool, sitePort int) (raw []byte, rejected []nodeapi.ListenerStatus, err error) {
	listeners := make([]map[string]any, 0, len(st.Inbounds)+1)
	keep := func(name string, l map[string]any, err error) {
		if err == nil {
			// The parser mihomo applies the config with; it opens nothing.
			_, err = listener.ParseListener(l)
		}
		if err != nil {
			rejected = append(rejected, nodeapi.ListenerStatus{Name: name, Error: err.Error()})
			return
		}
		listeners = append(listeners, l)
	}
	for _, in := range st.Inbounds {
		l, err := listenerFor(in, st.Slots, cert, proto.Options{SelfStealPort: st.SelfStealPort, SitePort: sitePort})
		keep(in.Name, l, err)
	}
	if st.Relay != nil {
		l, err := relayListener(st.Relay, cert)
		keep(nodeapi.RelayListener, l, err)
	}
	proxies, live, err := outbounds(st, &rejected)
	if err != nil {
		return nil, nil, err
	}
	cfg := map[string]any{
		"mode":              "rule",
		"log-level":         "warning",
		"ipv6":              true,
		"allow-lan":         false,
		"mixed-port":        0,
		"find-process-mode": "off",
		"profile":           map[string]any{"store-selected": false, "store-fake-ip": false},
		"dns":               dnsSection(st.DNS),
		"sniffer":           snifferSection(),
		"proxies":           proxies,
		"rules":             rules(st, allowPrivate, live),
		"listeners":         listeners,
	}
	raw, err = json.Marshal(cfg)
	return raw, rejected, err
}

// defaultNameservers are what the node resolves with until the panel's Settings → Routing
// says otherwise: what the profiles themselves use. Over HTTPS, so an intercepting
// resolver on the way cannot answer instead — the very reason the node resolves at all.
var defaultNameservers = []string{"https://1.1.1.1/dns-query", "https://dns.google/dns-query"}

// defaultBootstraps resolve those nameservers' own names. The host's resolver comes first:
// it is what the node used before it had a DNS of its own, and it works where the plain
// public ones are interfered with.
var defaultBootstraps = []string{"system", "1.1.1.1", "8.8.8.8"}

// dnsSection is the node's own resolver: it resolves the destinations of its users'
// traffic, so a client that sent an address, or whose own resolver answers with rubbish,
// still reaches the right site. No server listens: clients keep their resolvers, the node
// only resolves for its own outbound traffic.
func dnsSection(d *nodeapi.DNS) map[string]any {
	ns, boot := defaultNameservers, defaultBootstraps
	var policy []nodeapi.DNSPolicy
	if d != nil {
		if len(d.Nameserver) > 0 {
			ns = d.Nameserver
		}
		if len(d.Default) > 0 {
			boot = d.Default
		}
		policy = d.Policy
	}
	out := map[string]any{
		"enable": true, "listen": "", "ipv6": false, "enhanced-mode": "redir-host",
		"nameserver":         cleanServers(ns),
		"default-nameserver": cleanServers(boot),
	}
	if p := policySection(policy); len(p) > 0 {
		out["nameserver-policy"] = p
	}
	return out
}

// cleanServers drops what a node cannot use: the "#group" suffix names a proxy group of an
// app's profile (a node has none; its DoH goes out as it is), and an entry that is nothing
// but a suffix is no server.
func cleanServers(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if i := strings.IndexByte(s, '#'); i >= 0 {
			s = s[:i]
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// policySection is the policies a node can apply: the ones that name domains ("+.cn").
// A geosite: or rule-set: key needs the geodata, which only the apps have; sending it
// would refuse the whole config here.
func policySection(in []nodeapi.DNSPolicy) map[string]any {
	out := map[string]any{}
	for _, p := range in {
		match := strings.ToLower(strings.TrimSpace(p.Match))
		if match == "" || strings.HasPrefix(match, "geosite:") || strings.HasPrefix(match, "rule-set:") || strings.Contains(match, "geosite:") {
			continue
		}
		if servers := cleanServers(p.Servers); len(servers) > 0 {
			out[p.Match] = servers
		}
	}
	return out
}

// snifferSection reads the site's name off each connection: rules by domain then hold for
// a client that connected by address, and the outbound resolves the name the client never
// sent. Every port is sniffed — a user's traffic goes wherever it wants, not only to 443.
func snifferSection() map[string]any {
	all := map[string]any{} // no ports: every port
	return map[string]any{
		"enable": true, "parse-pure-ip": true, "override-destination": true, "force-dns-mapping": true,
		"sniff": map[string]any{"HTTP": all, "TLS": all, "QUIC": all},
	}
}

func listenerFor(in nodeapi.Inbound, slots []nodeapi.Slot, cert proto.Cert, o proto.Options) (map[string]any, error) {
	t, err := template(in)
	if err != nil {
		return nil, err
	}
	return proto.Listener(t, in.Name, in.Listen, in.Port, slots, cert, o)
}

// sharedListeners are the inbounds with one key for everyone.
func sharedListeners(st nodeapi.DesiredState) []string {
	var out []string
	for _, in := range st.Inbounds {
		if t, err := template(in); err == nil && proto.Shared(t.Type()) {
			out = append(out, in.Name)
		}
	}
	return out
}

// template reads the inbound's listener template; states saved by mikan ≤ 0.1.2 carry
// a preset with its settings instead.
func template(in nodeapi.Inbound) (proto.Template, error) {
	if len(in.Config) > 0 {
		return proto.FromJSON(in.Config)
	}
	return proto.FromPreset(in.Preset, in.Settings)
}
