package tgbot

import (
	"errors"
	"strings"
	"testing"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/tgate"
)

// The premium emoji of a menu button: an id of digits, taken as it is; anything that is no
// id is refused with the field's own error, and a menu saved before the field existed keeps
// its buttons as they are.
func TestButtonEmojiValidate(t *testing.T) {
	mutate := func(f func(c *Config)) *Config {
		c := Default("ru")
		f(&c)
		return &c
	}
	for id, ok := range map[string]bool{
		"":                      true,
		" 5368324170671202286":  true,
		"5368324170671202286":   true,
		"53683241706712022860":  true,
		"536832417067120228601": false, // over twenty digits: no Telegram id
		"5 3683":                false,
		"emoji":                 false,
		"12345678901234567890a": false,
	} {
		c := mutate(func(c *Config) { c.Buttons[0].EmojiID = id })
		err := c.Validate()
		if ok != (err == nil) {
			t.Errorf("emoji %q: err %v", id, err)
		}
	}
	c := mutate(func(c *Config) { c.Buttons[0].EmojiID = "плюс" })
	if err := c.Validate(); !errors.Is(err, ErrButtonEmoji) {
		t.Fatalf("want the emoji's error, got %v", err)
	}
	// Trimmed, like the style and the label.
	c = mutate(func(c *Config) { c.Buttons[0].EmojiID = " 5368 " })
	if err := c.Validate(); err != nil || c.Buttons[0].EmojiID != "5368" {
		t.Fatalf("trim: %q %v", c.Buttons[0].EmojiID, err)
	}
}

// The emoji of a menu button reaches the keyboard Telegram gets, on the button whatever it
// opens, beside the color.
func TestMenuCarriesEmoji(t *testing.T) {
	const id = "5368324170671202286"
	e := setup(t)
	cfg := Default("ru")
	for i := range cfg.Buttons {
		switch cfg.Buttons[i].Action {
		case "sub":
			cfg.Buttons[i].EmojiID = id
		case "devices":
			cfg.Buttons[i].EmojiID = id
			cfg.Buttons[i].Row = false
		}
	}
	cfg.Buttons = append(cfg.Buttons, MenuButton{ID: "site", Action: "url", Label: "Сайт", URL: "https://example.com", EmojiID: id, On: true})
	if err := settings.Set(e.ctx, e.set, tgate.KeyConfig, cfg); err != nil {
		t.Fatal(err)
	}
	n := e.tg.count()
	e.say(555, "/start "+e.code(e.user.ID))
	e.tg.wait(t, n, "sendMessage")
	e.later()
	n = e.tg.count()
	e.say(555, "меню")
	calls := e.tg.wait(t, n, "sendMessage")
	send, _ := find(calls, "sendMessage")
	kb, _ := send.body["reply_markup"].(map[string]any)
	rows, _ := kb["inline_keyboard"].([]any)
	seen := 0
	for _, r := range rows {
		for _, b := range r.([]any) {
			btn := b.(map[string]any)
			switch btn["text"] {
			case "👤 Подписка", "🛠 Устройства", "Сайт":
				if btn["icon_custom_emoji_id"] != id {
					t.Fatalf("%v: emoji %v", btn["text"], btn["icon_custom_emoji_id"])
				}
				seen++
			default:
				if _, has := btn["icon_custom_emoji_id"]; has {
					t.Fatalf("%v must carry none", btn["text"])
				}
			}
		}
	}
	if seen != 3 {
		t.Fatalf("buttons with the emoji: %d", seen)
	}
}

// The panel's admin sends the bot a premium emoji and gets its id back — the one thing
// Telegram shows nowhere, and what a menu button's field takes. Any other chat sends what it
// always sent: an emoji in a message is an emoji in a message, not a menu question.
func TestEmojiHelpAnswersTheAdmin(t *testing.T) {
	const admin, anna, id = 4242, 555, "5368324170671202286"
	e := setup(t)
	if err := settings.Set(e.ctx, e.set, tgate.KeyInfraAdminChat, int64(admin)); err != nil {
		t.Fatal(err)
	}
	emoji := func(chat int64) {
		e.msgID++
		e.tg.push(tgate.Update{Message: &tgate.Message{MessageID: e.msgID, From: &tgate.User{ID: chat, FirstName: "Admin"},
			Chat: tgate.Chat{ID: chat, Type: "private"}, Text: "🙂",
			Entities: []tgate.MessageEntity{{Type: "custom_emoji", CustomEmojiID: id}, {Type: "bold"}}}})
	}
	n := e.tg.count()
	emoji(admin)
	calls := e.tg.wait(t, n, "sendMessage")
	send, _ := find(calls, "sendMessage")
	if !strings.Contains(text(send), "<code>"+id+"</code>") || !strings.Contains(text(send), "Telegram Premium") {
		t.Fatalf("the admin is answered the id: %q", text(send))
	}

	// The same message from a subscriber: the menu, as for any other text.
	n = e.tg.count()
	emoji(anna)
	calls = e.tg.wait(t, n, "sendMessage")
	for _, c := range calls {
		if strings.Contains(text(c), "<code>") {
			t.Fatalf("a subscriber got an emoji id: %q", text(c))
		}
	}

	// No admin chat connected: nobody is answered the id.
	if err := settings.Set(e.ctx, e.set, tgate.KeyInfraAdminChat, int64(0)); err != nil {
		t.Fatal(err)
	}
	n = e.tg.count()
	emoji(anna)
	calls = e.tg.wait(t, n, "sendMessage")
	for _, c := range calls {
		if strings.Contains(text(c), "<code>") {
			t.Fatalf("an id without an admin chat: %q", text(c))
		}
	}
}

// The answer to the admin's emoji is in both languages, names the id and says what it is
// for; a Premium subscription is what makes it show.
func TestEmojiTexts(t *testing.T) {
	for name, w := range map[string]*words{"ru": &ru, "en": &en} {
		if !strings.Contains(w.emojiID, "%s") || !strings.Contains(w.emojiID, "Premium") || strings.ContainsAny(w.emojiID, "—–") {
			t.Errorf("%s: emojiID %q", name, w.emojiID)
		}
	}
}

// What Telegram refused of the emoji is kept for the admin's page, once per id; the addon
// reaches it through the panel whether it runs in the panel's process or over the Panel API.
func TestEmojiRefusalKept(t *testing.T) {
	for _, rpc := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "rpc"}[rpc], func(t *testing.T) {
			e := setup(t, func(_ *env, w *wiring) { w.RPC = rpc })
			if err := e.bot.d.Backend.NoteEmojiRefused(e.ctx, []string{"5368", "5368", "9999"}, "Bad Request: custom emoji is not allowed"); err != nil {
				t.Fatal(err)
			}
			r, _, err := settings.Get[EmojiRefusal](e.ctx, e.set, KeyEmojiRefused)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.IDs) != 2 || r.IDs[0] != "5368" || r.IDs[1] != "9999" || !strings.Contains(r.Error, "custom emoji") || r.At == 0 {
				t.Fatalf("kept: %+v", r)
			}
			// A call that names no emoji is no refusal of this menu: nothing is written over
			// what the last one said.
			if err := e.bot.d.Backend.NoteEmojiRefused(e.ctx, nil, ""); err != nil {
				t.Fatal(err)
			}
			if r, _, _ = settings.Get[EmojiRefusal](e.ctx, e.set, KeyEmojiRefused); len(r.IDs) != 2 {
				t.Fatalf("nothing told: %+v", r)
			}
		})
	}
}
