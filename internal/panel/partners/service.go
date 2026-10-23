package partners

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// The franchise's ground: a partner's code binds an account to them, and every paid order
// of such an account credits them their share. The first code an account came with stays.

// Service is the partners' books.
type Service struct {
	st  *store.Store
	now func() int64
}

// New: now is the clock of the writes (unix seconds).
func New(st *store.Store, now func() int64) *Service {
	return &Service{st: st, now: now}
}

// Code binds the Telegram account to the partner bot the code names, unless an earlier
// code already did, and names the partner for the bot's greeting. ok false: no such code.
func (s *Service) Code(ctx context.Context, tgID int64, code string) (string, bool, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", false, nil
	}
	pb, err := s.st.Q.PartnerBotByCode(ctx, code)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	// The partner's own account is not the code's customer: a partner paying themselves a
	// share of their own money is not the franchise.
	if owner := pb.OwnerTgID; owner != 0 && owner == tgID {
		return "", false, nil
	}
	if _, err := s.st.Q.SetChatPartner(ctx, db.SetChatPartnerParams{PartnerBotID: sql.NullInt64{Int64: pb.ID, Valid: true}, TgID: tgID}); err != nil {
		return "", false, err
	}
	name := ""
	if p, err := s.st.Q.GetPartner(ctx, pb.PartnerID); err == nil {
		name = p.Name
	}
	return name, true, nil
}

// TouchPayment keeps the buyer's partner on the payment: the order is theirs to credit.
func (s *Service) TouchPayment(ctx context.Context, q *db.Queries, tgID, paymentID int64) error {
	partner, err := q.ChatPartner(ctx, tgID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !partner.Valid) {
		return nil
	}
	if err != nil {
		return err
	}
	return q.SetPaymentPartner(ctx, db.SetPaymentPartnerParams{PartnerBotID: partner, ID: paymentID})
}

// TouchUser keeps the partner on the account whose subscription the payment made.
func (s *Service) TouchUser(ctx context.Context, q *db.Queries, userID, tgID int64) error {
	partner, err := q.ChatPartner(ctx, tgID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !partner.Valid) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = q.SetUserPartner(ctx, db.SetUserPartnerParams{PartnerBotID: partner, ID: userID})
	return err
}

// Credit accrues the partner's share of an applied payment: a percentage of its rubles,
// kept on the balance with the ledger line that made it. A payment in Stars or without a
// partner credits nothing.
func (s *Service) Credit(ctx context.Context, q *db.Queries, p db.Payment) error {
	if !p.PartnerBotID.Valid || p.Currency != "RUB" || p.Amount <= 0 {
		return nil
	}
	pb, err := q.GetPartnerBot(ctx, p.PartnerBotID.Int64)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !pb.Enabled) {
		return nil
	}
	if err != nil {
		return err
	}
	amount := p.Amount * int64(pb.CommissionPercent) / 100
	if amount <= 0 {
		return nil
	}
	if _, err := q.CreditPartner(ctx, db.CreditPartnerParams{AmountKop: amount, ID: pb.ID}); err != nil {
		return err
	}
	return q.AddPartnerTransaction(ctx, db.AddPartnerTransactionParams{PartnerBotID: pb.ID, PaymentID: p.ID, Kind: "commission",
		AmountKop: amount, CreatedAt: s.now()})
}

// Revert takes the commission of a refunded payment back off the partner's balance, with the
// ledger line that takes it: the money the share was of did not stay with the panel. A
// payment that credited nothing has nothing to take back, and the balance may go below zero
// when the share was withdrawn before the refund — the ledger keeps the truth either way.
func (s *Service) Revert(ctx context.Context, q *db.Queries, paymentID int64) error {
	r, err := q.PartnerCommissionOfPayment(ctx, paymentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.AmountKop <= 0 {
		return nil
	}
	// A second pass over the same refund (a retried reconcile) finds the debit already there.
	if _, err := q.PartnerRefundOfPayment(ctx, paymentID); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := q.DebitPartner(ctx, db.DebitPartnerParams{AmountKop: r.AmountKop, ID: r.PartnerBotID}); err != nil {
		return err
	}
	return q.AddPartnerTransaction(ctx, db.AddPartnerTransactionParams{PartnerBotID: r.PartnerBotID, PaymentID: paymentID, Kind: "refund",
		AmountKop: -r.AmountKop, CreatedAt: s.now()})
}
