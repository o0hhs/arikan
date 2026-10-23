package subs

import (
	"context"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// noticeLines is the banner an ended subscription carries in place of its servers: five
// lines the apps show as the names of dead entries, together saying what happened, that the
// server above them still takes Telegram, and the two ways out — the bot or support.
//
// These are the words of the subscription itself (the panel's language, cfg.Lang), not of
// the page: the apps show them where the servers are.
func (h *Handler) noticeLines(ctx context.Context, u db.User, state string, cfg Config) []string {
	en := cfg.Lang == "en"
	what := "У вас закончился тариф"
	if state == domain.StateLimited {
		what = "У вас закончился трафик"
	}
	if en {
		what = "Your plan has ended"
		if state == domain.StateLimited {
			what = "Your traffic is used up"
		}
	}
	lines := []string{
		"⛔ " + what,
		"Или вы достигли лимита",
		"Используйте сервер выше чтобы",
		"Продлить тариф через бота",
		"Или написать нам в поддержку",
	}
	if en {
		lines = []string{
			"⛔ " + what,
			"Or you have hit the limit",
			"Use the server above to",
			"Renew the plan in the bot",
			"Or write to support",
		}
	}
	if h.tg == nil {
		return lines
	}
	// The bot's link is the subscription's own (a tap opens the chat with this subscription
	// in hand) with the start code cut off: inside a profile it is read as a plain address.
	if url := BotURL(h.tg.LinkURL(ctx, u.ID)); url != "" {
		lines[3] += ": " + url
	}
	return lines
}
