package tgbot

import (
	"strings"
	"testing"

	"mikan/internal/tgapi"
)

// An emoji changes only the sign in front of the button's label: the price or the count in
// it stays. A text replaces the label whole, and a button built from data — a tariff's name,
// a price alone — is left as it is.
func TestButtonLabels(t *testing.T) {
	cfg := Default("ru")
	cfg.Labels = map[string]ButtonLabel{
		"trial": {Emoji: "🔥"},
		"back":  {Text: "↩️ В меню"},
		"buy":   {EmojiID: "5368324170671202286"},
	}
	w := wordsFor("ru")
	kb := &tgapi.Keyboard{InlineKeyboard: [][]tgapi.Button{
		{{Text: "🎁 Пробный период (150 ⭐)", CallbackData: "tr"}},
		{{Text: w.back, CallbackData: "m"}},
		{{Text: w.buy, CallbackData: "b"}},
		{{Text: "Тариф «Базовый» · 150 ⭐", CallbackData: "t:5"}},
	}}
	applyLabels(cfg, w, kb)
	rows := kb.InlineKeyboard
	if got := rows[0][0].Text; got != "🔥 Пробный период (150 ⭐)" {
		t.Fatalf("the trial's emoji: %q", got)
	}
	if got := rows[1][0].Text; got != "↩️ В меню" {
		t.Fatalf("the back button's text: %q", got)
	}
	if got := rows[2][0].IconCustomEmojiID; got != "5368324170671202286" {
		t.Fatalf("the premium emoji: %q", got)
	}
	if got := rows[3][0].Text; got != "Тариф «Базовый» · 150 ⭐" {
		t.Fatalf("a data button was renamed: %q", got)
	}
	// The second pass of the same labels changes nothing: screens that render another's
	// answer apply them twice.
	applyLabels(cfg, w, kb)
	if got := rows[0][0].Text; got != "🔥 Пробный период (150 ⭐)" {
		t.Fatalf("applied twice: %q", got)
	}
}

func TestWithEmoji(t *testing.T) {
	for _, c := range []struct{ text, emoji, want string }{
		{"💳 Продлить", "🔥", "🔥 Продлить"},
		{"Продлить", "🔥", "🔥 Продлить"},
		{"🎁 Пробный период (150 ⭐)", "🚀", "🚀 Пробный период (150 ⭐)"},
		{"🔁 Подписки (2)", "📚", "📚 Подписки (2)"},
		{"7 дней", "📅", "📅 7 дней"},
		{"💳", "🔥", "🔥"},
	} {
		if got := withEmoji(c.text, c.emoji); got != c.want {
			t.Errorf("%q with %q: %q, want %q", c.text, c.emoji, got, c.want)
		}
	}
}

// A key the panel has no button for is refused rather than saved and silently ignored.
func TestLabelKeysChecked(t *testing.T) {
	c := Default("ru")
	c.Labels = map[string]ButtonLabel{"no_such_button": {Emoji: "🔥"}}
	if err := c.Validate(); err != ErrLabelKey {
		t.Fatalf("an unknown key: %v", err)
	}
	c.Labels = map[string]ButtonLabel{"trial": {Text: strings.Repeat("x", maxLabel+1)}}
	if err := c.Validate(); err != ErrLabel {
		t.Fatalf("a long label: %v", err)
	}
}
