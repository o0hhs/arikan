package subs

import (
	"strings"
	"testing"
	"unicode/utf8"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/store/db"
)

// Fuzz targets run their seed corpus under ordinary go test. go test -fuzz explores
// further; a crash found that way is added here with f.Add so the next run keeps it.

func FuzzFormatAgreesWithTheNode(f *testing.F) {
	for _, ua := range []string{
		"", "curl/8.9", "Mozilla/5.0",
		"clash-verge/v2.2.3", "mihomo/1.19.31", "Koala-Clash/1.4.0", "kabosu/1.0.0",
		"Prizrak-Box/1.4.2 (windows)", "Flowvy/0.9", "Murge/2.0", "Rabbit/1.1 (macos)",
		"Stash/3.1.1 Clash/1.9.0",
		"Happ/3.4.1", "v2rayNG/1.10.2", "v2RayTun/Android", "INCY/2.0.8", "v2rayN/6.31",
		"HiddifyNext/2.5.7 (android) like ClashMeta v2ray sing-box",
		"HiddifyNext/4.1.1 (android) like ClashMeta v2ray sing-box",
		"Karing/1.2.26 platform/tvos sing-box 1.14.0", "NekoBox/1.3.7",
		"SFA (sing-box 1.14.2; language en_US)", "SFI (sing-box 1.11.4; language en_US)",
		"SFM (sing-box 1.12.20; language ru_RU)", "SFA/1.12.4", "SFA/1.10.0", "SFT/1.13.0",
		"Streisand/1.6", "Shadowrocket/2.2.60", "foxray/1.0",
	} {
		f.Add(ua, "*/*", "")
		f.Add(ua, "text/html", "")
		f.Add(ua, "Text/HTML", " clash ")
	}
	f.Add("INCY/2.1.0", "", "singbox")
	f.Add("Happ/3", "", "uri")
	f.Fuzz(func(t *testing.T, ua, accept, query string) {
		if len(ua) > 400 || len(accept) > 120 || len(query) > 32 {
			return
		}
		if Format(ua, accept, query) != nodeapi.SubFormat(query, ua, accept) {
			t.Fatalf("Format(%q, %q, %q) = %s, node = %s", ua, accept, query, Format(ua, accept, query), nodeapi.SubFormat(query, ua, accept))
		}
	})
}

func FuzzParseRules(f *testing.F) {
	f.Add("DOMAIN-SUFFIX,sber.ru,DIRECT\nGEOIP,telegram,PROXY,no-resolve\n")
	f.Add("# comment\n\nIP-CIDR,10.0.0.0/8,DIRECT,no-resolve\nIP-CIDR6,2001:db8::/32,REJECT\n")
	f.Add("DST-PORT,1-65535/443,PROXY\nNETWORK,udp,REJECT-DROP\n")
	f.Add("DOMAIN,example.com,DIRECT\nNOT A RULE\n")
	f.Add("DOMAIN-REGEX,^a+$,REJECT\n")
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 4000 {
			return
		}
		rules, err := ParseRules(text, Groups{Main: "VPN", Auto: "Авто"})
		if err != nil {
			return
		}
		if len(rules) > MaxRules {
			t.Fatalf("%d rules", len(rules))
		}
		for _, r := range rules {
			if strings.Contains(r, "\n") || strings.TrimSpace(r) != r || !utf8.ValidString(r) {
				t.Fatalf("rule %q", r)
			}
			again, err := ParseRules(r, Groups{Main: "VPN", Auto: "Авто"})
			if err != nil || len(again) != 1 || again[0] != r {
				t.Fatalf("rule %q did not round-trip: %v %q", r, err, again)
			}
		}
	})
}

func FuzzXrayOutbound(f *testing.F) {
	f.Add("vless://0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1@203.0.113.7:443?encryption=none&security=reality&sni=www.example.com&fp=chrome&pbk=abc&sid=12&type=tcp#n")
	f.Add("vless://0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1@203.0.113.7:8443?encryption=none&flow=xtls-rprx-vision&security=tls&sni=node.example.com&type=tcp#n")
	f.Add("trojan://secret@198.51.100.32:443?security=tls&sni=x.example&type=tcp#NL")
	f.Add("ss://2022-blake3-aes-128-gcm:c2VjcmV0@198.51.100.31:8388#SS")
	f.Add("hysteria2://pass@203.0.113.7:443/?sni=node.example.com#Hysteria2")
	f.Add("vless://id@[2001:db8::1]:443?encryption=none&type=tcp#n")
	f.Add("not a link")
	f.Add("")
	f.Fuzz(func(t *testing.T, link string) {
		if len(link) > 4096 {
			return
		}
		out, ok := xrayOutbound(link)
		if !ok {
			return
		}
		if str(out["protocol"]) == "" {
			t.Fatal("an outbound without a protocol")
		}
		sig := outboundSig(out)
		if sig.host == "" || sig.port == "" {
			t.Fatalf("outbound %+v", out)
		}
	})
}

