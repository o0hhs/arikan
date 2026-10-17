package node

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/tunnel"
	"github.com/miekg/dns"

	"mikan/internal/nodeapi"
	"mikan/internal/proto"
)

// sectionOf is the built config's top-level key, decoded.
func sectionOf(t *testing.T, st nodeapi.DesiredState, key string) map[string]any {
	t.Helper()
	raw, _, err := buildConfig(st, proto.Cert{}, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	s, _ := cfg[key].(map[string]any)
	if s == nil {
		t.Fatalf("no %q in the config", key)
	}
	return s
}

func stringList(t *testing.T, v any) []string {
	t.Helper()
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("not a list: %#v", v)
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, x.(string))
	}
	return out
}

// The node resolves with the panel's servers, and the entries it cannot use are dropped
// rather than refused: a "#group" names a group of an app's profile, a geosite: policy
// needs the geodata.
func TestDNSSection(t *testing.T) {
	st := nodeapi.DesiredState{DNS: &nodeapi.DNS{
		Nameserver: []string{"77.88.8.8", "https://1.1.1.1/dns-query#Proxy"},
		Default:    []string{"system", " "},
		Policy: []nodeapi.DNSPolicy{
			{Match: "+.ru", Servers: []string{"77.88.8.8"}},
			{Match: "geosite:category-ru", Servers: []string{"77.88.8.8"}},
			{Match: "rule-set:private", Servers: []string{"77.88.8.8"}},
			{Match: "lost.all", Servers: []string{"#Proxy"}},
		},
	}}
	d := sectionOf(t, st, "dns")
	if d["enable"] != true || d["listen"] != "" || d["enhanced-mode"] != "redir-host" {
		t.Fatalf("dns: %#v", d)
	}
	if got := stringList(t, d["nameserver"]); !slices.Equal(got, []string{"77.88.8.8", "https://1.1.1.1/dns-query"}) {
		t.Fatalf("nameserver = %v", got)
	}
	if got := stringList(t, d["default-nameserver"]); !slices.Equal(got, []string{"system"}) {
		t.Fatalf("default-nameserver = %v", got)
	}
	policy, _ := d["nameserver-policy"].(map[string]any)
	if len(policy) != 1 {
		t.Fatalf("nameserver-policy = %#v", policy)
	}
	if got := stringList(t, policy["+.ru"]); !slices.Equal(got, []string{"77.88.8.8"}) {
		t.Fatalf("+.ru = %v", got)
	}
	// The sniffer reads the site's name off the connection, every port: an app's traffic
	// goes wherever it wants, and the rule that takes it is by domain.
	s := sectionOf(t, st, "sniffer")
	if s["enable"] != true || s["parse-pure-ip"] != true || s["override-destination"] != true {
		t.Fatalf("sniffer: %#v", s)
	}
	sniff, _ := s["sniff"].(map[string]any)
	for _, name := range []string{"HTTP", "TLS", "QUIC"} {
		if sniff[name] == nil {
			t.Fatalf("no %s sniffer: %#v", name, sniff)
		}
	}
}

// A node the panel told nothing resolves with the panel's own defaults: over HTTPS, so a
// resolver on the way cannot answer instead.
func TestDNSDefaults(t *testing.T) {
	d := sectionOf(t, nodeapi.DesiredState{}, "dns")
	if d["enable"] != true {
		t.Fatalf("dns: %#v", d)
	}
	ns := stringList(t, d["nameserver"])
	if len(ns) == 0 || ns[0] != "https://1.1.1.1/dns-query" {
		t.Fatalf("nameserver = %v", ns)
	}
	if boot := stringList(t, d["default-nameserver"]); boot[0] != "system" {
		t.Fatalf("default-nameserver = %v", boot)
	}
	if d["nameserver-policy"] != nil {
		t.Fatalf("policy without settings: %#v", d["nameserver-policy"])
	}
}

// The resolver follows the state: names resolve through the server the panel named, and
// the next state's server takes its place. This is the "Gemini on Xray" case — the node
// answers with a resolver of the panel's, not with the host's own.
func TestResolverFollowsTheState(t *testing.T) {
	addrA, ipA, stopA := dnsStub(t, "203.0.113.7")
	defer stopA()
	addrB, ipB, stopB := dnsStub(t, "203.0.113.8")
	defer stopB()

	apply := func(addr string) {
		t.Helper()
		st := nodeapi.DesiredState{DNS: &nodeapi.DNS{Nameserver: []string{addr}, Default: []string{addr}}}
		raw, _, err := buildConfig(st, proto.Cert{}, false, 0)
		if err != nil {
			t.Fatal(err)
		}
		cfg := parseConfig(t, raw)
		e := &Engine{log: quiet()}
		e.applyDNS(cfg)
	}

	lookup := func(name string) netip.Addr {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ips, err := resolver.LookupIPv4(ctx, name)
		if err != nil || len(ips) == 0 {
			t.Fatalf("lookup %s: %v %v", name, ips, err)
		}
		return ips[0]
	}

	apply(addrA)
	if !tunnel.IsSniffing() {
		t.Fatal("the sniffer is not on: rules by domain would not see the site's name")
	}
	if got := lookup("gemini-a.example"); got != ipA {
		t.Fatalf("first state resolved %s as %s, want %s", "gemini-a.example", got, ipA)
	}
	apply(addrB)
	if got := lookup("gemini-b.example"); got != ipB {
		t.Fatalf("the next state resolved %s as %s, want %s", "gemini-b.example", got, ipB)
	}
}

// dnsStub answers every A query with ip, from a loopback UDP port: an address no other
// resolver on this machine would give out.
func dnsStub(t *testing.T, ip string) (addr string, want netip.Addr, stop func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	want = netip.MustParseAddr(ip)
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := new(dns.Msg)
			if q.Unpack(buf[:n]) != nil || len(q.Question) == 0 {
				continue
			}
			m := new(dns.Msg)
			m.SetReply(q)
			if q.Question[0].Qtype == dns.TypeA || q.Question[0].Qtype == dns.TypeAAAA {
				m.Answer = append(m.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A:   net.ParseIP(ip).To4(),
				})
			}
			if out, err := m.Pack(); err == nil {
				_, _ = pc.WriteTo(out, from)
			}
		}
	}()
	return pc.LocalAddr().String(), want, func() { _ = pc.Close() }
}
