package subs

import (
	"strings"
	"testing"
)

// The MTProto links carry the proxy's own three values, and the share link names it.
func TestMTProxyLinks(t *testing.T) {
	p := MTProxy{Server: "mt.example", Port: 443, Secret: "ee" + strings.Repeat("a", 30), Name: "Мой"}
	link, tg := p.Link(), p.TGLink()
	if !strings.HasPrefix(link, "https://t.me/proxy?server=mt.example&port=443&secret=") {
		t.Fatalf("share link %q", link)
	}
	if !strings.Contains(link, "name=%D0%9C%D0%BE%D0%B9") {
		t.Fatalf("the proxy is not named in its link: %q", link)
	}
	if tg != "tg://proxy?server=mt.example&port=443&secret="+p.Secret {
		t.Fatalf("tg link %q", tg)
	}
	// mtLinks drops what cannot work.
	if got := mtLinks([]MTProxy{{Server: "x"}, {Server: "mt.example", Port: 443, Secret: "s"}}); len(got) != 1 || got[0].TGLink == "" {
		t.Fatalf("mtLinks %+v", got)
	}
}
