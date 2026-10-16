package billing

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/store/db"
)

// hidden is a tariff off the shop: on_sale 0, priced, with a term, as an access code's hidden
// tariff is. It is what an ordinary buyer must not see or buy.
func (e *env) hidden() db.Tariff {
	e.t.Helper()
	ctx := context.Background()
	t, err := e.st.Q.CreateTariff(ctx, db.CreateTariffParams{Name: "Секретный", TrafficLimit: sql.NullInt64{Int64: 300 << 30, Valid: true}, DurationDays: 30,
		DeviceLimit: sql.NullInt64{Int64: 5, Valid: true}, ResetStrategy: "period", Sort: 9,
		PriceStars: sql.NullInt64{Int64: 900, Valid: true}, PriceRub: sql.NullInt64{Int64: 99000, Valid: true}, OnSale: 0, CreatedAt: e.now.Unix()})
	must(e.t, err)
	return t
}

// accessCode writes the access code of the hidden tariff: access_paid unlocks the purchase,
// access_free gives the subscription away.
func (e *env) accessCode(typ string, tariff db.Tariff) db.PromoCode {
	e.t.Helper()
	p, err := e.st.Q.CreatePromoCode(context.Background(), db.CreatePromoCodeParams{Code: "SECRET", Type: typ, Value: 1, AccessMode: func() string {
		if typ == promo.TypeAccessFree {
			return "free"
		}
		return "paid"
	}(), PerUserLimit: 1, TariffIds: "[" + itoa(tariff.ID) + "]", Enabled: 1, CreatedAt: e.now.Unix()})
	must(e.t, err)
	return p
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func (e *env) withPromo() {
	e.s.d.Promo = promo.New(e.st, func() time.Time { return e.now })
}

// TestHiddenTariffInvisible: without a code the hidden tariff is in no offer and no invoice
// goes through — for a new subscription and for a renewal alike.
func TestHiddenTariffInvisible(t *testing.T) {
	e := newEnv(t)
	e.withPromo()
	ctx := context.Background()
	hidden := e.hidden()

	offers, _, err := e.s.Offers(ctx, 555)
	must(t, err)
	for _, o := range offers {
		if o.Tariff.ID == hidden.ID {
			t.Fatal("the hidden tariff is on offer without a code")
		}
	}
	for _, user := range []int64{0, 0} {
		if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, UserID: user, TariffID: hidden.ID, Provider: Stars}); !errors.Is(err, ErrNotForSale) {
			t.Fatalf("invoice of the hidden tariff: %v, want ErrNotForSale", err)
		}
	}
}

// TestAccessCodeOpensTheHiddenTariff: with the code redeemed the tariff is on offer and can
// be bought; another account still cannot see or buy it.
func TestAccessCodeOpensTheHiddenTariff(t *testing.T) {
	e := newEnv(t)
	e.withPromo()
	ctx := context.Background()
	hidden := e.hidden()
	code := e.accessCode(promo.TypeAccessPaid, hidden)

	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: hidden.ID, Provider: Stars}); !errors.Is(err, ErrNotForSale) {
		t.Fatalf("before the code: %v, want ErrNotForSale", err)
	}
	if _, err := e.s.d.Promo.GrantAccess(ctx, 555, 0, code.Code); err != nil {
		t.Fatalf("grant: %v", err)
	}

	offers, _, err := e.s.Offers(ctx, 555)
	must(t, err)
	found := false
	for _, o := range offers {
		if o.Tariff.ID == hidden.ID {
			found = true
			if o.Stars != 900 {
				t.Fatalf("the opened tariff costs %d stars, want 900", o.Stars)
			}
		}
	}
	if !found {
		t.Fatal("the hidden tariff is not on offer after the code")
	}
	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: hidden.ID, Provider: Stars})
	must(t, err)
	if p.Amount != 900 || p.Status != "pending" {
		t.Fatalf("invoice of the opened tariff: %+v", p)
	}
	// Telegram asks about the very same payment as it is paid.
	if err := e.s.PreCheckout(ctx, 555, p.Payload, "XTR", 900); err != nil {
		t.Fatalf("precheckout of an opened tariff: %v", err)
	}

	// Another account: neither the offer nor the invoice.
	other, _, err := e.s.Offers(ctx, 777)
	must(t, err)
	for _, o := range other {
		if o.Tariff.ID == hidden.ID {
			t.Fatal("the hidden tariff is on offer for an account with no code")
		}
	}
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 777, TariffID: hidden.ID, Provider: Stars}); !errors.Is(err, ErrNotForSale) {
		t.Fatalf("another account: %v, want ErrNotForSale", err)
	}
}

