package billing

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/store/db"
)

func TestWebInvoice(t *testing.T) {
	e, fa := addonEnv(t)
	ctx := context.Background()
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "12", "secret_key": adapterSecret}); err != nil {
		t.Fatal(err)
	}
	e.s.SetWebReturn(func(context.Context, string) string { return "https://shop.example/back" })

	if _, err := e.s.Invoice(ctx, InvoiceRequest{Web: true, TariffID: e.sale.ID, Provider: Stars, WebAccountID: 1}); !errors.Is(err, ErrProviderOff) {
		t.Fatalf("stars in the browser: %v", err)
	}
	if _, err := e.s.Invoice(ctx, InvoiceRequest{Web: true, TariffID: e.sale.ID, Provider: AddonPrefix + "fake"}); !errors.Is(err, ErrNotYours) {
		t.Fatalf("new purchase with nobody: %v", err)
	}

	acc, err := e.st.Q.CreateWebAccount(ctx, db.CreateWebAccountParams{Email: "buyer@example.com", CreatedAt: e.now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.s.Invoice(ctx, InvoiceRequest{Web: true, WebAccountID: acc.ID, TariffID: e.sale.ID, Provider: AddonPrefix + "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if p.TgID != 0 || !p.Web || p.WebAccountID.Int64 != acc.ID || p.Kind != "new" {
		t.Fatalf("payment: %+v", p)
	}
	if len(fa.invoices) != 1 || fa.invoices[0].ReturnURL != "https://shop.example/back" {
		t.Fatalf("return url: %+v", fa.invoices)
	}
	if err := e.s.Apply(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if e.payment(p.ID).Status != PaymentPending {
		t.Fatal("an unpaid invoice changed")
	}
	if err := e.pay(p); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Apply(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	got := e.payment(p.ID)
	if got.Status != PaymentApplied || !got.UserID.Valid {
		t.Fatalf("applied: %+v", got)
	}
	u, err := e.st.Q.GetUser(ctx, got.UserID.Int64)
	if err != nil || u.Source != domain.UserFromWeb || u.Contact != "buyer@example.com" {
		t.Fatalf("user: %+v %v", u, err)
	}
	link, err := e.st.Q.GetWebLink(ctx, u.ID)
	if err != nil || link.AccountID != acc.ID {
		t.Fatalf("link: %+v %v", link, err)
	}
	// The same account renews it. A second email does not.
	other, err := e.st.Q.CreateWebAccount(ctx, db.CreateWebAccountParams{Email: "other@example.com", CreatedAt: e.now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Invoice(ctx, InvoiceRequest{Web: true, WebAccountID: other.ID, UserID: u.ID, TariffID: e.sale.ID, Provider: AddonPrefix + "fake"}); !errors.Is(err, ErrNotYours) {
		t.Fatalf("someone else's renewal: %v", err)
	}
	again, err := e.s.Invoice(ctx, InvoiceRequest{Web: true, WebAccountID: acc.ID, UserID: u.ID, TariffID: e.sale.ID, Provider: AddonPrefix + "fake"})
	if err != nil || again.Kind != "renew" {
		t.Fatalf("renew: %+v %v", again, err)
	}

	// The subscription token renews that subscription and no other.
	if _, err := e.s.Invoice(ctx, InvoiceRequest{Web: true, TokenRenew: true, UserID: u.ID, TariffID: e.sale.ID, Provider: AddonPrefix + "fake"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Invoice(ctx, InvoiceRequest{Web: true, TokenRenew: true, TariffID: e.sale.ID, Provider: AddonPrefix + "fake"}); !errors.Is(err, ErrNotYours) {
		t.Fatalf("token with no subscription: %v", err)
	}
}

func TestWebPromoIsPerAccount(t *testing.T) {
	e, _ := addonEnv(t)
	ctx := context.Background()
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "12", "secret_key": adapterSecret}); err != nil {
		t.Fatal(err)
	}
	e.s.d.Promo = promo.New(e.st, func() time.Time { return e.now })
	pc, err := e.st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "WEB10", Name: "web", Type: "percent", Value: 10, PerUserLimit: 1, TariffIds: "[]", Enabled: 1, CreatedAt: e.now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	_ = pc
	a, err := e.st.Q.CreateWebAccount(ctx, db.CreateWebAccountParams{Email: "a@example.com", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.st.Q.CreateWebAccount(ctx, db.CreateWebAccountParams{Email: "b@example.com", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	first, err := e.s.Invoice(ctx, InvoiceRequest{Web: true, WebAccountID: a.ID, TariffID: e.sale.ID, Provider: AddonPrefix + "fake", PromoCode: "WEB10"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Amount >= 19900 {
		t.Fatalf("discount not taken: %d", first.Amount)
	}
	if _, err := e.st.Q.SetPaymentStatus(ctx, db.SetPaymentStatusParams{NewStatus: PaymentFailed, ID: first.ID, OldStatus: PaymentPending}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Invoice(ctx, InvoiceRequest{Web: true, WebAccountID: a.ID, TariffID: e.sale.ID, Provider: AddonPrefix + "fake", PromoCode: "WEB10"}); !errors.Is(err, promo.ErrUserLimit) {
		t.Fatalf("the same browser account used the code twice: %v", err)
	}
	second, err := e.s.Invoice(ctx, InvoiceRequest{Web: true, WebAccountID: b.ID, TariffID: e.sale.ID, Provider: AddonPrefix + "fake", PromoCode: "WEB10"})
	if err != nil || second.Amount >= 19900 {
		t.Fatalf("another account: %+v %v", second, err)
	}
}

// pay marks a pending addon invoice paid the way a confirmed provider does, without
// trusting a browser redirect.
func (e *env) pay(p db.Payment) error {
	n, err := e.st.Q.MarkPaymentPaid(context.Background(), db.MarkPaymentPaidParams{ExternalID: p.ExternalID, PaidAt: sql.NullInt64{Int64: e.now.Unix(), Valid: true}, ID: p.ID})
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("not marked")
	}
	return nil
}
