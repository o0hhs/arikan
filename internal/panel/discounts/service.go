// Package discounts are the panel's own sales: while a discount stands, the price of some
// tariffs is lower, and the panel takes the money off by itself — the buyer types no code.
//
// A discount carries its own mass mailing: whether the bot reminds about the end of the
// sale, how long before the end it says so (three days and a day by default), and to whom
// it says it: to the buyers of the sale, or to everyone with a subscription on the tariffs
// the sale is about.
//
// A discount is reserved when the invoice is made and applied when the payment is; a
// payment that dies gives the discount back, so the sale's budget is whole again.
package discounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// The kinds of a discount (discounts.type): a percent of the price, or a fixed sum off it.
const (
	TypePercent = "percent"
	TypeFixed   = "fixed"
)

// The audiences of a mailing (discounts.notify_audience): the buyers of the sale, or every
// subscriber of the tariffs the sale is about.
const (
	AudienceBuyers      = "buyers"
	AudienceSubscribers = "subscribers"
)

// DefaultDays is when the bot reminds about the end of a sale: three days before it and a
// day before it.
var DefaultDays = []int{3, 1}

// maxDays is how many reminders a sale may have, and how far ahead of the end the first
// of them may come.
const maxDays = 30

// maxNotices is how many reminders one round tells: the rest wait for the next one, ten
// minutes later, so a big mailing does not hold the panel's own round.
const maxNotices = 500

// Service is the discounts of the panel: the sales it makes and the mailings they carry.
type Service struct {
	Store *store.Store
	Now   func() time.Time
}

func New(st *store.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{Store: st, Now: now}
}

// Applied is what a discount took off a payment.
type Applied struct {
	DiscountID int64
	Name       string
	Amount     int64
	Final      int64
	Currency   string
}

// Notice is a reminder about the end of a sale: the discount, the account and what is left
// of it.
type Notice struct {
	DiscountID int64
	TgID       int64
	// Kind is the reminder's own name (discount_3d, discount_1d): it is what the ledger
	// remembers, so a reminder is told once.
	Kind     string
	Days     int
	Name     string
	EndsAt   int64
	Value    int64
	Type     string
	Currency string
	// Text is the admin's own wording of the mailing; empty: the bot's own words.
	Text string
	// Tariffs are the names of the tariffs the sale is about.
	Tariffs []string
}

// Amount is what the discount takes off a price: a percent of it, or the sum itself, never
// more than the price and never more than the maximum discount.
func Amount(d db.Discount, original int64) int64 {
	if original <= 0 {
		return 0
	}
	var off int64
	if d.Type == TypePercent {
		if d.Value > 0 && d.Value <= 100 {
			off = (original/100)*d.Value + (original%100)*d.Value/100
		}
	} else {
		off = d.Value
	}
	if off < 0 {
		off = 0
	}
	if d.MaxDiscount > 0 && off > d.MaxDiscount {
		off = d.MaxDiscount
	}
	if off > original {
		off = original
	}
	return off
}

// Days are when the bot reminds about the end of a sale: three days and a day by default.
// A list the admin emptied is no reminders at all; a list that cannot be read is the
// default one.
func Days(raw string) []int {
	var out []int
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return slices.Clone(DefaultDays)
	}
	clean := make([]int, 0, len(out))
	for _, d := range out {
		if d > 0 && d <= maxDays {
			clean = append(clean, d)
		}
	}
	slices.Sort(clean)
	slices.Reverse(clean)
	return clean
}

// Kind is the reminder's name for a number of days: it is what the ledger remembers.
func Kind(days int) string { return "discount_" + strconv.Itoa(days) + "d" }

