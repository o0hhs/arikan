package app

import (
	"context"
	"strconv"

	"mikan/internal/panel/salenotice"
	"mikan/internal/panel/store/db"
)

// tellAdminOfSale tells the admin, in the chat connected for the panel's alerts, that a
// payment went through: what was sold, for how much, by which method, for how long and who
// bought it. The alerts' queue takes the message and tries again, so a bot that is down for
// a minute does not lose the news; without the bot or the chat nobody is told (the Payments
// page has it anyway).
func (p *Panel) tellAdminOfSale(ctx context.Context, pay db.Payment, u db.User, created bool) {
	if p.Alerts == nil || p.Gateway == nil {
		return
	}
	who := "tg " + strconv.FormatInt(pay.TgID, 10)
	if c, err := p.st.Q.GetTgChat(ctx, pay.TgID); err == nil {
		switch {
		case c.Username != "":
			who = "@" + c.Username
		case c.FirstName != "":
			who = c.FirstName
		}
	}
	p.Alerts.Notify(ctx, "sale/"+strconv.FormatInt(pay.ID, 10), salenotice.Text(p.Gateway.Lang(ctx), who, salenotice.Sale{
		Kind:       pay.Kind,
		Provider:   pay.Provider,
		Amount:     pay.Amount,
		Currency:   pay.Currency,
		TariffName: pay.TariffName,
		TermDays:   pay.TermDays.Int64,
		Created:    created,
		UserID:     u.ID,
		UserName:    u.Name,
	}))
}
