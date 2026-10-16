package nodesync

import (
	"strings"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/subs"
)

// dnsOf is the panel's Settings → Routing as a node can follow it: the resolvers the node
// resolves its users' traffic with. A policy that needs the geodata (geosite:, rule-set:)
// stays with the apps: a node has none, and mihomo would refuse the whole config over it.
// nil: nothing is set, and the node keeps the defaults of its own.
func dnsOf(r subs.Routes) *nodeapi.DNS {
	d := nodeapi.DNS{
		Nameserver:  r.DNS.Nameserver,
		Default:     r.DNS.Default,
		ProxyServer: r.DNS.ProxyServer,
	}
	for _, p := range r.DNS.Policy {
		if geoKey(p.Match) {
			continue
		}
		if len(p.Servers) > 0 {
			d.Policy = append(d.Policy, nodeapi.DNSPolicy{Match: p.Match, Servers: p.Servers})
		}
	}
	if len(d.Nameserver) == 0 && len(d.Default) == 0 && len(d.ProxyServer) == 0 && len(d.Policy) == 0 {
		return nil
	}
	return &d
}

// geoKey tells a policy key that names the geodata from one that names domains. The apps
// carry the geodata; the list form ("geosite:a,b") is one too.
func geoKey(match string) bool {
	m := strings.ToLower(match)
	return strings.HasPrefix(m, "geosite:") || strings.HasPrefix(m, "rule-set:") || strings.Contains(m, "geosite:")
}
