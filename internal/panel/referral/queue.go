package referral

// The days rule's queue (migration 0043, referral_reward_grants). A days rule does not add
// days to whatever the referrer happens to have: it switches the referrer onto the tariff
// the REFEREE bought, for the days the rule gives, and gives him his own subscription back
// when those days are over. A refunded payment takes its reward out again.
//
// One row is both the queue entry and the suspension record. The queue is ordered by the
// bought tariff's price, dearest first, then arrival, so a later cheaper purchase waits
// behind a running dearer one rather than downgrading it. Rewards run back to back: the
// next one starts where the last ended, not when the pass happens to look.
//
// Nothing here runs on a timer of its own: reward() writes a grant on the payment's own
// transaction, and Advance runs with the billing's reconcile pass (the ticker that already
// expires invoices and releases holds), so a restart loses nothing and no new daemon is
// needed.

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"mikan/internal/panel/store/db"
)

// The states of a grant row (referral_reward_grants.status).
const (
	GrantQueued  = "queued"
	GrantRunning = "running"
	GrantDone    = "done"
)

// UserFromReferral is users.source for a subscription the programme made for a referrer who
// had none: it is not a purchase and not a trial, and the admin's list must say so.
const UserFromReferral = "referral"

// referralNote is what such a subscription says in its note.
const referralNote = "Telegram · referral"

// daySeconds is a day in the unit the panel's expiries are in.
const daySeconds = int64(24 * time.Hour / time.Second)

// Grant is the queue order of one reward, without the database: the price the order is on
// and the arrival that breaks a tie. The SQL orders by the same two fields
// (ORDER BY price DESC, id), so this is the rule in one place a test can read.
type Grant struct {
	ID    int64 // arrival: the lower id arrived first
	Price int64 // kopecks of the tariff the referee bought
}

// CompareGrants is the queue's order: dearest reward first, and between equal prices the
// one that arrived first. A later cheaper purchase therefore never overtakes one already
// owed, which is what stops it downgrading a running reward.
func CompareGrants(a, b Grant) int {
	if a.Price != b.Price {
		return cmp.Compare(b.Price, a.Price)
	}
	return cmp.Compare(a.ID, b.ID)
}

// grantEnd is when a reward's days started at base run out.
func grantEnd(base, days int64) int64 { return base + days*daySeconds }

// Advance runs the days queue: it ends the rewards whose days have run out (giving each
// referrer his own subscription back) and starts what is next. The billing's reconcile pass
// calls it next to ReleaseClosed; reward() calls it on the payment's own transaction, so a
// reward starts at once when nothing is ahead of it.
//
// Safe at any moment and any number of times: every transition is guarded by the row's own
// status in the statement's WHERE, so two passes racing cannot run one grant twice.
func (s *Service) Advance(ctx context.Context, now time.Time) error {
	due, err := s.Store.Q.ListDueReferralRewardGrants(ctx, sql.NullInt64{Int64: now.Unix(), Valid: true})
	if err != nil {
		return err
	}
	// The referrers whose streak this pass moved, with the moment the reward that ended was
	// due to end: the reward behind it starts there, not when this pass happens to look, so
	// the paid streak stays contiguous even when a pass is late.
	handover := make(map[int64]int64, len(due))
	for _, g := range due {
		at := g.EndsAt.Int64
		if err := s.Store.Tx(ctx, func(q *db.Queries) error { return s.endGrant(ctx, q, g.ID, now) }); err != nil {
			return err
		}
		handover[g.ReferrerTgID] = at
	}
	rows, err := s.Store.Q.ListPendingReferralRewardGrants(ctx)
	if err != nil {
		return err
	}
	done := make(map[int64]bool, len(rows))
	for _, g := range rows {
		if done[g.ReferrerTgID] {
			continue
		}
		if g.Status == GrantRunning {
			if _, moved := handover[g.ReferrerTgID]; !moved {
				continue // something of his is on and not ending this pass
			}
		}
		done[g.ReferrerTgID] = true
		base := now.Unix()
		if at, ok := handover[g.ReferrerTgID]; ok {
			base = at
		}
		if err := s.Store.Tx(ctx, func(q *db.Queries) error { return s.startHead(ctx, q, g.ReferrerTgID, base, now) }); err != nil {
			return err
		}
	}
	return nil
}

