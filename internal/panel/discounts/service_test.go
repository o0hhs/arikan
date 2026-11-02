package discounts

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

func newTest(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := storetest.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, func() time.Time { return time.Unix(1_800_000_000, 0) }), st
}

// sale makes a 50% sale for the tariff, one use only.
func sale(t *testing.T, st *store.Store, tariffID int64) db.Discount {
	t.Helper()
	ctx := context.Background()
	d, err := st.Q.CreateDiscount(ctx, db.CreateDiscountParams{Name: "Sale", Type: TypePercent, Value: 50, Currency: "",
		StartsAt: 1_600_000_000, EndsAt: 2_000_000_000, MaxUses: sql.NullInt64{Int64: 1, Valid: true}, MinOrder: 0, MaxDiscount: 0,
		NotifyEnabled: 0, NotifyDays: "[]", NotifyAudience: "buyers", NotifyText: "", Enabled: 1, CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Q.SetDiscountTariffs(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Q.AddDiscountTariff(ctx, db.AddDiscountTariffParams{DiscountID: d.ID, TariffID: tariffID}); err != nil {
		t.Fatal(err)
	}
	return d
}

func payment(t *testing.T, st *store.Store, tariff db.Tariff, amount int64) db.Payment {
	t.Helper()
	p, err := st.Q.CreatePayment(context.Background(), db.CreatePaymentParams{Provider: "stars", Payload: fmt.Sprintf("pl-%d-%d-%d", tariff.ID, amount, time.Now().UnixNano()),
		TgID: 901, Kind: "new", TariffID: sql.NullInt64{Int64: tariff.ID, Valid: true}, TariffName: tariff.Name,
		Amount: amount, Currency: "RUB", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A refunded sale gives its budget back: the reservation is released, the use goes back, and
// the next buyer can take the same sale.
func TestRefundGivesTheBudgetBack(t *testing.T) {
	s, st := newTest(t)
	ctx := context.Background()
	tariff, err := st.Q.CreateTariff(ctx, db.CreateTariffParams{Name: "Плюс", DurationDays: 30, ResetStrategy: "none", CreatedAt: 1, OnSale: 1})
	if err != nil {
		t.Fatal(err)
	}
	d := sale(t, st, tariff.ID)
	p := payment(t, st, tariff, 10000)
	var applied Applied
	if err := st.Tx(ctx, func(q *db.Queries) error {
		var err error
		applied, err = s.Reserve(ctx, q, 901, 0, tariff.ID, p.Amount, p.Currency, p.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if applied.Amount != 5000 || applied.Final != 5000 {
		t.Fatalf("the sale's share: %+v", applied)
	}
	if d2, _ := st.Q.GetDiscount(ctx, d.ID); d2.UsedCount != 1 {
		t.Fatalf("used: %d", d2.UsedCount)
	}
	// The payment is paid (the redemption becomes applied), then refunded: the budget comes
	// back once.
	if err := st.Tx(ctx, func(q *db.Queries) error { return s.ApplyPayment(ctx, q, p.ID, 0) }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := st.Tx(ctx, func(q *db.Queries) error { return s.ReleaseApplied(ctx, q, p.ID) }); err != nil {
			t.Fatal(err)
		}
	}
	if d2, _ := st.Q.GetDiscount(ctx, d.ID); d2.UsedCount != 0 {
		t.Fatalf("after the refund: used %d", d2.UsedCount)
	}
	// The one use is free again: another invoice of the same size takes the sale.
	p2 := payment(t, st, tariff, 10000)
	if err := st.Tx(ctx, func(q *db.Queries) error {
		again, err := s.Reserve(ctx, q, 902, 0, tariff.ID, p2.Amount, p2.Currency, p2.ID)
		if err == nil && again.Amount != 5000 {
			t.Fatalf("the freed budget: %+v", again)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// An invoice that expired without paying gives its reservation back from the reconcile sweep.
func TestClosedInvoiceGivesTheReservationBack(t *testing.T) {
	s, st := newTest(t)
	ctx := context.Background()
	tariff, err := st.Q.CreateTariff(ctx, db.CreateTariffParams{Name: "Плюс", DurationDays: 30, ResetStrategy: "none", CreatedAt: 1, OnSale: 1})
	if err != nil {
		t.Fatal(err)
	}
	d := sale(t, st, tariff.ID)
	p := payment(t, st, tariff, 10000)
	if err := st.Tx(ctx, func(q *db.Queries) error {
		_, err := s.Reserve(ctx, q, 901, 0, tariff.ID, p.Amount, p.Currency, p.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "UPDATE payments SET status='expired' WHERE id=$1", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseClosed(ctx); err != nil {
		t.Fatal(err)
	}
	if d2, _ := st.Q.GetDiscount(ctx, d.ID); d2.UsedCount != 0 {
		t.Fatalf("the sweep left the budget spent: %d", d2.UsedCount)
	}
	if _, err := st.Q.GetDiscountRedemptionByPayment(ctx, sql.NullInt64{Int64: p.ID, Valid: true}); err != nil {
		t.Fatalf("the redemption: %v", err)
	}
}
