package botpanel

import (
	"database/sql"
	"strings"
	"testing"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
	"mikan/internal/panel/tgbot"
)

// kbButton is one button of an answer: its label and where it leads.
type kbButton struct {
	text  string
	value string
	style string
}

// kbRows reads the answer's inline keyboard row by row.
func kbRows(c call) [][]kbButton {
	kb, _ := c.body["reply_markup"].(map[string]any)
	rows, _ := kb["inline_keyboard"].([]any)
	out := make([][]kbButton, 0, len(rows))
	for _, r := range rows {
		row := []kbButton{}
		for _, b := range r.([]any) {
			m := b.(map[string]any)
			v, _ := m["callback_data"].(string)
			if u, ok := m["url"].(string); ok {
				v = u
			}
			st, _ := m["style"].(string)
			row = append(row, kbButton{text: m["text"].(string), value: v, style: st})
		}
		out = append(out, row)
	}
	return out
}

func kbFind(c call, want string) (kbButton, bool) {
	for _, row := range kbRows(c) {
		for _, b := range row {
			if b.value == want {
				return b, true
			}
		}
	}
	return kbButton{}, false
}

// The buyer's path: connect and renew are the menu's buttons; the subscription screen holds
// its own doings and the list of subscriptions. The connect screen hands the subscription to
// the app, and installing an app is a screen of its own rather than a second button per app.
func TestConnectBuyerFlow(t *testing.T) {
	e := setup(t)
	e.local.Billing.SetTelegram(e.gate)
	if err := settings.Set(e.ctx, e.set, billing.KeyConfig, billing.Config{Enabled: true, Stars: true, AllowNew: true}); err != nil {
		t.Fatal(err)
	}
	// A plan on sale: the subscription screen offers the list of subscriptions, where another
	// one is bought too.
	if _, err := e.st.Q.CreateTariff(e.ctx, db.CreateTariffParams{Name: "Тариф", DurationDays: 30, ResetStrategy: "none", CreatedAt: 1,
		PriceStars: sql.NullInt64{Int64: 150, Valid: true}, OnSale: 1}); err != nil {
		t.Fatal(err)
	}
	e.link(555)

	// «Моя подписка»: no duplicate connect and renew, the subscriptions list stands here.
	sub := e.tap(555, "s")
	for _, link := range []string{"c", "r"} {
		if _, ok := kbFind(sub, link); ok {
			t.Fatalf("the subscription screen repeats %q: %+v", link, kbRows(sub))
		}
	}
	if b, ok := kbFind(sub, "w"); !ok || b.text != "🔁 Подписки (1)" {
		t.Fatalf("the subscriptions list is not on the subscription screen: %+v", kbRows(sub))
	}
	// The main menu keeps the same button out of itself: one place for it.
	if menu := e.tap(555, "m"); func() bool { _, ok := kbFind(menu, "w"); return ok }() {
		t.Fatalf("the subscriptions list is still in the menu: %+v", kbRows(menu))
	}

	// The platform step offers all three, the desk included.
	plat := e.tap(555, "c")
	for _, want := range []string{"c2:ios", "c2:android", "c2:desktop"} {
		if _, ok := kbFind(plat, want); !ok {
			t.Fatalf("no %s among %+v", want, kbRows(plat))
		}
	}
	if strings.Contains(strings.Join(kbValues(plat), " "), "client=") {
		t.Fatal("the platform step already hands the subscription out")
	}

	// The desk's screen: one-tap into Kabosu, and one button to install it.
	desk := e.tap(555, "c2:desktop")
	deskRows := kbRows(desk)
	if len(deskRows) < 2 {
		t.Fatalf("the desk screen: %+v", deskRows)
	}
	first := deskRows[0][0]
	if !strings.Contains(first.value, "/import?client=kabosu") || first.style != "primary" {
		t.Fatalf("the desk's primary is not the one-tap: %+v", first)
	}
	if !strings.Contains(first.text, "Kabosu") || !strings.Contains(first.text, "1 клик") {
		t.Fatalf("the one-tap button does not name Kabosu: %+v", first)
	}
	install, ok := kbFind(desk, "ci:desktop")
	if !ok || !strings.Contains(install.text, "Установить") {
		t.Fatalf("no install step apart from the connect button: %+v", deskRows)
	}
	// The install screen is where the download is, and back leads to the desk's clients.
	installScreen := e.tap(555, "ci:desktop")
	if _, ok := kbFind(installScreen, "https://github.com/getmikan/kabosu"); !ok {
		t.Fatalf("no Kabosu download on the install screen: %+v", kbRows(installScreen))
	}
	if _, ok := kbFind(installScreen, "c2:desktop"); !ok {
		t.Fatalf("the install screen does not lead back to the desk: %+v", kbRows(installScreen))
	}
	// Two clients on a platform do not make four buttons: each is one connect button, and the
	// downloads are one screen behind one button.
	phones := kbRows(e.tap(555, "c2:ios"))
	connect, installs := 0, 0
	for _, row := range phones {
		for _, b := range row {
			if strings.Contains(b.value, "/import?client=") {
				connect++
			}
			if strings.HasPrefix(b.value, "ci:") {
				installs++
			}
		}
	}
	// Happ and Incy are two clients: two connect buttons and one install, not four.
	if connect != 2 || installs != 1 {
		t.Fatalf("the phone's screen: %d connect, %d install buttons: %+v", connect, installs, phones)
	}

	// A panel that shows every app at once: «Подключить» skips the platform question, and
	// every download is on the one install screen.
	cfg := tgbot.Default("ru")
	cfg.Connect.OneScreen = true
	if err := settings.Set(e.ctx, e.set, tgate.KeyConfig, cfg); err != nil {
		t.Fatal(err)
	}
	all := e.tap(555, "c")
	if _, ok := kbFind(all, "c2:ios"); ok {
		t.Fatalf("the platform step came back: %+v", kbRows(all))
	}
	var happ, kabosu bool
	for _, v := range kbValues(all) {
		happ = happ || strings.Contains(v, "client=happ")
		kabosu = kabosu || strings.Contains(v, "client=kabosu")
	}
	if !happ || !kabosu {
		t.Fatalf("every client on one screen: %+v", kbValues(all))
	}
	allInstall := e.tap(555, "ci:")
	downloads := strings.Join(kbValues(allInstall), " ")
	if !strings.Contains(downloads, "apps.apple.com") || !strings.Contains(downloads, "github.com/getmikan/kabosu") {
		t.Fatalf("the downloads of every client: %+v", kbRows(allInstall))
	}
}

