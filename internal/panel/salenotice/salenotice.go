// Package salenotice builds the admin's text of a successful payment, in the panel's
// language: the plan, the kind, the price, the method, the term and the buyer. It is the
// one source for both senders — the panel's alerts chat and the bot's own admins.
package salenotice

import (
	"fmt"
	"html"
	"strconv"
	"strings"

	"mikan/internal/panel/model"
	"mikan/internal/textutil"
)

// Sale is what a payment gave, as the notice tells it.
type Sale struct {
	Kind       string // new | renew | package | device | trial
	Provider   string // stars | balance | addon:<id> | ...
	Amount     int64  // minor units
	Currency   string
	TariffName string
	TermDays   int64
	Created    bool
	UserID     int64
	UserName   string
}

// Text is the message in Telegram HTML: the plan, what kind of payment it was, the amount,
// the method, the term and the buyer. Never a token.
func Text(lang, who string, s Sale) string {
	ru := !textutil.Lang(lang).En()
	kinds := kindWords(ru)
	header, subscription, created, method, term := "Payment", "Subscription", "A new subscription", "Method", "for %d days"
	if ru {
		header, subscription, created, method, term = "Оплата", "Подписка", "Новая подписка", "Метод", "на %d дней"
	}
	kind := s.Kind
	if kinds[kind] != "" {
		kind = kinds[kind]
	}
	name := s.TariffName
	if name == "" {
		name = header
	}
	lines := []string{"💸 <b>" + header + "</b>",
		html.EscapeString(name) + " · " + kind + " · " + Price(s.Amount, s.Currency),
		html.EscapeString(who)}
	if m := Method(lang, s.Provider); m != "" {
		lines = append(lines, method+": "+html.EscapeString(m))
	}
	if s.TermDays > 0 {
		lines = append(lines, fmt.Sprintf(term, s.TermDays))
	}
	if s.UserID != 0 {
		line := subscription + " #" + strconv.FormatInt(s.UserID, 10)
		if s.UserName != "" {
			line += " · " + html.EscapeString(s.UserName)
		}
		lines = append(lines, line)
	}
	if s.Created {
		lines = append(lines, created)
	}
	return strings.Join(lines, "\n")
}

// KindWord is the kind of a payment as the admin reads it; "" for an unknown kind.
func KindWord(lang, kind string) string {
	return kindWords(!textutil.Lang(lang).En())[kind]
}

// Method is the way the payment came: a built-in provider by its own word, an adapter by
// its id (addon:<id>). "" when the provider is not known.
func Method(lang, provider string) string {
	if m, ok := methodWords(!textutil.Lang(lang).En())[provider]; ok {
		return m
	}
	if id := model.AddonID(provider); id != "" {
		return id
	}
	return ""
}

func kindWords(ru bool) map[string]string {
	if ru {
		return map[string]string{"new": "покупка", "renew": "продление", "package": "пакет трафика", "device": "устройство", "trial": "пробный период"}
	}
	return map[string]string{"new": "purchase", "renew": "renewal", "package": "traffic package", "device": "device", "trial": "trial"}
}

func methodWords(ru bool) map[string]string {
	if ru {
		return map[string]string{"stars": "Звёзды", "balance": "Баланс", "yookassa": "YooKassa", "cryptobot": "CryptoBot"}
	}
	return map[string]string{"stars": "Stars", "balance": "Balance", "yookassa": "YooKassa", "cryptobot": "CryptoBot"}
}

// Price is an amount the admin reads: Stars whole, the rest in the currency's sign, with
// the kopecks when they are not zero.
func Price(amount int64, currency string) string {
	if currency == "XTR" {
		return "⭐ " + strconv.FormatInt(amount, 10)
	}
	sign, ok := currencySigns[currency]
	if !ok {
		sign = currency
	}
	s := strconv.FormatInt(amount/100, 10)
	if k := amount % 100; k != 0 {
		s += fmt.Sprintf(".%02d", k)
	}
	return s + " " + sign
}

// currencySigns: how amounts of a balance's currency show; another shows by its code.
var currencySigns = map[string]string{"RUB": "₽", "USD": "$", "EUR": "€", "UAH": "₴", "BYN": "Br", "KZT": "₸", "UZS": "сўм", "USDT": "USDT"}
