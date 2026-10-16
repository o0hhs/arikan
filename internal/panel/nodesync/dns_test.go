package nodesync

import (
	"testing"

	"mikan/internal/panel/subs"
)

// What the panel sends a node is Settings → Routing as far as a node follows it: the
// resolvers as they are, minus the policies that need the geodata a node has none of.
func TestDnsOf(t *testing.T) {
	if dnsOf(subs.Routes{}) != nil {
		t.Fatal("nothing set must send nothing: the node keeps its defaults")
	}
	d := dnsOf(subs.Routes{DNS: subs.DNS{
		Nameserver: []string{"https://1.1.1.1/dns-query#Auto"},
		Policy: []subs.DNSPolicy{
			{Match: "+.ru", Servers: []string{"77.88.8.8"}},
			{Match: "geosite:category-ru", Servers: []string{"77.88.8.8"}},
			{Match: "rule-set:mikan-blocked", Servers: []string{"77.88.8.8"}},
		},
	}})
	if d == nil {
		t.Fatal("a nameserver alone must reach the node")
	}
	if len(d.Nameserver) != 1 || len(d.Policy) != 1 || d.Policy[0].Match != "+.ru" {
		t.Fatalf("dns = %+v", d)
	}
	// A policy with no servers left is nothing to send.
	if d := dnsOf(subs.Routes{DNS: subs.DNS{Policy: []subs.DNSPolicy{{Match: "+.ru"}}}}); d != nil {
		t.Fatalf("empty policy reached the node: %+v", d)
	}
}