// TestAccessCodeExpires: an access with a ttl opens the tariff only inside it, and a code the
// admin turns off takes the access away.
func TestAccessCodeExpires(t *testing.T) {
	e := newEnv(t)
	e.withPromo()
	ctx := context.Background()
	hidden := e.hidden()
	code := e.accessCode(promo.TypeAccessPaid, hidden)
	if _, err := e.st.Q.UpdatePromoCode(context.Background(), db.UpdatePromoCodeParams{Code: code.Code, Name: code.Name, Description: code.Description,
		Type: code.Type, Value: code.Value, Currency: code.Currency, PerUserLimit: code.PerUserLimit, DiscountTtl: code.DiscountTtl,
		MinOrder: code.MinOrder, MaxDiscount: code.MaxDiscount, TariffIds: code.TariffIds, FirstPurchaseOnly: 0, NewUsersOnly: 0, Enabled: 1,
		AccessMode: code.AccessMode, AccessTtl: 3600, ID: code.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.d.Promo.GrantAccess(ctx, 555, 0, code.Code); err != nil {
		t.Fatalf("grant: %v", err)
	}
	ok, err := e.s.d.Promo.HasAccess(ctx, 555, hidden.ID)
	must(t, err)
	if !ok {
		t.Fatal("the access is gone inside its ttl")
	}
	// Past the ttl.
	e.now = e.now.Add(2 * time.Hour)
	if ok, err := e.s.d.Promo.HasAccess(ctx, 555, hidden.ID); err != nil || ok {
		t.Fatalf("past the ttl: %v %v, want no access", ok, err)
	}
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: hidden.ID, Provider: Stars}); !errors.Is(err, ErrNotForSale) {
		t.Fatalf("past the ttl: %v, want ErrNotForSale", err)
	}
	// A fresh grant, then the admin turns the code off.
	if _, err := e.s.d.Promo.GrantAccess(ctx, 556, 0, code.Code); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := e.st.Q.SetPromoEnabled(ctx, db.SetPromoEnabledParams{ID: code.ID, Enabled: 0}); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.s.d.Promo.HasAccess(ctx, 556, hidden.ID); err != nil || ok {
		t.Fatalf("a code turned off: %v %v, want no access", ok, err)
	}
}

// TestFreeAccessCodeGivesTheSubscription: access_free hands out a subscription on the tariff,
// with its limits, once per the code's own rules.
func TestFreeAccessCodeGivesTheSubscription(t *testing.T) {
	e := newEnv(t)
	e.withPromo()
	ctx := context.Background()
	hidden := e.hidden()
	code := e.accessCode(promo.TypeAccessFree, hidden)
	access, err := e.s.d.Promo.GrantAccess(ctx, 555, 0, code.Code)
	must(t, err)
	u, err := e.s.AccessPlan(ctx, 555, hidden, nil)
	must(t, err)
	if !u.TariffID.Valid || u.TariffID.Int64 != hidden.ID {
		t.Fatalf("the subscription is on tariff %v, want %d", u.TariffID, hidden.ID)
	}
	if u.TrafficLimit.Int64 != 300<<30 || u.DeviceLimit.Int64 != 5 {
		t.Fatalf("the subscription got %d bytes and %d devices, want the tariff's", u.TrafficLimit.Int64, u.DeviceLimit.Int64)
	}
	if u.Source != domain.UserFromCode {
		t.Fatalf("source %q, want %q", u.Source, domain.UserFromCode)
	}
	must(t, e.s.d.Promo.SetAccessUser(ctx, access.ID, u.ID))
	// The second redemption is the same access, not another use.
	again, err := e.s.d.Promo.GrantAccess(ctx, 555, 0, code.Code)
	must(t, err)
	if again.ID != access.ID {
		t.Fatalf("second grant %d, want the same %d", again.ID, access.ID)
	}
	after, err := e.st.Q.GetPromoCode(ctx, code.ID)
	must(t, err)
	if after.UsedCount != 1 {
		t.Fatalf("used %d, want 1", after.UsedCount)
	}
	// And the account it gave the subscription to has the access the lists show.
	acc, ok, err := e.s.d.Promo.AccessOf(ctx, 555, hidden.ID)
	must(t, err)
	if !ok || acc.Code != code.Code || acc.Mode != promo.TypeAccessFree {
		t.Fatalf("access: %+v %v", acc, ok)
	}
}
