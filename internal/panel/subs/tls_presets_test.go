package subs

import (
	"encoding/json"
	"net/url"
	"testing"

	"mikan/internal/panel/presets"
	"mikan/internal/proto"
)

// VLESS TLS runs on the node certificate: the listener gets the certificate files and no
// REALITY, clients verify the panel's domain like any website, and a self-signed
// certificate is pinned instead. XHTTP stays with the apps that speak it.
func TestVLESSTLSPresets(t *testing.T) {
	cert := proto.Cert{CertPath: "/data/tls/node.crt", KeyPath: "/data/tls/node.key"}
	slot := proto.Slot{Name: "s000001", UUID: "0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1", Secret: "x"}
	for _, id := range []string{presets.PresetTLSXHTTP, presets.PresetTLSVision, presets.PresetCDN, presets.PresetCDNGET} {
		src, err := presets.NewConfig(id, "")
		if err != nil {
			t.Fatal(err)
		}
		tpl, err := proto.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		if tpl["reality-config"] != nil || tpl.Ext().TLS != "node" {
			t.Fatalf("%s: %s", id, src)
		}
		l, err := proto.Listener(tpl, "in", "", "2443", []proto.Slot{slot}, cert, proto.Options{})
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if l["certificate"] != cert.CertPath || l["private-key"] != cert.KeyPath || l["reality-config"] != nil {
			t.Fatalf("%s listener: %v", id, l)
		}

		c, err := proto.ClientConfig(tpl, proto.ClientInput{Name: "N", Host: "panel.example.com", Port: 2443, PortSpec: "2443", SNI: "panel.example.com", Slot: slot})
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		u, err := url.Parse(c.URI)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		if u.Scheme != "vless" || q.Get("security") != "tls" || q.Get("sni") != "panel.example.com" || q.Get("fp") != proto.DefaultFingerprint ||
			q.Get("pbk") != "" || q.Get("insecure") != "" || q.Get("allowInsecure") != "" || q.Get("pcs") != "" {
			t.Fatalf("%s link: %s", id, c.URI)
		}
		m := c.Mihomo
		if m["tls"] != true || m["servername"] != "panel.example.com" || m["reality-opts"] != nil || m["fingerprint"] != nil || m["skip-cert-verify"] != nil {
			t.Fatalf("%s mihomo: %v", id, m)
		}
		sb, ok := singboxTLS(m)
		if !ok || sb["server_name"] != "panel.example.com" || sb["reality"] != nil || sb["insecure"] != nil {
			t.Fatalf("%s sing-box tls: %v", id, sb)
		}

		needs := proto.NeedsOf(tpl)
		xray, singbox := (App{Family: FamilyXray}).Supports(needs), (App{Family: FamilySingBox}).Supports(needs)
		switch id {
		case presets.PresetTLSXHTTP:
			if q.Get("type") != "xhttp" || q.Get("path") == "" || q.Get("flow") != "" || !xray || singbox {
				t.Fatalf("xhttp: %s xray=%v sing-box=%v", c.URI, xray, singbox)
			}
		case presets.PresetCDN:
			// The CDN inbound is XHTTP on the node certificate, in the mode a CDN carries:
			// one POST up, one streaming GET down. A GET uplink would need packet-up, which
			// mihomo's server refuses, so the mode is pinned here by a test.
			if q.Get("type") != "xhttp" || q.Get("mode") != "stream-one" || q.Get("path") == "" || q.Get("flow") != "" || !xray || singbox {
				t.Fatalf("cdn: %s xray=%v sing-box=%v", c.URI, xray, singbox)
			}
			// The distribution domain lives in "behind a proxy"; with none the client gets
			// the node's own address, which is what this call passes.
			if c.Mihomo["server"] != "panel.example.com" || c.Mihomo["network"] != "xhttp" {
				t.Fatalf("cdn mihomo: %v", c.Mihomo)
			}
			// No host is baked into xhttp-config: the admin sets it with the CDN.
			if opts, _ := c.Mihomo["xhttp-opts"].(map[string]any); opts != nil {
				if h, set := opts["host"]; set && h != "" {
					t.Fatalf("cdn opts carry a host: %v", opts)
				}
			}
		case presets.PresetCDNGET:
			// The GET-shaped CDN preset: packet-up with the uplink by GET and the payload
			// in X-Data headers baked into the template, both ends read them through
			// xhttpShared (mihomo's options and Xray's extra carry the same keys).
			if q.Get("type") != "xhttp" || q.Get("mode") != "packet-up" || q.Get("path") == "" || !xray || singbox {
				t.Fatalf("cdn get: %s xray=%v sing-box=%v", c.URI, xray, singbox)
			}
			var extra map[string]any
			if e := q.Get("extra"); e == "" || json.Unmarshal([]byte(e), &extra) != nil {
				t.Fatalf("cdn get extra: %q", q.Get("extra"))
			}
			if extra["uplinkHTTPMethod"] != "GET" || extra["uplinkDataPlacement"] != "header" || extra["uplinkDataKey"] != "X-Data" {
				t.Fatalf("cdn get extra keys: %v", extra)
			}
			opts, _ := c.Mihomo["xhttp-opts"].(map[string]any)
			if opts["uplink-http-method"] != "GET" || opts["uplink-data-placement"] != "header" || opts["uplink-data-key"] != "X-Data" {
				t.Fatalf("cdn get mihomo opts: %v", opts)
			}
		case presets.PresetTLSVision:
			if q.Get("type") != "tcp" || q.Get("flow") != "xtls-rprx-vision" || !xray || !singbox {
				t.Fatalf("vision: %s xray=%v sing-box=%v", c.URI, xray, singbox)
			}
			if us := l["users"].([]map[string]any); us[0]["flow"] != "xtls-rprx-vision" {
				t.Fatalf("vision users: %v", us)
			}
		}

		// No public certificate: the self-signed one is pinned where each app reads a pin.
		c, err = proto.ClientConfig(tpl, proto.ClientInput{Name: "N", Host: "203.0.113.7", Port: 2443, PortSpec: "2443", PinSHA256: "ab12", Slot: slot})
		if err != nil {
			t.Fatal(err)
		}
		u, _ = url.Parse(c.URI)
		if u.Query().Get("pcs") != "ab12" || c.Mihomo["fingerprint"] != "ab12" {
			t.Fatalf("%s pinned: %s %v", id, c.URI, c.Mihomo)
		}
	}
}
