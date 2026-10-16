package subs

import (
	"context"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// noticeText is what the entry in place of a working subscription says: why the subscription
// does not work and where to fix it. The bot's link is the subscription's own (a tap opens the
// chat with this subscription in hand) with the start code cut off: inside a profile it is
// read as a plain address, and the code is noise. Without a bot the text stands alone.
//
// These are the words of the subscription itself (the panel's language, cfg.Lang), not of the
// page: the apps show them where the servers are.
func (h *Handler) noticeText(ctx context.Context, u db.User, state string, cfg Config) string {
	en := cfg.Lang == "en"
	var what string
	switch {
	case state == domain.StateLimited:
		what = "⛔ Трафик закончился — продлите подписку в боте"
		if en {
			what = "⛔ The traffic is used up — renew the subscription in the bot"
		}
	default:
		what = "⛔ Подписка закончилась — продлите её в боте"
		if en {
			what = "⛔ The subscription has ended — renew it in the bot"
		}
	}
	if h.tg == nil {
		return what
	}
	if url := BotURL(h.tg.LinkURL(ctx, u.ID)); url != "" {
		return what + ": " + url
	}
	return what
}
