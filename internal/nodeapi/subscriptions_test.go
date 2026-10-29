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
		{"", "SFI/1.11.0 (sing-box 1.11.0)", "", SubFormatSingBox},
		{"", "SFA/1.10.0", "", SubFormatSingBox},
		{"", "Hiddify/2.0", "", SubFormatURI},
		{"", "Happ/3.0", "", SubFormatURI},
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
	b, ok = PickSubBody(all, "", "SFI/1.11", "")
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
}
