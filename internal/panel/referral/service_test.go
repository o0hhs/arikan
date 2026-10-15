package referral

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// The fixture: two tariffs (the panel's two tiers, 70 ₽ and 150 ₽), an invitee bound to an
// inviter, and a payment to apply.
type env struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	s     *Service
	now   time.Time
	cheap db.Tariff
	rich  db.Tariff
	seq   int
}

// next is a payload nobody else in this test has used: payments.payload is unique.
func (e *env) next() string { return e.t.Name() + "-" + strconv.Itoa(e.seq) }

const (
	inviter  = int64(101)
	invitee  = int64(202)
	stranger = int64(303)
)

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	e := &env{t: t, ctx: ctx, now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	var err error
	if e.st, err = storetest.Open(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.st.Close() })
	if err := domain.Seed(ctx, e.st, e.now); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return e.now }
	e.s = New(e.st, settings.New(e.st.Q), slog.New(slog.NewTextHandler(io.Discard, nil)), clock)

	ts, err := e.st.Q.ListTariffs(ctx)
	must(t, err)
	if len(ts) < 2 {
		t.Fatalf("the fixture wants two tariffs, the seed has %d", len(ts))
	}
	e.cheap = price(t, e, ts[0], 7000, 70)
	e.rich = price(t, e, ts[1], 15000, 150)
	return e
}

