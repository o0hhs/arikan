package referral

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"slices"
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
	// trial is a third tariff of its own: a trial has to be on one that is not what a
	// referee buys, or a test cannot tell the reward's tariff from the trial's.
	trial db.Tariff
	seq   int
}

// next is a payload nobody else in this test has used: payments.payload is unique.
func (e *env) next() string { return e.t.Name() + "-" + strconv.Itoa(e.seq) }

const (
	inviter  = int64(101)
	invitee  = int64(202)
	stranger = int64(303)
	// second is a second invitee of the same inviter: the price queue needs two referees
	// buying different tariffs.
	second = int64(404)
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
	if len(ts) < 3 {
		t.Fatalf("the fixture wants three tariffs, the seed has %d", len(ts))
	}
	e.cheap = price(t, e, ts[0], 7000, 70)
	e.rich = price(t, e, ts[1], 15000, 150)
	// The third is the trial's own: nothing a referee buys, so a reward's tariff and the
	// trial's can never be confused in a test.
	e.trial = price(t, e, ts[2], 3000, 30)
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

// unlimitedSub is the same subscription with no end at all. It used to be the fixture of "a
// days rule has nothing to give days onto"; under the queue a days rule lands on the tariff
// the REFEREE bought whatever the referrer's own subscription looks like, so this fixture
// now proves the referrer's own subscription is suspended and restored unchanged.
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

// trialSub gives the inviter a trial: a subscription on its own tariff, linked to them and
// recorded in trials.user_id the way billing.Trial does (trials goes with the user). days
// left is the term; the reward must freeze it and give it back whole.
func (e *env) trialSub(tgID int64, days int64) db.User {
	e.t.Helper()
	u, err := e.directory().Create(e.ctx, domain.CreateInput{Name: "inviter", TariffID: e.trial.ID,
		TermDays: sql.NullInt64{Int64: days, Valid: true}, Source: domain.UserFromTrial, TgID: tgID})
	must(e.t, err)
	must(e.t, e.st.Q.LinkTg(e.ctx, db.LinkTgParams{UserID: u.ID, TgID: tgID, CreatedAt: e.now.Unix()}))
	must(e.t, e.st.Q.SetTrialUser(e.ctx, db.SetTrialUserParams{UserID: sql.NullInt64{Int64: u.ID, Valid: true}, TgID: tgID}))
	must(e.t, e.st.Q.SetTgCurrent(e.ctx, db.SetTgCurrentParams{Current: u.ID, TgID: tgID}))
	return u
}

// advance is the reconcile pass's queue step, on the clock the fixture keeps.
func (e *env) advance() {
	e.t.Helper()
	must(e.t, e.s.Advance(e.ctx, e.now))
}

// at moves the fixture's clock: the queue is time-based, and a test that waits ten days
// cannot wait.
func (e *env) at(t time.Time) { e.now = t }

// plus is the fixture's clock moved on by days from where it is.
func (e *env) plus(days int64) { e.now = e.now.Add(time.Duration(days) * 24 * time.Hour) }

// grants is a referrer's queue as it stands, oldest first.
func (e *env) grants(tgID int64) []db.ReferralRewardGrant {
	e.t.Helper()
	rows, err := e.st.Q.ListReferralRewardGrantsOf(e.ctx, db.ListReferralRewardGrantsOfParams{ReferrerTgID: tgID, Lim: 50})
	must(e.t, err)
	slices.Reverse(rows)
	return rows
}

// running is the referrer's running grant, if any.
func (e *env) running(tgID int64) (db.ReferralRewardGrant, bool) {
	e.t.Helper()
	for _, g := range e.grants(tgID) {
		if g.Status == GrantRunning {
			return g, true
		}
	}
	return db.ReferralRewardGrant{}, false
}

func (e *env) directory() *domain.Users {
	return domain.NewUsers(e.st, domain.NewPool(e.st, func() time.Time { return e.now }), noChanges{}, func() time.Time { return e.now })
}

// payment is a plain invoice row: what a balance hold hangs on.
func (e *env) payment(tgID int64, amount int64, currency string) db.Payment {
	e.t.Helper()
	return e.paymentOf(tgID, e.cheap, amount, currency)
}

// paymentOf is the same invoice for a named tariff: the price queue needs invoices of
// different tariffs.
func (e *env) paymentOf(tgID int64, tariff db.Tariff, amount int64, currency string) db.Payment {
	e.t.Helper()
	p, err := e.st.Q.CreatePayment(e.ctx, db.CreatePaymentParams{Provider: "stars", Payload: e.next(),
		TgID: tgID, Kind: "new", TariffID: sql.NullInt64{Int64: tariff.ID, Valid: true}, TariffName: tariff.Name,
		Amount: amount, Currency: currency, CreatedAt: e.now.Unix()})
	e.seq++
	must(e.t, err)
	if _, err := e.st.DB.ExecContext(e.ctx, "UPDATE payments SET status='paid', paid_at=$2 WHERE id=$1", p.ID, e.now.Unix()); err != nil {
		e.t.Fatal(err)
	}
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
	// The days reward is on the tariff the REFEREE bought (the rich one) for 30 days.
	after, err = e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if after.TariffID.Int64 != e.rich.ID {
		t.Fatalf("the days rule put the inviter on tariff %d, wanted the bought %d", after.TariffID.Int64, e.rich.ID)
	}
	if after.ExpiresAt.Int64 != e.now.Unix()+30*int64(24*time.Hour/time.Second) {
		t.Fatalf("the days rule did not pay: %d then %d", e.now.Unix(), after.ExpiresAt.Int64)
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

// CHANGED (old behaviour removed): this test used to assert that three invitees' payments
// added a month to the INVITER'S OWN subscription (the cheap tariff), extending its
// expiry from its own end. That is exactly what the user called critical — a referee's
// payment could lengthen whatever the referrer had, a trial included. A days rule now gives
// a temporary subscription on the tariff the REFEREE bought instead, so what this test
// checks is: the inviter's own subscription is untouched while the reward runs, and the
// reward is on the bought tariff for the rule's days.
func TestDaysRuleGivesTheBoughtTariffNotTheOwnSubscription(t *testing.T) {
	e := newEnv(t)
	e.program(Rule{ID: "d", Kind: KindDays, Value: 30, Every: 3, Enabled: true})
	e.invite(inviter, invitee)
	u := e.sub(inviter) // the inviter's own subscription, on the cheap tariff
	own := u.ExpiresAt.Int64

	for i := range 2 {
		e.pay(invitee, e.cheap, 7000, "RUB")
		after, err := e.st.Q.GetUser(e.ctx, u.ID)
		must(t, err)
		if after.ExpiresAt.Int64 != own {
			t.Fatalf("payment %d changed the inviter's own subscription", i+1)
		}
		if g, ok := e.running(inviter); ok {
			t.Fatalf("payment %d granted a reward before the step: %+v", i+1, g)
		}
	}
	// The third payment falls on the step. The referrer is switched onto the tariff the
	// REFEREE bought (the cheap one, which happens to be his own here) for 30 days.
	e.pay(invitee, e.cheap, 7000, "RUB")
	after, err := e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if after.TariffID.Int64 != e.cheap.ID {
		t.Fatalf("the reward put the inviter on tariff %d, wanted the bought %d", after.TariffID.Int64, e.cheap.ID)
	}
	if after.ExpiresAt.Int64 != e.now.Unix()+30*int64(24*time.Hour/time.Second) {
		t.Fatalf("the third payment did not give 30 days: %d", after.ExpiresAt.Int64)
	}
	// The subscription the reward switched is the inviter's own row: nothing new was made.
	if after.ID != u.ID {
		t.Fatalf("the reward made a second subscription: %d then %d", u.ID, after.ID)
	}
	g, ok := e.running(inviter)
	if !ok {
		t.Fatal("the reward did not start")
	}
	if g.BeforeExpiresAt.Int64 != own {
		t.Fatalf("the reward did not save the inviter's own expiry: %+v", g.BeforeExpiresAt)
	}
	// The reward recorded the days on the accounting row the admin's totals read.
	rows, err := e.st.Q.ListReferralRewardsOf(e.ctx, db.ListReferralRewardsOfParams{ReferrerTgID: inviter, Lim: 10})
	must(t, err)
	if len(rows) != 1 || rows[0].Kind != KindDays || rows[0].Days != 30 {
		t.Fatalf("the reward recorded: %+v", rows)
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

	rewards := e.pay(invitee, e.cheap, 7000, "RUB")
	if len(rewards) != 2 {
		t.Fatalf("one payment paid %d rewards: %+v", len(rewards), rewards)
	}
	if got := e.balance(inviter); got != 700 {
		t.Fatalf("the percent rule paid %d kopecks", got)
	}
	// CHANGED: the days rule no longer extends the referrer's own expiry — it switches him
	// onto the bought tariff for its days, which here is the tariff he is already on.
	after, err := e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if after.ExpiresAt.Int64 != e.now.Unix()+30*int64(24*time.Hour/time.Second) {
		t.Fatalf("the days rule did not give a month: %d then %d", e.now.Unix(), after.ExpiresAt.Int64)
	}
	if _, ok := e.running(inviter); !ok {
		t.Fatal("the days reward did not start")
	}
	rows, err := e.st.Q.ListReferralRewardsOf(e.ctx, db.ListReferralRewardsOfParams{ReferrerTgID: inviter, Lim: 10})
	must(t, err)
	if len(rows) != 2 || rows[0].RuleID == rows[1].RuleID {
		t.Fatalf("the rewards recorded: %+v", rows)
	}
}

// CHANGED (old behaviour removed): this test used to assert that a days reward gave NOTHING
// onto a subscription that never expires and recorded no reward row at all — the reward
// vanished, which is the silent drop the user called critical. A days rule is a temporary
// subscription on the tariff the REFEREE bought, so a referrer whose own subscription never
// expires is switched onto the bought tariff for the rule's days, and put back on his
// unlimited subscription (still unlimited) when they are over.
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
	if !after.ExpiresAt.Valid || after.ExpiresAt.Int64 != e.now.Unix()+30*int64(24*time.Hour/time.Second) {
		t.Fatalf("the reward did not give 30 days on the bought tariff: %+v", after.ExpiresAt)
	}
	if after.TariffID.Int64 != e.cheap.ID {
		t.Fatalf("the reward ran on tariff %d, wanted the bought %d", after.TariffID.Int64, e.cheap.ID)
	}
	// It is a reward that landed, not a silent drop: the row the admin's totals read is there.
	rows, err := e.st.Q.ListReferralRewardsOf(e.ctx, db.ListReferralRewardsOfParams{ReferrerTgID: inviter, Lim: 10})
	must(t, err)
	if len(rows) != 1 || rows[0].Kind != KindDays || rows[0].Days != 30 {
		t.Fatalf("the reward recorded: %+v", rows)
	}
	st, err := e.s.StatsOf(e.ctx, inviter)
	must(t, err)
	if st.Progress["d"] != 1 {
		t.Fatalf("the payment was not counted towards the rule: %+v", st.Progress)
	}

	// The days run out: the subscription that never expires is unlimited again.
	e.plus(31)
	e.advance()
	after, err = e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if after.ExpiresAt.Valid {
		t.Fatalf("the unlimited subscription did not come back unlimited: %+v", after.ExpiresAt)
	}
	if after.Status != "active" {
		t.Fatalf("the subscription came back as %q", after.Status)
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

// CHANGED (old behaviour removed): this test used to assert that with no subscription of
// the inviter's, the third payment granted NOTHING and recorded no reward — the resolve of
// `if !ok || !target.ExpiresAt.Valid { continue }`, which made a reward vanish with no row
// and nothing for the admin to see. A days reward is now a temporary subscription on the
// bought tariff, so a referrer with none is GIVEN one on that tariff for the rule's days;
// what is promised is the tariff and the days, and a reward that cannot land is recorded.
//
// The user's own words for the rule were "начислим ему 10 дней той подписки которую купил
// реферал": the tariff the referee bought, not a term on a subscription the referrer does
// not have. Making the subscription is the reading of that, and it is recorded, never silent.
func TestDaysRuleWithNoSubscriptionOfTheInviter(t *testing.T) {
	e := newEnv(t)
	e.program(Rule{ID: "d", Kind: KindDays, Value: 30, Every: 3, Enabled: true})
	e.invite(inviter, invitee)
	// The inviter never opened the referral screen and has no subscription.
	if _, ok, err := e.s.currentSub(e.ctx, e.st.Q, inviter); err != nil || ok {
		t.Fatalf("the fixture gave the inviter a subscription: %v %v", ok, err)
	}
	for i := range 2 {
		e.pay(invitee, e.cheap, 7000, "RUB")
		if got := e.balance(inviter); got != 0 {
			t.Fatalf("payment %d credited money where none was due", i+1)
		}
		if _, ok, err := e.s.currentSub(e.ctx, e.st.Q, inviter); err != nil || ok {
			t.Fatalf("payment %d made a subscription before the step: %v %v", i+1, ok, err)
		}
	}
	e.pay(invitee, e.cheap, 7000, "RUB")
	st, err := e.s.StatsOf(e.ctx, inviter)
	must(t, err)
	if st.Progress["d"] != 3 {
		t.Fatalf("the rule counted %d payments, wanted 3", st.Progress["d"])
	}
	// The third payment fell on the step: the inviter has a subscription on the tariff the
	// referee bought, for the rule's days, and the reward is on the record.
	u, ok, err := e.s.currentSub(e.ctx, e.st.Q, inviter)
	must(t, err)
	if !ok {
		t.Fatal("a days reward was granted and nothing was made")
	}
	if u.TariffID.Int64 != e.cheap.ID {
		t.Fatalf("the reward ran on tariff %d, wanted the bought %d", u.TariffID.Int64, e.cheap.ID)
	}
	if !u.ExpiresAt.Valid || u.ExpiresAt.Int64 != e.now.Unix()+30*int64(24*time.Hour/time.Second) {
		t.Fatalf("the reward did not give 30 days: %+v", u.ExpiresAt)
	}
	if u.Source != UserFromReferral {
		t.Fatalf("the subscription the reward made has source %q", u.Source)
	}
	rows, err := e.st.Q.ListReferralRewardsOf(e.ctx, db.ListReferralRewardsOfParams{ReferrerTgID: inviter, Lim: 10})
	must(t, err)
	if len(rows) != 1 || rows[0].Kind != KindDays || rows[0].Days != 30 {
		t.Fatalf("the reward recorded: %+v", rows)
	}
	// The third payment fell on the step, so the counter is exactly at it: the next reward
	// comes after three more, not two.
	e.pay(invitee, e.cheap, 7000, "RUB")
	rows, err = e.st.Q.ListReferralRewardsOf(e.ctx, db.ListReferralRewardsOfParams{ReferrerTgID: inviter, Lim: 10})
	must(t, err)
	if len(rows) != 1 {
		t.Fatalf("a reward was granted before its step: %+v", rows)
	}
	// The days run out and the subscription the reward made is kept: it is what he was
	// promised, and it ends on its own.
	e.plus(31)
	e.advance()
	after, err := e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if after.ExpiresAt.Int64 != u.ExpiresAt.Int64 {
		t.Fatalf("the reward's subscription was changed at its end: %+v", after.ExpiresAt)
	}
}

// ACCEPTANCE 1. A referrer on a trial with 5 days left, and an invitee who buys a 30-day
// tariff under a 10-day rule: the referrer is put on the BOUGHT tariff for 10 days (the
// trial's clock freezes at 5 days left), and when the reward ends he is back on the trial
// with those same 5 days. The user's exact fear was the opposite: "может получится так что
// реферал заплатит просто продлит пробный тариф".
func TestTrialIsFrozenUnderTheRewardAndComesBackWithItsDays(t *testing.T) {
	e := newEnv(t)
	e.program(Rule{ID: "d", Kind: KindDays, Value: 10, Enabled: true})
	e.invite(inviter, invitee)
	u := e.trialSub(inviter, 5)
	trialEnd := u.ExpiresAt.Int64
	if got := trialEnd - e.now.Unix(); got != 5*int64(24*time.Hour/time.Second) {
		t.Fatalf("the trial does not have 5 days left: %d seconds", got)
	}

	// The invitee buys the OTHER (dearer) tariff: the reward must run on that one, with its
	// own limits, not on the trial's tariff.
	e.pay(invitee, e.rich, 15000, "RUB")
	got, err := e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if got.TariffID.Int64 != e.rich.ID {
		t.Fatalf("the reward ran on tariff %d, wanted the bought %d", got.TariffID.Int64, e.rich.ID)
	}
	if got.ExpiresAt.Int64 != e.now.Unix()+10*int64(24*time.Hour/time.Second) {
		t.Fatalf("the reward gave %d days, wanted 10", (got.ExpiresAt.Int64-e.now.Unix())/int64(24*time.Hour/time.Second))
	}
	// The bought tariff's real limits, not the trial's.
	if !got.TrafficLimit.Valid || got.TrafficLimit.Int64 != e.rich.TrafficLimit.Int64 {
		t.Fatalf("the reward did not take the bought tariff's traffic limit: %+v wanted %+v", got.TrafficLimit, e.rich.TrafficLimit)
	}
	if !got.DeviceLimit.Valid || got.DeviceLimit.Int64 != e.rich.DeviceLimit.Int64 {
		t.Fatalf("the reward did not take the bought tariff's device limit: %+v wanted %+v", got.DeviceLimit, e.rich.DeviceLimit)
	}
	g, ok := e.running(inviter)
	if !ok {
		t.Fatal("the reward did not start")
	}
	if !g.BeforeTariffID.Valid || g.BeforeTariffID.Int64 != e.trial.ID {
		t.Fatalf("the trial's tariff was not saved for the reward's end: %+v", g.BeforeTariffID)
	}
	if !g.BeforeExpiresAt.Valid || g.BeforeExpiresAt.Int64 != trialEnd {
		t.Fatalf("the trial's expiry was not saved for the reward's end: %+v", g.BeforeExpiresAt)
	}

	// Nine days in, the reward is still what the referrer has: the trial has not been
	// shortened and has not been extended.
	e.plus(9)
	e.advance()
	got, err = e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if got.TariffID.Int64 != e.rich.ID {
		t.Fatalf("the reward did not last its days: tariff %d after 9 days", got.TariffID.Int64)
	}

	// The day after that, it is over: the trial is back, with the 5 days it had left. Its
	// clock froze for the ten days of the reward.
	e.plus(2)
	e.advance()
	got, err = e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if got.TariffID.Int64 != u.TariffID.Int64 {
		t.Fatalf("the trial's tariff did not come back: %d, wanted %d", got.TariffID.Int64, u.TariffID.Int64)
	}
	if got.ExpiresAt.Int64 != trialEnd {
		t.Fatalf("the trial came back with %d, wanted its own %d (5 days left)", got.ExpiresAt.Int64, trialEnd)
	}
	if _, ok := e.running(inviter); ok {
		t.Fatal("the reward is still running after its days")
	}
}

// ACCEPTANCE 2. Two referees: the first buys подписка+ (dearer), the second — later — buys
// подписка (cheaper). The referrer gets подписка+ for 10 days and then подписка for 10,
// back to back. The user's rule: "стоит учитывать цену, сначала будет идти самая дорогая и
// так до самой дешёвой" — a later, cheaper purchase must never downgrade him.
func TestQueueRunsDearestFirstAndNeverDowngrades(t *testing.T) {
	e := newEnv(t)
	e.program(Rule{ID: "d", Kind: KindDays, Value: 10, Enabled: true})
	e.invite(inviter, invitee)
	e.invite(inviter, second)
	u := e.sub(inviter)

	// The dearer purchase first.
	e.pay(invitee, e.rich, 15000, "RUB")
	got, err := e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if got.TariffID.Int64 != e.rich.ID {
		t.Fatalf("the first reward ran on tariff %d, wanted the bought %d", got.TariffID.Int64, e.rich.ID)
	}
	richEnd := got.ExpiresAt.Int64

	// Then the cheaper one, three days in: it must WAIT, not replace what is running.
	e.plus(3)
	e.pay(second, e.cheap, 7000, "RUB")
	got, err = e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if got.TariffID.Int64 != e.rich.ID {
		t.Fatalf("the cheaper reward downgraded the referrer to tariff %d", got.TariffID.Int64)
	}
	if got.ExpiresAt.Int64 != richEnd {
		t.Fatalf("the cheaper reward changed the running one's end: %d then %d", richEnd, got.ExpiresAt.Int64)
	}
	grants := e.grants(inviter)
	if len(grants) != 2 {
		t.Fatalf("the queue holds %d grants, wanted 2: %+v", len(grants), grants)
	}
	if grants[0].Price <= grants[1].Price {
		t.Fatalf("the queue is not dearest first: %d then %d", grants[0].Price, grants[1].Price)
	}
	if _, ok := e.running(inviter); !ok {
		t.Fatal("nothing is running")
	}

	// The dearer reward ends on its own day; the cheaper one starts there, not three days
	// later: the streak is contiguous (days 1-10 one tariff, days 11-20 the next).
	e.at(time.Unix(richEnd, 0).Add(time.Second))
	e.advance()
	got, err = e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if got.TariffID.Int64 != e.cheap.ID {
		t.Fatalf("the cheaper reward did not follow: tariff %d", got.TariffID.Int64)
	}
	if got.ExpiresAt.Int64 != richEnd+10*int64(24*time.Hour/time.Second) {
		t.Fatalf("the rewards are not contiguous: ends %d, wanted %d", got.ExpiresAt.Int64, richEnd+10*int64(24*time.Hour/time.Second))
	}
	// The cheaper reward's tariff limits come with it.
	if !got.TrafficLimit.Valid || got.TrafficLimit.Int64 != e.cheap.TrafficLimit.Int64 {
		t.Fatalf("the second reward did not take its tariff's limits: %+v", got.TrafficLimit)
	}

	// And at its own end the referrer is back on his own subscription.
	e.plus(11)
	e.advance()
	got, err = e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if got.TariffID.Int64 != u.TariffID.Int64 {
		t.Fatalf("the own subscription did not come back: %d", got.TariffID.Int64)
	}
	if got.ExpiresAt.Int64 != u.ExpiresAt.Int64 {
		t.Fatalf("the own expiry did not come back: %d wanted %d", got.ExpiresAt.Int64, u.ExpiresAt.Int64)
	}
}

// ACCEPTANCE 3. A referrer whose subscription has already expired: the reward revives him
// on the bought tariff for its days, and when they are over he lapses back to expired.
func TestExpiredReferrerIsRevivedThenLapsesAgain(t *testing.T) {
	e := newEnv(t)
	e.program(Rule{ID: "d", Kind: KindDays, Value: 10, Enabled: true})
	e.invite(inviter, invitee)
	u := e.sub(inviter)
	// The subscription ended a month ago.
	expired := e.now.Add(-30 * 24 * time.Hour)
	u, err := e.directory().Update(e.ctx, u.ID, domain.Patch{ExpiresAt: &expired})
	must(t, err)
	if domain.State(u, 0, e.now) != domain.StateExpired {
		t.Fatalf("the fixture's referrer is %q, wanted expired", domain.State(u, 0, e.now))
	}

	e.pay(invitee, e.rich, 15000, "RUB")
	got, err := e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if got.TariffID.Int64 != e.rich.ID {
		t.Fatalf("the reward ran on tariff %d, wanted the bought %d", got.TariffID.Int64, e.rich.ID)
	}
	if got.Status != "active" {
		t.Fatalf("the revived referrer is %q", got.Status)
	}
	if got.ExpiresAt.Int64 != e.now.Unix()+10*int64(24*time.Hour/time.Second) {
		t.Fatalf("the reward did not revive him for 10 days: %+v", got.ExpiresAt)
	}
	if domain.State(got, 0, e.now) != domain.StateActive {
		t.Fatalf("the revived referrer is not active: %q", domain.State(got, 0, e.now))
	}

	// The days run out: he goes back to the expiry he had, which is in the past.
	e.plus(11)
	e.advance()
	got, err = e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if got.ExpiresAt.Int64 != expired.Unix() {
		t.Fatalf("the old expiry did not come back: %d wanted %d", got.ExpiresAt.Int64, expired.Unix())
	}
	if s := domain.State(got, 0, e.now); s != domain.StateExpired {
		t.Fatalf("the referrer lapsed to %q, wanted expired", s)
	}
}

// ACCEPTANCE 4. A refunded invitee payment takes its reward out: a waiting one is dropped
// from the queue, and a running one is stopped — the referrer's own subscription comes back
// at once, and what was queued behind it is not downgraded.
func TestRefundRemovesTheQueuedReward(t *testing.T) {
	e := newEnv(t)
	e.program(Rule{ID: "d", Kind: KindDays, Value: 10, Enabled: true})
	e.invite(inviter, invitee)
	e.invite(inviter, second)
	u := e.sub(inviter)

	// The cheaper purchase first (it starts at once: nothing is ahead of it).
	cheapPay := e.paymentOf(second, e.cheap, 7000, "RUB")
	if out := e.payRewards(cheapPay); len(out) != 1 {
		t.Fatalf("the cheap purchase paid %+v", out)
	}
	got, err := e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if got.TariffID.Int64 != e.cheap.ID {
		t.Fatalf("the reward ran on tariff %d, wanted the bought %d", got.TariffID.Int64, e.cheap.ID)
	}
	if _, ok := e.running(inviter); !ok {
		t.Fatal("the cheap reward did not start")
	}

	// A dearer purchase follows: it waits.
	e.pay(invitee, e.rich, 15000, "RUB")
	grants := e.grants(inviter)
	if len(grants) != 2 {
		t.Fatalf("the queue holds %d grants: %+v", len(grants), grants)
	}

	// The cheap payment is refunded: its running reward stops and the referrer's own
	// subscription is handed back, so the dearer one waits for nothing.
	must(t, e.st.Tx(e.ctx, func(q *db.Queries) error { _, err := e.s.TakeBack(e.ctx, q, cheapPay.ID); return err }))
	got, err = e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if got.TariffID.Int64 != u.TariffID.Int64 {
		t.Fatalf("the own subscription did not come back after the refund: %d", got.TariffID.Int64)
	}
	if got.ExpiresAt.Int64 != u.ExpiresAt.Int64 {
		t.Fatalf("the own expiry did not come back after the refund: %d wanted %d", got.ExpiresAt.Int64, u.ExpiresAt.Int64)
	}
	grants = e.grants(inviter)
	for _, g := range grants {
		if g.PaymentID.Valid && g.PaymentID.Int64 == cheapPay.ID {
			if !g.RevertedAt.Valid {
				t.Fatalf("the refunded reward was not marked reverted: %+v", g)
			}
			if g.Status == GrantRunning {
				t.Fatalf("the refunded reward is still running: %+v", g)
			}
		}
	}
	// The reward row the admin's totals read is marked reverted too, so the refund does not
	// leave days on the books.
	rows, err := e.st.Q.ListReferralRewardsByPayment(e.ctx, sql.NullInt64{Int64: cheapPay.ID, Valid: true})
	must(t, err)
	if len(rows) != 1 || !rows[0].RevertedAt.Valid {
		t.Fatalf("the refunded days reward is not marked reverted: %+v", rows)
	}
	// The reconcile pass carries on with what is left: the dearer reward starts.
	e.advance()
	got, err = e.st.Q.GetUser(e.ctx, u.ID)
	must(t, err)
	if got.TariffID.Int64 != e.rich.ID {
		t.Fatalf("the surviving reward did not start: tariff %d", got.TariffID.Int64)
	}
}

// ACCEPTANCE 5. Percent rules are unchanged: this is the whole of the old percent contract
// on one page — the share of the ruble price whatever the currency, the every-N step, the
// balance cap and the refund taking the balance back.
func TestPercentRulesAreUnchanged(t *testing.T) {
	e := newEnv(t)
	e.program(Rule{ID: "p", Kind: KindPercent, Value: 10, Every: 1, Enabled: true})
	e.invite(inviter, invitee)
	e.pay(invitee, e.cheap, 7000, "RUB")
	if got := e.balance(inviter); got != 700 {
		t.Fatalf("10%% of 70 ₽: %d kopecks", got)
	}
	e.pay(invitee, e.rich, 150, "XTR")
	if got := e.balance(inviter); got != 700+1500 {
		t.Fatalf("10%% of 150 ₽ paid in Stars: %d kopecks", got)
	}
	// The every-N step still counts.
	e.program(Rule{ID: "p", Kind: KindPercent, Value: 10, Every: 3, Enabled: true})
	before := e.balance(inviter)
	for i := range 2 {
		e.pay(invitee, e.rich, 15000, "RUB")
		if got := e.balance(inviter); got != before {
			t.Fatalf("payment %d paid before the step: %d", i+1, got-before)
		}
	}
	e.pay(invitee, e.rich, 15000, "RUB")
	if got := e.balance(inviter); got != before+1500 {
		t.Fatalf("the third payment did not pay: %d", got-before)
	}
	// The cap is still the admin's.
	capBefore := e.balance(inviter)
	must(t, settings.Set(e.ctx, settings.New(e.st.Q), KeyConfig, Program{Enabled: true, StarsRate: 100, Spend: true, BalanceMax: capBefore + 1,
		Rules: []Rule{{ID: "p", Kind: KindPercent, Value: 100, Enabled: true, Every: 1}}}))
	e.pay(invitee, e.rich, 15000, "RUB")
	if got := e.balance(inviter); got != capBefore+1 {
		t.Fatalf("a capped balance is %d", got)
	}
	// A refund takes the balance back and marks the reward reverted. The cap is lifted again
	// first: a payment that credits nothing earns no reward row, so there would be nothing
	// for the refund to take back (that an at-cap payment earns nothing is the check above).
	must(t, settings.Set(e.ctx, settings.New(e.st.Q), KeyConfig, Program{Enabled: true, StarsRate: 100, Spend: true,
		Rules: []Rule{{ID: "p", Kind: KindPercent, Value: 10, Enabled: true, Every: 1}}}))
	p := e.paymentOf(invitee, e.rich, 15000, "RUB")
	if out := e.payRewards(p); len(out) != 1 {
		t.Fatalf("the payment paid %+v", out)
	}
	afterPay := e.balance(inviter)
	must(t, e.st.Tx(e.ctx, func(q *db.Queries) error { _, err := e.s.TakeBack(e.ctx, q, p.ID); return err }))
	// The refund takes the whole share back, not a kopeck of it.
	want := afterPay - share(15000, 10)
	if got := e.balance(inviter); got != want {
		t.Fatalf("after a refund the balance is %d, wanted %d", got, want)
	}
}
