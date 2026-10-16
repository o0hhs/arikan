package domain

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"mikan/internal/panel/store/db"
)

// Outbounds the admin added by a share link: another server of their own (3x-ui and the
// like) that an inbound's traffic leaves through, the way it can leave through another
// node's relay. The node parses the link — mihomo's subscription parser lives there, and
// the panel keeps the admin's text as it was pasted.

var (
	ErrOutboundLink = errors.New("outbound_link") // not one link, or one the node cannot read
	ErrOutboundName = errors.New("outbound_name")
	ErrOutboundSelf = errors.New("outbound_self") // the link points at this panel's own server
	ErrOutboundUsed = errors.New("outbound_used") // an inbound leaves through it
	ErrOutboundFull = errors.New("outbound_full")
)

// MaxOutbounds bounds the list: each one is an extra proxy in the config of every node
// whose inbounds leave through it.
const MaxOutbounds = 50

// MaxOutboundLink bounds the pasted text: a link is a line, and a vmess one carries a
// whole JSON inside.
const MaxOutboundLink = 4096

// maxOutboundName bounds what the panel shows.
const maxOutboundName = 64

// outboundSchemes are the prefixes a link may have: the ones mihomo's subscription parser
// (convert.ConvertsV2Ray) turns into a proxy, which is where the reading really happens.
// The panel checks only this much — that the node will have something to read.
var outboundSchemes = []string{
	"vless", "vmess", "trojan", "ss", "ssr", "hysteria", "hysteria2", "hy2", "hy2+realm", "hysteria2+realm",
	"tuic", "anytls", "socks", "socks5", "socks5h", "http", "https",
}

// OutboundLink is a pasted link as the panel reads it: the text itself, the protocol, a
// name for the list, and the host it leads to (what the self-check compares).
type OutboundLink struct {
	Link string
	Kind string
	Name string
	Host string
}

// OutboundNameFallback is the name of a link that has none of its own.
func (l OutboundLink) nameOrFallback() string {
	if n := cleanOutboundName(l.Name); n != "" {
		return n
	}
	host := l.Host
	if host == "" {
		host = "?"
	}
	return l.Kind + " " + host
}

// CleanOutboundName is a name as the panel keeps it, "" when nothing is left.
func cleanOutboundName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxOutboundName {
		name = string([]rune(name)[:maxOutboundName])
	}
	return name
}

// CheckOutboundLink reads a pasted link as far as the panel can: one line, a protocol the
// node reads, and a host that is not one of this panel's own servers — a link back into
// them would send an inbound's traffic into the node it came from, a loop the admin sees
// only as "no internet". own are the panel's and its nodes' addresses.
func CheckOutboundLink(raw string, own []string) (OutboundLink, error) {
	l := OutboundLink{Link: strings.TrimSpace(raw)}
	if l.Link == "" || len(l.Link) > MaxOutboundLink || strings.ContainsAny(l.Link, "\n\r\t ") {
		return OutboundLink{}, ErrOutboundLink
	}
	scheme, _, ok := strings.Cut(l.Link, "://")
	if !ok {
		return OutboundLink{}, ErrOutboundLink
	}
	l.Kind = strings.ToLower(scheme)
	if !slices.Contains(outboundSchemes, l.Kind) {
		return OutboundLink{}, ErrOutboundLink
	}
	l.Name, l.Host = outboundRemark(l.Link), outboundHost(l.Kind, l.Link)
	if l.Host != "" && ownHost(l.Host, own) {
		return OutboundLink{}, ErrOutboundSelf
	}
	// A vmess link carries its own JSON; whatever is inside must at least be that.
	if l.Kind == "vmess" && l.Host == "" && l.Name == "" {
		return OutboundLink{}, ErrOutboundLink
	}
	return l, nil
}

// ownHost says whether host is one of the panel's own addresses. An address of one of the
// nodes and the panel's are what a loop starts with.
func ownHost(host string, own []string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "" {
		return false
	}
	for _, o := range own {
		o = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(o), "."))
		if o == "" {
			continue
		}
		// The address of a node is kept with its port; the name may be a domain.
		if name, _, err := splitHostMaybePort(o); err == nil && name != "" {
			o = name
		}
		if h == o {
			return true
		}
	}
	return false
}

func splitHostMaybePort(s string) (string, string, error) {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.String(), "", nil
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().String(), strconv.Itoa(int(ap.Port())), nil
	}
	if i := strings.LastIndexByte(s, ':'); i >= 0 && !strings.Contains(s[i:], "]") {
		return s[:i], s[i+1:], nil
	}
	return s, "", nil
}

// outboundRemark is the link's own name: what follows "#" for the URL-shaped links, and
// the "ps" field for a vmess one.
func outboundRemark(link string) string {
	if i := strings.IndexByte(link, '#'); i >= 0 {
		if name, err := url.PathUnescape(link[i+1:]); err == nil {
			return strings.TrimSpace(name)
		}
		return strings.TrimSpace(link[i+1:])
	}
	if strings.HasPrefix(strings.ToLower(link), "vmess://") {
		var v struct {
			PS string `json:"ps"`
		}
		if json.Unmarshal(vmessJSON(link), &v) == nil {
			return strings.TrimSpace(v.PS)
		}
	}
	return ""
}

