package nodeapi

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Subscriptions are the profiles the panel renders and hands a node, so the node answers the
// subscriber's link while the panel itself is unreachable: the panel builds every body once
// per user and the node serves the bytes it was given. Nothing on the node renders a
// profile, and nothing there looks a subscriber up: a request is a path the node was told
// about, or nothing.
//
// The path is a bearer token (db.User.SubToken): it must never reach a log, a metric or a
// page, on the panel or on the node.

// The bodies a record may carry. A client asks for one of them by its User-Agent and
// Accept, or by ?format=; SubFormat says which, and a record without it falls back to
// SubFormatURI.
const (
	SubFormatURI     = "uri"     // base64 share links: v2rayN, Streisand, Shadowrocket, Happ
	SubFormatClash   = "clash"   // the mihomo/Clash profile (YAML)
	SubFormatSingBox = "singbox" // the sing-box profile (JSON)
	SubFormatTurn    = "turn"    // the WL/TURN servers for the client program next to the apps
	SubFormatHTML    = "html"    // the page a browser gets: what to do with the link
)

// SubRecord is one subscription as the panel hands it to a node: what to answer at a path,
// with which headers, until when.
type SubRecord struct {
	// Path is the subscription token: the request's first path segment. A bearer token.
	Path string `json:"path"`
	// Expires is when the record stops being served: the subscription's own end, or the
	// cache's limit for a subscription without one. Past it the node answers 410 until the
	// panel sends the record again.
	Expires time.Time `json:"expires"`
	// Headers go out with every body: Subscription-Userinfo, Profile-Title and the like.
	Headers map[string]string `json:"headers,omitempty"`
	// Bodies are the answers by format, at least uri.
	Bodies []SubBody `json:"bodies"`
}

// SubBody is one rendered answer of a record.
type SubBody struct {
	Format string `json:"format"`         // SubFormat*
	Type   string `json:"type,omitempty"` // Content-Type; "" — text/plain
	Name   string `json:"name,omitempty"` // Content-Disposition's file name; "" — none
	Body   []byte `json:"body"`           // the profile itself (base64 in JSON)
}

// Limits of the node's store: what one record may weigh, how many a node keeps, and how much
// they may take in all. A record past its limit is refused whole rather than cut: a profile
// cut in half is a broken app.
const (
	MaxSubRecordBytes = 1 << 20 // 1 MiB: every body of one subscription
	MaxSubBodies      = 8       // formats per record
	MaxSubRecords     = 20000   // subscriptions per node
	MaxSubsBytes      = 1 << 30 // 1 GiB on the node's disk
	MaxSubPath        = 64      // the path's length bound
	SubSyncChunk      = 20      // records the panel sends in one request
)

// SubsRequest is a chunk of records the panel hands a node (PUT /v1/subs). The records are
// written one by one; the whole set of a sync is done once Prune has run with its
// generation, which drops everything not sent with it.
type SubsRequest struct {
	// Generation is the panel's sync number: it grows with every full sync, and a record
	// written with it counts as sent in that sync.
	Generation int64       `json:"generation"`
	Records    []SubRecord `json:"records"`
}

// SubsRemoveRequest drops the records of paths at once (POST /v1/subs/remove): a blocked or
// removed subscriber is gone from the node before the next full sync.
type SubsRemoveRequest struct {
	Generation int64    `json:"generation"`
	Paths      []string `json:"paths"`
}

// SubsPruneRequest ends a full sync (POST /v1/subs/prune): every record not written with
// this generation is dropped — a user deleted while the sync ran, one the panel stopped
// serving, anything of a panel that failed halfway.
type SubsPruneRequest struct {
	Generation int64 `json:"generation"`
}