// TariffIDs are the tariffs the discount is about; none: every tariff on sale.
func (s *Service) TariffIDs(ctx context.Context, q *db.Queries, discountID int64) ([]int64, error) {
	rows, err := q.ListDiscountTariffs(ctx, discountID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// applies says whether the discount stands for this purchase: the tariff is one of its
// own, the currency is the one it is given in, the order is big enough, and the discount
// takes something off.
func applies(d db.Discount, ids map[int64]bool, tariffID int64, amount int64, currency string) bool {
	if amount <= 0 {
		return false
	}
	if len(ids) > 0 && !ids[tariffID] {
		return false
	}
	if d.Currency != "" && d.Currency != currency {
		return false
	}
	if d.MinOrder > 0 && amount < d.MinOrder {
		return false
	}
	off := Amount(d, amount)
	return off > 0 && off < amount
}

// Best is the discount that takes most off a price among those that stand for the tariff:
// the buyer gets the best price that is on. It is no discount when none of them applies.
func (s *Service) Best(ctx context.Context, q *db.Queries, tariffID, amount int64, currency string) (db.Discount, int64, error) {
	list, err := q.ListActiveDiscountsForTariff(ctx, db.ListActiveDiscountsForTariffParams{TariffID: tariffID, Now: s.Now().Unix()})
	if err != nil {
		return db.Discount{}, 0, err
	}
	var best db.Discount
	bestOff := int64(0)
	for _, d := range list {
		ids, err := s.tariffSet(ctx, q, d.ID)
		if err != nil {
			return db.Discount{}, 0, err
		}
		if !applies(d, ids, tariffID, amount, currency) {
			continue
		}
		off := Amount(d, amount)
		if off > bestOff {
			best, bestOff = d, off
		}
	}
	return best, bestOff, nil
}

// tariffSet is the set of the tariffs the discount is about.
func (s *Service) tariffSet(ctx context.Context, q *db.Queries, discountID int64) (map[int64]bool, error) {
	rows, err := q.ListDiscountTariffs(ctx, discountID)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]bool, len(rows))
	for _, id := range rows {
		out[id] = true
	}
	return out, nil
}

// Reserve takes the discount off the payment that was just made, in the same transaction,
// and records who got it. It does nothing when no discount stands for the tariff: the
// buyer pays the full price.
func (s *Service) Reserve(ctx context.Context, q *db.Queries, tgID, userID, tariffID, amount int64, currency string, paymentID int64) (Applied, error) {
	list, err := q.ListActiveDiscountsForTariff(ctx, db.ListActiveDiscountsForTariffParams{TariffID: tariffID, Now: s.Now().Unix()})
	if err != nil {
		return Applied{}, err
	}
	if len(list) == 0 {
		return Applied{}, nil
	}
	// The discount that takes most off comes first: the buyer gets the best price that is
	// on, and a sale over its budget does not stop the next one from standing.
	slices.SortStableFunc(list, func(a, b db.Discount) int {
		return int(Amount(b, amount) - Amount(a, amount))
	})
	for _, d := range list {
		ids, err := s.tariffSet(ctx, q, d.ID)
		if err != nil {
			return Applied{}, err
		}
		if !applies(d, ids, tariffID, amount, currency) {
			continue
		}
		off := Amount(d, amount)
		n, err := q.IncrementDiscountUse(ctx, d.ID)
		if err != nil {
			return Applied{}, err
		}
		if n != 1 {
			continue // the sale is over its budget
		}
		now := s.Now()
		_, err = q.CreateDiscountRedemption(ctx, db.CreateDiscountRedemptionParams{
			DiscountID: d.ID, UserID: sql.NullInt64{Int64: userID, Valid: userID != 0}, TgID: tgID,
			PaymentID: sql.NullInt64{Int64: paymentID, Valid: paymentID != 0}, Status: "reserved",
			RedeemedAt: now.Unix(), DiscountAmount: off, OriginalAmount: amount, FinalAmount: amount - off, Currency: currency,
		})
		if err != nil {
			_, _ = q.DecrementDiscountUse(ctx, d.ID)
			return Applied{}, err
		}
		return Applied{DiscountID: d.ID, Name: d.Name, Amount: off, Final: amount - off, Currency: currency}, nil
	}
	return Applied{}, nil
}

// ApplyPayment marks the discount of a paid payment as given: the buyer is one of the
// sale's buyers now, and the mailing's list is one name longer.
func (s *Service) ApplyPayment(ctx context.Context, q *db.Queries, paymentID, userID int64) error {
	r, err := q.GetDiscountRedemptionByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: paymentID != 0})
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = q.MarkDiscountApplied(ctx, db.MarkDiscountAppliedParams{UserID: sql.NullInt64{Int64: userID, Valid: userID != 0}, ID: r.ID})
	return err
}

// ReleasePayment gives the discount back when the payment it was taken off died: the buyer
// did not pay, so the sale's budget is whole again.
func (s *Service) ReleasePayment(ctx context.Context, paymentID int64) error {
	return s.Store.Tx(ctx, func(q *db.Queries) error {
		r, err := q.GetDiscountRedemptionByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: paymentID != 0})
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		n, err := q.ReleaseDiscountRedemptionByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: paymentID != 0})
		if err != nil || n == 0 {
			return err
		}
		_, err = q.DecrementDiscountUse(ctx, r.DiscountID)
		return err
	})
}

