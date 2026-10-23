package tgbot

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"strings"

	"mikan/internal/panel/model"
	"mikan/internal/tgapi"
)

// Connecting a device in two steps: the platform, then the clients of it. Each client has
// where it is downloaded; the one-tap button hands the whole subscription over through the
// panel's /import redirect (happ://add, incy://add, kabosu://install-config), and the link
// itself stays under the buttons in code for a manual copy.

// clientApp is one app of the page: its own import scheme (the panel's redirect builds it)
// and where it comes from.
type clientApp struct {
	Name     string
	Client   string // the /import client: happ, incy, kabosu
	Download string
}

// platforms are the offers of the first step; desktop is Windows, macOS and Linux together.
func platforms(w *words) []struct{ ID, Label string } {
	return []struct{ ID, Label string }{
		{"ios", w.platformIOS}, {"android", w.platformAndroid}, {"desktop", w.platformDesktop},
	}
}

// appsOf is what the page offers for a platform: the phones get Happ and Incy, the desk
// gets Kabosu.
func appsOf(platform string, w *words) []clientApp {
	switch platform {
	case "ios":
		return []clientApp{{w.appHapp, "happ", happAppStore}, {w.appIncy, "incy", incySite}}
	case "android":
		return []clientApp{{w.appHapp, "happ", happGooglePlay}, {w.appIncy, "incy", incySite}}
	default:
		return []clientApp{{w.appKabosu, "kabosu", kabosuSite}}
	}
}

// The places the clients come from.
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

// connectPlatforms is the first step: which device is the buyer on.
func (b *Bot) connectPlatforms(ctx context.Context, w *words, notice string, u model.User) (string, *tgapi.Keyboard) {
	rows := [][]tgapi.Button{}
	var row []tgapi.Button
	for _, p := range platforms(w) {
		row = append(row, tgapi.Button{Text: p.Label, CallbackData: "c2:" + p.ID})
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if row != nil {
		rows = append(rows, row)
	}
	rows = append(rows, []tgapi.Button{{Text: w.back, CallbackData: "m"}})
	lines := []string{"<b>" + html.EscapeString(w.connectTitle) + "</b>", "", html.EscapeString(w.connectPlatforms)}
	// The link stays here as well: a reissued one is seen at once, before the platform is
	// picked.
	if sub := b.subURL(ctx, u); sub != "" {
		lines = append(lines, "", fmt.Sprintf(w.connectManual, html.EscapeString(w.appManual)), "<code>"+html.EscapeString(sub)+"</code>")
	}
	return withNoticeText(notice, strings.Join(lines, "\n")), &tgapi.Keyboard{InlineKeyboard: rows}
}

// connectClients is the second step: the apps of the platform, each with its download and
// its one-tap import, and the subscription link under them in code.
func (b *Bot) connectClients(ctx context.Context, cfg Config, w *words, u model.User, arg, notice string) (string, *tgapi.Keyboard) {
	platform, _, _ := strings.Cut(arg, ":")
	apps := appsOf(platform, w)
	sub := b.subURL(ctx, u)
	lines := []string{"<b>" + html.EscapeString(w.connectTitle) + "</b>", ""}
	rows := [][]tgapi.Button{}
	for i, a := range apps {
		if a.Download != "" {
			rows = append(rows, []tgapi.Button{{Text: fmt.Sprintf(w.connectDownload, a.Name), URL: a.Download}})
		}
		if importLink := b.importURL(ctx, u, a.Client); importLink != "" {
			label := fmt.Sprintf(w.connectOneTap, a.Name)
			if i > 0 {
				// The second app's one-tap is the same subscription in another client; the
				// first is the page's own recommendation.
				label = fmt.Sprintf(w.connectOneTapAny, a.Name)
			}
			rows = append(rows, []tgapi.Button{{Text: label, URL: importLink, Style: tgapi.StylePrimary}})
		}
	}
	// The messenger itself needs no subscription: the MTProto proxy is a button of its own.
	if mts := b.d.Backend.MTProto(ctx); len(mts) > 0 {
		rows = append(rows, []tgapi.Button{{Text: w.tgProxy, URL: mts[0].Link}})
	}
	if sub != "" {
		lines = append(lines, fmt.Sprintf(w.connectManual, html.EscapeString(w.appManual)), "<code>"+html.EscapeString(sub)+"</code>")
	}
	if btn, ok := b.pageButton(ctx, cfg, w, w.openPage); ok {
		rows = append(rows, []tgapi.Button{btn})
	}
	rows = append(rows, []tgapi.Button{{Text: w.back, CallbackData: "c"}})
	return withNoticeText(notice, strings.Join(lines, "\n")), &tgapi.Keyboard{InlineKeyboard: rows}
}

// withNoticeText puts the screen's notice over its text; "" is the text as it is.
func withNoticeText(notice, text string) string {
	if notice == "" {
		return text
	}
	return html.EscapeString(notice) + "\n\n" + text
}
