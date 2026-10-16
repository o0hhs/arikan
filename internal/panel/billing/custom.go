package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// The tariff constructor: a buyer takes a base tariff the admin points out and puts its
// numbers together themselves — traffic, devices, speed and term. The admin sets multipliers
// (Coefficients) that say how much more one unit of each dimension costs; the price starts
// from the price of the term the buyer chose and is scaled by how far the choice is from the
// tariff's own numbers, then rounded up to a whole step (five stars, one ruble), so the buyer
// never pays pennies.
//
// The choice is recorded with the payment (payments.custom, a CustomPlan snapshot) and is what
// the subscription gets; the base tariff itself is not changed by it.

// KeyCoefficients is the settings key the multipliers live under.
const KeyCoefficients = "tariff_coefficients"

// constructorMaxDays caps a built term at ten years.
const constructorMaxDays int64 = 3650

// Coefficients are the multipliers of the constructor: 1 prices a dimension at the base
// tariff's own rate, 2 at twice that for the same growth.
type Coefficients struct {
	Traffic  float64 `json:"traffic"`
	Devices  float64 `json:"devices"`
	Speed    float64 `json:"speed"`
	Duration float64 `json:"duration"`
}

// DefaultCoefficients: everything at the base tariff's own rate.
func DefaultCoefficients() Coefficients {
	return Coefficients{Traffic: 1, Devices: 1, Speed: 1, Duration: 1}
}

// Coefficients reads the multipliers; an unset key, or one saved by a panel that knew fewer
// dimensions, means the default 1 for what is missing.
func (s *Service) Coefficients(ctx context.Context) Coefficients {
	c := DefaultCoefficients()
	got, _, err := settings.GetOver(ctx, s.d.Settings, KeyCoefficients, c)
	if err != nil {
		s.d.Log.Warn("billing: the constructor multipliers are unreadable, the base ones are used", "err", err)
		return c
	}
	if got.Traffic > 0 {
		c.Traffic = got.Traffic
	}
	if got.Devices > 0 {
		c.Devices = got.Devices
	}
	if got.Speed > 0 {
		c.Speed = got.Speed
	}
	if got.Duration > 0 {
		c.Duration = got.Duration
	}
	return c
}

var (
	// ErrNoConstructor: the admin pointed at no base tariff, so nothing can be put together.
	ErrNoConstructor = errors.New("constructor_off")
	// ErrBadOptions: the buyer's numbers are outside what the base tariff allows (a term of
	// no length, traffic the tariff cannot meter, and the like).
	ErrBadOptions = errors.New("bad_options")
)

// The bounds of a built tariff: the same the panel applies everywhere.
var (
	CustomTrafficMin int64 = domain.MinGrantBytes // 1 GiB
	CustomTrafficMax int64 = domain.MaxGrantBytes // 100 TiB
	CustomDevicesMin int64 = 1
	CustomDevicesMax int64 = 100
	CustomSpeedMin   int64 = 1
	CustomSpeedMax   int64 = 10000 // Mbit/s
)

// CustomPlan is a tariff the buyer put together: the numbers the subscription gets and the
// term. A field left out (nil, empty) keeps the base tariff's own. Every number is checked
// against the base tariff before the plan is priced or applied.
type CustomPlan struct {
	// TermDays is the term: an existing term of the base tariff, or a length of its own, up
	// to constructorMaxDays. 0: the tariff's first term.
	TermDays int64 `json:"days,omitempty"`
	// TrafficLimit is the traffic in bytes. Only for a tariff that meters traffic: a
	// tariff with endless traffic cannot be given a limit through the constructor.
	TrafficLimit *int64 `json:"traffic,omitempty"`
	DeviceLimit  *int64 `json:"devices,omitempty"`
	SpeedLimit   *int64 `json:"speed,omitempty"`
	// ResetStrategy overrides the tariff's own (monthly, by used traffic, ...).
	ResetStrategy string `json:"reset,omitempty"`
}

// Options turns the plan into the limits a subscription runs with (domain.Options).
func (c CustomPlan) Options() *domain.Options {
	return &domain.Options{
		TrafficLimit:  nullPlanInt(c.TrafficLimit),
		DeviceLimit:   nullPlanInt(c.DeviceLimit),
		SpeedLimit:    nullPlanInt(c.SpeedLimit),
		ResetStrategy: c.ResetStrategy,
	}
}

