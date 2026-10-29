package subs

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
	"mikan/internal/textutil"
)

// The node's copy of a subscription: the panel renders every body an app may ask for and
// hands the node the bytes, so the subscriber's link keeps working when the panel cannot be
// reached (nodesync pushes, internal/node serves). Rendering happens here, once per user per
// sync: the node never renders anything.
//
// Binding is not part of a pre-rendered record: every device of the user gets the user's own
// keys. The node still enforces the device limit by the addresses it sees (nodeapi.Policy),
// but the per-device slots, their traffic and a single device's ban are the panel's own
// feature and need it.

// SubCacheMaxAge is how long a node keeps serving a record the panel stopped refreshing: a
// subscription without a term of its own is renewed this often, so a panel gone for good does
// not hand out working profiles for ever.
const SubCacheMaxAge = 30 * 24 * time.Hour

// The apps the three pre-rendered profile formats are made for. The node picks among them by
// the User-Agent alone (nodeapi.SubFormat); each is the widest member of its family, so a
// client that gets one of these takes it.
var (
	recordAppURI     = App{Family: FamilyXray}
	recordAppClash   = App{Family: FamilyMihomo, Core: previewCore}
	recordAppSingBox = App{Family: FamilySingBox, Whole: true, Core: singboxSince}
)

// NodeRecord renders u's subscription for the node's own answers: every body an app may ask
// for, the headers they go with, and until when the node may serve them. A user without a
// slot has no keys to hand out, and the error says so.
func (h *Handler) NodeRecord(ctx context.Context, u db.User) (nodeapi.SubRecord, error) {
	rec := nodeapi.SubRecord{Path: u.SubToken}
	if u.SubToken == "" || !u.SlotID.Valid {
		return rec, ErrNoSlot
	}
	cfg, err := h.cfg(ctx)
	if err != nil {
		return rec, err
	}
	slot, err := h.st.Q.GetSlot(ctx, u.SlotID.Int64)
	if err != nil {
		return rec, err
	}
	now := h.now()
	grants, err := domain.UserGrantsLeft(ctx, h.st.Q, u.ID, now)
	if err != nil {
		return rec, err
	}
	state := domain.State(u, grants.Main(u.ID), now)
	// The values the title, the announcement and the notices use; no device count, which is
	// the panel's own view.
	vars := titleValues(u, grants.Main(u.ID), -1, cfg, now)
	rec.Expires = recordExpiry(u, state, now)
	rec.Headers = recordHeaders(u, grants.Main(u.ID), cfg, vars, now)

	// A subscription that cannot connect at all gets the one notice in place of its servers
	// (the admin's InactiveServer): the same three stubs the panel serves, rendered here.
	if notice := inactiveNotice(cfg, state, vars); notice != nil {
		for _, f := range []string{nodeapi.SubFormatURI, nodeapi.SubFormatClash, nodeapi.SubFormatSingBox} {
			body, typ, name := h.stubBody(u, cfg, f, notice)
			rec.Bodies = append(rec.Bodies, nodeapi.SubBody{Format: f, Type: typ, Name: name, Body: body})
		}
		rec.Bodies = append(rec.Bodies, htmlBody(cfg, state, u, grants.Main(u.ID), now, strings.TrimPrefix(notice.Error(), "inactive: ")))
		return rec, nil
	}

	prof, err := h.profile(ctx, u, cfg, slot, grants)
	if err != nil {
		return rec, err
	}
	prof.Skip = func(db.Inbound, error) {}
	if state == domain.StateExpired || state == domain.StateLimited {
		prof.Notice = &Notice{Lines: h.noticeLines(ctx, u, state, cfg)}
		prof.emergencyOnly()
	}

	for _, f := range []string{nodeapi.SubFormatURI, nodeapi.SubFormatClash, nodeapi.SubFormatSingBox} {
		body, typ, name, err := h.profileBody(ctx, prof, cfg, state, vars, f)
		if err != nil {
			if f == nodeapi.SubFormatURI {
				return rec, err // the link list is the one every app reads: without it, nothing
			}
			continue // one app family without a profile must not cost the others theirs
		}
		rec.Bodies = append(rec.Bodies, nodeapi.SubBody{Format: f, Type: typ, Name: name, Body: body})
	}
	if len(prof.Turn) > 0 {
		rec.Bodies = append(rec.Bodies, nodeapi.SubBody{Format: nodeapi.SubFormatTurn, Type: "text/plain; charset=utf-8", Body: []byte(TurnSubscription(prof))})
	}
	rec.Bodies = append(rec.Bodies, htmlBody(cfg, state, u, grants.Main(u.ID), now, ""))
	return rec, nil
}