// startHead starts the dearest reward a referrer has waiting, when nothing of his is
// running. base is when the reward's days start: now, or the moment the reward that just
// ended was due to end, so the streak is contiguous (days 1-10 one tariff, days 11-20 the
// next) even when the pass that hands over runs late.
func (s *Service) startHead(ctx context.Context, q *db.Queries, tgID int64, base int64, now time.Time) error {
	pending, err := q.ListPendingReferralRewardGrants(ctx)
	if err != nil {
		return err
	}
	for _, g := range pending {
		if g.ReferrerTgID == tgID && g.Status == GrantRunning {
			return nil // something of his is on: nothing starts
		}
	}
	head, err := q.NextQueuedReferralRewardGrant(ctx, tgID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// The tariff the referee bought may be gone since (deleted): the reward cannot land,
	// and the row is closed rather than left waiting for ever.
	if !head.TariffID.Valid {
		_, err := q.FinishReferralRewardGrant(ctx, db.FinishReferralRewardGrantParams{UpdatedAt: now.Unix(), ID: head.ID})
		return err
	}
	if _, err := q.GetTariff(ctx, head.TariffID.Int64); errors.Is(err, sql.ErrNoRows) {
		_, err := q.FinishReferralRewardGrant(ctx, db.FinishReferralRewardGrantParams{UpdatedAt: now.Unix(), ID: head.ID})
		return err
	} else if err != nil {
		return err
	}
	return s.startGrant(ctx, q, head, base, now)
}

// startGrant switches the referrer onto the grant's tariff for its days and saves what he
// had, so it can be put back. The saved subscription and the running mark are one statement
// on the queue row, so a pass that dies between the two cannot lose the suspension.
func (s *Service) startGrant(ctx context.Context, q *db.Queries, g db.ReferralRewardGrant, base int64, now time.Time) error {
	before, ok, err := s.currentSub(ctx, q, g.ReferrerTgID)
	if err != nil {
		return err
	}
	var haveBefore sql.NullInt64
	if ok {
		haveBefore = sql.NullInt64{Int64: before.ID, Valid: true}
	}
	user, err := s.land(ctx, q, g, before, ok, base, now)
	if err != nil {
		return err
	}
	n, err := q.StartReferralRewardGrant(ctx, db.StartReferralRewardGrantParams{
		RunAt: base, EndsAt: sql.NullInt64{Int64: grantEnd(base, g.Days), Valid: true}, RewardID: g.RewardID,
		BeforeUserID: haveBefore, BeforeStatus: before.Status,
		BeforeTariffID: before.TariffID, BeforeTrafficLimit: before.TrafficLimit, BeforeDeviceLimit: before.DeviceLimit,
		BeforeSpeedLimit: before.SpeedLimit, BeforeResetStrategy: before.ResetStrategy, BeforeDeviceMode: before.DeviceMode,
		BeforePeriodDays: before.PeriodDays, BeforePeriodStart: before.PeriodStart,
		BeforeExpiresAt: before.ExpiresAt, BeforeBillingDay: before.BillingDay,
		UpdatedAt: now.Unix(), ID: g.ID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return nil // another pass started it first; its write stands
	}
	// A grant that made the referrer a subscription rather than switching an existing one
	// must remember which one: the reward's end must not leave it behind as if the referrer
	// had owned it.
	if !ok {
		if _, err := q.SetReferralRewardGrantBeforeUser(ctx, db.SetReferralRewardGrantBeforeUserParams{
			BeforeUserID: sql.NullInt64{Int64: user.ID, Valid: true}, ID: g.ID,
		}); err != nil {
			return err
		}
	}
	if s.Changed != nil {
		s.Changed()
	}
	return nil
}

// land puts the granted tariff on the referrer for the term, with that tariff's real limits
// (traffic, devices, speed, reset strategy) — the reward is a subscription on the tariff the
// referee paid for, not a copy of the referrer's own.
//
// The subscription switched is the one the referrer is shown in his chat (currentSub), the
// same one the days rule used to extend. A referrer with no subscription at all is given a
// new one on the bought tariff: what the rule promises is that tariff and its days, and a
// ready reward that cannot land is recorded and carried out rather than dropped.
func (s *Service) land(ctx context.Context, q *db.Queries, g db.ReferralRewardGrant, before db.User, had bool, base int64, now time.Time) (db.User, error) {
	t, err := q.GetTariff(ctx, g.TariffID.Int64)
	if err != nil {
		return db.User{}, err
	}
	expires := sql.NullInt64{Int64: grantEnd(base, g.Days), Valid: true}
	if !had {
		slot, err := q.TakeFreeSlot(ctx)
		if err != nil {
			return db.User{}, err
		}
		u, err := q.CreateUser(ctx, db.CreateUserParams{
			Name: subscriptionName(ctx, q, g.ReferrerTgID), Contact: "", Note: referralNote, Tags: "[]",
			TariffID: sql.NullInt64{Int64: t.ID, Valid: true}, TrafficLimit: t.TrafficLimit, DeviceLimit: t.DeviceLimit,
			SpeedLimit: t.SpeedLimit, ResetStrategy: t.ResetStrategy, PeriodDays: 30, PeriodStart: base,
			ExpiresAt: expires, BillingDay: t.BillingDay, SlotID: sql.NullInt64{Int64: slot.ID, Valid: true},
			CreatedAt: now.Unix(), UpdatedAt: now.Unix(), Source: UserFromReferral,
		})
		if err != nil {
			return db.User{}, err
		}
		if err := q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: g.ReferrerTgID, CreatedAt: now.Unix()}); err != nil {
			return db.User{}, err
		}
		if err := q.SetTgCurrent(ctx, db.SetTgCurrentParams{Current: u.ID, TgID: g.ReferrerTgID}); err != nil {
			return db.User{}, err
		}
		if t.DeviceMode != "" {
			if err := q.SetUserDeviceMode(ctx, db.SetUserDeviceModeParams{DeviceMode: t.DeviceMode, UpdatedAt: now.Unix(), ID: u.ID}); err != nil {
				return db.User{}, err
			}
			u.DeviceMode = t.DeviceMode
		}
		return u, nil
	}
	// The reward runs the tariff's limits; the traffic counters are the referrer's own and
	// are not touched — the reward is a different tariff for a while, not a new quota. The
	// subscription's own name, contact, note, tags, inbounds and credentials stay too.
	//
	// The traffic period follows the tariff that comes with the reward: a tariff that resets
	// every period starts its period here, as a purchase of that tariff does, so the granted
	// quota is a full one. A tariff that resets by month or not at all keeps the period the
	// referrer had (and the grant row restores it either way).
	periodStart := before.PeriodStart
	if t.ResetStrategy == "period" {
		periodStart = base
	}
	u, err := q.UpdateUser(ctx, db.UpdateUserParams{
		Name: before.Name, Contact: before.Contact, Note: before.Note, Tags: before.Tags, Status: "active",
		TariffID: sql.NullInt64{Int64: t.ID, Valid: true}, TrafficLimit: t.TrafficLimit, DeviceLimit: t.DeviceLimit,
		SpeedLimit: t.SpeedLimit, ResetStrategy: t.ResetStrategy, PeriodDays: before.PeriodDays, PeriodStart: periodStart,
		ExpiresAt: expires, Inbounds: before.Inbounds, BillingDay: t.BillingDay, UpdatedAt: now.Unix(), ID: before.ID,
	})
	if err != nil {
		return db.User{}, err
	}
	if t.DeviceMode != "" && t.DeviceMode != before.DeviceMode {
		if err := q.SetUserDeviceMode(ctx, db.SetUserDeviceModeParams{DeviceMode: t.DeviceMode, UpdatedAt: now.Unix(), ID: u.ID}); err != nil {
			return db.User{}, err
		}
		u.DeviceMode = t.DeviceMode
	}
	return u, nil
}