func nullPlanInt(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

// bytesOf, devicesOf and speedOf read a plan's numbers over the tariff's own: the plan's
// value when it has one, the tariff's otherwise (invalid = endless).
func bytesOf(c CustomPlan, t db.Tariff) sql.NullInt64 {
	if c.TrafficLimit != nil {
		return sql.NullInt64{Int64: *c.TrafficLimit, Valid: true}
	}
	return t.TrafficLimit
}

func devicesOf(c CustomPlan, t db.Tariff) sql.NullInt64 {
	if c.DeviceLimit != nil {
		return sql.NullInt64{Int64: *c.DeviceLimit, Valid: true}
	}
	return t.DeviceLimit
}

func speedOf(c CustomPlan, t db.Tariff) sql.NullInt64 {
	if c.SpeedLimit != nil {
		return sql.NullInt64{Int64: *c.SpeedLimit, Valid: true}
	}
	return t.SpeedLimit
}

// CheckCustom checks a plan against its base tariff: every number inside what the tariff can
// give and the term a real one. A refusal here is the buyer's own doing (ErrBadOptions), not
// the panel's.
func CheckCustom(t db.Tariff, c CustomPlan) error {
	if c.TermDays < 0 || c.TermDays > constructorMaxDays {
		return fmt.Errorf("%w: term %d", ErrBadOptions, c.TermDays)
	}
	if t.DurationDays <= 0 {
		return fmt.Errorf("%w: the tariff has no term", ErrBadOptions)
	}
	if c.TrafficLimit != nil {
		if v := *c.TrafficLimit; v < CustomTrafficMin || v > CustomTrafficMax {
			return fmt.Errorf("%w: traffic %d", ErrBadOptions, v)
		}
		// A tariff whose traffic is endless has no meters to set a limit on, and no unit to
		// price one from.
		if !t.TrafficLimit.Valid {
			return fmt.Errorf("%w: the tariff does not meter traffic", ErrBadOptions)
		}
	}
	if c.DeviceLimit != nil {
		if v := *c.DeviceLimit; v < CustomDevicesMin || v > CustomDevicesMax {
			return fmt.Errorf("%w: devices %d", ErrBadOptions, v)
		}
		if !t.DeviceLimit.Valid {
			return fmt.Errorf("%w: the tariff has no device limit", ErrBadOptions)
		}
	}
	if c.SpeedLimit != nil {
		if v := *c.SpeedLimit; v < CustomSpeedMin || v > CustomSpeedMax {
			return fmt.Errorf("%w: speed %d", ErrBadOptions, v)
		}
		if !t.SpeedLimit.Valid {
			return fmt.Errorf("%w: the tariff does not limit speed", ErrBadOptions)
		}
	}
	if c.ResetStrategy != "" && !slices.Contains(domain.ResetStrategies, c.ResetStrategy) {
		return fmt.Errorf("%w: reset %q", ErrBadOptions, c.ResetStrategy)
	}
	return nil
}

// IsCustom: the plan asks for something the base tariff does not give as it stands (a plan
// that changes nothing is the tariff's own order, not a built one).
func IsCustom(t db.Tariff, c CustomPlan) bool {
	if c.TermDays > 0 && c.TermDays != t.DurationDays {
		return true
	}
	if c.TrafficLimit != nil && (!t.TrafficLimit.Valid || *c.TrafficLimit != t.TrafficLimit.Int64) {
		return true
	}
	if c.DeviceLimit != nil && (!t.DeviceLimit.Valid || *c.DeviceLimit != t.DeviceLimit.Int64) {
		return true
	}
	if c.SpeedLimit != nil && (!t.SpeedLimit.Valid || *c.SpeedLimit != t.SpeedLimit.Int64) {
		return true
	}
	return c.ResetStrategy != "" && c.ResetStrategy != t.ResetStrategy
}

const (
	starsStep int64 = 5    // Stars are sold in fives
	rubStep   int64 = 100  // a ruble: prices are kept in kopecks
	priceCeil       = 1e12 // far above any real price; guards the float product
)

// planFactor is how much more expensive the plan is than the base term: the ratio of each
// dimension to the tariff's own, times the admin's multiplier for it. A dimension the plan
// does not touch scales by 1; one with nothing to measure against is a bad plan.
func planFactor(t db.Tariff, c CustomPlan, baseDays int64, k Coefficients) (float64, error) {
	ratio := func(want, base sql.NullInt64, mult float64) (float64, error) {
		if !want.Valid {
			return 1, nil
		}
		if !base.Valid || base.Int64 <= 0 {
			return 0, fmt.Errorf("%w: nothing to price against", ErrBadOptions)
		}
		return max(float64(want.Int64)/float64(base.Int64)*mult, 0.1), nil
	}
	days := c.TermDays
	if days <= 0 {
		days = baseDays
	}
	rTraffic, err := ratio(bytesOf(c, t), t.TrafficLimit, k.Traffic)
	if err != nil {
		return 0, err
	}
	rDevices, err := ratio(devicesOf(c, t), t.DeviceLimit, k.Devices)
	if err != nil {
		return 0, err
	}
	rSpeed, err := ratio(speedOf(c, t), t.SpeedLimit, k.Speed)
	if err != nil {
		return 0, err
	}
	// The base price is the one of the term the plan was made for, so the term scales only by
	// how far the plan's own days go from that term's.
	rDays, err := ratio(sql.NullInt64{Int64: days, Valid: days > 0}, sql.NullInt64{Int64: baseDays, Valid: baseDays > 0}, k.Duration)
	if err != nil {
		return 0, err
	}
	return rTraffic * rDevices * rSpeed * rDays, nil
}

// scalePrice grows base by factor and rounds up to a whole step. An invalid or non-positive
// base has no price (0).
func scalePrice(base sql.NullInt64, factor float64, step int64) int64 {
	if !base.Valid || base.Int64 <= 0 || factor <= 0 {
		return 0
	}
	v := float64(base.Int64) * factor
	if v > priceCeil || v != v {
		return priceCeil
	}
	n := int64(v)
	if n <= 0 {
		n = 1
	}
	if step > 1 {
		n = (n + step - 1) / step * step
	}
	return n
}

// CustomPrice is what the plan costs through one provider, starting from the price of the
// term the buyer chose. The provider must take the term's price; the currency follows it.
func (s *Service) CustomPrice(ctx context.Context, t db.Tariff, c CustomPlan, term domain.Term, provider string) (int64, string, error) {
	if !IsCustom(t, c) {
		return 0, "", ErrBadOptions
	}
	if err := CheckCustom(t, c); err != nil {
		return 0, "", err
	}
	price, inCurrency, ok := s.Available(ctx).price(provider, term.PriceStars, term.PriceRub)
	if !ok {
		return 0, "", ErrProviderOff
	}
	factor, err := planFactor(t, c, term.Days, s.Coefficients(ctx))
	if err != nil {
		return 0, "", err
	}
	step := starsStep
	if inCurrency == "RUB" {
		step = rubStep
	}
	v := scalePrice(sql.NullInt64{Int64: price, Valid: price > 0}, factor, step)
	if v <= 0 {
		return 0, "", ErrNotForSale
	}
	return v, inCurrency, nil
}

// planJSON is the plan as it is stored with the payment.
func planJSON(c CustomPlan) string {
	b, err := json.Marshal(c)
	if err != nil || string(b) == "null" || string(b) == "{}" {
		return ""
	}
	return string(b)
}

// PlanOf reads the plan a payment was made for; ok false for a payment that bought a tariff
// as it stands.
func PlanOf(p db.Payment) (CustomPlan, bool) {
	if strings.TrimSpace(p.Custom) == "" {
		return CustomPlan{}, false
	}
	var c CustomPlan
	if err := json.Unmarshal([]byte(p.Custom), &c); err != nil {
		return CustomPlan{}, false
	}
	return c, true
}

// Constructor is the base tariff of the constructor as the buyer sees it: the tariff, the
// terms with a price, and what may be put together.
type Constructor struct {
	Tariff db.Tariff
	Terms  []OfferTerm
	First  int64
	// Days, Traffic, Devices and Speed are the tariff's own numbers: 0 means endless (or no
	// limit), and then that dimension cannot be built.
	Days    int64
	Traffic int64 // bytes
	Devices int64
	Speed   int64 // Mbit/s
	// Stars and Rub are the price of the first term, for the plan that changes nothing.
	Stars int64
	Rub   int64
	// Rates are the multipliers the panel prices with, so the page can preview a price
	// without asking the panel on every tap.
	Rates Coefficients
	// Bounds of what a plan may ask for, so the page can refuse before the panel does.
	TrafficMin int64
	TrafficMax int64
	DevicesMin int64
	DevicesMax int64
	SpeedMin   int64
	SpeedMax   int64
	MaxDays    int64
}

// CustomTariff is the tariff the constructor builds on; ok false when the admin set none, it
// is gone or archived, or it has no term.
func (s *Service) CustomTariff(ctx context.Context) (db.Tariff, bool) {
	id := s.Config(ctx).CustomTariffID
	if id == 0 {
		return db.Tariff{}, false
	}
	t, err := s.d.Store.Q.GetTariff(ctx, id)
	if err != nil || t.Archived != 0 || t.DurationDays <= 0 {
		return db.Tariff{}, false
	}
	return t, true
}

// CustomConstructor is the constructor the buyer may use now. ok is false when the admin set
// no base tariff, it is gone, no provider takes its price, or the panel sells nothing.
func (s *Service) CustomConstructor(ctx context.Context) (Constructor, bool) {
	t, ok := s.CustomTariff(ctx)
	if !ok {
		return Constructor{}, false
	}
	if !s.Available(ctx).Any() {
		return Constructor{}, false
	}
	terms, err := domain.TermsOf(ctx, s.d.Store.Q, t)
	if err != nil {
		return Constructor{}, false
	}
	out := Constructor{Tariff: t, Days: t.DurationDays, Rates: s.Coefficients(ctx),
		TrafficMin: CustomTrafficMin, TrafficMax: CustomTrafficMax, DevicesMin: CustomDevicesMin, DevicesMax: CustomDevicesMax,
		SpeedMin: CustomSpeedMin, SpeedMax: CustomSpeedMax, MaxDays: constructorMaxDays}
	if t.TrafficLimit.Valid {
		out.Traffic = t.TrafficLimit.Int64
	}
	if t.DeviceLimit.Valid {
		out.Devices = t.DeviceLimit.Int64
	}
	if t.SpeedLimit.Valid {
		out.Speed = t.SpeedLimit.Int64
	}
	av := s.Available(ctx)
	for _, term := range terms {
		ot := OfferTerm{Days: term.Days}
		ot.Stars, ot.Rub = av.prices(term.PriceStars, term.PriceRub)
		if ot.Stars > 0 || ot.Rub > 0 {
			out.Terms = append(out.Terms, ot)
		}
	}
	if len(out.Terms) == 0 {
		return Constructor{}, false
	}
	out.First = terms[0].Days
	out.Stars, out.Rub = out.Terms[0].Stars, out.Terms[0].Rub
	return out, true
}

// ConstructorTerm is the constructor term of so many days (nil: the first), with its price.
func (c Constructor) Term(days *int64) (OfferTerm, bool) {
	first := c.First
	if days == nil {
		days = &first
	}
	for _, t := range c.Terms {
		if t.Days == *days {
			return t, true
		}
	}
	return OfferTerm{}, false
}

// AccessPlan gives tgID a subscription on a tariff an access code opened, for nothing: the
// free half of the code. It works like the free plan in the way that matters — the account's
// own row decides, and the subscription is made whole or not at all — but it is not the free
// plan: the account may take it as often as the admin lets the code be redeemed, and it does
// not stand in the way of taking the free plan itself.
//
// The plan (custom) is the numbers the subscription runs with; nil: the tariff's own.
func (s *Service) AccessPlan(ctx context.Context, tgID int64, t db.Tariff, plan *CustomPlan) (db.User, error) {
	if t.ID == 0 || t.Archived != 0 {
		return db.User{}, ErrNotForSale
	}
	var opts *domain.Options
	if plan != nil {
		if err := CheckCustom(t, *plan); err != nil {
			return db.User{}, err
		}
		opts = plan.Options()
	}
	var u db.User
	run := func(q *db.Queries) error {
		u = db.User{}
		now := s.d.Now()
		if n, err := q.CountTgLinksOf(ctx, tgID); err != nil {
			return err
		} else if n >= s.d.MaxLinks {
			return ErrTooManySubs
		}
		created, err := s.d.Users.CreateOn(ctx, q, domain.CreateInput{Name: buyerName(ctx, q, tgID), Note: "Telegram · access code",
			TariffID: t.ID, Source: domain.UserFromCode, TgID: tgID, Options: opts}, domain.Patch{})
		if err != nil {
			return err
		}
		u = created
		if err := q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: tgID, CreatedAt: now.Unix()}); err != nil {
			return err
		}
		return q.SetTgCurrent(ctx, db.SetTgCurrentParams{Current: u.ID, TgID: tgID})
	}
	err := s.d.Store.Tx(ctx, run)
	if errors.Is(err, domain.ErrNoSlots) {
		if err = s.d.Users.RefillSlots(ctx); err == nil {
			err = s.d.Store.Tx(ctx, run)
		}
	}
	if err != nil {
		return db.User{}, err
	}
	s.d.Events.Kick()
	s.d.Users.Changed()
	s.d.Log.Info("billing: a code gave a subscription", "tg", tgID, "user", u.ID, "tariff", t.ID)
	return u, nil
}