// profileBody is the body of one of the three profile formats, as the panel writes it.
func (h *Handler) profileBody(ctx context.Context, prof Profile, cfg Config, state string, vars map[string]string, format string) ([]byte, string, string, error) {
	switch format {
	case nodeapi.SubFormatClash:
		p := prof
		p.App = recordAppClash
		p.Inbounds = forApp(p.Inbounds, p.App, state)
		body, _, err := h.clashProfile(p, cfg)
		if err != nil {
			return nil, "", "", err
		}
		return body, "text/yaml; charset=utf-8", fileName(profileTitle(cfg, vars)) + ".yaml", nil
	case nodeapi.SubFormatSingBox:
		p := prof
		p.App = recordAppSingBox
		p.Inbounds = forApp(p.Inbounds, p.App, state)
		p.Rules, p.Routes, p.Lang = cfg.Rules, cfg.Routes, cfg.Lang
		body, err := SingBox(ctx, p, cfg.Groups, cfg.Routing, cfg.Template)
		if err != nil {
			return nil, "", "", err
		}
		return body, "application/json; charset=utf-8", fileName(profileTitle(cfg, vars)) + ".json", nil
	}
	p := prof
	p.App = recordAppURI
	p.Inbounds = forApp(p.Inbounds, p.App, state)
	links, _, err := uris(p)
	if err != nil {
		return nil, "", "", err
	}
	return []byte(base64.StdEncoding.EncodeToString([]byte(links))), "text/plain; charset=utf-8", "", nil
}

// stubBody is what the panel answers a device that gets no keys at all, in one format: the
// same bytes h.stub writes, captured rather than sent.
func (h *Handler) stubBody(u db.User, cfg Config, format string, reason error) ([]byte, string, string) {
	c := &capture{}
	h.stub(c, u, cfg, format, reason)
	return c.body.Bytes(), c.header.Get("Content-Type"), ""
}

// capture is a ResponseWriter that keeps the answer instead of sending it.
type capture struct {
	header http.Header
	body   bytes.Buffer
	code   int
}

func (c *capture) Header() http.Header {
	if c.header == nil {
		c.header = http.Header{}
	}
	return c.header
}

func (c *capture) Write(p []byte) (int, error) { return c.body.Write(p) }

func (c *capture) WriteHeader(code int) { c.code = code }

// recordHeaders are the headers every answer of the record carries: what the apps show of
// the traffic and the term, and the profile's name.
func recordHeaders(u db.User, grants int64, cfg Config, vars map[string]string, now time.Time) map[string]string {
	var total, expire int64
	if u.TrafficLimit.Valid {
		total = u.TrafficLimit.Int64
		if grants > 0 {
			total = max(total, u.UsedUp+u.UsedDown) + grants
		}
	}
	if u.ExpiresAt.Valid {
		expire = u.ExpiresAt.Int64
	}
	hd := map[string]string{
		"Subscription-Userinfo": "upload=" + strconv.FormatInt(u.UsedUp, 10) + "; download=" + strconv.FormatInt(u.UsedDown, 10) +
			"; total=" + strconv.FormatInt(total, 10) + "; expire=" + strconv.FormatInt(expire, 10),
		"Profile-Update-Interval": "1",
		"Profile-Title":           "base64:" + base64.StdEncoding.EncodeToString([]byte(profileTitle(cfg, vars))),
	}
	if cfg.SupportURL != "" {
		hd["Support-Url"] = cfg.SupportURL
	}
	return hd
}

// recordExpiry is when the node stops serving the record: the subscription's own end while
// that is still ahead, and SubCacheMaxAge otherwise — a subscription that already ended keeps
// its notice (and the way to renew) readable, and one without a term is renewed by that time.
func recordExpiry(u db.User, state string, now time.Time) time.Time {
	exp := now.Add(SubCacheMaxAge)
	if u.ExpiresAt.Valid && domain.CanConnect(state) {
		if end := time.Unix(u.ExpiresAt.Int64, 0); end.After(now) && end.Before(exp) {
			return end
		}
	}
	return exp
}

