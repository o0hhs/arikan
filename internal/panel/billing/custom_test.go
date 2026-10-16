package billing

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// The arithmetic of TestConstructorPrice, written out so the test does not just repeat the
// code: the base tariff is the test's own sale (150 stars, 199.00 ₽ for 30 days, 150 GiB,
// 3 devices, no speed limit).

func stars(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: true} }

func (e *env) withConstructor(t db.Tariff, k Coefficients) {
	e.t.Helper()
	ctx := context.Background()
	cfg := e.s.Config(ctx)
	cfg.CustomTariffID = t.ID
	must(e.t, settings.Set(ctx, e.s.d.Settings, KeyConfig, cfg))
	must(e.t, settings.Set(ctx, e.s.d.Settings, KeyCoefficients, k))
}

func (e *env) baseTerms() []domain.Term {
	e.t.Helper()
	terms, err := domain.TermsOf(context.Background(), e.st.Q, e.sale)
	must(e.t, err)
	return terms
}

// TestConstructorPrice: every dimension scales the base term's price by how far the plan is
// from the tariff's own numbers, times the admin's multiplier; the result is rounded up to a
// whole step (five stars, a ruble). The figures below are worked out by hand.
func TestConstructorPrice(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	devices := func(n int64) *int64 { return &n }
	bytes := func(gib int64) *int64 { v := gib << 30; return &v }
	terms := e.baseTerms()
	// (1/3 devices × 1) × (200/150 GiB × 1) × (30/30 × 1) = 0.4444 → 150 × 0.4444 = 66.67 → 70
	e.withConstructor(e.sale, Coefficients{Traffic: 1, Devices: 1, Speed: 1, Duration: 1})
	plan := CustomPlan{DeviceLimit: devices(1), TrafficLimit: bytes(200)}
	got, currency, err := e.s.CustomPrice(ctx, e.sale, plan, terms[0], Stars)
	must(t, err)
	if got != 70 || currency != "XTR" {
		t.Fatalf("devices 1 of 3, traffic 200 of 150 = %d %s, want 70 XTR", got, currency)
	}
	// The device multiplier of 2 doubles the changes: 1/3 × 2 = 0.6667 of the devices, against
	// the traffic's 1.3333 → 0.8889 → 133.33 → 135.
	e.withConstructor(e.sale, Coefficients{Traffic: 1, Devices: 2, Speed: 1, Duration: 1})
	got, _, err = e.s.CustomPrice(ctx, e.sale, plan, terms[0], Stars)
	must(t, err)
	if got != 135 {
		t.Fatalf("with devices ×2: %d, want 135", got)
	}
	// Back to the plain rate: devices 5 of 3, nothing else: 1.6667 → 250.00 → 250.
	e.withConstructor(e.sale, Coefficients{Traffic: 1, Devices: 1, Speed: 1, Duration: 1})
	got, _, err = e.s.CustomPrice(ctx, e.sale, CustomPlan{DeviceLimit: devices(5)}, terms[0], Stars)
	must(t, err)
	if got != 250 {
		t.Fatalf("devices 5 of 3: %d, want 250", got)
	}
	// Traffic 300 of 150, nothing else: 2 → 300 → 300 (already a whole step).
	got, _, err = e.s.CustomPrice(ctx, e.sale, CustomPlan{TrafficLimit: bytes(300)}, terms[0], Stars)
	must(t, err)
	if got != 300 {
		t.Fatalf("traffic 300 of 150: %d, want 300", got)
	}
	// Duration: a 45-day plan against the 30-day term, plain rate: 1.5 → 225 → 225.
	got, _, err = e.s.CustomPrice(ctx, e.sale, CustomPlan{TermDays: 45}, terms[0], Stars)
	must(t, err)
	if got != 225 {
		t.Fatalf("45 days of 30: %d, want 225", got)
	}
	if _, _, err := e.s.CustomPrice(ctx, e.sale, CustomPlan{TrafficLimit: bytes(100)}, terms[0], "addon:x"); !errors.Is(err, ErrProviderOff) {
		t.Fatalf("a provider that is not there: %v, want ErrProviderOff", err)
	}
}

// TestConstructorRubRounding: the ruble's step, with an adapter that takes rubles.
func TestConstructorRubRounding(t *testing.T) {
	e, _ := addonEnv(t)
	ctx := context.Background()
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "12", "secret_key": adapterSecret}); err != nil {
		t.Fatal(err)
	}
	terms := e.baseTerms()
	e.withConstructor(e.sale, DefaultCoefficients())
	// 100 of 150 GiB: 0.6667 → 19900 × 0.6667 = 13266.67 → rounded up to 13300 kopecks.
	v := int64(100 << 30)
	got, currency, err := e.s.CustomPrice(ctx, e.sale, CustomPlan{TrafficLimit: &v}, terms[0], "addon:fake")
	must(t, err)
	if got != 13300 || currency != "RUB" {
		t.Fatalf("100 of 150 GiB = %d %s, want 13300 RUB", got, currency)
	}
}