// outboundHost is where the link leads. The node resolves it for real; the panel reads it
// only to keep a link from pointing back at its own servers.
func outboundHost(kind, link string) string {
	if kind == "vmess" {
		var v struct {
			Add string `json:"add"`
		}
		if json.Unmarshal(vmessJSON(link), &v) == nil {
			return strings.Trim(strings.TrimSpace(v.Add), "[]")
		}
		return ""
	}
	// ss links keep their credentials before the address, sometimes base64-encoded; the
	// address itself is always the last "@"-separated part of the URL's authority.
	body := link
	if i := strings.Index(body, "://"); i >= 0 {
		body = body[i+3:]
	}
	if i := strings.IndexAny(body, "#?"); i >= 0 {
		body = body[:i]
	}
	if i := strings.LastIndexByte(body, '@'); i >= 0 {
		body = body[i+1:]
	} else if kind == "ss" || kind == "ssr" {
		// The whole body may be base64: "method:password@host:port".
		if raw, err := decodeLinkBase64(body); err == nil {
			body = string(raw)
			if i := strings.LastIndexByte(body, '@'); i >= 0 {
				body = body[i+1:]
			}
		}
	}
	host, _, err := splitHostMaybePort(body)
	if err != nil {
		return ""
	}
	return strings.Trim(host, "[]")
}

// vmessJSON is the JSON a vmess link carries, base64 in any of the usual spellings.
func vmessJSON(link string) []byte {
	body := link
	if i := strings.LastIndex(link, "://"); i >= 0 {
		body = link[i+3:]
	}
	raw, err := decodeLinkBase64(body)
	if err != nil {
		return nil
	}
	return raw
}

func decodeLinkBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "=")
	if raw, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return raw, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// OutboundPatch is what an admin changes about an outbound.
type OutboundPatch struct {
	Name    *string
	Link    *string
	Enabled *bool
}

// AddOutbound keeps a new link. own are the panel's own addresses (CheckOutboundLink).
func AddOutbound(ctx context.Context, q *db.Queries, link string, name string, own []string, now time.Time) (db.Outbound, error) {
	l, err := CheckOutboundLink(link, own)
	if err != nil {
		return db.Outbound{}, err
	}
	if n := cleanOutboundName(name); n != "" {
		l.Name = n
	}
	if err := outboundNameFree(ctx, q, l.nameOrFallback(), 0); err != nil {
		return db.Outbound{}, err
	}
	count, err := q.CountOutbounds(ctx)
	if err != nil {
		return db.Outbound{}, err
	}
	if count >= MaxOutbounds {
		return db.Outbound{}, ErrOutboundFull
	}
	return q.CreateOutbound(ctx, db.CreateOutboundParams{Name: l.nameOrFallback(), Link: l.Link, Enabled: 1, CreatedAt: now.Unix()})
}

// UpdateOutbound changes an outbound on the admin's word. A new link is read like a new
// one: what is kept is checked, what is not sent stays.
func UpdateOutbound(ctx context.Context, q *db.Queries, id int64, p OutboundPatch, own []string, now time.Time) (db.Outbound, error) {
	prev, err := q.GetOutbound(ctx, id)
	if err != nil {
		return db.Outbound{}, err
	}
	next := prev
	if p.Link != nil {
		l, err := CheckOutboundLink(*p.Link, own)
		if err != nil {
			return db.Outbound{}, err
		}
		if n := cleanOutboundName(prev.Name); n == "" || p.Name == nil {
			// The old name was the old link's own: follow the link unless the admin is
			// naming it in this same patch.
			next.Name = l.nameOrFallback()
		}
		next.Link = l.Link
	}
	if p.Name != nil {
		if n := cleanOutboundName(*p.Name); n != "" {
			next.Name = n
		}
	}
	if next.Name == "" {
		return db.Outbound{}, ErrOutboundName
	}
	if p.Enabled != nil {
		next.Enabled = 0
		if *p.Enabled {
			next.Enabled = 1
		}
	}
	if err := outboundNameFree(ctx, q, next.Name, id); err != nil {
		return db.Outbound{}, err
	}
	return q.UpdateOutbound(ctx, db.UpdateOutboundParams{Name: next.Name, Link: next.Link, Enabled: next.Enabled, UpdatedAt: now.Unix(), ID: id})
}

func outboundNameFree(ctx context.Context, q *db.Queries, name string, except int64) error {
	all, err := q.ListOutbounds(ctx)
	if err != nil {
		return err
	}
	for _, o := range all {
		if o.ID != except && strings.EqualFold(o.Name, name) {
			return &NameInUseError{Owner: o.Name}
		}
	}
	return nil
}

// OutboundUsesOf lists the inbounds whose traffic leaves through an outbound. Deleting it
// would quietly send them out the direct way.
func OutboundUsesOf(ctx context.Context, q *db.Queries, id int64) ([]db.Inbound, error) {
	ins, err := q.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	var used []db.Inbound
	for _, in := range ins {
		if in.OutboundID.Valid && in.OutboundID.Int64 == id {
			used = append(used, in)
		}
	}
	return used, nil
}

// DeleteOutbound removes an outbound nothing leaves through any more.
func DeleteOutbound(ctx context.Context, q *db.Queries, id int64) error {
	if _, err := q.GetOutbound(ctx, id); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	used, err := OutboundUsesOf(ctx, q, id)
	if err != nil {
		return err
	}
	if len(used) > 0 {
		return ErrOutboundUsed
	}
	n, err := q.DeleteOutbound(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