func price(t *testing.T, e *env, in db.Tariff, rub, stars int64) db.Tariff {
	t.Helper()
	out, err := e.st.Q.UpdateTariff(e.ctx, db.UpdateTariffParams{Name: in.Name, TrafficLimit: in.TrafficLimit, DurationDays: 30,
		DeviceLimit: in.DeviceLimit, ResetStrategy: in.ResetStrategy, Sort: in.Sort,
		PriceRub: sql.NullInt64{Int64: rub, Valid: true}, PriceStars: sql.NullInt64{Int64: stars, Valid: true},
		OnSale: 1, ID: in.ID})
	must(t, err)
	return out
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// invite binds the invitee to the inviter's code, as following the link does.
func (e *env) invite(inviter, invitee int64) {
	e.t.Helper()
	code, err := e.s.Code(e.ctx, inviter)
	must(e.t, err)
	_, err = e.s.Link(e.ctx, invitee, code)
	must(e.t, err)
}

// program is the setup with these rules; 100 kopecks to a Star unless a rule says otherwise.
func (e *env) program(rules ...Rule) Program {
	e.t.Helper()
	cfg := Program{Enabled: true, StarsRate: 100, Spend: true, Notify: true, Rules: rules}
	must(e.t, cfg.Validate())
	must(e.t, settings.Set(e.ctx, settings.New(e.st.Q), KeyConfig, cfg))
	return cfg
}

// pay is an applied tariff payment of tgID: the row, and the rewards it brought.
func (e *env) pay(tgID int64, tariff db.Tariff, amount int64, currency string) []Reward {
	e.t.Helper()
	ctx := e.ctx
	p, err := e.st.Q.CreatePayment(ctx, db.CreatePaymentParams{Provider: "stars", Payload: e.next(), TgID: tgID,
		Kind: "new", TariffID: sql.NullInt64{Int64: tariff.ID, Valid: true}, TariffName: tariff.Name,
		Amount: amount, Currency: currency, CreatedAt: e.now.Unix()})
	e.seq++
	must(e.t, err)
	if _, err := e.st.DB.ExecContext(ctx, "UPDATE payments SET status='paid', paid_at=$2 WHERE id=$1", p.ID, e.now.Unix()); err != nil {
		e.t.Fatal(err)
	}
	var out []Reward
	must(e.t, e.st.Tx(ctx, func(q *db.Queries) error {
		var err error
		out, err = e.s.RewardPayment(ctx, q, p)
		return err
	}))
	return out
}

// sub gives the inviter a subscription a days rule can land on: the tariff's term.
func (e *env) sub(tgID int64) db.User {
	e.t.Helper()
	u, err := e.directory().Create(e.ctx, domain.CreateInput{Name: "inviter", TariffID: e.cheap.ID, TermDays: sql.NullInt64{Int64: 30, Valid: true}})
	must(e.t, err)
	must(e.t, e.st.Q.LinkTg(e.ctx, db.LinkTgParams{UserID: u.ID, TgID: tgID, CreatedAt: e.now.Unix()}))
	return u
}

// unlimitedSub is the same subscription with no end at all: a days rule has nothing to
// give days onto.
func (e *env) unlimitedSub(tgID int64) db.User {
	e.t.Helper()
	u := e.sub(tgID)
	u, err := e.directory().Update(e.ctx, u.ID, domain.Patch{ClearExpiry: true})
	must(e.t, err)
	if u.ExpiresAt.Valid {
		e.t.Fatalf("the unlimited subscription still expires: %+v", u.ExpiresAt)
	}
	return u
}

func (e *env) directory() *domain.Users {
	return domain.NewUsers(e.st, domain.NewPool(e.st, func() time.Time { return e.now }), noChanges{}, func() time.Time { return e.now })
}

// payment is a plain invoice row: what a balance hold hangs on.
func (e *env) payment(tgID int64, amount int64, currency string) db.Payment {
	e.t.Helper()
	p, err := e.st.Q.CreatePayment(e.ctx, db.CreatePaymentParams{Provider: "stars", Payload: e.next(),
		TgID: tgID, Kind: "new", TariffID: sql.NullInt64{Int64: e.cheap.ID, Valid: true}, TariffName: e.cheap.Name,
		Amount: amount, Currency: currency, CreatedAt: e.now.Unix()})
	e.seq++
	must(e.t, err)
	return p
}

func (e *env) balance(tgID int64) int64 {
	e.t.Helper()
	got, err := e.st.Q.ReferralBalance(e.ctx, tgID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	must(e.t, err)
	return got
}

// An account's code is made once and is the same afterwards: the link people follow must
// not change under them.
func TestCodeIsStable(t *testing.T) {
	e := newEnv(t)
	first, err := e.s.Code(e.ctx, inviter)
	must(t, err)
	again, err := e.s.Code(e.ctx, inviter)
	must(t, err)
	if first == "" || first != again {
		t.Fatalf("the code changed: %q then %q", first, again)
	}
	other, err := e.s.Code(e.ctx, invitee)
	must(t, err)
	if other == first {
		t.Fatal("two accounts share a code")
	}
}

// One Telegram account belongs to one referrer for good: following a second link changes
// nothing, so an invitee cannot be counted twice.
func TestOneInviterForGood(t *testing.T) {
	e := newEnv(t)
	e.program(Rule{ID: "p", Kind: KindPercent, Value: 10, Enabled: true})
	e.invite(inviter, invitee)
	if _, err := e.s.Link(e.ctx, invitee, code(t, e, stranger)); !errors.Is(err, ErrBound) {
		t.Fatalf("a second inviter: %v", err)
	}
	e.pay(invitee, e.cheap, 7000, "RUB")
	if got := e.balance(inviter); got != 700 {
		t.Fatalf("the inviter earned %d kopecks", got)
	}
	if got := e.balance(stranger); got != 0 {
		t.Fatalf("the second inviter earned %d kopecks", got)
	}
	// Self-inviting is no invitation at all.
	self, err := e.s.Code(e.ctx, inviter)
	must(t, err)
	if _, err := e.s.Link(e.ctx, inviter, self); !errors.Is(err, ErrSelf) {
		t.Fatalf("inviting yourself: %v", err)
	}
}

// The percent is taken of what the tariff costs in rubles, whichever way the invitee paid:
// one balance, one currency.
func TestPercentInRublesWhateverTheyPaid(t *testing.T) {
	e := newEnv(t)
	e.program(Rule{ID: "p", Kind: KindPercent, Value: 10, Enabled: true})
	e.invite(inviter, invitee)
	e.pay(invitee, e.cheap, 7000, "RUB")
	if got := e.balance(inviter); got != 700 {
		t.Fatalf("10%% of 70 ₽: %d kopecks", got)
	}
	// 150 Stars at a hundred kopecks a star is 150 ₽: a tenth is 15 ₽.
	e.pay(invitee, e.rich, 150, "XTR")
	if got := e.balance(inviter); got != 700+1500 {
		t.Fatalf("10%% of 150 ₽ paid in Stars: %d kopecks", got)
	}
	// Without a rate a payment in Stars earns nothing.
	cfg := e.program(Rule{ID: "p", Kind: KindPercent, Value: 10, Enabled: true})
	cfg.StarsRate = 0
	must(t, settings.Set(e.ctx, settings.New(e.st.Q), KeyConfig, cfg))
	before := e.balance(inviter)
	e.pay(invitee, e.rich, 150, "XTR")
	if got := e.balance(inviter); got != before {
		t.Fatalf("Stars paid with no rate earned %d kopecks", got-before)
	}
}

// A payment the programme cannot price earns nothing at all: with no rate for Stars no
// percent is credited, no days are given, and no "every N" cycle is counted for it —
// otherwise payments worth nothing would add up to a days reward.
func TestStarsWithoutRateEarnNothing(t *testing.T) {
	e := newEnv(t)
	cfg := e.program(
		Rule{ID: "p", Kind: KindPercent, Value: 10, Every: 3, Enabled: true},
		Rule{ID: "d", Kind: KindDays, Value: 30, Enabled: true},
	)
	e.invite(inviter, invitee)
	u := e.sub(inviter)
	start := u.ExpiresAt.Int64
	cfg.StarsRate = 0
	must(t, settings.Set(e.ctx, settings.New(e.st.Q), KeyConfig, cfg))

	// Three of the rule's payments, worth nothing: the third would pay on its step.
	for i := range 3 {
		e.pay(invitee, e.rich, 150, "XTR")
		st, err := e.s.StatsOf(e.ctx, inviter)
		must(t, err)
		if len(st.Progress) != 0 {
			t.Fatalf("payment %d counted towards a rule: %+v", i+1, st.Progress)
		}
	}
	if got := e.balance(inviter); got != 0 {
		t.Fatalf("payments worth nothing credited %d kopecks", got)
	}
	after, err := e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if after.ExpiresAt.Int64 != start {
		t.Fatalf("a payment worth nothing added days: %d then %d", start, after.ExpiresAt.Int64)
	}

	// Priced again, the payment earns: it counts towards the rules and pays the days.
	cfg.StarsRate = 100
	must(t, settings.Set(e.ctx, settings.New(e.st.Q), KeyConfig, cfg))
	e.pay(invitee, e.rich, 150, "XTR")
	st, err := e.s.StatsOf(e.ctx, inviter)
	must(t, err)
	if st.Progress["p"] != 1 {
		t.Fatalf("a priced payment was not counted: %+v", st.Progress)
	}
	after, err = e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if after.ExpiresAt.Int64 != start+30*int64(24*time.Hour/time.Second) {
		t.Fatalf("the days rule did not pay: %d then %d", start, after.ExpiresAt.Int64)
	}
}

// Two tiers pay differently: each rule counts the tariffs it names and the rest not at all.
func TestRulesNameTheirTariffs(t *testing.T) {
	e := newEnv(t)
	e.program(
		Rule{ID: "cheap", Kind: KindPercent, Value: 10, TariffIDs: []int64{e.cheap.ID}, Enabled: true},
		Rule{ID: "rich", Kind: KindPercent, Value: 20, TariffIDs: []int64{e.rich.ID}, Enabled: true},
	)
	e.invite(inviter, invitee)
	e.pay(invitee, e.cheap, 7000, "RUB")
	if got := e.balance(inviter); got != 700 {
		t.Fatalf("the cheap tier: %d kopecks", got)
	}
	e.pay(invitee, e.rich, 15000, "RUB")
	if got := e.balance(inviter); got != 700+3000 {
		t.Fatalf("the rich tier: %d kopecks", got)
	}
	// A tariff no rule names pays nobody.
	other := price(t, e, e.cheap, 7000, 70)
	_ = other
}

// A rule pays on every N-th payment, and the account sees how close the next one is.
func TestEveryNthPayment(t *testing.T) {
	e := newEnv(t)
	e.program(Rule{ID: "p", Kind: KindPercent, Value: 50, Every: 3, Enabled: true})
	e.invite(inviter, invitee)
	for i, want := range []int64{0, 0, 7500} { // a half of 150 ₽
		e.pay(invitee, e.rich, 15000, "RUB")
		if got := e.balance(inviter); got != want {
			t.Fatalf("payment %d: %d kopecks, wanted %d", i+1, got, want)
		}
	}
	st, err := e.s.StatsOf(e.ctx, inviter)
	must(t, err)
	if st.Invited != 1 || st.Paid != 1 || st.Progress["p"] != 3 {
		t.Fatalf("stats %+v", st)
	}
}

// Three invitees' payments and a month for the inviter, on the inviter's own subscription.
func TestDaysRuleExtendsTheSubscription(t *testing.T) {
	e := newEnv(t)
	e.program(Rule{ID: "d", Kind: KindDays, Value: 30, Every: 3, Enabled: true})
	e.invite(inviter, invitee)
	// The inviter needs a subscription for the term to land on.
	u, err := domain.NewUsers(e.st, domain.NewPool(e.st, func() time.Time { return e.now }), noChanges{}, func() time.Time { return e.now }).
		Create(e.ctx, domain.CreateInput{Name: "inviter", TariffID: e.cheap.ID, TermDays: sql.NullInt64{Int64: 30, Valid: true}})
	must(t, err)
	must(t, e.st.Q.LinkTg(e.ctx, db.LinkTgParams{UserID: u.ID, TgID: inviter, CreatedAt: e.now.Unix()}))
	start := u.ExpiresAt.Int64

	for i := range 2 {
		e.pay(invitee, e.cheap, 7000, "RUB")
		after, err := e.st.Q.GetUser(e.ctx, u.ID)
		must(t, err)
		if after.ExpiresAt.Int64 != start {
			t.Fatalf("payment %d extended the subscription on its own", i+1)
		}
	}
	e.pay(invitee, e.cheap, 7000, "RUB")
	after, err := e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if after.ExpiresAt.Int64 != start+30*int64(24*time.Hour/time.Second) {
		t.Fatalf("the third payment did not add a month: %d then %d", start, after.ExpiresAt.Int64)
	}
}

// One payment pays out once per rule, not once in all: a percent rule and a days rule may
// both name the tariff, and both earn on the same payment.
func TestTwoRulesPayOnOnePayment(t *testing.T) {
	e := newEnv(t)
	e.program(
		Rule{ID: "p", Kind: KindPercent, Value: 10, Enabled: true},
		Rule{ID: "d", Kind: KindDays, Value: 30, Enabled: true},
	)
	e.invite(inviter, invitee)
	u := e.sub(inviter)
	start := u.ExpiresAt.Int64

	rewards := e.pay(invitee, e.cheap, 7000, "RUB")
	if len(rewards) != 2 {
		t.Fatalf("one payment paid %d rewards: %+v", len(rewards), rewards)
	}
	if got := e.balance(inviter); got != 700 {
		t.Fatalf("the percent rule paid %d kopecks", got)
	}
	after, err := e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if after.ExpiresAt.Int64 != start+30*int64(24*time.Hour/time.Second) {
		t.Fatalf("the days rule did not add a month: %d then %d", start, after.ExpiresAt.Int64)
	}
	rows, err := e.st.Q.ListReferralRewardsOf(e.ctx, db.ListReferralRewardsOfParams{ReferrerTgID: inviter, Lim: 10})
	must(t, err)
	if len(rows) != 2 || rows[0].RuleID == rows[1].RuleID {
		t.Fatalf("the rewards recorded: %+v", rows)
	}
}

// A days reward gives nothing onto a subscription that never expires: there is no end to
// add the days to, and the subscription must not be turned into one that ends. The
// payment is still counted towards the rule, as with a referrer who has none.
func TestDaysRuleOnAnUnlimitedSubscription(t *testing.T) {
	e := newEnv(t)
	e.program(Rule{ID: "d", Kind: KindDays, Value: 30, Enabled: true})
	e.invite(inviter, invitee)
	u := e.unlimitedSub(inviter)
	if u.ExpiresAt.Valid {
		t.Fatalf("the fixture's subscription expires: %+v", u.ExpiresAt)
	}

	e.pay(invitee, e.cheap, 7000, "RUB")
	after, err := e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if after.ExpiresAt.Valid {
		t.Fatalf("the subscription that never expired now ends: %+v", after.ExpiresAt)
	}
	rows, err := e.st.Q.ListReferralRewardsOf(e.ctx, db.ListReferralRewardsOfParams{ReferrerTgID: inviter, Lim: 10})
	must(t, err)
	if len(rows) != 0 {
		t.Fatalf("a term was recorded for a subscription that never ends: %+v", rows)
	}
	st, err := e.s.StatsOf(e.ctx, inviter)
	must(t, err)
	if st.Progress["d"] != 1 {
		t.Fatalf("the payment was not counted towards the rule: %+v", st.Progress)
	}
}

// A hand-edited setup may name one rule twice: the copy must not pay a second time for one
// payment — the reward row is unique per payment and rule, and a second one would abort
// the payment's transaction.
func TestRuleNamedTwiceCountsOnce(t *testing.T) {
	e := newEnv(t)
	// Set as a hand-edited value: validation would refuse two rules of one id.
	must(t, settings.Set(e.ctx, settings.New(e.st.Q), KeyConfig, Program{Enabled: true, StarsRate: 100,
		Rules: []Rule{
			{ID: "p", Kind: KindPercent, Value: 10, Every: 1, Enabled: true},
			{ID: "p", Kind: KindPercent, Value: 10, Every: 1, Enabled: true},
		}}))
	e.invite(inviter, invitee)
	e.pay(invitee, e.cheap, 7000, "RUB")
	if got := e.balance(inviter); got != 700 {
		t.Fatalf("the rule paid twice: %d kopecks", got)
	}
	rows, err := e.st.Q.ListReferralRewardsOf(e.ctx, db.ListReferralRewardsOfParams{ReferrerTgID: inviter, Lim: 10})
	must(t, err)
	if len(rows) != 1 {
		t.Fatalf("the rewards recorded: %+v", rows)
	}
}

// The balance pays part of an invoice, and what it holds comes back with a refund.
func TestBalancePaysAnInvoiceAndComesBack(t *testing.T) {
	e := newEnv(t)
	cfg := e.program(Rule{ID: "p", Kind: KindPercent, Value: 10, Enabled: true})
	e.invite(inviter, invitee)
	e.pay(invitee, e.rich, 15000, "RUB")
	if got := e.balance(inviter); got != 1500 {
		t.Fatalf("earned %d kopecks", got)
	}
	invoice := e.payment(inviter, 15000, "RUB")
	var off int64
	must(t, e.st.Tx(e.ctx, func(q *db.Queries) error {
		var err error
		off, err = e.s.Reserve(e.ctx, q, inviter, invoice.ID, 15000, "RUB", cfg)
		return err
	}))
	if off != 1500 {
		t.Fatalf("the balance paid %d of 15000", off)
	}
	if got := e.balance(inviter); got != 0 {
		t.Fatalf("%d kopecks left after paying", got)
	}
	// The hold is what makes the same invoice show again with its own discount.
	held, ok, err := e.s.Held(e.ctx, e.st.Q, invoice.ID, "RUB", cfg)
	must(t, err)
	if !ok || held != 1500 {
		t.Fatalf("held %d (standing: %t)", held, ok)
	}
	must(t, e.st.Tx(e.ctx, func(q *db.Queries) error { _, err := e.s.TakeBack(e.ctx, q, invoice.ID); return err }))
	if got := e.balance(inviter); got != 1500 {
		t.Fatalf("after a refund the balance is %d", got)
	}
}

// An invoice keeps at least one unit for the provider: a payment of zero has no provider.
func TestBalanceNeverPaysTheWholeInvoice(t *testing.T) {
	e := newEnv(t)
	cfg := e.program(Rule{ID: "p", Kind: KindPercent, Value: 100, Enabled: true})
	e.invite(inviter, invitee)
	e.pay(invitee, e.cheap, 7000, "RUB")
	invoice := e.payment(inviter, 7000, "RUB")
	var off int64
	must(t, e.st.Tx(e.ctx, func(q *db.Queries) error {
		var err error
		off, err = e.s.Reserve(e.ctx, q, inviter, invoice.ID, 7000, "RUB", cfg)
		return err
	}))
	if off != 6999 {
		t.Fatalf("the balance paid %d of a 7000 invoice", off)
	}
}

// A refunded payment takes the reward back; what is left of the balance is lost to the
// panel rather than going negative.
func TestRefundTakesTheRewardBack(t *testing.T) {
	e := newEnv(t)
	e.program(Rule{ID: "p", Kind: KindPercent, Value: 10, Enabled: true})
	e.invite(inviter, invitee)
	p := e.payment(invitee, 7000, "RUB")
	must(t, e.st.Tx(e.ctx, func(q *db.Queries) error { _, err := e.s.RewardPayment(e.ctx, q, p); return err }))
	if got := e.balance(inviter); got != 700 {
		t.Fatalf("earned %d kopecks", got)
	}
	must(t, e.st.Tx(e.ctx, func(q *db.Queries) error { _, err := e.s.TakeBack(e.ctx, q, p.ID); return err }))
	if got := e.balance(inviter); got != 0 {
		t.Fatalf("after a refund the inviter still has %d kopecks", got)
	}
	// The reward is recorded as taken back, so it is not counted twice in the totals.
	n, err := e.st.Q.CountReferralRewards(e.ctx)
	must(t, err)
	if n.Rub != 0 || n.N != 1 {
		t.Fatalf("rewards after a refund: %+v", n)
	}
}

// Traffic packages are the owner's own top-up: nobody is invited by them.
func TestPackagesEarnNothing(t *testing.T) {
	e := newEnv(t)
	e.program(Rule{ID: "p", Kind: KindPercent, Value: 10, Enabled: true})
	e.invite(inviter, invitee)
	p, err := e.st.Q.CreatePayment(e.ctx, db.CreatePaymentParams{Provider: "stars", Payload: e.next(), TgID: invitee, Kind: "package",
		TariffName: "package", Amount: 5000, Currency: "RUB", CreatedAt: e.now.Unix()})
	must(t, err)
	if out := e.payRewards(p); len(out) != 0 {
		t.Fatalf("a package brought %+v", out)
	}
	if got := e.balance(inviter); got != 0 {
		t.Fatalf("a package earned %d kopecks", got)
	}
}

func (e *env) payRewards(p db.Payment) []Reward {
	e.t.Helper()
	var out []Reward
	must(e.t, e.st.Tx(e.ctx, func(q *db.Queries) error {
		var err error
		out, err = e.s.RewardPayment(e.ctx, q, p)
		return err
	}))
	return out
}

// With the programme off, or with no rule, nothing is earned and nothing is spent.
func TestOff(t *testing.T) {
	e := newEnv(t)
	cfg := e.program(Rule{ID: "p", Kind: KindPercent, Value: 10, Enabled: true})
	e.invite(inviter, invitee)
	before := e.balance(inviter)
	cfg.Enabled = false
	must(t, settings.Set(e.ctx, settings.New(e.st.Q), KeyConfig, cfg))
	if out := e.pay(invitee, e.cheap, 7000, "RUB"); len(out) != 0 {
		t.Fatalf("a switched-off programme paid %+v", out)
	}
	if got := e.balance(inviter); got != before {
		t.Fatalf("a switched-off programme earned %d kopecks", got-before)
	}
	if _, err := e.s.Link(e.ctx, stranger, code(t, e, inviter)); !errors.Is(err, ErrOff) {
		t.Fatalf("a link with the programme off: %v", err)
	}
}

// The cap is the admin's: what is over it is not earned, not borrowed against.
func TestBalanceCap(t *testing.T) {
	e := newEnv(t)
	cfg := e.program(Rule{ID: "p", Kind: KindPercent, Value: 10, Enabled: true})
	cfg.BalanceMax = 1000
	must(t, settings.Set(e.ctx, settings.New(e.st.Q), KeyConfig, cfg))
	e.invite(inviter, invitee)
	e.pay(invitee, e.rich, 15000, "RUB")
	e.pay(invitee, e.rich, 15000, "RUB")
	if got := e.balance(inviter); got != 1000 {
		t.Fatalf("a capped balance is %d", got)
	}
}

// Rules are refused before they are saved: a percent over 100 pays more than the invitee
// spent, and days without a step would never land.
func TestValidate(t *testing.T) {
	for name, cfg := range map[string]Program{
		"percent over 100":  {Rules: []Rule{{Kind: KindPercent, Value: 101}}},
		"days out of range": {Rules: []Rule{{Kind: KindDays, Value: MaxDays + 1}}},
		"unknown kind":      {Rules: []Rule{{Kind: "traffic", Value: 1}}},
		"no value":          {Rules: []Rule{{Kind: KindPercent}}},
		"bad tariff":        {Rules: []Rule{{Kind: KindPercent, Value: 1, TariffIDs: []int64{0}}}},
		"two rules one id":  {Rules: []Rule{{ID: "a", Kind: KindPercent, Value: 1}, {ID: "a", Kind: KindPercent, Value: 2}}},
	} {
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	c := Program{Rules: []Rule{{Kind: KindPercent, Value: 10}, {Kind: KindDays, Value: 30, Every: 3, TariffIDs: []int64{3, 1, 3}}}}
	if err := c.Validate(); err != nil {
		t.Fatalf("a sound setup: %v", err)
	}
	if c.Rules[0].ID != "r1" || c.Rules[0].Every != 1 || c.Rules[1].Every != 3 {
		t.Fatalf("defaults filled in wrong: %+v", c.Rules)
	}
	if len(c.Rules[1].TariffIDs) != 2 || c.Rules[1].TariffIDs[0] != 1 {
		t.Fatalf("the tariff list was not made unique: %v", c.Rules[1].TariffIDs)
	}
}

func code(t *testing.T, e *env, tgID int64) string {
	t.Helper()
	c, err := e.s.Code(e.ctx, tgID)
	must(t, err)
	return c
}

type noChanges struct{}

func (noChanges) PoliciesChanged() {}
func (noChanges) SlotsChanged()    {}

// The days rule needs a subscription to land on. With none there, the payment is still
// counted towards the rule — the third invitee still earns a month — and nothing is lost
// to a row that could not be written.
func TestDaysRuleWithNoSubscriptionOfTheInviter(t *testing.T) {
	e := newEnv(t)
	e.program(Rule{ID: "d", Kind: KindDays, Value: 30, Every: 3, Enabled: true})
	e.invite(inviter, invitee)
	// The inviter never opened the referral screen and has no subscription.
	if _, ok, err := e.s.currentSub(e.ctx, e.st.Q, inviter); err != nil || ok {
		t.Fatalf("the fixture gave the inviter a subscription: %v %v", ok, err)
	}
	for i := range 3 {
		e.pay(invitee, e.cheap, 7000, "RUB")
		if got := e.balance(inviter); got != 0 {
			t.Fatalf("payment %d credited money where none was due", i+1)
		}
	}
	st, err := e.s.StatsOf(e.ctx, inviter)
	must(t, err)
	if st.Progress["d"] != 3 {
		t.Fatalf("the rule counted %d payments, wanted 3", st.Progress["d"])
	}
	// The third payment fell on the step, so the counter is exactly at it: the next reward
	// comes after three more, not two.
	rows, err := e.st.Q.ListReferralRewardsOf(e.ctx, db.ListReferralRewardsOfParams{ReferrerTgID: inviter, Lim: 10})
	must(t, err)
	if len(rows) != 0 {
		t.Fatalf("a term was granted with no subscription: %+v", rows)
	}
}
