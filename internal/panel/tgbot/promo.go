package tgbot

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"mikan/internal/tgapi"
)

// Promo codes in the chat: a code is typed as a message, so the bot has no page in the way.
// The screen asks for one; a discount is kept for the next purchase and goes with the
// invoice, a bonus is given at once, an access code opens its tariff.

const (
	// promoAwaitTTL is how long after the screen asked a message is read as a code even if
	// it does not look like one.
	promoAwaitTTL = 15 * time.Minute
	// promoCodeMax bounds what is sent to the panel as a code.
	promoCodeMax = 40
)

// promoLike is a text a code may be: no spaces, no punctuation, short.
var promoLike = regexp.MustCompile(`^[\p{L}\p{N}_-]{2,40}$`)

// promoState is what a chat remembers of its codes: when the screen asked for one, and the
// discount kept for the next purchase.
type promoState struct {
	await time.Time
	code  string
}

func (b *Bot) promo(chat int64) promoState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.promos[chat]
}

// setPromo changes the chat's state with every other change of the bot's under the same
// lock.
func (b *Bot) setPromo(chat int64, fn func(*promoState)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.promos[chat]
	fn(&st)
	if st.code == "" && !st.await.After(b.d.Now()) {
		delete(b.promos, chat)
		return
	}
	b.promos[chat] = st
}

// savedPromo is the discount code an invoice takes.
func (b *Bot) savedPromo(chat int64) string { return b.promo(chat).code }

// promoEntry is the screen the button opens: it says how a code is entered here and shows
// the one kept for the purchase.
func (b *Bot) promoEntry(ctx context.Context, cfg Config, w *words, chat int64, notice string) (string, *tgapi.Keyboard) {
	st := b.promo(chat)
	// Before the now it was asked, the next message is read as a code.
	b.setPromo(chat, func(s *promoState) { s.await = b.d.Now().Add(promoAwaitTTL) })
	lines := []string{"<b>" + html.EscapeString(w.promo) + "</b>", "", html.EscapeString(w.promoIntro)}
	rows := [][]tgapi.Button{}
	if st.code != "" {
		lines = append(lines, "", html.EscapeString(fmt.Sprintf(w.promoSaved, st.code)))
		label, target := w.promoBuy, "b"
		if _, _, ok := b.subs(ctx, chat); ok {
			label, target = w.renewButton, "r"
		}
		rows = append(rows, []tgapi.Button{{Text: label, CallbackData: target, Style: tgapi.StyleSuccess}})
		rows = append(rows, []tgapi.Button{{Text: w.promoClear, CallbackData: "px"}})
	}
	rows = append(rows, []tgapi.Button{{Text: w.back, CallbackData: "m"}})
	return withNoticeText(notice, strings.Join(lines, "\n")), &tgapi.Keyboard{InlineKeyboard: rows}
}

// promoMessage takes a message as a promo code and answers it: true when it was one. The
// screen's ask makes the next message a code whatever it looks like; without it only a text
// shaped like a code is tried, and one the panel does not know is left to the menu.
func (b *Bot) promoMessage(ctx context.Context, cfg Config, w *words, m *tgapi.Message) bool {
	text := strings.TrimSpace(m.Text)
	if text == "" || strings.HasPrefix(text, "/") || strings.Contains(text, "://") || len(text) > promoCodeMax {
		return false
	}
	st := b.promo(m.Chat.ID)
	asked := st.await.After(b.d.Now())
	if !asked && !promoLike.MatchString(text) {
		return false
	}
	code := strings.ToUpper(strings.Join(strings.Fields(text), ""))
	if code == "" {
		return false
	}
	// The ask is spent by the message it was meant for.
	b.setPromo(m.Chat.ID, func(s *promoState) { s.await = time.Time{} })
	_, u, hasSub := b.subs(ctx, m.Chat.ID)
	var userID int64
	if hasSub {
		userID = u.ID
	}
	p, err := b.d.Backend.Shop().Promo(ctx, m.Chat.ID, userID, code)
	if err != nil {
		if !asked {
			return false // a text that merely looks like a code; the menu answers it
		}
		b.freshMenu(m.Chat.ID, w.promoBad)
		return true
	}
	switch {
	case p.Days > 0:
		b.freshScreen(m.Chat.ID, "s", fmt.Sprintf(w.promoDays, p.Days))
	case p.Bytes > 0:
		b.freshScreen(m.Chat.ID, "s", fmt.Sprintf(w.promoTraffic, w.bytes(p.Bytes)))
	case p.Percent > 0 || p.Amount > 0:
		// Kept for the invoice: the discount is reserved by the order, not here.
		b.setPromo(m.Chat.ID, func(s *promoState) { s.code = p.Code })
		cut := fmt.Sprintf("%d%%", p.Percent)
		if p.Percent == 0 {
			cut = w.price(p.Amount, p.Currency)
		}
		b.freshScreen(m.Chat.ID, "pr", fmt.Sprintf(w.promoKept, cut))
	case p.Tariff != nil:
		if p.Subscription != "" {
			b.freshScreen(m.Chat.ID, "s", fmt.Sprintf(w.promoAccessFree, p.Tariff.Name, p.Subscription))
		} else {
			b.freshScreen(m.Chat.ID, "b", fmt.Sprintf(w.promoAccess, p.Tariff.Name))
		}
	default:
		if !asked {
			return false
		}
		b.freshMenu(m.Chat.ID, w.promoBad)
	}
	return true
}