// TestConstructorBounds: the plan is refused when it asks for what the base tariff cannot
// give, and when it changes nothing (that is the tariff's own order).
func TestConstructorBounds(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	terms := e.baseTerms()
	e.withConstructor(e.sale, DefaultCoefficients())
	devices := func(n int64) *int64 { return &n }
	days := func(n int64) *int64 { return &n }
	for _, tc := range []struct {
		name string
		plan CustomPlan
	}{
		{"no change", CustomPlan{}},
		{"same as the tariff", CustomPlan{DeviceLimit: devices(3)}},
		{"no devices", CustomPlan{DeviceLimit: devices(0)}},
		{"devices over the cap", CustomPlan{DeviceLimit: devices(101)}},
		{"traffic under a gigabyte", CustomPlan{TrafficLimit: &[]int64{1 << 20}[0]}},
		{"term over ten years", CustomPlan{TermDays: 3651}},
		{"negative term", CustomPlan{TermDays: -1}},
		{"reset that does not exist", CustomPlan{ResetStrategy: "weekly"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := e.s.CustomPrice(ctx, e.sale, tc.plan, terms[0], Stars); !errors.Is(err, ErrBadOptions) {
				t.Fatalf("price: %v, want ErrBadOptions", err)
			}
			price := tc.plan
			if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: Stars, Custom: &price}); !errors.Is(err, ErrBadOptions) {
				t.Fatalf("invoice: %v, want ErrBadOptions", err)
			}
		})
	}
	// A tariff that does not meter traffic cannot be given a limit: the seeded "unlimited".
	ts, _ := e.st.Q.ListTariffs(ctx)
	var unlimited db.Tariff
	for _, x := range ts {
		if !x.TrafficLimit.Valid {
			unlimited = x
		}
	}
	if unlimited.ID == 0 {
		t.Skip("no unlimited tariff in the seed")
	}
	if err := CheckCustom(unlimited, CustomPlan{TrafficLimit: &[]int64{50 << 30}[0]}); !errors.Is(err, ErrBadOptions) {
		t.Fatalf("a limit on an endless tariff: %v, want ErrBadOptions", err)
	}
	_ = days
}

// TestConstructorPurchase: a bought plan is what the subscription gets, and the payment keeps
// the plan for the record and the refunds.
func TestConstructorPurchase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.withConstructor(e.sale, DefaultCoefficients())
	traffic := int64(400 << 30)
	devices := int64(5)
	plan := CustomPlan{TrafficLimit: &traffic, DeviceLimit: &devices, TermDays: 60}
	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: Stars, Custom: &plan})
	must(t, err)
	// 400/150 × 5/3 × 60/30 = 4.4444 → 150 × 4.4444 = 666.67 → 670.
	if p.Amount != 670 {
		t.Fatalf("amount %d, want 670", p.Amount)
	}
	if p.TermDays.Int64 != 60 {
		t.Fatalf("term %d, want the plan's 60", p.TermDays.Int64)
	}
	stored := e.payment(p.ID)
	got, ok := PlanOf(stored)
	if !ok || got.TermDays != 60 || got.TrafficLimit == nil || *got.TrafficLimit != traffic {
		t.Fatalf("stored plan: %+v (%v)", got, ok)
	}
	if !stored.CustomBaseID.Valid || stored.CustomBaseID.Int64 != e.sale.ID {
		t.Fatalf("custom base %v, want the sale tariff", stored.CustomBaseID)
	}
	// Telegram's own check of the very invoice.
	if err := e.s.PreCheckout(ctx, 555, p.Payload, "XTR", p.Amount); err != nil {
		t.Fatalf("precheckout: %v", err)
	}
	// Paid: the subscription runs with the plan's numbers.
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "charge-plan", "XTR", p.Amount))
	links, err := e.st.Q.ListTgLinksOf(context.Background(), 555)
	must(t, err)
	if len(links) != 1 {
		t.Fatalf("%d subscriptions, want 1", len(links))
	}
	u := links[0]
	if u.TrafficLimit.Int64 != traffic || u.DeviceLimit.Int64 != devices {
		t.Fatalf("the subscription got %d bytes and %d devices, want the plan's %d/%d", u.TrafficLimit.Int64, u.DeviceLimit.Int64, traffic, devices)
	}
	want := e.now.Add(60 * 24 * time.Hour).Unix()
	if u.ExpiresAt.Int64 != want {
		t.Fatalf("expires %d, want the 60-day term %d", u.ExpiresAt.Int64, want)
	}
	// A plan on a tariff that is not the constructor's base is nobody's.
	other := e.sale.ID*10 + 1
	bad := CustomPlan{DeviceLimit: &devices}
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 556, TariffID: other, Provider: Stars, Custom: &bad}); err == nil {
		t.Fatal("a plan on an unknown tariff went through")
	}
}

// TestConstructorOnTheHiddenTariff: the constructor's base tariff may be off the shop, and
// then only a code opens it.
func TestConstructorOnTheHiddenTariff(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	hidden := e.hidden()
	e.withConstructor(hidden, DefaultCoefficients())
	devices := int64(4)
	plan := CustomPlan{DeviceLimit: &devices}
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: hidden.ID, Provider: Stars, Custom: &plan}); !errors.Is(err, ErrNotForSale) {
		t.Fatalf("without a code: %v, want ErrNotForSale", err)
	}
	if _, ok := e.s.CustomConstructor(ctx); !ok {
		t.Fatal("the constructor does not offer a hidden base tariff")
	}
	e.withPromo()
	code := e.accessCode("access_paid", hidden)
	if _, err := e.s.d.Promo.GrantAccess(ctx, 555, 0, code.Code); err != nil {
		t.Fatal(err)
	}
	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: hidden.ID, Provider: Stars, Custom: &plan})
	must(t, err)
	// 4 of 5 devices: 0.8 → 900 × 0.8 = 720 → 720.
	if p.Amount != 720 {
		t.Fatalf("amount %d, want 720", p.Amount)
	}
}
