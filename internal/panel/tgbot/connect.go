package tgbot

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"slices"
	"strings"

	"mikan/internal/panel/model"
	"mikan/internal/tgapi"
)

// Connecting a device: the platform (unless the admin shows every app at once), then the
// apps of it. Each app has where it is downloaded; its button hands the whole subscription
// over through the panel's /import redirect (happ://add, incy://add,
// kabosu://install-config), and the link itself stays under the buttons for a manual copy.

// clientApp is one app of the screen: its name in the buttons, its own import scheme (the
// panel's redirect builds it), where it comes from and whether its button is primary.
type clientApp struct {
	Name     string
	Client   string // the /import client: happ, incy, kabosu, clash, mihomo
	Download string
	Primary  bool
}

// platforms are the offers of the first step; desktop is Windows, macOS and Linux together.
func platforms(w *words) []struct{ ID, Label string } {
	return []struct{ ID, Label string }{
		{"ios", w.platformIOS}, {"android", w.platformAndroid}, {"desktop", w.platformDesktop},
	}
}

// enabledApps are the admin's apps that are on, in their order.
func enabledApps(cfg Config) []ConnectApp {
	out := make([]ConnectApp, 0, len(cfg.Connect.Apps))
	for _, a := range cfg.Connect.Apps {
		if a.On {
			out = append(out, a)
		}
	}
	return out
}

// appsOf is what the screen offers for a platform, the admin's order kept and one entry per
// client. The first app of a group is primary when the admin marked none; a marked one is
// always primary.
func appsOf(cfg Config, platform string) []clientApp {
	hasPrimary := slices.ContainsFunc(cfg.Connect.Apps, func(a ConnectApp) bool {
		return a.On && a.Primary && (platform == "" || slices.Contains(a.Platforms, platform))
	})
	out := []clientApp{}
	used := map[string]bool{}
	for _, a := range enabledApps(cfg) {
		if platform != "" && !slices.Contains(a.Platforms, platform) {
			continue
		}
		if used[a.Client] {
			continue // one button per client: two platforms' entries of it are the same app
		}
		used[a.Client] = true
		out = append(out, clientApp{Name: a.Name, Client: a.Client, Download: a.Download, Primary: a.Primary || !hasPrimary && len(out) == 0})
	}
	return out
}

// platformsWithApps keeps the first step to the platforms that have something to offer.
func platformsWithApps(cfg Config, w *words) []struct{ ID, Label string } {
	out := []struct{ ID, Label string }{}
	for _, p := range platforms(w) {
		if len(appsOf(cfg, p.ID)) > 0 {
			out = append(out, p)
		}
	}
	return out
}

// The places the default clients come from.
const (
	happAppStore   = "https://apps.apple.com/app/happ-proxy-utility/id6504287215"
	happGooglePlay = "https://play.google.com/store/apps/details?id=com.happproxy"
	incySite       = "https://incy.app/"
	kabosuSite     = "https://github.com/getmikan/kabosu"
)

// importURL is the panel's one-tap redirect for the subscription: the sub link's own base
// (the token cut off) with the client and the link to hand over.
func (b *Bot) importURL(ctx context.Context, u model.User, client string) string {
	sub := b.subURL(ctx, u)
	i := strings.LastIndex(sub, "/")
	if sub == "" || i < 0 {
		return ""
	}
	return sub[:i+1] + "import?client=" + client + "&sub_url=" + url.QueryEscape(sub)
}

