package salenotice

import (
	"strings"
	"testing"
)

func TestText(t *testing.T) {
	s := Sale{Kind: "new", Provider: "stars", Amount: 150, Currency: "XTR", TariffName: "Escape+", TermDays: 30, Created: true, UserID: 7, UserName: "Иван"}
	got := Text("ru", "@ivanov", s)
	for _, want := range []string{"Оплата", "Escape+", "покупка", "⭐ 150", "Звёзды", "на 30 дней", "@ivanov", "Подписка #7 · Иван", "Новая подписка"} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q:\n%s", want, got)
		}
	}

	s = Sale{Kind: "renew", Provider: "balance", Amount: 12000, Currency: "RUB", TariffName: "Escape", UserID: 7, UserName: "Иван"}
	got = Text("en", "Ivan", s)
	for _, want := range []string{"Payment", "renewal", "120 ₽", "Balance", "Subscription #7 · Иван"} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "for ") || strings.Contains(got, "на ") {
		t.Errorf("a payment without a term must not name one:\n%s", got)
	}
}

func TestMethod(t *testing.T) {
	for provider, want := range map[string]string{
		"stars": "Stars", "balance": "Balance", "yookassa": "YooKassa", "addon:yookassa": "yookassa",
	} {
		if got := Method("en", provider); got != want {
			t.Errorf("Method(%q) = %q, want %q", provider, got, want)
		}
	}
	if got := Method("en", "unknown"); got != "" {
		t.Errorf("Method(unknown) = %q, want \"\"", got)
	}
	if got := KindWord("ru", "package"); got != "пакет трафика" {
		t.Errorf("KindWord(ru, package) = %q", got)
	}
}
