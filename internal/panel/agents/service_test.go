package agents

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"mikan/internal/panel/campaigns"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

type env struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	s      *Service
	camps  *campaigns.Service
	now    time.Time
	stars  int64
	seq    int
	buyer  int64
	second int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	e := &env{t: t, ctx: ctx, now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), buyer: 601, second: 602}
	var err error
	if e.st, err = storetest.Open(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.st.Close() })
	if err := domain.Seed(ctx, e.st, e.now); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.s = New(e.st, log, func() time.Time { return e.now })
	e.camps = campaigns.New(e.st, log, func() time.Time { return e.now })
	e.camps.StarsRate = func(context.Context) int64 { return e.stars }
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// agent makes one with a percent-carrying campaign and counts the buyer's arrival through it.
func (e *env) agent(login string, percent int64) (db.CampaignAgent, db.CampaignTag) {
	e.t.Helper()
	a, err := e.s.Create(e.ctx, login, "long-enough-pass-phrase", "", nil)
	must(e.t, err)
	c, err := e.camps.Create(e.ctx, login, login, "")
	must(e.t, err)
	must(e.t, e.camps.Assign(e.ctx, c.ID, a.ID, percent))
	_, _, err = e.camps.Visit(e.ctx, e.buyer, login)
	must(e.t, err)
	return a, c
}

// payment is a tariff invoice for the buyer, in rubles by default.
func (e *env) payment(tgID, amount int64, kind, currency string) db.Payment {
	e.t.Helper()
	ts, err := e.st.Q.ListTariffs(e.ctx)
	must(e.t, err)
	if len(ts) == 0 {
		e.t.Fatal("the fixture wants a tariff, the seed has none")
	}
	e.seq++
	p, err := e.st.Q.CreatePayment(e.ctx, db.CreatePaymentParams{Provider: "stars", Payload: e.t.Name() + "-" + strconv.Itoa(e.seq),
		TgID: tgID, Kind: kind, TariffID: sql.NullInt64{Int64: ts[0].ID, Valid: true}, TariffName: ts[0].Name,
		Amount: amount, Currency: currency, CreatedAt: e.now.Unix()})
	must(e.t, err)
	return p
}

// A commission follows the payment the campaign counted: percent of the sale at the
// percent the link stood at then, refund takes it back, a changed percent only changes
// the future.
func TestCommissionFollowsTheSale(t *testing.T) {
	e := newEnv(t)
	a, c := e.agent("vasya", 10)

	bought := e.payment(e.buyer, 15000, "new", "RUB")
	must(t, e.camps.Attribute(e.ctx, e.st.Q, bought))
	renew := e.payment(e.buyer, 10000, "renew", "RUB")
	must(t, e.camps.Attribute(e.ctx, e.st.Q, renew))

	st, err := e.s.StatsOf(e.ctx, a.ID)
	must(t, err)
	if st.Earned != 2500 || st.Revenue != 25000 || st.Visits != 1 || st.Bought != 1 {
		t.Fatalf("stats: %+v", st)
	}
	if st.Owed() != 2500 {
		t.Fatalf("owed = %d", st.Owed())
	}

	// A percent change reaches only payments from now on: this renewal is worth half.
	must(t, e.camps.Assign(e.ctx, c.ID, a.ID, 50))
	after := e.payment(e.buyer, 10000, "renew", "RUB")
	must(t, e.camps.Attribute(e.ctx, e.st.Q, after))
	if st, err = e.s.StatsOf(e.ctx, a.ID); err != nil {
		t.Fatal(err)
	} else if st.Earned != 2500+5000 {
		t.Fatalf("earned after a percent change = %d", st.Earned)
	}

	// A refund of the first purchase takes its commission back.
	must(t, e.camps.Revert(e.ctx, e.st.Q, bought.ID))
	if st, err = e.s.StatsOf(e.ctx, a.ID); err != nil {
		t.Fatal(err)
	} else if st.Earned != 6000 || st.Revenue != 20000 {
		t.Fatalf("after a refund: %+v", st)
	}
}

// A Stars sale is worth the referral rate's kopecks and nothing without a rate.
func TestCommissionOfStars(t *testing.T) {
	e := newEnv(t)
	a, _ := e.agent("zvezda", 10)

	e.stars = 0
	must(t, e.camps.Attribute(e.ctx, e.st.Q, e.payment(e.buyer, 10, "new", "XTR")))
	if st, err := e.s.StatsOf(e.ctx, a.ID); err != nil {
		t.Fatal(err)
	} else if st.Earned != 0 {
		t.Fatalf("Stars without a rate earned %d", st.Earned)
	}

	e.stars = 500
	must(t, e.camps.Attribute(e.ctx, e.st.Q, e.payment(e.buyer, 10, "renew", "XTR")))
	if st, err := e.s.StatsOf(e.ctx, a.ID); err != nil {
		t.Fatal(err)
	} else if st.Earned != 500 {
		t.Fatalf("Stars at 5 ₽ earned %d, want 500", st.Earned)
	}
}

// A campaign without an agent earns nothing, and a stranger's payment is nobody's.
func TestCommissionNeedsTheCampaign(t *testing.T) {
	e := newEnv(t)
	_, _ = e.agent("uzh", 10)
	if _, _, err := e.camps.Visit(e.ctx, e.second, "own"); err != nil {
		t.Fatal(err)
	}
	own := e.payment(e.second, 15000, "new", "RUB")
	must(t, e.camps.Attribute(e.ctx, e.st.Q, own))
	if sum, err := e.s.Summary(e.ctx); err != nil {
		t.Fatal(err)
	} else if sum.Earned != 0 {
		t.Fatalf("the admin's own campaign earned %d", sum.Earned)
	}
}

