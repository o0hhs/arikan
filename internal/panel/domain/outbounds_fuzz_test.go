package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzCheckOutboundLink(f *testing.F) {
	f.Add("vless://11111111-2222-3333-4444-555555555555@198.51.100.30:443?security=tls#NL%20Amsterdam")
	f.Add("vless://11111111-2222-3333-4444-555555555555@[2001:db8::1]:443")
	f.Add("trojan://secret@198.51.100.32:443?sni=x.example")
	f.Add("ss://YWVzLTI1Ni1nY206c2VjcmV0@198.51.100.31:8388#SS")
	f.Add("ss://YWVzLTI1Ni1nY206c2VjcmV0QDE5OC41MS4xMDAuMzE6ODM4OA")
	f.Add("vmess://eyJ2IjoiMiIsInBzIjoiVk0iLCJhZGQiOiIxOTguNTEuMTAwLjMzIiwicG9ydCI6IjQ0MyJ9")
	f.Add("hysteria2://pass@198.51.100.34:8443?sni=x.example#😀")
	f.Add("tuic://11111111-2222-3333-4444-555555555555:secret@203.0.113.9:443")
	f.Add("hy2://pass@[2001:db8::2]:443#name")
	f.Add("")
	f.Add("not a link")
	f.Add("foo://x@203.0.113.9:1")
	f.Add("ss://\xf6")
	f.Add("vless://11111111-2222-3333-4444-555555555555@%:443")
	f.Add("ss://#%800")
	f.Fuzz(func(t *testing.T, link string) {
		if len(link) > 5000 {
			return
		}
		got, err := CheckOutboundLink(link, []string{"203.0.113.5", "vpn.example.com", "example.org:8443"})
		if err != nil {
			return
		}
		if got.Link == "" || strings.ContainsAny(got.Link, "\n\r\t ") {
			t.Fatalf("kept %q", got.Link)
		}
		if got.Kind == "" || strings.ToLower(got.Kind) != got.Kind {
			t.Fatalf("kind %q", got.Kind)
		}
		if !utf8.ValidString(got.Name) || !utf8.ValidString(got.Host) {
			t.Fatalf("name %q host %q", got.Name, got.Host)
		}
		if strings.ContainsAny(got.Host, " \n") {
			t.Fatalf("host %q", got.Host)
		}
		// The same link reads the same way a second time.
		again, err := CheckOutboundLink(got.Link, nil)
		if err != nil || again.Kind != got.Kind || again.Host != got.Host {
			t.Fatalf("reread %+v (%v), was %+v", again, err, got)
		}
	})
}