func FuzzRenderProfile(f *testing.F) {
	f.Add("🇳🇱 Нидерланды", "203.0.113.7", "www.example.com", "443", "VLESS")
	f.Add("😀", "2001:db8::1", "пример.рф", "65535", "DIRECT")
	f.Add("Germany", "пример.рф", "under_score.example", "1", "")
	f.Add("true", "not a host", "sni\nbad", "20000-20010", "null")
	f.Add(strings.Repeat("я", 80), "example.com", strings.Repeat("a", 60)+".com", "0", "PROXY")
	f.Add("node", "203.0.113.7", "", "99999", "   ")
	// "%" is not an authority: the link would not parse.
	f.Add("0", "%", "0", "0", "0")
	f.Fuzz(func(t *testing.T, node, host, sni, port, name string) {
		if len(node) > 160 || len(host) > 160 || len(sni) > 200 || len(port) > 32 || len(name) > 300 {
			return
		}
		p := Profile{
			Slot: goldenSlot,
			Inbounds: []db.Inbound{
				vlessIn(1, 1, "vless", port, name),
				hy2In(2, 1, "hy2", "443", "Hysteria2"),
			},
			Nodes:  []Node{{ID: 1, Name: node, Endpoint: Endpoint{Host: host, SNI: sni}}},
			Direct: []string{host, "ok.example.com", "bad,host"},
		}
		assertSubscription(t, p)
	})
}

func FuzzParseList(f *testing.F) {
	f.Add("payload:\n  - DOMAIN-SUFFIX,example.com\n  - DOMAIN,a.com\n")
	f.Add("# comment\nDOMAIN-SUFFIX,example.com\n\nDOMAIN,a.com\n")
	f.Add("not: yaml: [")
	f.Add("")
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 8000 {
			return
		}
		got := parseList([]byte(text))
		for _, line := range got {
			if line == "" || strings.Contains(line, "\n") {
				t.Fatalf("entry %q", line)
			}
		}
	})
}

func FuzzFillTitle(f *testing.F) {
	f.Add("{brand} {name}", "Arikan", "Аня")
	f.Add("line\n\tnext {name}", "бренд", "😀")
	f.Add(strings.Repeat("я", 50)+" {days}", "x", "")
	f.Add("", "", "")
	f.Fuzz(func(t *testing.T, tmpl, brand, name string) {
		if len(tmpl) > 2000 || len(brand) > 200 || len(name) > 200 {
			return
		}
		vars := map[string]string{"brand": brand, "name": name, "days": "3"}
		title := fillTitle(tmpl, vars)
		if utf8.RuneCountInString(title) > TitleMax {
			t.Fatalf("title is %d runes", utf8.RuneCountInString(title))
		}
		if strings.ContainsAny(title, "\r\n\t") {
			t.Fatalf("title %q", title)
		}
		file := fileName(title)
		if file == "" || strings.ContainsAny(file, "/\\:*?\"<>|") {
			t.Fatalf("file name %q of %q", file, title)
		}
		ann := fillAnnounce(tmpl, vars)
		if utf8.RuneCountInString(ann) > AnnounceMax {
			t.Fatalf("announce is %d runes", utf8.RuneCountInString(ann))
		}
		if n := len(strings.Split(ann, "\n")); ann != "" && n > AnnounceLines {
			t.Fatalf("%d lines", n)
		}
	})
}

func FuzzHappRouting(f *testing.F) {
	f.Add("DOMAIN-SUFFIX,example.com,DIRECT\nGEOIP,ru,PROXY\nMATCH,PROXY\n")
	f.Add("IP-CIDR,10.0.0.0/8,REJECT,no-resolve\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, rules string) {
		if len(rules) > 4000 {
			return
		}
		link := HappRoutingOf("test", strings.Split(rules, "\n"))
		if !strings.HasPrefix(link, "happ://routing/onadd/") {
			t.Fatalf("link %q", link)
		}
		incy := incyLink(link)
		if !strings.HasPrefix(incy, "incy://routing/onadd/") {
			t.Fatalf("incy %q", incy)
		}
	})
}