// Assign checks the agent and the percent; unbinding clears both.
func TestAssignChecks(t *testing.T) {
	e := newEnv(t)
	a, c := e.agent("kontrol", 10)
	if err := e.camps.Assign(e.ctx, c.ID, a.ID, 0); !errors.Is(err, campaigns.ErrAgentPercent) {
		t.Fatalf("a zero percent: %v", err)
	}
	if err := e.camps.Assign(e.ctx, c.ID, a.ID, 101); !errors.Is(err, campaigns.ErrAgentPercent) {
		t.Fatalf("a percent over 100: %v", err)
	}
	if err := e.camps.Assign(e.ctx, c.ID, a.ID+100, 10); !errors.Is(err, campaigns.ErrAgentNotFound) {
		t.Fatalf("an unknown agent: %v", err)
	}
	must(t, e.camps.Assign(e.ctx, c.ID, 0, 0))
	if v, err := e.st.Q.GetCampaignTag(e.ctx, c.ID); err != nil {
		t.Fatal(err)
	} else if v.AgentID.Valid || v.AgentPercent.Valid {
		t.Fatal("an unbound campaign kept its agent")
	}
}

// A login is made once, answers only its password, and a switched-off agent answers
// nothing.
func TestLoginAndSessions(t *testing.T) {
	e := newEnv(t)
	a, _ := e.agent("pravilny", 10)

	if _, err := e.s.Create(e.ctx, "Pravilny ", "another-long-pass-phrase", "", nil); !errors.Is(err, ErrLoginTaken) {
		t.Fatalf("a second login of one name: %v", err)
	}
	if _, _, err := e.s.Login(e.ctx, "pravilny", "wrong-pass-phrase", "127.0.0.1:1", "test"); !errors.Is(err, ErrCredentials) {
		t.Fatalf("a wrong password: %v", err)
	}
	token, sess, err := e.s.Login(e.ctx, " Pravilny ", "long-enough-pass-phrase", "127.0.0.1:1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if sess.AgentID != a.ID || sess.CsrfToken == "" {
		t.Fatalf("session: %+v", sess)
	}
	if got, err := e.s.LookupSession(e.ctx, token); err != nil || got.AgentID != a.ID {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	if _, err := e.s.LookupSession(e.ctx, token+"x"); err == nil {
		t.Fatal("a stranger's token opened the panel")
	}

	if _, err := e.s.Update(e.ctx, a.ID, "pravilny", "", true, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.s.Login(e.ctx, "pravilny", "long-enough-pass-phrase", "127.0.0.1:1", "test"); !errors.Is(err, ErrCredentials) {
		t.Fatalf("a disabled agent logged in: %v", err)
	}
	if _, err := e.s.LookupSession(e.ctx, token); err == nil {
		t.Fatal("a disabled agent's session survived")
	}
}

// A payout comes off the owed at once, and the panel data is the agent's own.
func TestPayoutsAndPanelData(t *testing.T) {
	e := newEnv(t)
	a, _ := e.agent("chek", 20)
	must(t, e.camps.Attribute(e.ctx, e.st.Q, e.payment(e.buyer, 15000, "new", "RUB")))
	must(t, e.camps.Attribute(e.ctx, e.st.Q, e.payment(e.buyer, 5000, "renew", "RUB")))

	if _, err := e.s.Payout(e.ctx, a.ID, 0, ""); !errors.Is(err, ErrPayout) {
		t.Fatalf("a zero payout: %v", err)
	}
	if _, err := e.s.Payout(e.ctx, a.ID, -5, ""); !errors.Is(err, ErrPayout) {
		t.Fatalf("a negative payout: %v", err)
	}
	if _, err := e.s.Payout(e.ctx, a.ID, 2000, "наличными"); err != nil {
		t.Fatal(err)
	}
	if st, err := e.s.StatsOf(e.ctx, a.ID); err != nil {
		t.Fatal(err)
	} else if st.Earned != 4000 || st.Paid != 2000 || st.Owed() != 2000 {
		t.Fatalf("after a payout: %+v", st)
	}
	rows, total, err := e.s.Payouts(e.ctx, a.ID, 10, 0)
	must(t, err)
	if total != 1 || len(rows) != 1 || rows[0].Note != "наличными" {
		t.Fatalf("payouts: %d %v", total, rows)
	}

	links, linksTotal, err := e.s.Links(e.ctx, a.ID, 10, 0)
	must(t, err)
	if linksTotal != 1 || len(links) != 1 || links[0].Percent != 20 || links[0].Earned != 4000 {
		t.Fatalf("links: %+v %v", links, err)
	}
	refs, refsTotal, err := e.s.Referrals(e.ctx, a.ID, 10, 0)
	must(t, err)
	if refsTotal != 1 || len(refs) != 1 || refs[0].Bought != 1 || refs[0].Rub != 20000 {
		t.Fatalf("referrals: %+v", refs)
	}

	// A password change ends every session the agent holds.
	token, _, err := e.s.Login(e.ctx, "chek", "long-enough-pass-phrase", "127.0.0.1:1", "test")
	must(t, err)
	must(t, e.s.SetPassword(e.ctx, a.ID, "another-long-pass-phrase"))
	if _, err := e.s.LookupSession(e.ctx, token); err == nil {
		t.Fatal("the session survived a password change")
	}
}