// endGrant finishes a reward whose days are over and gives the referrer his own
// subscription back exactly as it was: an active trial comes back with the days it had
// left (its clock froze), an expired one comes back expired.
func (s *Service) endGrant(ctx context.Context, q *db.Queries, id int64, now time.Time) error {
	g, err := q.GetReferralRewardGrant(ctx, id)
	if err != nil {
		return err
	}
	n, err := q.FinishReferralRewardGrant(ctx, db.FinishReferralRewardGrantParams{UpdatedAt: now.Unix(), ID: id})
	if err != nil || n == 0 {
		return err
	}
	return s.restore(ctx, q, g, now)
}

// restore puts back the subscription a grant suspended, from the copy on the grant row.
// Beyond the tariff and the expiry it puts back the whole subscription definition — the
// limits, the reset strategy, the billing day, the device mode and the traffic period
// (period_days/period_start), so the referrer's traffic counter is not silently restarted
// by a reward he was given.
//
// A referrer who had no subscription keeps the one the reward made: there is nothing of his
// to go back to, and the days he was promised are his.
func (s *Service) restore(ctx context.Context, q *db.Queries, g db.ReferralRewardGrant, now time.Time) error {
	if !g.BeforeUserID.Valid {
		if s.Changed != nil {
			s.Changed()
		}
		return nil
	}
	u, err := q.GetUser(ctx, g.BeforeUserID.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // the subscription is gone since: nothing to put back
	}
	if err != nil {
		return err
	}
	made := g.BeforeStatus == ""
	if made {
		// The grant made this subscription rather than suspending one: the before_* columns
		// name the row it created, and their empty status says so (users.status is never
		// empty, so a suspended subscription always carries the status it had). The reward's
		// days are what it has and it expires on its own; turning it off here would take
		// away the very days that were owed.
		if s.Changed != nil {
			s.Changed()
		}
		return nil
	}
	// The state saved on the row is what the referrer had; his expiry is restored verbatim,
	// past or future, so an expired subscription lapses again and a trial keeps the days it
	// had left.
	//
	// What the reward itself set is the marker: the subscription's tariff is the granted one
	// AND its expiry is the granted end. Anything else means somebody changed the
	// subscription since (the admin, a purchase in the bot, a package) and the reward must
	// not undo that — the reward gives back only what it took.
	if u.TariffID != g.TariffID || u.ExpiresAt != g.EndsAt {
		if s.Changed != nil {
			s.Changed()
		}
		return nil
	}
	par := db.UpdateUserParams{
		Name: u.Name, Contact: u.Contact, Note: u.Note, Tags: u.Tags, Status: g.BeforeStatus,
		TariffID: g.BeforeTariffID, TrafficLimit: g.BeforeTrafficLimit, DeviceLimit: g.BeforeDeviceLimit,
		SpeedLimit: g.BeforeSpeedLimit, ResetStrategy: g.BeforeResetStrategy,
		PeriodDays: g.BeforePeriodDays, PeriodStart: g.BeforePeriodStart,
		ExpiresAt: g.BeforeExpiresAt, Inbounds: u.Inbounds, BillingDay: g.BeforeBillingDay,
		UpdatedAt: now.Unix(), ID: u.ID,
	}
	if _, err := q.UpdateUser(ctx, par); err != nil {
		return err
	}
	if g.BeforeDeviceMode != "" {
		if err := q.SetUserDeviceMode(ctx, db.SetUserDeviceModeParams{DeviceMode: g.BeforeDeviceMode, UpdatedAt: now.Unix(), ID: u.ID}); err != nil {
			return err
		}
	}
	if s.Changed != nil {
		s.Changed()
	}
	return nil
}