// connectPlatforms is the first step: which device the buyer is on.
func (b *Bot) connectPlatforms(ctx context.Context, cfg Config, w *words, notice string, u model.User) (string, *tgapi.Keyboard) {
	rows := [][]tgapi.Button{}
	row := []tgapi.Button{}
	for _, p := range platformsWithApps(cfg, w) {
		row = append(row, tgapi.Button{Text: p.Label, CallbackData: "c2:" + p.ID})
		if len(row) == 2 {
			rows, row = append(rows, row), nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []tgapi.Button{{Text: w.back, CallbackData: "m"}})
	lines := []string{"<b>" + html.EscapeString(w.connectTitle) + "</b>", "", html.EscapeString(connectIntro(cfg, w))}
	// The link stays here as well: a reissued one is seen at once, before the platform is
	// picked.
	if sub := b.subURL(ctx, u); sub != "" {
		lines = append(lines, "", fmt.Sprintf(w.connectManual, html.EscapeString(w.appManual)), "<code>"+html.EscapeString(sub)+"</code>")
	}
	return withNoticeText(notice, strings.Join(lines, "\n")), &tgapi.Keyboard{InlineKeyboard: rows}
}

// connectIntro is the admin's text over the screen; empty is the built-in line.
func connectIntro(cfg Config, w *words) string {
	if s := strings.TrimSpace(cfg.Connect.Intro); s != "" {
		return s
	}
	return w.connectPlatforms
}

// connectClients is the second step: the apps of the platform, each with its one-tap
// import first and its download under it, and the subscription link in code.
func (b *Bot) connectClients(ctx context.Context, cfg Config, w *words, u model.User, arg, notice string) (string, *tgapi.Keyboard) {
	platform, _, _ := strings.Cut(arg, ":")
	return b.connectApps(ctx, cfg, w, u, platform, notice)
}

// connectAll is the screen for a panel that shows every app at once: no platform step, one
// button per client in the admin's order.
func (b *Bot) connectAll(ctx context.Context, cfg Config, w *words, u model.User, notice string) (string, *tgapi.Keyboard) {
	return b.connectApps(ctx, cfg, w, u, "", notice)
}

// connectApps draws the apps of one platform ("" — all of them).
func (b *Bot) connectApps(ctx context.Context, cfg Config, w *words, u model.User, platform, notice string) (string, *tgapi.Keyboard) {
	apps := appsOf(cfg, platform)
	sub := b.subURL(ctx, u)
	lines := []string{"<b>" + html.EscapeString(w.connectTitle) + "</b>", ""}
	if intro := strings.TrimSpace(cfg.Connect.Intro); intro != "" {
		lines = append(lines, html.EscapeString(intro), "")
	}
	lines = append(lines, html.EscapeString(w.connectTapHint))
	rows := [][]tgapi.Button{}
	for _, a := range apps {
		if link := b.importURL(ctx, u, a.Client); link != "" {
			btn := tgapi.Button{Text: fmt.Sprintf(w.connectOneTap, a.Name), URL: link}
			if a.Primary {
				btn.Style = tgapi.StylePrimary
			}
			rows = append(rows, []tgapi.Button{btn})
		}
		if a.Download != "" {
			rows = append(rows, []tgapi.Button{{Text: fmt.Sprintf(w.connectDownload, a.Name), URL: a.Download}})
		}
	}
	// The messenger itself needs no subscription: the MTProto proxy is a button of its own.
	if mts := b.d.Backend.MTProto(ctx); len(mts) > 0 {
		rows = append(rows, []tgapi.Button{{Text: w.tgProxy, URL: mts[0].Link}})
	}
	if sub != "" {
		lines = append(lines, "", fmt.Sprintf(w.connectManual, html.EscapeString(w.appManual)), "<code>"+html.EscapeString(sub)+"</code>")
	}
	if btn, ok := b.pageButton(ctx, cfg, w, w.openPage); ok {
		rows = append(rows, []tgapi.Button{btn})
	}
	back := tgapi.Button{Text: w.back, CallbackData: "m"}
	if platform != "" {
		back.CallbackData = "c"
	}
	rows = append(rows, []tgapi.Button{back})
	return withNoticeText(notice, strings.Join(lines, "\n")), &tgapi.Keyboard{InlineKeyboard: rows}
}

// withNoticeText puts the screen's notice over its text; "" is the text as it is.
func withNoticeText(notice, text string) string {
	if notice == "" {
		return text
	}
	return html.EscapeString(notice) + "\n\n" + text
}