// htmlBody is the page a browser gets when it opens a subscription link: the panel's own page
// is a whole app and needs the panel's server, so the node answers with what a subscriber
// needs to know — the state of the subscription and what to do with the link.
func htmlBody(cfg Config, state string, u db.User, grants int64, now time.Time, notice string) nodeapi.SubBody {
	en := textutil.Lang(cfg.Lang).En()
	t := func(ru, eng string) string {
		if en {
			return eng
		}
		return ru
	}
	brand := cfg.Brand
	if brand == "" {
		brand = "VPN"
	}
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"" + t("ru", "en") + "\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<title>" + html.EscapeString(brand) + "</title>\n")
	b.WriteString("<style>body{font:16px/1.5 system-ui,sans-serif;margin:0;padding:2rem;max-width:40rem;color:#1b1b1f;background:#faf8ff}" +
		"h1{font-size:1.4rem;margin:0 0 1rem}dt{font-weight:600}dd{margin:0 0 .6rem}a{color:#5b4bd6}" +
		".state{font-weight:600}</style>\n</head>\n<body>\n")
	b.WriteString("<h1>" + html.EscapeString(brand) + "</h1>\n")
	line := notice
	if line == "" {
		line = t("Подписка активна", "The subscription is active")
		switch state {
		case domain.StateLimited:
			line = t("Трафик закончился — докупите или продлите подписку", "The traffic is used up — buy more or renew")
		case domain.StateExpired:
			line = t("Подписка закончилась — продлите её", "The subscription has ended — renew it")
		case domain.StateDisabled:
			line = t("Подписка отключена — напишите в поддержку", "The subscription is turned off — write to support")
		case domain.StateExpiring:
			line = t("Подписка скоро заканчивается", "The subscription ends soon")
		}
	}
	b.WriteString("<p class=\"state\">" + html.EscapeString(strings.TrimLeft(line, "⛔⏸ ")) + "</p>\n<dl>\n")
	used := u.UsedUp + u.UsedDown
	if u.TrafficLimit.Valid {
		total := u.TrafficLimit.Int64
		if grants > 0 {
			total = max(total, used) + grants
		}
		b.WriteString("<dt>" + t("Трафик", "Traffic") + "</dt><dd>" + html.EscapeString(humanBytes(used)+" / "+humanBytes(total)) + "</dd>\n")
	} else {
		b.WriteString("<dt>" + t("Трафик", "Traffic") + "</dt><dd>" + html.EscapeString(humanBytes(used)) + "</dd>\n")
	}
	if u.ExpiresAt.Valid {
		end := time.Unix(u.ExpiresAt.Int64, 0).In(now.Location())
		b.WriteString("<dt>" + t("Действует до", "Valid until") + "</dt><dd>" + html.EscapeString(end.Format("2006-01-02 15:04")) + "</dd>\n")
	}
	b.WriteString("</dl>\n")
	b.WriteString("<p>" + t("Эта ссылка — подписка для приложения: импортируйте её в Happ, v2RayTun, INCY, Koala Clash или sing-box. "+
		"Страница с кнопками и настройками открывается на адресе панели.",
		"This link is a subscription for the app: import it into Happ, v2RayTun, INCY, Koala Clash or sing-box. "+
			"The page with buttons and settings opens on the panel's address.") + "</p>\n")
	if cfg.SupportURL != "" {
		b.WriteString("<p>" + t("Поддержка", "Support") + ": <a href=\"" + html.EscapeString(urlSafe(cfg.SupportURL)) + "\">" +
			html.EscapeString(cfg.SupportURL) + "</a></p>\n")
	}
	b.WriteString("</body>\n</html>\n")
	return nodeapi.SubBody{Format: nodeapi.SubFormatHTML, Type: "text/html; charset=utf-8", Body: []byte(b.String())}
}

// urlSafe keeps only http(s) links in the page: the admin's support URL is theirs, but a
// javascript: one in an href is nobody's support.
func urlSafe(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" {
		return ""
	}
	return u.String()
}

// humanBytes is a traffic amount as the page says it.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ErrNoSlot: a user without keys has no subscription to hand a node.
var ErrNoSlot = errors.New("subscription: the user has no slot")