// dueKind is the reminder that is due now: the smallest of the days the end is within, so
// one sale says one thing at a time.
func dueKind(days []int, left time.Duration) (int, bool) {
	for _, d := range days {
		if left <= time.Duration(d)*24*time.Hour {
			return d, true
		}
	}
	return 0, false
}

// Notices are the reminders due now: every sale that stands, whose end is near enough for
// one of its days, and every account the mailing is for.
func (s *Service) Notices(ctx context.Context, now time.Time) ([]Notice, error) {
	list, err := s.Store.Q.ListActiveDiscounts(ctx, now.Unix())
	if err != nil {
		return nil, err
	}
	var out []Notice
	for _, d := range list {
		if len(out) >= maxNotices {
			break
		}
		if d.NotifyEnabled == 0 {
			continue
		}
		days := Days(d.NotifyDays)
		if len(days) == 0 {
			continue
		}
		left := time.Unix(d.EndsAt, 0).Sub(now)
		if left <= 0 {
			continue
		}
		day, ok := dueKind(days, left)
		if !ok {
			continue
		}
		kind := Kind(day)
		tgIDs, err := s.recipients(ctx, d)
		if err != nil {
			return nil, err
		}
		if len(tgIDs) == 0 {
			continue
		}
		told, err := s.toldKinds(ctx, d.ID, kind)
		if err != nil {
			return nil, err
		}
		tariffs, err := s.Store.Q.ListDiscountTariffNames(ctx, d.ID)
		if err != nil {
			return nil, err
		}
		for _, tg := range tgIDs {
			if told[tg] {
				continue
			}
			out = append(out, Notice{DiscountID: d.ID, TgID: tg, Kind: kind, Days: day, Name: d.Name,
				EndsAt: d.EndsAt, Value: d.Value, Type: d.Type, Currency: d.Currency, Text: d.NotifyText, Tariffs: tariffs})
			if len(out) >= maxNotices {
				break
			}
		}
	}
	return out, nil
}

// recipients are the accounts a mailing goes to: the buyers of the sale, or every
// subscriber of the tariffs it is about.
func (s *Service) recipients(ctx context.Context, d db.Discount) ([]int64, error) {
	if d.NotifyAudience == AudienceSubscribers {
		return s.Store.Q.ListDiscountSubscribers(ctx, d.ID)
	}
	return s.Store.Q.ListDiscountBuyers(ctx, d.ID)
}

// toldKinds are the accounts a kind of reminder was already told of.
func (s *Service) toldKinds(ctx context.Context, discountID int64, kind string) (map[int64]bool, error) {
	rows, err := s.Store.Q.ListDiscountNotices(ctx, discountID)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]bool, len(rows))
	for _, r := range rows {
		if r.Kind == kind {
			out[r.TgID] = true
		}
	}
	return out, nil
}

// NoticeSent says whether a reminder was already told of.
func (s *Service) NoticeSent(ctx context.Context, discountID, tgID int64, kind string) (bool, error) {
	return s.Store.Q.DiscountNoticeSent(ctx, db.DiscountNoticeSentParams{DiscountID: discountID, TgID: tgID, Kind: kind})
}

// NoticeDelivered remembers that a reminder was told, so it is not told again.
func (s *Service) NoticeDelivered(ctx context.Context, discountID, tgID int64, kind string, at time.Time) error {
	_, err := s.Store.Q.AddDiscountNotice(ctx, db.AddDiscountNoticeParams{DiscountID: discountID, TgID: tgID, Kind: kind, SentAt: at.Unix()})
	return err
}

// PruneNotices forgets the reminders told long ago.
func (s *Service) PruneNotices(ctx context.Context, before time.Time) error {
	return s.Store.Q.PruneDiscountNotices(ctx, before.Unix())
}

// Status is where a discount stands now, for the admin's page.
func Status(d db.Discount, now time.Time) string {
	switch {
	case d.Deleted != 0:
		return "deleted"
	case d.Enabled == 0:
		return "disabled"
	case now.Unix() < d.StartsAt:
		return "inactive"
	case now.Unix() >= d.EndsAt:
		return "expired"
	case d.MaxUses.Valid && d.UsedCount >= d.MaxUses.Int64:
		return "exhausted"
	}
	return "active"
}
