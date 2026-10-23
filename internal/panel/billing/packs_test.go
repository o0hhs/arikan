package billing

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
)

// A pack of extra device places rides the invoice: its monthly price adds to the term's,
// its share of every day. Applied, its slots widen the subscription's device limit and the
// pack stays with it; a renewal without one takes the places back.
func TestDevicePackRidesThePurchase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pack, err := e.st.Q.CreateDevicePack(ctx, db.CreateDevicePackParams{Title: "+2 устройства", Slots: 2,
		PriceRub: 5000, PriceStars: sql.NullInt64{Int64: 100, Valid: true}, OnSale: true, CreatedAt: e.now.Unix()})
	must(t, err)
	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: Stars, DevicePackID: pack.ID})
	must(t, err)
	// Thirty days of a 100 Stars pack on the tariff's 150 Stars term.
	if p.Amount != 250 || p.DevicePackID.Int64 != pack.ID {
		t.Fatalf("payment %+v", p)
	}
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "ch-pack", p.Currency, p.Amount))
	if e.payment(p.ID).Status != "applied" {
		t.Fatal("the packed payment did not apply")
	}
	u := e.user(p.UserID.Int64)
	if !u.DevicePackID.Valid || u.DevicePackID.Int64 != pack.ID {
		t.Fatalf("the pack is not kept: %+v", u.DevicePackID)
	}
	// The tariff's three places widened by the pack's two.
	if !u.DeviceLimit.Valid || u.DeviceLimit.Int64 != 5 {
		t.Fatalf("device limit %v", u.DeviceLimit)
	}
	// A renewal without the pack is the base offer again: the places go with it.
	e.now = e.now.Add(24 * time.Hour)
	p2 := e.invoice(555, u.ID, Stars)
	must(t, e.s.StarsPaid(ctx, 555, p2.Payload, "ch-plain", p2.Currency, p2.Amount))
	u = e.user(u.ID)
	if u.DevicePackID.Valid || !u.DeviceLimit.Valid || u.DeviceLimit.Int64 != 3 {
		t.Fatalf("renewal: pack %+v, limit %v", u.DevicePackID, u.DeviceLimit)
	}
}

// A pack nobody sells for the provider's currency cannot ride its invoice.
func TestPackNeedsTheProvidersCurrency(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pack, err := e.st.Q.CreateDevicePack(ctx, db.CreateDevicePackParams{Title: "рублёвый", Slots: 2,
		PriceRub: 5000, OnSale: true, CreatedAt: e.now.Unix()})
	must(t, err)
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: Stars, DevicePackID: pack.ID}); err == nil {
		t.Fatal("a ruble-only pack was sold for Stars")
	}
}

// An archived tariff is nobody's to buy new, but the subscriptions on it keep renewing it,
// and Renewable names it for the renew screen.
func TestArchivedTariffIsItsOwnSubscribers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.subscriber(time.Hour)
	if _, err := e.st.Q.ArchiveTariff(ctx, e.sale.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 900, TariffID: e.sale.ID, Provider: Stars}); err == nil {
		t.Fatal("an archived tariff was sold to a new buyer")
	}
	o, ok, err := e.s.Renewable(ctx, u.ID)
	must(t, err)
	if !ok || o.Tariff.ID != e.sale.ID {
		t.Fatalf("renewable %+v ok=%v", o.Tariff, ok)
	}
	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, UserID: u.ID, TariffID: e.sale.ID, Provider: Stars})
	must(t, err)
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "ch-renew", p.Currency, p.Amount))
	if e.payment(p.ID).Status != "applied" {
		t.Fatal("the renewal of an archived tariff did not apply")
	}
}
