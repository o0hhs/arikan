package partners

import (
	"context"
	"database/sql"
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
	return New(st, func() int64 { return 1_800_000_000 }), st
}

// A partner's code binds the account; an earlier code stays, and a paid order in rubles
// credits the partner's share with the ledger line that made it.
func TestPartnerCodeAndCommission(t *testing.T) {
	s, st := newTest(t)
	ctx := context.Background()
	tariff, err := st.Q.CreateTariff(ctx, db.CreateTariffParams{Name: "Плюс", DurationDays: 30, ResetStrategy: "none", CreatedAt: 1, PriceStars: sql.NullInt64{Int64: 150, Valid: true}, PriceRub: sql.NullInt64{Int64: 7000, Valid: true}, OnSale: 1})
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.Q.CreatePartner(ctx, db.CreatePartnerParams{Name: "Иван", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.Q.CreatePartnerBot(ctx, db.CreatePartnerBotParams{PartnerID: p.ID, Code: "first", CommissionPercent: 10, CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.Q.CreatePartnerBot(ctx, db.CreatePartnerBotParams{PartnerID: p.ID, Code: "second", CommissionPercent: 50, CreatedAt: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO tg_chats (tg_id, username, first_name, created_at, updated_at) VALUES (555, 'anna', 'Анна', 1, 1)"); err != nil {
		t.Fatal(err)
	}
	if name, ok, err := s.Code(ctx, 555, "first"); err != nil || !ok || name != "Иван" {
		t.Fatalf("code: %q %v %v", name, ok, err)
	}
	// The first code stays.
	if _, ok, _ := s.Code(ctx, 555, "second"); !ok {
		t.Fatal("a second code is not known")
	}
	partner, err := st.Q.ChatPartner(ctx, 555)
	if err != nil || partner.Int64 != first.ID {
		t.Fatalf("the first code must stay: %+v %v", partner, err)
	}
	// An unknown code binds nothing.
	if _, ok, err := s.Code(ctx, 556, "nope"); err != nil || ok {
		t.Fatalf("an unknown code: %v %v", ok, err)
	}

	// Ten percent of a ruble order, in kopecks.
	pay, err := st.Q.CreatePayment(ctx, db.CreatePaymentParams{Provider: "addon:yookassa", Payload: "pl", TgID: 555, Kind: "new", TariffID: sql.NullInt64{Int64: tariff.ID, Valid: true},
		TariffName: "Плюс", Amount: 7000, Currency: "RUB", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Q.SetPaymentPartner(ctx, db.SetPaymentPartnerParams{PartnerBotID: sql.NullInt64{Int64: first.ID, Valid: true}, ID: pay.ID}); err != nil {
		t.Fatal(err)
	}
	pay, err = st.Q.GetPayment(ctx, pay.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Credit(ctx, st.Q, pay); err != nil {
		t.Fatal(err)
	}
	b, err := st.Q.GetPartnerBot(ctx, first.ID)
	if err != nil || b.BalanceKop != 700 {
		t.Fatalf("salary: %+v %v", b, err)
	}
	if turnover, err := st.Q.PartnerTurnover(ctx, first.ID); err != nil || turnover != 700 {
		t.Fatalf("turnover: %d %v", turnover, err)
	}
	// Stars credit nothing: the franchise's books are in rubles.
	stars, err := st.Q.CreatePayment(ctx, db.CreatePaymentParams{Provider: "stars", Payload: "pl2", TgID: 555, Kind: "new", TariffID: sql.NullInt64{Int64: tariff.ID, Valid: true},
		TariffName: "Плюс", Amount: 150, Currency: "XTR", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Q.SetPaymentPartner(ctx, db.SetPaymentPartnerParams{PartnerBotID: sql.NullInt64{Int64: first.ID, Valid: true}, ID: stars.ID}); err != nil {
		t.Fatal(err)
	}
	stars, err = st.Q.GetPayment(ctx, stars.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Credit(ctx, st.Q, stars); err != nil {
		t.Fatal(err)
	}
	if b, _ := st.Q.GetPartnerBot(ctx, first.ID); b.BalanceKop != 700 {
		t.Fatalf("stars credited: %+v", b)
	}
	// The second bot's own share stays its own (50%).
	pay2, err := st.Q.CreatePayment(ctx, db.CreatePaymentParams{Provider: "addon:yookassa", Payload: "pl3", TgID: 555, Kind: "new", TariffID: sql.NullInt64{Int64: tariff.ID, Valid: true},
		TariffName: "Плюс", Amount: 2000, Currency: "RUB", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Q.SetPaymentPartner(ctx, db.SetPaymentPartnerParams{PartnerBotID: sql.NullInt64{Int64: second.ID, Valid: true}, ID: pay2.ID}); err != nil {
		t.Fatal(err)
	}
	pay2, err = st.Q.GetPayment(ctx, pay2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Credit(ctx, st.Q, pay2); err != nil {
		t.Fatal(err)
	}
	if b, _ := st.Q.GetPartnerBot(ctx, second.ID); b.BalanceKop != 1000 {
		t.Fatalf("the second bot: %+v", b)
	}
	_ = time.Now
}
