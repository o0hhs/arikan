package referral

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Service is the referral programme: who invited whom, what they earned and what of that
// balance is still theirs. Everything money- or term-giving happens on the transaction
// that applied the invitee's payment, so a reward is paid out once or not at all.
type Service struct {
	Store    *store.Store
	Settings *settings.Settings
	Log      *slog.Logger
	Now      func() time.Time
	// Changed tells the nodes about a subscription the days rule extended.
	Changed func()
	// Reward is told what an invitee's payment brought a referrer; nil: nobody is told.
	Reward func(ctx context.Context, r Reward)
}

// codeLen is how long an invite code is: eight of 62 characters is nearly 48 bits, and a
// code only has to be unguessable enough that nobody links a stranger by accident.
const codeLen = 8

// Errors the bot and the admin panel answer with (ref_<code>).
var (
	ErrOff        = errors.New("ref_off")
	ErrCode       = errors.New("ref_code_invalid")
	ErrSelf       = errors.New("ref_self")
	ErrBound      = errors.New("ref_already_bound")
	ErrNoAccount  = errors.New("ref_no_account")
	ErrNoTariff   = errors.New("ref_tariff_required")
	ErrNoSub      = errors.New("ref_no_subscription")
	ErrTooManyOwn = errors.New("ref_too_many_codes")
)

func New(st *store.Store, set *settings.Settings, log *slog.Logger, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{Store: st, Settings: set, Log: log, Now: now}
}

// Load reads the setup through set, which may be a transaction's own queries. A read error
// is returned, never replaced by the defaults: settings changed on top of those and saved
// would wipe the rules.
func Load(ctx context.Context, set *settings.Settings) (Program, error) {
	c, _, err := settings.GetOver(ctx, set, KeyConfig, Default())
	if err != nil {
		return Default(), err
	}
	_ = c.Validate()
	return c, nil
}

// Setup is the programme as it is now. A read error pauses the programme rather than
// guessing: rewards are money.
func (s *Service) Setup(ctx context.Context) Program {
	c, err := Load(ctx, s.Settings)
	if err != nil {
		s.Log.Warn("referral: settings unreadable, the programme is paused", "err", err)
		c.Enabled = false
	}
	return c
}

// On says whether the programme runs: on, and with a rule to earn with.
func (p Program) On() bool { return p.Enabled && len(p.Rules) > 0 }

// StarsRate is how many kopecks one Telegram Star counts as, by the programme's setting
// — the rate the campaigns' agent commissions share with the balance. 0 when the
// setting is unreadable or unset: a Stars payment is worth nothing then, to nobody.
func (s *Service) StarsRate(ctx context.Context) int64 {
	c, err := Load(ctx, s.Settings)
	if err != nil {
		s.Log.Warn("referral: settings unreadable, Stars worth nothing", "err", err)
		return 0
	}
	return c.StarsRate
}

// Reward is what one applied payment gave a referrer, to tell them about.
type Reward struct {
	Referrer int64 // the Telegram account that earned it
	Referee  int64 // the account that brought it
	Payment  int64
	Kind     string // percent or days
	// Rub is the kopecks that went into the balance; Days the days of the temporary
	// subscription on the tariff the referee bought (queue.go).
	Rub  int64
	Days int64
	// TariffID is the tariff a days reward runs on: the one the referee bought. 0 for a
	// percent reward, and for a days one whose tariff is gone.
	TariffID int64
	// EveryN is the rule's step: on the N-th qualifying payment, not on this one alone.
	EveryN int64
}

// Account is an account's own line of the programme: its code and what it may spend.
func (s *Service) Account(ctx context.Context, tgID int64) (db.ReferralAccount, bool, error) {
	a, err := s.Store.Q.GetReferralAccount(ctx, tgID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.ReferralAccount{}, false, nil
	}
	return a, err == nil, err
}

// Code is the account's invite code, made on first use. Every account has one, so the bot
// can always show a link; whether it earns anything is the rules' business.
func (s *Service) Code(ctx context.Context, tgID int64) (string, error) {
	a, ok, err := s.Account(ctx, tgID)
	if err != nil {
		return "", err
	}
	if ok {
		return a.Code, nil
	}
	a, err = s.create(ctx, s.Store.Q, tgID)
	return a.Code, err
}

