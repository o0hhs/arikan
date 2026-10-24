package tgbot

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"mikan/internal/tgapi"
)

// The bot's own buttons, by the id the admin changes their emoji and text with. The menu's
// buttons have their own labels (Config.Buttons); everything the screens build themselves —
// the trial, connect, renew and pay buttons, the back one — is here.
//
// A key names one button wherever it appears: "back" is every screen's back, "trial" the
// trial button on the welcome screen. Emoji replaces the emoji at the start of the button's
// text and keeps the rest, so the price or the count in it stays; Text replaces all of it.
var builtinLabelKeys = map[string]bool{
	"back": true, "sub": true, "devices": true, "connect": true, "renew": true,
	"buy": true, "buy_another": true, "buy_traffic": true, "buy_device": true,
	"subscriptions": true, "promo": true, "promo_clear": true, "balance": true,
	"remove_sub": true, "new_link": true, "trial": true, "free": true,
	"support": true, "open_page": true, "tg_proxy": true, "install": true,
	"one_tap": true, "download": true, "platform_ios": true, "platform_android": true,
	"platform_desktop": true, "referral": true, "ref_share": true, "ref_history": true,
	"channel_join": true, "channel_check": true, "topup": true, "cancel": true,
}

// labelKeyOf is what the admin's labels call this button; "" is one the screens build from
// data (a tariff's name, a device's, a price only) — the admin's label would erase it.
func labelKeyOf(w *words, b tgapi.Button) string {
	if b.CallbackData != "" {
		cmd, payload, _ := strings.Cut(b.CallbackData, ":")
		switch cmd {
		case "m":
			return "back"
		case "s":
			return "sub"
		case "d":
			return "devices"
		case "c":
			return "connect"
		case "r":
			return "renew"
		case "b":
			if b.Text == w.buyAnother {
				return "buy_another"
			}
			return "buy"
		case "f":
			return "referral"
		case "fh":
			return "ref_history"
		case "w":
			return "subscriptions"
		case "pr":
			return "promo"
		case "px":
			return "promo_clear"
		case "bl":
			return "balance"
		case "bt":
			return "topup"
		case "bx":
			return "cancel"
		case "x":
			return "buy_traffic"
		case "e":
			return "buy_device"
		case "rm":
			return "remove_sub"
		case "rl":
			return "new_link"
		case "tr":
			return "trial"
		case "fp":
			return "free"
		case "gc":
			return "channel_check"
		case "ci":
			return "install"
		case "c2":
			switch payload {
			case "ios":
				return "platform_ios"
			case "android":
				return "platform_android"
			case "desktop":
				return "platform_desktop"
			}
		}
		return ""
	}
	// The buttons of the bot's own that open a link; the labels with a name or a price in
	// them are matched by the part before it, which the emoji's place does not touch.
	switch {
	case b.Text == w.support:
		return "support"
	case b.Text == w.openPage:
		return "open_page"
	case b.Text == w.tgProxy:
		return "tg_proxy"
	case b.Text == w.channelJoin:
		return "channel_join"
	case b.Text == w.refShare:
		return "ref_share"
	case strings.HasPrefix(b.Text, strings.TrimSpace(strings.TrimSuffix(w.connectOneTap, "%s"))):
		return "one_tap"
	case strings.HasPrefix(b.Text, strings.TrimSpace(strings.TrimSuffix(w.connectDownload, "%s"))):
		return "download"
	}
	return ""
}

// applyLabels puts the admin's labels and premium emoji on the buttons of the bot's own.
func applyLabels(cfg Config, w *words, kb *tgapi.Keyboard) *tgapi.Keyboard {
	if kb == nil || len(cfg.Labels) == 0 {
		return kb
	}
	for i := range kb.InlineKeyboard {
		for j := range kb.InlineKeyboard[i] {
			btn := &kb.InlineKeyboard[i][j]
			key := labelKeyOf(w, *btn)
			label, ok := cfg.Labels[key]
			if key == "" || !ok {
				continue
			}
			switch {
			case label.Text != "":
				btn.Text = label.Text
			case label.Emoji != "":
				btn.Text = withEmoji(btn.Text, label.Emoji)
			}
			if label.EmojiID != "" {
				btn.IconCustomEmojiID = label.EmojiID
			}
		}
	}
	return kb
}

// withEmoji puts emoji where the leading emoji of text is and keeps the rest: "💳 Продлить"
// with "🔥" is "🔥 Продлить", and "🎁 … (150 ⭐)" keeps its price. A label with no leading
// emoji gets one in front.
func withEmoji(text, emoji string) string {
	i := 0
	for i < len(text) {
		r, size := utf8.DecodeRuneInString(text[i:])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			break
		}
		i += size
	}
	rest := strings.TrimLeft(text[i:], " ")
	if rest == "" {
		return emoji
	}
	return emoji + " " + rest
}
