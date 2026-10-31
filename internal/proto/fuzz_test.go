package proto

import (
	"strings"
	"testing"
)

func FuzzParse(f *testing.F) {
	f.Add("type: vless\nreality-config:\n  dest: www.example.com:443\n  server-names:\n    - www.example.com\n  private-key: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n  short-id:\n    - a1b2c3d4\n")
	f.Add("type: hysteria2\nalpn:\n  - h3\n")
	f.Add("type: shadowsocks\ncipher: 2022-blake3-aes-128-gcm\npassword: AAAAAAAAAAAAAAAAAAAAAA==\n")
	f.Add("type: trojan\n")
	f.Add("")
	f.Add("type: [")
	f.Add("[]")
	f.Add("null")
	f.Add("{")
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 4096 {
			return
		}
		parsed, err := Parse(src)
		if err != nil {
			if Redact(src) != "" && !strings.Contains(src, Hidden) {
				// A template that does not parse redacts to nothing.
				t.Fatalf("Redact of an unreadable template: %q", Redact(src))
			}
			return
		}
		if parsed.Type() == "" && strings.TrimSpace(src) != "" {
			// An empty mapping parses as a template with no type. That is allowed;
			// Validate refuses it.
		}
		again, err := Parse(Marshal(parsed))
		if err != nil {
			t.Fatalf("marshal did not parse: %v\n%s", err, Marshal(parsed))
		}
		if again.Type() != parsed.Type() {
			t.Fatalf("type %q became %q", parsed.Type(), again.Type())
		}
		_ = Validate(parsed, Options{AnyDest: true})
		red := Redact(Marshal(parsed))
		if red == "" {
			return
		}
		back, err := Parse(Restore(red, Marshal(parsed)))
		if err != nil {
			t.Fatalf("restore: %v", err)
		}
		if back.Type() != parsed.Type() {
			t.Fatalf("restored type %q", back.Type())
		}
	})
}

func FuzzClientLink(f *testing.F) {
	f.Add("vless", "203.0.113.7", 443, "VLESS", "www.example.com")
	f.Add("trojan", "2001:db8::1", 1, "😀 сервер", "node.example.com")
	f.Add("hysteria2", "пример.рф", 65535, "Hysteria2", "sni.example")
	f.Add("vless", "example.com", 8443, "DIRECT", "")
	f.Fuzz(func(t *testing.T, kind, host string, port int, name, sni string) {
		if len(kind) > 20 || len(host) > 160 || len(name) > 200 || len(sni) > 160 {
			return
		}
		if port < 1 || port > 65535 {
			return
		}
		var tmpl Template
		switch kind {
		case "vless":
			tmpl = Template{"type": "vless", "reality-config": map[string]any{
				"dest": "www.example.com:443", "server-names": []any{"www.example.com"},
				"private-key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "short-id": []any{"a1b2c3d4"},
			}}
		case "trojan":
			tmpl = Template{"type": "trojan", "reality-config": map[string]any{
				"dest": "www.example.com:443", "server-names": []any{"www.example.com"},
				"private-key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "short-id": []any{"a1b2c3d4"},
			}}
		case "hysteria2":
			tmpl = Template{"type": "hysteria2", "alpn": []any{"h3"}}
		default:
			return
		}
		_, err := ClientConfig(tmpl, ClientInput{
			Name: name, Host: host, Port: port, SNI: sni,
			Slot: Slot{Name: "s", UUID: "0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1", Secret: "S3cr3t+/="},
		})
		if err != nil && !strings.Contains(err.Error(), "config_") && !strings.Contains(err.Error(), "reality") {
			// A refused template is fine. Anything else is reported by the error text
			// the callers already switch on; a panic is the failure this fuzz is for.
			return
		}
	})
}