func (s *Service) create(ctx context.Context, q *db.Queries, tgID int64) (db.ReferralAccount, error) {
	now := s.Now().Unix()
	// A code is random, so a collision is possible and only means trying again: the unique
	// index on code turns it into an error rather than a shared invite link.
	for range 5 {
		code := secure.Token(codeLen)
		n, err := q.TakeReferralAccount(ctx, db.TakeReferralAccountParams{TgID: tgID, Code: code, CreatedAt: now})
		if err != nil {
			if store.IsUnique(err) {
				continue
			}
			return db.ReferralAccount{}, err
		}
		if n == 0 {
			return q.GetReferralAccount(ctx, tgID)
		}
		return db.ReferralAccount{TgID: tgID, Code: code, CreatedAt: now, UpdatedAt: now}, nil
	}
	return db.ReferralAccount{}, ErrTooManyOwn
}

// Link ties the Telegram account that followed an invite code to the account that made it.
// A Telegram account belongs to one referrer for good: a second link changes nothing, so a
// code cannot be shared around and one invitee cannot be counted twice.
func (s *Service) Link(ctx context.Context, tgID int64, code string) (db.Referral, error) {
	if !s.Setup(ctx).Enabled {
		return db.Referral{}, ErrOff
	}
	if code == "" {
		return db.Referral{}, ErrCode
	}
	referrer, err := s.Store.Q.FindReferralAccountByCode(ctx, code)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Referral{}, ErrCode
	}
	if err != nil {
		return db.Referral{}, err
	}
	if referrer.TgID == tgID {
		return db.Referral{}, ErrSelf
	}
	var out db.Referral
	err = s.Store.Tx(ctx, func(q *db.Queries) error {
		out = db.Referral{}
		if _, err := q.GetReferralOfReferee(ctx, tgID); err == nil {
			return ErrBound
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		n, err := q.LinkReferral(ctx, db.LinkReferralParams{ReferrerTgID: referrer.TgID, RefereeTgID: tgID, Code: code, CreatedAt: s.Now().Unix()})
		if err != nil {
			return err
		}
		if n == 0 {
			// Bound by somebody else while this transaction waited.
			return ErrBound
		}
		out, err = q.GetReferralOfReferee(ctx, tgID)
		return err
	})
	if err != nil {
		return db.Referral{}, err
	}
	return out, nil
}

// Announce tells the referrers what they earned. It runs after the transaction the reward
// was committed with, so the bot may take its time and nothing holds the database for it.
func (s *Service) Announce(ctx context.Context, rewards []Reward) {
	if len(rewards) == 0 {
		return
	}
	terms := false
	for _, r := range rewards {
		terms = terms || r.Days > 0
	}
	if terms && s.Changed != nil {
		s.Changed()
	}
	if s.Reward == nil || !s.Setup(ctx).Notify {
		return
	}
	for _, r := range rewards {
		r := r
		s.Reward(ctx, r)
	}
}

// Stats is what the bot's referral screen shows.
type Stats struct {
	Invited int64
	Paid    int64
	Balance int64
	// Progress is the number of qualifying payments under each rule, by rule id.
	Progress map[string]int64
}

// StatsOf is the account's own numbers. A missing account is not an error: everybody has
// an invite code, and only some of them have invited anybody.
func (s *Service) StatsOf(ctx context.Context, tgID int64) (Stats, error) {
	q := s.Store.Q
	var st Stats
	invited, err := q.CountReferralsOf(ctx, tgID)
	if err != nil {
		return st, err
	}
	st.Invited, st.Paid = invited.Invited, invited.Paid
	if bal, err := q.ReferralBalance(ctx, tgID); err == nil {
		st.Balance = bal
	} else if !errors.Is(err, sql.ErrNoRows) {
		return st, err
	}
	rows, err := q.ListReferralCounts(ctx, tgID)
	if err != nil {
		return st, err
	}
	st.Progress = make(map[string]int64, len(rows))
	for _, r := range rows {
		st.Progress[r.RuleID] = r.Paid
	}
	return st, nil
}

// RewardPayment gives the referrer what the applied payment p earned, on q's transaction —
// the one that created or renewed the buyer's subscription. Nothing is paid twice: the
// reward row's payment id is unique, and a payment is applied once.
func (s *Service) RewardPayment(ctx context.Context, q *db.Queries, p db.Payment) ([]Reward, error) {
	cfg, err := Load(ctx, settings.New(q))
	if err != nil {
		return nil, err
	}
	return s.reward(ctx, q, cfg, p)
}