// SubsResult is what one call of the three above did.
type SubsResult struct {
	Stored  int   `json:"stored"`
	Removed int   `json:"removed"`
	Bytes   int64 `json:"bytes"`
	// Rejected counts the records the node refused (too big, the store full, no token as a
	// path); Reason is the first refusal, a code for the panel's log.
	Rejected int    `json:"rejected,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// SubsStatus is the subscription store as the node reports it (GET /v1/subs and Health).
type SubsStatus struct {
	// OK: the node answers subscription requests on its public 443. Error says why not.
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Records are the subscriptions kept, Expired those past their expiry and not renewed,
	// Bytes what they take on the disk.
	Records    int   `json:"records"`
	Expired    int   `json:"expired"`
	Bytes      int64 `json:"bytes"`
	Generation int64 `json:"generation,omitempty"`
	// SyncedAt is when the last full sync finished; zero when there never was one.
	SyncedAt time.Time `json:"synced_at,omitzero"`
	// Listen says where on 443 the answers come from: "router" (the SNI router hands them
	// over) or "site" (the node's own page holds the port).
	Listen string `json:"listen,omitempty"`
}

// Why a node does not answer subscription links, as SubsStatus.Error says it: the panel
// translates the code for the admin.
const (
	CodeSubsOff    = "subs_off"     // the feature is not in the state
	CodeSubsNoPort = "subs_no_port" // the public 443 belongs to a protocol, and no router shares it
	CodeSubsNoCert = "subs_no_cert" // there is no certificate to present
)

// SubsServer is the node's subscription listener in its desired state: the names it answers
// for and the certificate it presents.
type SubsServer struct {
	// Names are the TLS names (SNI) that reach the subscription answers, lowercased: the
	// subscription domain, and the node's own when they are the same. The SNI router hands
	// these connections to the store instead of an inbound.
	Names []string `json:"names"`
	// TLS is the certificate the answers present; nil: the node's own (DesiredState.TLS).
	TLS *TLSFiles `json:"tls,omitempty"`
}

// The core the official sing-box apps name, and the "SFA/1.12.4" token when they do not.
// Kept in step with subs.DetectApp; a config of sing-box 1.12 does not load in 1.11.
var (
	subSingboxCore = regexp.MustCompile(`sing-box[ /]v?(\d+)\.(\d+)\.(\d+)`)
	subSingboxApp  = regexp.MustCompile(`^(?:sfa|sfi|sfm|sft)/v?(\d+)\.(\d+)\.(\d+)`)
)

// SubFormat picks the format a client asks for: ?format= first, then the User-Agent, then
// Accept. It mirrors subs.Format: Hiddify says "like ClashMeta" and reads links, Karing
// and the other sing-box apps route by their own settings and read links, and only the
// official apps (SFA, SFI, SFM, SFT) on core 1.12 or newer get the sing-box config. Where
// it is unsure it says SubFormatURI, which every app reads. TestFormatAgreesWithTheNode
// in package subs locks the two.
func SubFormat(query, userAgent, accept string) string {
	switch strings.ToLower(strings.TrimSpace(query)) {
	case "clash", "mihomo", "yaml":
		return SubFormatClash
	case "uri", "v2ray", "base64":
		return SubFormatURI
	case "singbox", "sing-box":
		return SubFormatSingBox
	case "turn", "wl":
		return SubFormatTurn
	case "html":
		return SubFormatHTML
	}
	ua := strings.ToLower(userAgent)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(ua, w) {
				return true
			}
		}
		return false
	}
	whole := strings.HasPrefix(ua, "sfa") || strings.HasPrefix(ua, "sfi") || strings.HasPrefix(ua, "sfm") || strings.HasPrefix(ua, "sft")
	switch {
	case strings.HasPrefix(ua, "stash"):
		return SubFormatClash
	case has("hiddify"):
		// "like ClashMeta" is how Hiddify asks other panels for Clash. It reads links.
		return SubFormatURI
	case whole && subSingboxAtLeast112(ua):
		return SubFormatSingBox
	case whole || has("karing", "sing-box", "singbox", "nekobox", "nekoray", "husi", "throne", "deskbox", "inhive"):
		return SubFormatURI
	case has("mihomo", "clash", "koala", "kabosu", "verge", "prizrak", "flowvy", "murge", "rabbit"):
		return SubFormatClash
	case has("happ", "v2raytun", "incy", "v2rayng", "v2rayn", "onexray", "v2box", "simplexray", "renoarx",
		"streisand", "shadowrocket", "foxray"):
		// An app that names itself, even one whose Accept asks for a page.
		return SubFormatURI
	}
	if strings.Contains(strings.ToLower(accept), "text/html") {
		return SubFormatHTML
	}
	return SubFormatURI
}

// subSingboxAtLeast112 reports a core of 1.12 or newer, from the "sing-box x.y.z" phrase
// or, without one, from the official app's own token. Unknown is not newer.
func subSingboxAtLeast112(ua string) bool {
	m := subSingboxCore.FindStringSubmatch(ua)
	if m == nil {
		m = subSingboxApp.FindStringSubmatch(ua)
	}
	if m == nil {
		return false
	}
	var v [3]int
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	min := [3]int{1, 12, 0}
	for i := range v {
		if v[i] != min[i] {
			return v[i] > min[i]
		}
	}
	return true
}

// PickSubBody is the body of bodies for a request: the one its format asks for, or the link
// list without it. A record always has a body (the panel sends uri), so false means the
// record is damaged.
func PickSubBody(bodies []SubBody, query, userAgent, accept string) (SubBody, bool) {
	want := SubFormat(query, userAgent, accept)
	find := func(f string) (SubBody, bool) {
		for _, b := range bodies {
			if b.Format == f {
				return b, true
			}
		}
		return SubBody{}, false
	}
	for _, f := range []string{want, SubFormatURI, SubFormatClash, SubFormatSingBox, SubFormatHTML, SubFormatTurn} {
		if b, ok := find(f); ok {
			return b, true
		}
	}
	if len(bodies) > 0 {
		return bodies[0], true
	}
	return SubBody{}, false
}
