package nodeapi

import "testing"

func TestSubFormatPicksByQueryAgentAndAccept(t *testing.T) {
	cases := []struct{ query, ua, accept, want string }{
		{"clash", "v2rayNG", "", SubFormatClash},
		{"mihomo", "", "", SubFormatClash},
		{"base64", "", "", SubFormatURI},
		{"sing-box", "", "", SubFormatSingBox},
		{"wl", "", "", SubFormatTurn},
		{"html", "", "", SubFormatHTML},
		{"", "clash-verge/v2.0.0", "", SubFormatClash},
		{"", "ClashMetaForAndroid/2.11.1.Meta", "", SubFormatClash},
		{"", "Stash/2.7.0", "", SubFormatClash},
		{"", "SFI/1.11.0 (sing-box 1.11.0)", "", SubFormatURI}, // a 1.12 config would not load
		{"", "SFA/1.10.0", "", SubFormatURI},
		{"", "SFA/1.12.4", "", SubFormatSingBox},
		{"", "SFA (sing-box 1.14.2; language en_US)", "", SubFormatSingBox},
		{"", "Karing/1.2.26 platform/tvos sing-box 1.14.0", "", SubFormatURI},
		{"", "HiddifyNext/2.5.7 (android) like ClashMeta v2ray sing-box", "", SubFormatURI},
		{"", "Prizrak-Box/1.4.2", "", SubFormatClash},
		{"", "Murge/2.0", "", SubFormatClash},
		{"", "Rabbit/1.1", "", SubFormatClash},
		{"", "Hiddify/2.0", "", SubFormatURI},
		{"", "Happ/3.0", "", SubFormatURI},
		{"xray", "v2rayNG", "", SubFormatXray},
		{"json", "", "", SubFormatXray},
		{"", "Mozilla/5.0", "text/html,application/xhtml+xml", SubFormatHTML},
		{"", "v2rayN/6.31", "", SubFormatURI},
		{"", "", "", SubFormatURI},
	}
	for _, c := range cases {
		if got := SubFormat(c.query, c.ua, c.accept); got != c.want {
			t.Errorf("SubFormat(%q, %q, %q) = %q, want %q", c.query, c.ua, c.accept, got, c.want)
		}
	}
}

func TestPickSubBodyFallsBackToTheLinks(t *testing.T) {
	all := []SubBody{{Format: SubFormatURI, Body: []byte("u")}, {Format: SubFormatClash, Body: []byte("c")}}
	b, ok := PickSubBody(all, "", "ClashMetaForAndroid/2", "")
	if !ok || b.Format != SubFormatClash {
		t.Fatalf("clash: %+v %v", b, ok)
	}
	// The app asks for a format the record does not carry: the links, which every app reads.
	b, ok = PickSubBody(all, "", "SFA (sing-box 1.14.2)", "")
	if !ok || b.Format != SubFormatURI {
		t.Fatalf("sing-box without a body: %+v %v", b, ok)
	}
	b, ok = PickSubBody(all, "turn", "", "")
	if !ok || b.Format != SubFormatURI {
		t.Fatalf("turn without a body: %+v %v", b, ok)
	}
	if _, ok := PickSubBody(nil, "", "", ""); ok {
		t.Fatal("a record with no bodies has one")
	}
	// Happ and INCY take the Xray configs when the record has them. Another app, and an
	// explicit format, keep what they asked for. Without the configs, they get the links.
	withXray := []SubBody{{Format: SubFormatURI, Body: []byte("u")}, {Format: SubFormatXray, Body: []byte("[]")}}
	b, ok = PickSubBody(withXray, "", "Happ/3.4.1", "")
	if !ok || b.Format != SubFormatXray {
		t.Fatalf("happ: %+v %v", b, ok)
	}
	b, ok = PickSubBody(withXray, "", "INCY/2.1.0", "")
	if !ok || b.Format != SubFormatXray {
		t.Fatalf("incy: %+v %v", b, ok)
	}
	b, ok = PickSubBody(withXray, "uri", "Happ/3.4.1", "")
	if !ok || b.Format != SubFormatURI {
		t.Fatalf("happ ?format=uri: %+v %v", b, ok)
	}
	b, ok = PickSubBody(withXray, "", "v2rayNG/1.10.2", "")
	if !ok || b.Format != SubFormatURI {
		t.Fatalf("v2rayng: %+v %v", b, ok)
	}
	b, ok = PickSubBody(all, "", "Happ/3.4.1", "")
	if !ok || b.Format != SubFormatURI {
		t.Fatalf("happ without configs: %+v %v", b, ok)
	}
}