// The admin's words and apps reach the buyer: a renamed app and the intro text are what the
// screen shows.
func TestConnectScreenFollowsTheAdmin(t *testing.T) {
	e := setup(t)
	e.link(555)
	cfg := tgbot.Default("ru")
	cfg.Connect.Intro = "Ставьте приложение и жмите одну кнопку."
	cfg.Connect.Apps = []tgbot.ConnectApp{{ID: "custom", Name: "Мой клиент", Client: "happ", Platforms: []string{"desktop"}, On: true}}
	if err := settings.Set(e.ctx, e.set, tgate.KeyConfig, cfg); err != nil {
		t.Fatal(err)
	}
	desk := e.tap(555, "c2:desktop")
	adminRows := kbRows(desk)
	if !strings.Contains(text(desk), "Ставьте приложение и жмите одну кнопку.") || len(adminRows) == 0 || !strings.Contains(adminRows[0][0].text, "Мой клиент") {
		t.Fatalf("the admin's screen: %q %+v", text(desk), adminRows)
	}
	// The platform step keeps only the platforms that have apps: the phones are not offered.
	plat := e.tap(555, "c")
	if _, ok := kbFind(plat, "c2:ios"); ok {
		t.Fatalf("a platform without apps is offered: %+v", kbRows(plat))
	}
}

func kbValues(c call) []string {
	out := []string{}
	for _, row := range kbRows(c) {
		for _, b := range row {
			out = append(out, b.value)
		}
	}
	return out
}
