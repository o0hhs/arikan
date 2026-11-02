package subsrc

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
	"mikan/internal/sub/apps"
	"mikan/internal/sub/build"
	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/profile"
	"mikan/internal/sub/serve"
	"mikan/internal/sub/singbox"
	"mikan/internal/sub/xray"
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
	recordAppURI     = apps.App{Family: apps.FamilyXray}
	recordAppClash   = apps.App{Family: apps.FamilyMihomo, Core: previewCore}
	recordAppSingBox = apps.App{Family: apps.FamilySingBox, Whole: true, Core: singbox.Since}
)

// ErrNoSlot: a user without keys has no subscription to hand a node.
var ErrNoSlot = errors.New("subscription: the user has no slot")

// NodeRecord renders u's subscription for the node's own answers: every body an app may ask
// for, the headers they go with, and until when the node may serve them.
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
	user := userOf(u, grants.Main(u.ID), cfg, now)
	vars := serve.Values(user, -1, cfg.Config, now)
	rec.Expires = recordExpiry(u, state, now)
	rec.Headers = recordHeaders(user, cfg, vars)

	if !serve.State(state).CanConnect() {
		// A subscription that cannot connect gets the node's own stub in place of its
		// servers, the same words the panel's page would give.
		for _, f := range []string{nodeapi.SubFormatURI, nodeapi.SubFormatClash, nodeapi.SubFormatSingBox} {
			rec.Bodies = append(rec.Bodies, nodeapi.SubBody{Format: f, Type: "text/plain; charset=utf-8", Body: []byte(serve.StubText(cfg.Lang, apps.Decision{Action: apps.Action{Format: f}}))})
		}
		rec.Bodies = append(rec.Bodies, htmlBody(cfg, state, u, grants.Main(u.ID), now, ""))
		return rec, nil
	}

	prof, err := h.recordProfile(ctx, u, cfg, slot, grants)
	if err != nil {
		return rec, err
	}
	for _, f := range []string{nodeapi.SubFormatURI, nodeapi.SubFormatClash, nodeapi.SubFormatSingBox} {
		body, typ, name, err := profileBody(ctx, prof, cfg, f, vars)
		if err != nil {
			if f == nodeapi.SubFormatURI {
				return rec, err // the link list is the one every app reads: without it, nothing
			}
			continue // one app family without a profile must not cost the others theirs
		}
		rec.Bodies = append(rec.Bodies, nodeapi.SubBody{Format: f, Type: typ, Name: name, Body: body})
	}
	rec.Bodies = append(rec.Bodies, htmlBody(cfg, state, u, grants.Main(u.ID), now, ""))
	return rec, nil
}

// recordProfile is the profile of u over every inbound the user may use, the panel's own
// settings and the record's app.
func (h *Handler) recordProfile(ctx context.Context, u db.User, cfg Config, slot db.Slot, grants domain.GrantsLeft) (profile.Profile, error) {
	r, err := h.reach(ctx, u, cfg, grants)
	if err != nil {
		return profile.Profile{}, err
	}
	hosts, err := h.hosts(ctx, r.inbounds)
	if err != nil {
		return profile.Profile{}, err
	}
	keys := proto.Slot{Name: slot.Name, UUID: slot.Uuid, Secret: slot.Secret}
	ps := build.Proxies(build.Profile{Keys: keys, Inbounds: inbounds(r.inbounds, hosts), Nodes: cfg.Nodes, Fingerprint: cfg.Fingerprint})
	return cfg.Profile(ps), nil
}

// profileBody is the body of one of the three profile formats, as the panel writes it.
func profileBody(ctx context.Context, prof profile.Profile, cfg Config, format string, vars map[string]string) ([]byte, string, string, error) {
	title := serve.ProfileTitle(cfg.Config, vars)
	switch format {
	case nodeapi.SubFormatClash:
		p := prof
		p.App = recordAppClash
		body, err := mihomo.Render(p, cfg.Groups, cfg.Mode)
		if err != nil {
			return nil, "", "", err
		}
		return body, "text/yaml; charset=utf-8", serve.FileName(title) + ".yaml", nil
	case nodeapi.SubFormatSingBox:
		p := prof
		p.App = recordAppSingBox
		body, err := singbox.Render(ctx, p, cfg.Groups, cfg.Mode, serve.ProfileTemplate(cfg.Templates, serve.FormatSingBox, 0, p.App))
		if err != nil {
			return nil, "", "", err
		}
		return body, "application/json; charset=utf-8", serve.FileName(title) + ".json", nil
	}
	p := prof
	p.App = recordAppURI
	links, _, err := xray.Links(p.Proxies)
	if err != nil {
		return nil, "", "", err
	}
	return xray.Encode(links), "text/plain; charset=utf-8", "", nil
}

// recordHeaders are the headers every answer of the record carries: what the apps show of
// the traffic and the term, and the profile's name.
func recordHeaders(u serve.User, cfg Config, vars map[string]string) map[string]string {
	var expire int64
	if !u.Expires.IsZero() {
		expire = u.Expires.Unix()
	}
	totalLimit := int64(0)
	if u.Limit != nil {
		totalLimit = *u.Limit
		if u.Grants > 0 {
			totalLimit = max(totalLimit, u.UsedUp+u.UsedDown) + u.Grants
		}
	}
	hd := map[string]string{
		"Subscription-Userinfo": "upload=" + fmt.Sprint(u.UsedUp) + "; download=" + fmt.Sprint(u.UsedDown) +
			"; total=" + fmt.Sprint(totalLimit) + "; expire=" + fmt.Sprint(expire),
		"Profile-Update-Interval": "1",
		"Profile-Title":           "base64:" + base64.StdEncoding.EncodeToString([]byte(serve.ProfileTitle(cfg.Config, vars))),
	}
	if cfg.SupportURL != "" {
		hd["Support-Url"] = cfg.SupportURL
	}
	return hd
}

// recordExpiry is when the node stops serving the record: the subscription's own end while
// that is still ahead, and SubCacheMaxAge otherwise.
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
