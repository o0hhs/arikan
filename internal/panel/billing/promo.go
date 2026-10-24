package billing

import (
	"context"

	"mikan/internal/panel/promo"
	"mikan/internal/panel/store/db"
)

// PromoUse is what a subscriber's code does as the bot enters it: a bonus is given at once,
// an access code opens its tariff (a free one gives a subscription), a discount comes back
// for the invoice to take off. It is the bot's side of the Mini App's promo endpoint: the
// same checks, the same refusals, one refusal code across the RPC.
type PromoUse struct {
	Code  string
	Type  string
	Days  int64
	Bytes int64
	// Percent is a percent code's cut; Amount is a fixed code's, in the minor units of
	// Currency. A discount is not reserved here: the invoice reserves it with its payment.
	Percent  int64
	Amount   int64
	Currency string
	// Tariff is the one an access code opened; User the subscription a free access gave.
	Tariff *db.Tariff
	User   *db.User
}

// UsePromo takes the code the account entered. A bonus and an access code are applied here;
// a discount is only looked at, and its code goes with the invoice. A code for another
// buyer, off, spent or restricted is promo's refusal as it is.
func (s *Service) UsePromo(ctx context.Context, tgID, userID int64, code string) (PromoUse, error) {
	if s.d.Promo == nil {
		return PromoUse{}, promo.ErrUnavailable
	}
	p, err := s.d.Promo.Check(ctx, tgID, userID, code)
	if err != nil {
		return PromoUse{}, err
	}
	out := PromoUse{Code: p.Code, Type: p.Type, Currency: p.Currency}
	switch {
	case promo.IsBonus(p.Type):
		r, err := s.d.Promo.RedeemBonus(ctx, tgID, userID, p.Code)
		if err != nil {
			return PromoUse{}, err
		}
		out.Days, out.Bytes = r.Days, r.Bytes
	case promo.IsDiscount(p.Type):
		if p.Type == promo.TypePercent {
			out.Percent = p.Value
		} else {
			out.Amount = p.Value
		}
	case promo.IsAccess(p.Type):
		tariffID, ok := promo.TariffOf(p)
		if !ok {
			return PromoUse{}, promo.ErrNotAccess
		}
		t, err := s.d.Store.Q.GetTariff(ctx, tariffID)
		if err != nil {
			return PromoUse{}, err
		}
		access, err := s.d.Promo.GrantAccess(ctx, tgID, userID, p.Code)
		if err != nil {
			return PromoUse{}, err
		}
		out.Tariff = &t
		if access.Mode == promo.TypeAccessFree {
			u, err := s.AccessPlan(ctx, tgID, t, nil)
			if err != nil {
				return PromoUse{}, err
			}
			_ = s.d.Promo.SetAccessUser(ctx, access.ID, u.ID)
			out.User = &u
		}
	default:
		return PromoUse{}, promo.ErrUnavailable
	}
	return out, nil
}
