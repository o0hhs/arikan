package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/metacubex/mihomo/common/convert"

	"mikan/internal/nodeapi"
	"mikan/internal/proto"
)

// listenerNames are the listeners the state runs, the relay and the cascade included:
// rules may name only these.
func listenerNames(st nodeapi.DesiredState) map[string]bool {
	present := map[string]bool{}
	for _, in := range st.Inbounds {
		present[in.Name] = true
	}
	if st.Relay != nil {
		present[nodeapi.RelayListener] = true
	}
	if st.Cascade != nil && st.Cascade.Exit != nil {
		present[nodeapi.CascadeListener] = true
	}
	return present
}

// relayListener is the hidden VLESS REALITY door other nodes of the panel come in by.
func relayListener(r *nodeapi.Relay, cert proto.Cert) (map[string]any, error) {
	t, err := proto.FromJSON(r.Config)
	if err != nil {
		return nil, err
	}
	if t.Type() != "vless" || t["reality-config"] == nil {
		return nil, fmt.Errorf("the relay is VLESS REALITY")
	}
	return proto.Listener(t, nodeapi.RelayListener, "", r.Port, r.Users, cert, proto.Options{})
}

// exitProxyConfig is one outbound the panel named. NODE- is another node's relay or, with
// the cascade's tunnel, the node's own bridge to it: the panel builds that proxy itself, so
// everything about it must be right. OUT- is an outbound the admin added by a share link:
// the node reads the link with mihomo's own parser, which is what the apps read
// subscriptions with — the panel keeps the admin's text and knows nothing of the protocols.
func exitProxyConfig(e nodeapi.Exit) (map[string]any, error) {
	if !nodeapi.IsExitName(e.Name) || !safeRuleValue(e.Name) {
		return nil, fmt.Errorf("exit name %q", e.Name)
	}
	p, err := proxyOf(e)
	if err != nil {
		return nil, fmt.Errorf("exit %s: %w", e.Name, err)
	}
	p["name"] = e.Name
	return p, nil
}

func proxyOf(e nodeapi.Exit) (map[string]any, error) {
	if !strings.HasPrefix(e.Name, "OUT-") {
		var p map[string]any
		if err := json.Unmarshal(e.Proxy, &p); err != nil {
			return nil, err
		}
		switch p["type"] {
		case "vless":
			// The relay of a node older than the cascade: dialled directly, as before.
			return p, nil
		case "socks5":
			// The cascade's exit: the node's own bridge, on its own loopback. An address off
			// the server is the direct dial to the exit that the cascade exists to remove,
			// so a state naming one is refused as a whole rather than carried out.
			if host, _ := p["server"].(string); !loopbackHost(host) {
				return nil, fmt.Errorf("a cascade exit is on loopback, not %q", host)
			}
			// Datagrams do not cross the tunnel (the bridge carries TCP connections); UDP
			// through an exit would have to fall back to a direct path, which is no path here.
			p["udp"] = false
			return p, nil
		default:
			return nil, fmt.Errorf("exit type %v", p["type"])
		}
	}
	proxies, err := convert.ConvertsV2Ray([]byte(e.Link))
	if err != nil {
		return nil, err
	}
	if len(proxies) != 1 {
		return nil, fmt.Errorf("the link is not one outbound (%d read)", len(proxies))
	}
	if t, _ := proxies[0]["type"].(string); t == "" {
		return nil, errors.New("the link names no protocol")
	}
	return proxies[0], nil
}

// exitRules send the inbounds of each exit to that node. An inbound named by several
// exits goes to the first.
func exitRules(st nodeapi.DesiredState, live map[string]bool) []string {
	present := listenerNames(st)
	taken := map[string]bool{}
	var r []string
	for _, e := range st.Exits {
		if !nodeapi.IsExitName(e.Name) || !safeRuleValue(e.Name) || (live != nil && !live[e.Name]) {
			continue
		}
		for _, name := range e.Inbounds {
			if present[name] && !taken[name] && safeRuleValue(name) {
				taken[name] = true
				r = append(r, "IN-NAME,"+name+","+e.Name)
			}
		}
	}
	return r
}
