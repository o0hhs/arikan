package node

import (
	"encoding/json"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"

	"mikan/internal/nodeapi"
	"mikan/internal/proto"
)

const relayKey = "SN3ZEwGwUfpBNbJ6EXWCIqaUD0g8F3iKTi8NJnHaX0s"

func cascadeState(t *testing.T) nodeapi.DesiredState {
	t.Helper()
	st := warpState()
	relay := proto.Template{"type": "vless", "reality-config": map[string]any{"dest": "www.microsoft.com:443", "private-key": relayKey,
		"short-id": []any{"a1b2c3d4"}, "server-names": []any{"www.microsoft.com"}}}
	st.Relay = &nodeapi.Relay{Port: "7443", Config: relay.JSON(), Users: []nodeapi.Slot{{Name: "relay-2", UUID: "0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1"}}}
	exit, _ := json.Marshal(map[string]any{"name": "ignored", "type": "vless", "server": "198.51.100.30", "port": 7443,
		"uuid": "1c5eed5d-8d5a-4b47-8e73-7a2b55c9d5f2", "tls": true, "servername": "www.microsoft.com", "network": "tcp", "udp": true,
		"client-fingerprint": "chrome", "reality-opts": map[string]any{"public-key": "kBPI_JRK3J0k3ShNb_g_6R1H4G4sdvIdC6V4hxGjyAw", "short-id": "a1b2c3d4"}})
	// The relay's own traffic leaves through node 3; in-anytls through node 3 too, which
	// wins over its WARP entry (exits come first).
	st.Exits = []nodeapi.Exit{{Name: nodeapi.ExitName(3), Proxy: exit, Inbounds: []string{nodeapi.RelayListener, "in-anytls", "in-gone"}}}
	return st
}

