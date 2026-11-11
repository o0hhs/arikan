package billing

import (
	"context"
	"database/sql"
	"testing"

	"mikan/internal/panel/store/db"
)

// A payment whose subscription was removed since the invoice (the removal of inactive ones)
// still applies: the buyer paid.
func TestPaymentForARemovedUserApplies(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	before := e.users()
	p := e.invoice(555, 0, Stars) // new subscription
	// The subscription is created, then removed while its invoice was on the way.
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "ch-1", "XTR", 150))
	paid1 := e.payment(p.ID)
	if paid1.Status != "applied" || !paid1.UserID.Valid {
		t.Fatalf("first payment: %+v", paid1)
	}
	uid := paid1.UserID.Int64
	// A second invoice while the subscription still exists (a renewal), then it is removed
	// before the buyer pays.
	p2 := e.invoice(555, uid, Stars)
	must(t, e.st.Q.DeleteUsers(ctx, []int64{uid}))
	_, err := e.st.Q.MarkPaymentPaid(ctx, db.MarkPaymentPaidParams{ExternalID: sql.NullString{String: "ch-2", Valid: true},
		PaidAt: sql.NullInt64{Int64: e.now.Unix(), Valid: true}, ID: p2.ID})
	must(t, err)
	if err := e.s.Apply(ctx, p2.ID); err != nil {
		t.Fatalf("apply a payment for a removed user: %v", err)
	}
	if got := e.payment(p2.ID); got.Status != "applied" {
		t.Fatalf("payment %+v, users %d (was %d)", got, e.users(), before)
	}
}
