package domain

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"mikan/internal/panel/audit"
	"mikan/internal/panel/events"
	"mikan/internal/panel/events/feed"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// The removal of inactive subscriptions: by the admin's RemovalRules, a subscription goes
// so many days after its term ended and after its last connection, both. Never one that
// still holds something of its subscriber's (Keep*). The admin sees the list before turning
// it on (Inactive); each removal has its line in the journal and its event.
//
// The rules live in the settings table under KeyLifecycle. Upstream keeps them beside the
// carry-over of leftover traffic; this fork stores only the removal. A waiting term and a
// next tariff, which upstream also keeps out of the removal, are not part of this panel.

// KeyLifecycle is the setting Lifecycle is kept under; unset: DefaultLifecycle.
const KeyLifecycle = "user_lifecycle"

// Lifecycle is what the admin decides about removing inactive subscriptions.
type Lifecycle struct {
	Removal RemovalRules `json:"removal"`
}

// RemovalRules: the panel removes a subscription Days after its term ended and after its last
// connection, both, unless something of its subscriber's is still in it (InactiveUser.Keep).
// Off by default: a removal cannot be undone.
type RemovalRules struct {
	On   bool `json:"on" doc:"Удалять неактивные подписки сами"`
	Days int  `json:"days" minimum:"7" maximum:"3650" doc:"Через сколько дней после конца срока и последнего подключения"`
}

// DefaultLifecycle removes nothing; turned on, a subscription goes a month after its end.
var DefaultLifecycle = Lifecycle{Removal: RemovalRules{Days: 30}}

// The bounds of the rules. A week at least before a removal: a subscriber who pays a few
// days late finds the subscription and its link where they were.
const (
	MinRemovalDays = 7
	MaxRemovalDays = 3650
)

// Validate checks the rules the admin sends; the error names the field.
func (l Lifecycle) Validate() error {
	if l.Removal.Days < MinRemovalDays || l.Removal.Days > MaxRemovalDays {
		return fieldErr("removal.days", "bad_removal_days")
	}
	return nil
}

// LoadLifecycle reads the admin's rules; DefaultLifecycle until they set any. Rules stored
// broken fall back to the default, which removes nothing: only a database that cannot be
// read is an error.
func LoadLifecycle(ctx context.Context, set *settings.Settings) (Lifecycle, error) {
	raw, found, err := set.Raw(ctx, KeyLifecycle)
	if err != nil || !found {
		return DefaultLifecycle, err
	}
	l := DefaultLifecycle
	if json.Unmarshal([]byte(raw), &l) != nil || l.Validate() != nil {
		return DefaultLifecycle, nil
	}
	return l, nil
}

// Why an inactive subscription stays (InactiveUser.Keep).
const (
	KeepBalance = "balance" // money on the balance of the account linked to it
	KeepPayment = "payment" // a payment of it not applied yet, or a top-up of the account waiting for the admin
	KeepGrant   = "grant"   // traffic packages still running
)

// InactiveUser is a subscription the removal rules reach: removed by the next round, or kept
// for Keep ("": not kept).
type InactiveUser struct {
	ID        int64
	Name      string
	TariffID  sql.NullInt64
	ExpiresAt time.Time
	OnlineAt  sql.NullInt64
	TgID      int64 // the account linked to it; 0: none
	Keep      string
}

func inactiveOf(r db.ListRemovableUsersRow) InactiveUser {
	u := InactiveUser{ID: r.ID, Name: r.Name, TariffID: r.TariffID, ExpiresAt: store.Time(r.ExpiresAt), OnlineAt: r.OnlineAt, TgID: r.TgID}
	switch {
	case r.HasBalance:
		u.Keep = KeepBalance
	case r.Paying || r.ToppingUp:
		u.Keep = KeepPayment
	case r.Granted:
		u.Keep = KeepGrant
	}
	return u
}

// removalBefore is the moment both the end of a term and the last connection must be at or
// before for the rules to reach the subscription now.
func removalBefore(rules RemovalRules, now time.Time) int64 {
	return now.Add(-time.Duration(rules.Days) * 24 * time.Hour).Unix()
}

// Inactive lists the subscriptions the rules reach now, the longest over first: those the
// next round removes and those it keeps, alike, whether the rules are on or not, so that the
// admin sees who would go before letting anyone go.
func (s *Users) Inactive(ctx context.Context, rules RemovalRules) ([]InactiveUser, error) {
	now := s.now()
	rows, err := s.st.Q.ListRemovableUsers(ctx, db.ListRemovableUsersParams{Now: now.Unix(), Before: removalBefore(rules, now)})
	if err != nil {
		return nil, err
	}
	out := make([]InactiveUser, len(rows))
	for i, r := range rows {
		out[i] = inactiveOf(r)
	}
	return out, nil
}

// RemoveInactive removes up to max of the subscriptions the rules reach and nothing keeps,
// the longest over first; none while the rules are off. Each goes in a transaction of its
// own that checks it again under its lock (a payment may have come meanwhile), with its line
// in the journal and its event (events.UserRemoved). It returns how many went.
func (s *Users) RemoveInactive(ctx context.Context, rules RemovalRules, max int) (int, error) {
	if !rules.On || max <= 0 {
		return 0, nil
	}
	now := s.now()
	par := db.ListRemovableUsersParams{Now: now.Unix(), Before: removalBefore(rules, now)}
	rows, err := s.st.Q.ListRemovableUsers(ctx, par)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		if n >= max {
			break
		}
		if inactiveOf(r).Keep != "" {
			continue
		}
		removed := false
		err := s.st.Tx(ctx, func(q *db.Queries) error {
			removed = false
			if _, err := q.LockUserRowsForDelete(ctx, []int64{r.ID}); err != nil {
				return err
			}
			one := par
			one.UserID = r.ID
			again, err := q.ListRemovableUsers(ctx, one)
			if err != nil || len(again) == 0 {
				return err
			}
			u := inactiveOf(again[0])
			if u.Keep != "" {
				return nil
			}
			if err := deleteUsers(ctx, q, []int64{u.ID}, now.Unix()); err != nil {
				return err
			}
			id := strconv.FormatInt(u.ID, 10)
			// The line names the subscription: it is gone, and the journal is where the admin
			// finds who it was.
			details := map[string]any{"name": u.Name, "expired_at": u.ExpiresAt.Unix(), "days": rules.Days}
			if u.OnlineAt.Valid {
				details["online_at"] = u.OnlineAt.Int64
			}
			if err := audit.Write(ctx, q, now, audit.Entry{Action: "user.remove_inactive", TargetType: "user", TargetID: id, Details: details}); err != nil {
				return err
			}
			removed = true
			return feed.Emit(ctx, q, now, events.UserRemoved, u.ID, u.TgID,
				events.Removed{By: events.RemovedInactive, ExpiredAt: u.ExpiresAt.Unix(), OnlineAt: u.OnlineAt.Int64, Days: rules.Days}, "")
		})
		if err != nil {
			return n, err
		}
		if removed {
			n++
		}
	}
	if n > 0 {
		s.hub.Kick()
		s.changes.PoliciesChanged()
	}
	return n, nil
}