// revertGrants takes the days rewards of a refunded payment out of the queue, on the
// caller's transaction: a reward that is waiting is dropped, and one that is running is
// stopped — the referrer's own subscription comes back at once — after which the streak
// closes up so the rewards behind it run back to back as if it had never been there.
//
// Idempotent: a grant already reverted is left as it is, so the refund, the reconcile pass
// and a second call for the same refund may all ask.
func (s *Service) revertGrants(ctx context.Context, q *db.Queries, paymentID int64, now int64) error {
	rows, err := q.ListReferralRewardGrantsByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: paymentID != 0})
	if err != nil {
		return err
	}
	for _, g := range rows {
		if g.RevertedAt.Valid {
			continue
		}
		n, err := q.RevertReferralRewardGrant(ctx, db.RevertReferralRewardGrantParams{
			RevertedAt: sql.NullInt64{Int64: now, Valid: true}, ID: g.ID,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		if g.Status == GrantRunning {
			// The reward's days are cut short here: the referrer gets his own subscription
			// back. What was queued behind it does not start on this transaction — TakeBack
			// runs inside the refund's — and the reconcile pass picks it up in a minute, as
			// it picks up everything else the queue does. Until then a reward is waiting,
			// which is where it already was.
			if err := s.restore(ctx, q, g, time.Unix(now, 0)); err != nil {
				return err
			}
		}
	}
	return nil
}

// subscriptionName is what a subscription the programme makes for a referrer is called:
// the account's Telegram name, as a purchase in the bot names its own.
func subscriptionName(ctx context.Context, q *db.Queries, tgID int64) string {
	if c, err := q.GetTgChat(ctx, tgID); err == nil {
		if c.Username != "" {
			return "@" + c.Username
		}
		if name := strings.TrimSpace(c.FirstName); name != "" {
			return name
		}
	}
	return fmt.Sprintf("tg %d", tgID)
}