// A cascade: the relay listener and the exits parse with mihomo, exit rules come after
// the REJECT ones and before WARP's, and only listeners that exist are named.
func TestCascadeConfig(t *testing.T) {
	st := cascadeState(t)
	raw, _, err := buildConfig(st, proto.Cert{CertPath: "/tmp/c.pem", KeyPath: "/tmp/k.pem"}, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Proxies   []map[string]any `json:"proxies"`
		Rules     []string         `json:"rules"`
		Listeners []map[string]any `json:"listeners"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(cfg.Rules, "\n")
	want := "DST-PORT,25,REJECT\nIN-NAME,mikan~relay,NODE-3\nIN-NAME,in-anytls,NODE-3\nDOMAIN-SUFFIX,openai.com,WARP"
	if !strings.Contains(got, want) || strings.Contains(got, "in-gone") || strings.Contains(got, "IN-NAME,in-anytls,WARP") {
		t.Fatalf("rules:\n%s", got)
	}
	var relay map[string]any
	for _, l := range cfg.Listeners {
		if l["name"] == nodeapi.RelayListener {
			relay = l
		}
	}
	users, _ := relay["users"].([]any)
	if relay == nil || relay["port"] != "7443" || len(users) != 1 {
		t.Fatalf("relay listener: %v", relay)
	}
	names := []string{}
	for _, p := range cfg.Proxies {
		names = append(names, p["name"].(string))
	}
	if strings.Join(names, ",") != "WARP,NODE-3" {
		t.Fatalf("proxies: %v", names)
	}
	raw = warpAsSocks5(t, raw)
	parsed, err := executor.ParseWithBytes(raw)
	if err != nil {
		t.Fatalf("mihomo refuses the config: %v", err)
	}
	if _, ok := parsed.Proxies["NODE-3"]; !ok {
		t.Fatal("NODE-3 not parsed")
	}
	if _, ok := parsed.Listeners[nodeapi.RelayListener]; !ok {
		t.Fatal("relay listener not parsed")
	}

	// Only VLESS relays and NODE-<id> names.
	bad := cascadeState(t)
	bad.Exits[0].Proxy = json.RawMessage(`{"type":"socks5","server":"1.2.3.4","port":1}`)
	if _, _, err := buildConfig(bad, proto.Cert{}, false, 0); err == nil {
		t.Fatal("a non-VLESS exit was accepted")
	}
	bad = cascadeState(t)
	bad.Exits[0].Name = "DIRECT"
	if _, _, err := buildConfig(bad, proto.Cert{}, false, 0); err == nil {
		t.Fatal("an exit named like a built-in policy was accepted")
	}
	if routesKey(cascadeState(t), false) == routesKey(warpState(), false) {
		t.Fatal("routesKey must follow the exits")
	}
}

// recordingTunnel stands for mihomo's tunnel: it notes what reached it.
type recordingTunnel struct{ tcp atomic.Int32 }

func (r *recordingTunnel) HandleTCPConn(conn net.Conn, _ *C.Metadata) { r.tcp.Add(1); _ = conn.Close() }
func (r *recordingTunnel) HandleUDPPacket(C.UDPPacket, *C.Metadata)   {}
func (r *recordingTunnel) NatTable() C.NatTable                       { return nil }

// The relay's connections reach mihomo without a subscriber; any other unknown user is
// turned away.
func TestRelayBypassesAccounting(t *testing.T) {
	inner := &recordingTunnel{}
	tun := &Tunnel{inner: inner, reg: NewRegistry("e", 0, time.Minute, time.Now)}
	a, b := net.Pipe()
	defer b.Close()
	tun.HandleTCPConn(a, &C.Metadata{Type: C.VLESS, InName: nodeapi.RelayListener, InUser: "relay-2", SrcIP: mustAddr("198.51.100.20")})
	c, d := net.Pipe()
	defer d.Close()
	tun.HandleTCPConn(c, &C.Metadata{Type: C.VLESS, InName: "in-vless", InUser: "relay-2", SrcIP: mustAddr("198.51.100.20")})
	if inner.tcp.Load() != 1 {
		t.Fatalf("connections that reached mihomo: %d, want only the relay's", inner.tcp.Load())
	}
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

// byLinkInbound is a listener the outbound tests send through: a VLESS REALITY one, the
// only kind a node runs without a certificate of its own.
func byLinkInbound(t *testing.T) nodeapi.Inbound {
	t.Helper()
	tmpl := proto.Template{"type": "vless", "reality-config": map[string]any{"dest": "www.microsoft.com:443", "private-key": relayKey,
		"short-id": []any{"a1b2c3d4"}, "server-names": []any{"www.microsoft.com"}}}
	if err := proto.Validate(tmpl, proto.Options{}); err != nil {
		t.Fatal(err)
	}
	return nodeapi.Inbound{Name: "in-vless", Port: "8443", Config: tmpl.JSON()}
}

// An outbound the admin added by link is read by the node itself (mihomo's own parser),
// whichever protocol it is, and its inbounds are sent through it like through a relay.
func TestOutboundByLink(t *testing.T) {
	st := warpState()
	st.Inbounds = append(st.Inbounds, byLinkInbound(t))
	st.Exits = []nodeapi.Exit{
		{Name: nodeapi.OutboundName(7), Link: "vless://11111111-2222-3333-4444-555555555555@198.51.100.30:443?encryption=none&security=tls&sni=example.com&type=ws#Token", Inbounds: []string{"in-vless"}},
		{Name: nodeapi.OutboundName(8), Link: "ss://YWVzLTI1Ni1nY206c2VjcmV0@198.51.100.31:8388#SS node", Inbounds: []string{"in-missing"}},
		{Name: nodeapi.OutboundName(9), Link: "trojan://secret@198.51.100.32:443?sni=example.com", Inbounds: []string{"in-gone"}},
	}
	raw, rejected, err := buildConfig(st, proto.Cert{}, false, 0)
	if err != nil {
		t.Fatalf("a link outbound broke the config: %v", err)
	}
	for _, r := range rejected {
		if nodeapi.IsExitName(r.Name) {
			t.Fatalf("an outbound was rejected: %+v", r)
		}
	}
	var cfg struct {
		Proxies []map[string]any `json:"proxies"`
		Rules   []string         `json:"rules"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	found := map[string]map[string]any{}
	for _, p := range cfg.Proxies {
		found[p["name"].(string)] = p
	}
	if p := found[nodeapi.OutboundName(7)]; p == nil || p["type"] != "vless" || p["server"] != "198.51.100.30" || p["uuid"] != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("vless link read as %+v", p)
	}
	if p := found[nodeapi.OutboundName(8)]; p == nil || p["type"] != "ss" || p["cipher"] != "aes-256-gcm" || p["server"] != "198.51.100.31" {
		t.Fatalf("ss link read as %+v", p)
	}
	if p := found[nodeapi.OutboundName(9)]; p == nil || p["type"] != "trojan" || p["server"] != "198.51.100.32" {
		t.Fatalf("trojan link read as %+v", p)
	}
	if got := strings.Join(cfg.Rules, "\n"); !strings.Contains(got, "IN-NAME,in-vless,OUT-7") {
		t.Fatalf("the inbound does not leave through the outbound:\n%s", got)
	}
	// Only the listeners that exist are named, whatever the kind of outbound.
	for _, name := range []string{"in-missing", "in-gone"} {
		if strings.Contains(strings.Join(cfg.Rules, "\n"), name) {
			t.Fatalf("a listener that does not exist is named: %s", name)
		}
	}
	raw = warpAsSocks5(t, raw)
	parsed, err := executor.ParseWithBytes(raw)
	if err != nil {
		t.Fatalf("mihomo refuses the config: %v", err)
	}
	for _, id := range []int64{7, 8, 9} {
		if _, ok := parsed.Proxies[nodeapi.OutboundName(id)]; !ok {
			t.Fatalf("%s not parsed by mihomo", nodeapi.OutboundName(id))
		}
	}
}

// A link that does not read is left out and said so, and the rest of the state arrives: a
// typo in one link must not hold up users, quotas and the other outbounds.
func TestBrokenLinkIsRejectedAlone(t *testing.T) {
	st := warpState()
	st.Inbounds = append(st.Inbounds, byLinkInbound(t))
	st.Exits = []nodeapi.Exit{
		{Name: nodeapi.OutboundName(4), Link: "vless://broken", Inbounds: []string{"in-vless"}},
		{Name: nodeapi.OutboundName(5), Link: "trojan://secret@198.51.100.32:443", Inbounds: []string{}},
	}
	raw, rejected, err := buildConfig(st, proto.Cert{}, false, 0)
	if err != nil {
		t.Fatalf("the whole state was refused: %v", err)
	}
	var bad []nodeapi.ListenerStatus
	for _, r := range rejected {
		if nodeapi.IsExitName(r.Name) {
			bad = append(bad, r)
		}
	}
	if len(bad) != 1 || bad[0].Name != nodeapi.OutboundName(4) || bad[0].Error == "" {
		t.Fatalf("rejected = %+v", bad)
	}
	raw = warpAsSocks5(t, raw)
	if _, err := executor.ParseWithBytes(raw); err != nil {
		t.Fatalf("the rest of the config does not parse: %v", err)
	}
	// The broken one is not in the config: its inbound falls through the rules, it is not
	// sent to a proxy that is not there.
	if strings.Contains(string(raw), nodeapi.OutboundName(4)) {
		t.Fatal("the broken outbound reached the config")
	}
	if !strings.Contains(string(raw), nodeapi.OutboundName(5)) {
		t.Fatal("the working outbound was dropped with it")
	}
}
