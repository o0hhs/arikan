package app

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"

	"mikan/internal/panel/store/db"
	"mikan/internal/textutil"
)

// tellAdminOfSale tells the admin, in the chat connected for the panel's alerts, that a
// payment went through: what was sold, for how much and who bought it. The alerts' queue
// takes the message and tries again, so a bot that is down for a minute does not lose the
// news; without the bot or the chat nobody is told (the Payments page has it anyway).
func (p *Panel) tellAdminOfSale(ctx context.Context, pay db.Payment, u db.User, created bool) {
	if p.Alerts == nil || p.Gateway == nil {
		return
	}
	who := "tg " + strconv.FormatInt(pay.TgID, 10)
	if c, err := p.st.Q.GetTgChat(ctx, pay.TgID); err == nil && c.Username != "" {
		who = "@" + c.Username
	}
	p.Alerts.Notify(ctx, "sale/"+strconv.FormatInt(pay.ID, 10), saleNoticeText(p.Gateway.Lang(ctx), pay, u, created, who))
}

// saleNoticeText is the message in Telegram HTML: the plan, what kind of payment it was,
// the amount and the buyer. Never a token.
func saleNoticeText(lang string, pay db.Payment, u db.User, created bool, who string) string {
	ru := !textutil.Lang(lang).En()
	w := saleWords{ru: ru, header: "Payment", subscription: "Subscription", created: "A new subscription",
		kinds: map[string]string{"new": "purchase", "renew": "renewal", "package": "traffic package", "device": "device", "trial": "trial"}}
	if ru {
		w.header, w.subscription, w.created = "Оплата", "Подписка", "Новая подписка"
		w.kinds = map[string]string{"new": "покупка", "renew": "продление", "package": "пакет трафика", "device": "устройство", "trial": "пробный период"}
	}
	kind := pay.Kind
	if w.kinds[kind] != "" {
		kind = w.kinds[kind]
	}
	name := pay.TariffName
	if name == "" {
		name = w.header
	}
	lines := []string{"💸 <b>" + w.header + "</b>",
		html.EscapeString(name) + " · " + kind + " · " + salePrice(pay.Amount, pay.Currency), html.EscapeString(who)}
	if u.ID != 0 {
		lines = append(lines, w.subscription+" #"+strconv.FormatInt(u.ID, 10))
	}
	if created {
		lines = append(lines, w.created)
	}
	return strings.Join(lines, "\n")
}

// saleWords is the message's words in the panel's language.
type saleWords struct {
	header, subscription, created string
	kinds                         map[string]string
	ru                            bool
}

// salePrice is an amount the admin reads: Stars are whole, the rest are minor units.
func salePrice(amount int64, currency string) string {
	if currency == "XTR" {
		return fmt.Sprintf("%d ⭐", amount)
	}
	return fmt.Sprintf("%d.%02d %s", amount/100, amount%100, html.EscapeString(currency))
}
