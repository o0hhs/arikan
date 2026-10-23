package subs

import (
	"fmt"
	"net/url"
	"strconv"
)

// MTProxy is one MTProto proxy for Telegram itself, as the admin sets it (settings.KeyMTProto):
// an outside server the panel only hands links to. Telegram's own censorship of the messenger
// is what it answers — the tunnel needs a working subscription, this works anyway.
type MTProxy struct {
	Server string `json:"server"`
	Port   int    `json:"port"`
	Secret string `json:"secret"`
	Name   string `json:"name,omitempty"`
}

// Link is the proxy as a t.me share link: Telegram opens it from any tap, in the browser
// included, and adds the proxy to the app's own settings.
func (p MTProxy) Link() string {
	name := p.Name
	if name == "" {
		name = p.Server
	}
	return fmt.Sprintf("https://t.me/proxy?server=%s&port=%d&secret=%s", url.QueryEscape(p.Server), p.Port, url.QueryEscape(p.Secret)) +
		"&" + url.Values{"bot": {"false"}, "name": {name}}.Encode()
}

// TGLink is the proxy as the tg:// scheme, for a tap inside Telegram itself.
func (p MTProxy) TGLink() string {
	return fmt.Sprintf("tg://proxy?server=%s&port=%s&secret=%s", url.QueryEscape(p.Server), strconv.Itoa(p.Port), url.QueryEscape(p.Secret))
}

// mtLinks reads the proxies of a Config as the page and the bot hand them out: each with
// both of its links ready.
func mtLinks(ps []MTProxy) []MTLink {
	out := make([]MTLink, 0, len(ps))
	for _, p := range ps {
		if p.Server == "" || p.Port <= 0 || p.Secret == "" {
			continue
		}
		out = append(out, MTLink{Name: p.Name, Link: p.Link(), TGLink: p.TGLink()})
	}
	return out
}

// MTLink is one MTProto proxy as the page reads it.
type MTLink struct {
	Name   string `json:"name,omitempty"`
	Link   string `json:"link"`
	TGLink string `json:"tg"`
}
