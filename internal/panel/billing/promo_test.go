package billing

import (
	"context"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/store/db"
)

// The bot enters a code through UsePromo: a bonus lands on the subscription at once, a
// discount only comes back for the invoice (its use is not spent by entering it).
func TestUsePromoBonusAndDiscount(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.s.d.Promo = promo.New(e.st, func() time.Time { return e.now })
	u, err := e.s.d.Users.Create(ctx, domain.CreateInput{Name: "mine", TariffID: e.sale.ID})
	must(t, err)
	must(t, e.st.Q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: 555, CreatedAt: 1}))

	_, err = e.st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "SALE25", Type: "percent", Value: 25, Currency: "XTR",
		PerUserLimit: 1, TariffIds: "[]", Enabled: 1, CreatedAt: e.now.Unix()})
	must(t, err)
	p, err := e.s.UsePromo(ctx, 555, u.ID, "sale25")
	if err != nil || p.Code != "SALE25" || p.Type != "percent" || p.Percent != 25 {
		t.Fatalf("a discount: %+v %v", p, err)
	}
	// Entering the discount did not spend it: the invoice still takes it.
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, UserID: u.ID, TariffID: e.sale.ID, Provider: Stars, PromoCode: "SALE25"}); err != nil {
		t.Fatalf("the kept code does not work at the invoice: %v", err)
	}

	_, err = e.st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "WEEK", Type: "days", Value: 7,
		PerUserLimit: 1, TariffIds: "[]", Enabled: 1, CreatedAt: e.now.Unix()})
	must(t, err)
	before, err := e.st.Q.GetUser(ctx, u.ID)
	must(t, err)
	b, err := e.s.UsePromo(ctx, 555, u.ID, "WEEK")
	if err != nil || b.Days != 7 {
		t.Fatalf("a days code: %+v %v", b, err)
	}
	after, err := e.st.Q.GetUser(ctx, u.ID)
	must(t, err)
	if after.ExpiresAt.Int64-before.ExpiresAt.Int64 != 7*86400 {
		t.Fatalf("the term grew by %d, want %d", after.ExpiresAt.Int64-before.ExpiresAt.Int64, 7*86400)
	}
	// A bonus code does not go twice.
	if _, err := e.s.UsePromo(ctx, 555, u.ID, "WEEK"); err == nil {
		t.Fatal("the same code applied twice")
	}
}
