package botpanel

import (
	"context"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/model"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgbot"
)

// shop is the panel's billing as the bot's screens use it (tgbot.Shop): what it answers, in the
// bot's model.
type shop struct{ b *billing.Service }

var _ tgbot.Shop = shop{}

func user(u db.User) model.User          { return model.User(u) }
func payment(p db.Payment) model.Payment { return model.Payment(p) }
func topup(t db.Topup) model.Topup       { return model.Topup(t) }

func (s shop) Config(ctx context.Context) model.Config { return s.b.Config(ctx).Model() }

func (s shop) Offers(ctx context.Context, tgID int64) ([]model.Offer, model.Available, error) {
	o, av, err := s.b.Offers(ctx, tgID)
	return model.Each(o, billing.Offer.Model), av.Model(), err
}

func (s shop) DevicePacks(ctx context.Context) ([]db.DevicePack, error) {
	return s.b.DevicePacks(ctx)
}

func (s shop) Renewable(ctx context.Context, userID int64) (model.Offer, bool, error) {
	o, ok, err := s.b.Renewable(ctx, userID)
	return o.Model(), ok, err
}

func (s shop) AddonName(ctx context.Context, id, lang string) string {
	return s.b.AddonName(ctx, id, lang)
}

// refusal is an error the screens are to tell as a refusal of a code: the RPC crosses every
// promo refusal as model.ErrPromo, and the in-process bot answers the same.
func refusal(err error) error {
	if promo.IsRefusal(err) {
		return model.ErrPromo
	}
	return err
}

func (s shop) Invoice(ctx context.Context, req model.InvoiceRequest) (model.Payment, error) {
	p, err := s.b.Invoice(ctx, billing.InvoiceRequest{TgID: req.TgID, UserID: req.UserID, TariffID: req.TariffID, TermDays: req.TermDays, Provider: req.Provider, PromoCode: req.PromoCode, DevicePackID: req.DevicePackID})
	return payment(p), refusal(err)
}

func (s shop) Promo(ctx context.Context, tgID, userID int64, code string) (model.Promo, error) {
	u, err := s.b.UsePromo(ctx, tgID, userID, code)
	if err != nil {
		return model.Promo{}, refusal(err)
	}
	p := model.Promo{Code: u.Code, Type: u.Type, Days: u.Days, Bytes: u.Bytes, Percent: u.Percent,
		Amount: u.Amount, Currency: u.Currency}
	if u.Tariff != nil {
		t := model.Tariff(*u.Tariff)
		p.Tariff = &t
	}
	if u.User != nil {
		p.Subscription = u.User.Name
	}
	return p, nil
}

func (s shop) PackageOffers(ctx context.Context, userID int64) ([]model.PackageOffer, model.Available, error) {
	o, av, err := s.b.PackageOffers(ctx, userID)
	return model.Each(o, billing.PackageOffer.Model), av.Model(), err
}

func (s shop) PackageInvoice(ctx context.Context, req model.PackageRequest) (model.Payment, error) {
	p, err := s.b.PackageInvoice(ctx, billing.PackageRequest(req))
	return payment(p), refusal(err)
}

func (s shop) DeviceOfferFor(ctx context.Context, userID int64) (model.DeviceOffer, model.Available, bool, error) {
	o, av, ok, err := s.b.DeviceOfferFor(ctx, userID)
	return o.Model(), av.Model(), ok, err
}

func (s shop) DeviceInvoice(ctx context.Context, req model.DeviceRequest) (model.Payment, error) {
	p, err := s.b.DeviceInvoice(ctx, billing.DeviceRequest(req))
	return payment(p), err
}

func (s shop) TrialTariff(ctx context.Context) (model.Tariff, bool) {
	t, ok := s.b.TrialTariff(ctx)
	return model.Tariff(t), ok
}

func (s shop) TrialOpen(ctx context.Context, tgID int64) bool { return s.b.TrialOpen(ctx, tgID) }

func (s shop) TrialAllowed(ctx context.Context, tgID int64) bool { return s.b.TrialAllowed(ctx, tgID) }

func (s shop) Trial(ctx context.Context, tgID int64) (model.User, error) {
	u, err := s.b.Trial(ctx, tgID)
	return user(u), err
}

func (s shop) TrialOffer(ctx context.Context, tgID int64) (model.TrialOffer, model.Available, bool) {
	o, av, ok := s.b.TrialOffer(ctx, tgID)
	return o.Model(), av.Model(), ok
}

func (s shop) TrialInvoice(ctx context.Context, tgID int64, provider string) (model.Payment, error) {
	p, err := s.b.TrialInvoice(ctx, tgID, provider)
	return payment(p), err
}

func (s shop) FreeOffer(ctx context.Context, tgID int64) (model.FreeOffer, bool) {
	o, ok := s.b.FreeOffer(ctx, tgID)
	return o.Model(), ok
}

func (s shop) FreePlan(ctx context.Context, tgID int64) (model.User, error) {
	u, err := s.b.FreePlan(ctx, tgID)
	return user(u), err
}

func (s shop) BalanceOf(ctx context.Context, tgID int64) (int64, error) {
	return s.b.BalanceOf(ctx, tgID)
}

func (s shop) BalanceHistory(ctx context.Context, tgID int64, n int32) ([]model.BalanceOp, error) {
	ops, err := s.b.BalanceHistory(ctx, tgID, n)
	return model.Each(ops, func(o db.BalanceOp) model.BalanceOp { return model.BalanceOp(o) }), err
}

func (s shop) StartTopup(ctx context.Context, tgID, amount int64) (model.Topup, error) {
	t, err := s.b.StartTopup(ctx, tgID, amount)
	return topup(t), err
}

func (s shop) SetTopupAmount(ctx context.Context, tgID, amount int64) (model.Topup, error) {
	t, err := s.b.SetTopupAmount(ctx, tgID, amount)
	return topup(t), err
}

func (s shop) TopupDraft(ctx context.Context, tgID int64) (model.Topup, bool) {
	t, ok := s.b.TopupDraft(ctx, tgID)
	return topup(t), ok
}

func (s shop) CancelTopup(ctx context.Context, tgID int64) error { return s.b.CancelTopup(ctx, tgID) }

func (s shop) SubmitTopup(ctx context.Context, tgID int64, photo string) (model.Topup, error) {
	t, err := s.b.SubmitTopup(ctx, tgID, photo)
	return topup(t), err
}
