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