func (s *Service) reward(ctx context.Context, q *db.Queries, cfg Program, p db.Payment) ([]Reward, error) {
	if !cfg.On() {
		return nil, nil
	}
	// Only what a referrer brought in: a subscription bought or renewed. Traffic packages
	// are the owner's own top-up.
	if p.Kind != "new" && p.Kind != "renew" {
		return nil, nil
	}
	if !p.TariffID.Valid {
		return nil, nil
	}
	ref, err := q.GetReferralOfReferee(ctx, p.TgID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Nobody earns from themselves, whatever a hand-edited row says.
	if ref.ReferrerTgID == p.TgID {
		return nil, nil
	}
	if _, err := q.MarkReferralPaid(ctx, db.MarkReferralPaidParams{ID: ref.ID,
		FirstPaidAt: sql.NullInt64{Int64: s.Now().Unix(), Valid: true}}); err != nil {
		return nil, err
	}
	rubles := Rub(p.Amount, p.Currency, cfg.StarsRate)
	// A payment the programme cannot price earns nothing at all: Stars with no rate pay no
	// percent, give no days and burn no "every N" cycle — otherwise payments worth nothing
	// would add up to a days reward, against what the rate means (config.go: 0, a payment
	// in Stars earns nothing). The invitee still counts as having paid.
	if p.Currency != Currency && rubles <= 0 {
		return nil, nil
	}
	now := s.Now().Unix()
	payID := sql.NullInt64{Int64: p.ID, Valid: true}
	var out []Reward
	seen := make(map[string]bool, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		if !rule.Enabled || !rule.counts(p.TariffID.Int64) {
			continue
		}
		// A hand-edited setup may name one rule twice: the copy must not earn a second time
		// for one payment (the reward is unique per payment and rule, and the row would
		// abort the payment's transaction).
		if seen[rule.ID] {
			continue
		}
		seen[rule.ID] = true
		switch rule.Kind {
		case KindPercent:
			amount := share(rubles, rule.Value)
			if rule.Every > 1 {
				paid, err := q.AddReferralCount(ctx, db.AddReferralCountParams{TgID: ref.ReferrerTgID, RuleID: rule.ID})
				if err != nil {
					return nil, err
				}
				if paid%rule.Every != 0 {
					continue
				}
			}
			credited, err := s.credit(ctx, q, ref.ReferrerTgID, amount, cfg, now)
			if err != nil || credited <= 0 {
				if err != nil {
					return nil, err
				}
				continue
			}
			if _, err := q.CreateReferralReward(ctx, db.CreateReferralRewardParams{ReferrerTgID: ref.ReferrerTgID, ReferralID: ref.ID,
				PaymentID: payID, RuleID: rule.ID, Kind: KindPercent, Amount: credited, Days: 0, EveryN: rule.Every, Currency: Currency, CreatedAt: now}); err != nil {
				return nil, err
			}
			out = append(out, Reward{Referrer: ref.ReferrerTgID, Referee: p.TgID, Payment: p.ID, Kind: KindPercent, Rub: credited, EveryN: rule.Every})
		case KindDays:
			paid, err := q.AddReferralCount(ctx, db.AddReferralCountParams{TgID: ref.ReferrerTgID, RuleID: rule.ID})
			if err != nil {
				return nil, err
			}
			if paid%rule.Every != 0 {
				continue
			}
			// A days rule gives a temporary subscription on the tariff the REFEREE bought,
			// for the rule's days — not days on whatever subscription the referrer happens
			// to have. The reward is a queue entry from here on: it may run now, or wait
			// behind a dearer reward already running (grantDays).
			//
			// The bought tariff's price orders the queue: dearest first, so a later cheaper
			// purchase waits rather than downgrading the referrer. A tariff scoped rule
			// counts the payment even when the tariff cannot be read (deleted since): the
			// grant is then recorded and closed rather than silently dropped.
			grant, err := s.grantDays(ctx, q, cfg, ref, p, rule, now)
			if err != nil {
				return nil, err
			}
			if grant.ID == 0 {
				continue
			}
			out = append(out, Reward{Referrer: ref.ReferrerTgID, Referee: p.TgID, Payment: p.ID, Kind: KindDays,
				Days: rule.Value, TariffID: p.TariffID.Int64, EveryN: rule.Every})
		}
	}
	return out, nil
}

// credit puts amount kopecks into an account's balance, under the admin's cap, and says how
// much really went in: an amount over the cap is cut, an account at the cap gets nothing.
func (s *Service) credit(ctx context.Context, q *db.Queries, tgID, amount int64, cfg Program, now int64) (int64, error) {
	if amount <= 0 {
		return 0, nil
	}
	// The referrer's account is guaranteed by the foreign key on referrals.referrer_tg_id:
	// a row that is not there is a database that is not the one this code expects, and the
	// payment waits for the next attempt rather than quietly paying nothing.
	bal, err := q.ReferralBalance(ctx, tgID)
	if err != nil {
		return 0, err
	}
	if cfg.BalanceMax > 0 && bal+amount > cfg.BalanceMax {
		amount = max(cfg.BalanceMax-bal, 0)
	}
	if amount <= 0 {
		return 0, nil
	}
	n, err := q.AddReferralBalance(ctx, db.AddReferralBalanceParams{TgID: tgID, Amount: amount, UpdatedAt: now})
	if err != nil || n != 1 {
		return 0, err
	}
	return amount, nil
}

// grantDays gives a days rule what it promised: a reward recorded on the payment's own
// transaction (referral_rewards, the accounting half the admin's totals read) and a queue
// entry (referral_reward_grants) that says on which tariff and for how many days the
// referrer is to be switched. When nothing of the referrer's is running and his reward is
// the dearest waiting, it starts at once, on this same transaction; otherwise it waits its
// turn and the reconcile pass starts it (Advance).
//
// A reward on a tariff that is not there any more (deleted since the invoice) is recorded
// and closed, never dropped: what was promised happened, and the admin sees it.
func (s *Service) grantDays(ctx context.Context, q *db.Queries, cfg Program, ref db.Referral, p db.Payment, rule Rule, now int64) (db.ReferralRewardGrant, error) {
	payID := sql.NullInt64{Int64: p.ID, Valid: true}
	// The accounting half first: what the rule promised is on the record the admin's totals
	// read, whatever becomes of the queue entry below.
	if _, err := q.CreateReferralReward(ctx, db.CreateReferralRewardParams{ReferrerTgID: ref.ReferrerTgID, ReferralID: ref.ID,
		PaymentID: payID, RuleID: rule.ID, Kind: KindDays, Amount: 0, Days: rule.Value, EveryN: rule.Every, Currency: Currency, CreatedAt: now}); err != nil {
		return db.ReferralRewardGrant{}, err
	}
	// The price of the bought tariff orders the queue. A tariff that cannot be read has no
	// price to sort by: it sorts last, and its grant is closed below.
	var price int64
	t, terr := q.GetTariff(ctx, p.TariffID.Int64)
	if terr == nil {
		price = tariffPrice(t)
	} else if !errors.Is(terr, sql.ErrNoRows) {
		return db.ReferralRewardGrant{}, terr
	}
	grant, err := q.CreateReferralRewardGrant(ctx, db.CreateReferralRewardGrantParams{
		ReferrerTgID: ref.ReferrerTgID, ReferralID: ref.ID, PaymentID: payID, RuleID: rule.ID,
		TariffID: p.TariffID, Days: rule.Value, Price: price, CreatedAt: now,
	})
	if err != nil {
		return db.ReferralRewardGrant{}, err
	}
	if terr != nil {
		// What the referee bought is gone: there is no tariff to run the reward on. The
		// reward stays on the record, closed, and the referrer is told nothing of it.
		if _, err := q.FinishReferralRewardGrant(ctx, db.FinishReferralRewardGrantParams{UpdatedAt: now, ID: grant.ID}); err != nil {
			return db.ReferralRewardGrant{}, err
		}
		return db.ReferralRewardGrant{}, nil
	}
	// The reward may start this minute: the same call the reconcile pass makes, so the
	// rule is one code path whether the queue is moved by a payment or by the pass. The
	// moments line up: a queued reward waits for what is ahead of it, a ready one starts now.
	if err := s.startHead(ctx, q, ref.ReferrerTgID, now, time.Unix(now, 0)); err != nil {
		if errors.Is(err, domain.ErrNoSlots) {
			// A referrer with no subscription needs a slot for the one the reward makes;
			// the pool is refilled by the caller's retry. The grant is already recorded
			// and waits, so nothing is lost if the retry does not come.
			return grant, nil
		}
		return db.ReferralRewardGrant{}, err
	}
	return grant, nil
}

// tariffPrice is what a tariff costs in kopecks, for the queue's order: the ruble price,
// else the Stars price at the programme's rate, else nothing (it sorts last). A tariff no
// one pays for still gives its days, at the end of the queue.
func tariffPrice(t db.Tariff) int64 {
	if t.PriceRub.Valid && t.PriceRub.Int64 > 0 {
		return t.PriceRub.Int64
	}
	if t.PriceStars.Valid && t.PriceStars.Int64 > 0 {
		return t.PriceStars.Int64
	}
	return 0
}

// currentSub is the subscription the referrer is shown in their chat: the current one,
// else the first they linked.
func (s *Service) currentSub(ctx context.Context, q *db.Queries, tgID int64) (db.User, bool, error) {
	list, err := q.ListTgLinksOf(ctx, tgID)
	if err != nil {
		return db.User{}, false, err
	}
	if len(list) == 0 {
		return db.User{}, false, nil
	}
	if ch, err := q.GetTgChat(ctx, tgID); err == nil {
		for _, u := range list {
			if u.ID == ch.Current {
				return u, true, nil
			}
		}
	}
	return list[0], true, nil
}

// Reserve holds what the balance pays of an invoice and says how much of the price that is,
// in the invoice's own currency. Nothing is held when the programme is off, when the
// balance cannot pay any of it, or when paying all of it would leave the provider nothing
// to take: an invoice is a payment, and a payment of zero has no provider behind it.
func (s *Service) Reserve(ctx context.Context, q *db.Queries, tgID, paymentID, amount int64, currency string, cfg Program) (int64, error) {
	if !cfg.Spend || amount <= 1 {
		return 0, nil
	}
	bal, err := q.ReferralBalance(ctx, tgID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	off := min(FromRub(bal, currency, cfg.StarsRate), amount-1)
	if off <= 0 {
		return 0, nil
	}
	kopecks := Rub(off, currency, cfg.StarsRate)
	if kopecks <= 0 {
		return 0, nil
	}
	n, err := q.TakeReferralBalance(ctx, db.TakeReferralBalanceParams{TgID: tgID, Amount: kopecks, UpdatedAt: s.Now().Unix()})
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, nil
	}
	if _, err := q.CreateReferralSpend(ctx, db.CreateReferralSpendParams{PaymentID: sql.NullInt64{Int64: paymentID, Valid: true},
		TgID: tgID, Amount: kopecks, CreatedAt: s.Now().Unix()}); err != nil {
		return 0, err
	}
	return off, nil
}

// Off is what the balance would pay of an invoice, in the invoice's currency, without
// touching it: the price an open invoice is compared with.
func (s *Service) Off(ctx context.Context, tgID, amount int64, currency string, cfg Program) (int64, error) {
	if !cfg.Spend || amount <= 1 {
		return 0, nil
	}
	bal, err := s.Store.Q.ReferralBalance(ctx, tgID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return min(FromRub(bal, currency, cfg.StarsRate), amount-1), nil
}

// Held says what the balance is already holding for an invoice, in the invoice's currency,
// and whether that hold still stands. An open invoice made a minute ago is shown again
// with the discount it was made with, rather than taking the balance a second time.
func (s *Service) Held(ctx context.Context, q *db.Queries, paymentID int64, currency string, cfg Program) (int64, bool, error) {
	sp, err := q.GetReferralSpendByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: paymentID != 0})
	if errors.Is(err, sql.ErrNoRows) || sp.RevertedAt.Valid {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return FromRub(sp.Amount, currency, cfg.StarsRate), true, nil
}

// Released says whether the balance hold of a payment was given back: the invoice expired
// with the hold it made (or the payment was refunded), and the reconciler sent the money
// back to its owner. A payment a provider reports paid after that no longer costs what it
// was priced at — the hold covers part of the invoice and is the buyer's again — so the
// billing refunds it instead of applying it (billing.refundLateReferralPayment).
func (s *Service) Released(ctx context.Context, paymentID int64) (bool, error) {
	sp, err := s.Store.Q.GetReferralSpendByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return sp.RevertedAt.Valid, nil
}

// TakeBack gives back what a payment held or earned for a referrer, on q's transaction. A
// hold on an invoice nobody paid is money of its owner again; what a percent rule already
// put into a balance comes out of that balance, so a spent one simply stays spent.
//
// A days rule's reward goes whole: a queued one is dropped from the queue, and a running
// one is stopped — the referrer's own subscription (a trial's leftover days included) comes
// back at once — after which the rewards behind it close up and run back to back as if it
// had never been there. A reward that has already ended is left: the subscription it gave
// has been used, as days added to a subscription always were.
func (s *Service) TakeBack(ctx context.Context, q *db.Queries, paymentID int64) (int64, error) {
	now := s.Now().Unix()
	var back int64
	if n, err := q.RevertReferralSpend(ctx, db.RevertReferralSpendParams{RevertedAt: sql.NullInt64{Int64: now, Valid: true},
		PaymentID: sql.NullInt64{Int64: paymentID, Valid: paymentID != 0}}); err != nil {
		return 0, err
	} else if n == 1 {
		if sp, err := q.GetReferralSpendByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: true}); err == nil {
			if _, err := q.AddReferralBalance(ctx, db.AddReferralBalanceParams{TgID: sp.TgID, Amount: sp.Amount, UpdatedAt: now}); err != nil {
				return 0, err
			}
			back += sp.Amount
		}
	}
	// The days rewards of the payment come out of the queue first, so the referral_rewards
	// rows below are marked reverted with the queue already closed up.
	if err := s.revertGrants(ctx, q, paymentID, now); err != nil {
		return back, err
	}
	rows, err := q.ListReferralRewardsByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: paymentID != 0})
	if err != nil {
		return back, err
	}
	for _, r := range rows {
		if r.RevertedAt.Valid {
			continue
		}
		// A days reward gives no balance back: the days it gave are taken out of the queue
		// above, and the row is marked reverted here so the totals do not count it.
		if r.Kind != KindPercent {
			if _, err := q.RevertReferralReward(ctx, db.RevertReferralRewardParams{ID: r.ID, RevertedAt: sql.NullInt64{Int64: now, Valid: true}}); err != nil {
				return back, err
			}
			continue
		}
		n, err := q.RevertReferralReward(ctx, db.RevertReferralRewardParams{ID: r.ID, RevertedAt: sql.NullInt64{Int64: now, Valid: true}})
		if err != nil {
			return back, err
		}
		if n == 1 {
			// A balance already spent on something else does not go below zero; what is
			// left of the reward is simply lost to the panel.
			if _, err := q.AddReferralBalance(ctx, db.AddReferralBalanceParams{TgID: r.ReferrerTgID, Amount: -r.Amount, UpdatedAt: now}); err != nil {
				return back, err
			}
			back += r.Amount
		}
	}
	return back, nil
}

