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

// The buyer's path: «Моя подписка» leads with connect and renew, and the connect screen
// hands the subscription to the app first — the download comes under it.
func TestConnectBuyerFlow(t *testing.T) {
	e := setup(t)
	if err := settings.Set(e.ctx, e.set, billing.KeyConfig, billing.Config{Enabled: true, Stars: true, AllowNew: true}); err != nil {
		t.Fatal(err)
	}
	// A plan on sale: «Продлить» stands beside «Подключить» only while there is something
	// to sell.
	if _, err := e.st.Q.CreateTariff(e.ctx, db.CreateTariffParams{Name: "Тариф", DurationDays: 30, ResetStrategy: "none", CreatedAt: 1,
		PriceStars: sql.NullInt64{Int64: 150, Valid: true}, OnSale: 1}); err != nil {
		t.Fatal(err)
	}
	e.link(555)

	// «Моя подписка»: connect and renew are the first row.
	sub := e.tap(555, "s")
	rows := kbRows(sub)
	if len(rows) == 0 || len(rows[0]) != 2 || rows[0][0].value != "c" || rows[0][1].value != "r" {
		t.Fatalf("the subscription screen's first row: %+v", rows)
	}
	if rows[0][0].text != "⚡ Подключить устройство" || rows[0][0].style != "primary" {
		t.Fatalf("connect is not the primary button: %+v", rows[0][0])
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

	// The desk's screen: one-tap into Kabosu first, then where to download it.
	desk := e.tap(555, "c2:desktop")
	deskRows := kbRows(desk)
	if len(deskRows) < 2 {
		t.Fatalf("the desk screen: %+v", deskRows)
	}
	first := deskRows[0][0]
	if !strings.Contains(first.value, "/import?client=kabosu") || first.style != "primary" {
		t.Fatalf("the desk's primary is not the one-tap: %+v", first)
	}
	if second := deskRows[1][0]; !strings.Contains(second.value, "github.com/getmikan/kabosu") {
		t.Fatalf("the download under the one-tap: %+v", second)
	}
	if !strings.Contains(text(desk), "Kabosu") || !strings.Contains(text(desk), "1 клик") {
		t.Fatalf("no one-tap button for Kabosu: %q", text(desk))
	}

	// A panel that shows every app at once: «Подключить» skips the platform question.
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
	if !strings.Contains(text(desk), "Ставьте приложение и жмите одну кнопку.") || !strings.Contains(text(desk), "Мой клиент") {
		t.Fatalf("the admin's screen: %q", text(desk))
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
