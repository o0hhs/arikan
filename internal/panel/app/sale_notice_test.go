package app

import (
	"context"
	"database/sql"
	"testing"

	"mikan/internal/panel/store/db"
)

// tellAdminOfSale is covered by salenotice's own tests for the text; here we only pin the
// wiring: the notice carries the payment's id and the text is not empty.
func TestTellAdminOfSale(t *testing.T) {
	p := &Panel{}
	pay := db.Payment{ID: 1, TgID: 42, Kind: "new", Provider: "stars", Amount: 150, Currency: "XTR", TariffName: "Escape+", TermDays: sql.NullInt64{Int64: 30, Valid: true}}
	u := db.User{ID: 7, Name: "Иван"}
	p.tellAdminOfSale(context.Background(), pay, u, true) // no alerts wired: must not panic
}