// ReleaseClosed gives back the holds of invoices nobody paid and will not: it runs with
// the billing's reconcile pass, right after the pending ones have been expired.
func (s *Service) ReleaseClosed(ctx context.Context) error {
	rows, err := s.Store.Q.ListSpentClosedPayments(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := s.Store.Tx(ctx, func(q *db.Queries) error {
			// The invoice may have been paid while this pass waited its turn: its row is
			// locked and looked at again, and a hold goes back only for an invoice that is
			// still closed. Without this, a payment reported paid between the listing and
			// this transaction would be applied with its discount given back.
			p, err := q.GetPaymentForUpdate(ctx, r.PaymentID.Int64)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if p.Status != "expired" && p.Status != "failed" && p.Status != "refunded" {
				return nil
			}
			if _, err := s.TakeBack(ctx, q, r.PaymentID.Int64); err != nil {
				return err
			}
			_, err = q.ReleaseReferralSpend(ctx, db.ReleaseReferralSpendParams{ID: r.ID, RevertedAt: sql.NullInt64{Int64: s.Now().Unix(), Valid: true}})
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// AdminStats is what the admin's page shows above the rules.
type AdminStats struct {
	Invited  int64 `json:"invited"`
	Paid     int64 `json:"paid"`
	Accounts int64 `json:"accounts"`
	// Owed is every balance in the programme: what it would cost to pay them all out.
	Owed       int64 `json:"owed"`
	Earned     int64 `json:"earned"`
	EarnedDays int64 `json:"earned_days"`
	Spent      int64 `json:"spent"`
}

// View is the setup with the numbers behind it.
func (s *Service) View(ctx context.Context) (Program, AdminStats, error) {
	q := s.Store.Q
	cfg := s.Setup(ctx)
	var out AdminStats
	refs, err := q.CountReferrals(ctx)
	if err != nil {
		return cfg, out, err
	}
	accs, err := q.CountReferralAccounts(ctx)
	if err != nil {
		return cfg, out, err
	}
	rw, err := q.CountReferralRewards(ctx)
	if err != nil {
		return cfg, out, err
	}
	sp, err := q.ReferralSpendTotals(ctx)
	if err != nil {
		return cfg, out, err
	}
	out = AdminStats{Invited: refs.Total, Paid: refs.Paid, Accounts: accs.Accounts, Owed: accs.Owed,
		Earned: rw.Rub, EarnedDays: rw.Days, Spent: sp.Total}
	return cfg, out, nil
}
