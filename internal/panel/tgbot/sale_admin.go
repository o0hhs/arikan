package tgbot

import (
	"context"
	"strconv"

	"mikan/internal/panel/events"
	"mikan/internal/panel/model"
	"mikan/internal/panel/salenotice"
	"mikan/internal/tgapi"
)

// tellAdminsOfSale tells the bot's admins, in their own chats, that a payment went through:
// who bought it, what, for how much, by which method and for how long. The buyer's own chat
// hears the paid message; an admin who is also the buyer is not told twice.
func (b *Bot) tellAdminsOfSale(ctx context.Context, tgID int64, p events.Payment, u model.User) {
	admins, err := b.d.Backend.Admins(ctx)
	if err != nil || len(admins) == 0 {
		return
	}
	text := salenotice.Text(b.lang(ctx), b.buyerName(ctx, tgID), salenotice.Sale{
		Kind:       p.Kind,
		Provider:   p.Provider,
		Amount:     p.Amount,
		Currency:   p.Currency,
		TariffName: p.TariffName,
		TermDays:   p.TermDays,
		Created:    p.Created,
		UserID:     u.ID,
		UserName:    u.Name,
	})
	for _, chat := range admins {
		if chat == tgID {
			continue
		}
		b.d.Outbox.Notice(chat, func(ctx context.Context, c *tgapi.Client) error {
			_, err := c.Send(ctx, chat, text, nil, false)
			return err
		}, nil)
	}
}

// buyerName is the buyer as the admin reads it: @username, else the first name, else the id.
func (b *Bot) buyerName(ctx context.Context, tgID int64) string {
	if c, err := b.d.Backend.Chat(ctx, tgID); err == nil {
		switch {
		case c.Username != "":
			return "@" + c.Username
		case c.FirstName != "":
			return c.FirstName
		}
	}
	return "tg " + strconv.FormatInt(tgID, 10)
}
