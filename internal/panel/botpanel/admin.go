package botpanel

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"mikan/internal/panel/model"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgbot"
)

// The bot's admin side: who may run it (admin_users and the owner setting), and what the
// support does to one subscription — the card, the days, the sessions, the block — and the
// accounts whose trial went unused.

// KeyAdminOwner is the Telegram account that owns the bot: the superadmin before any was
// named in admin_users.
const KeyAdminOwner = "admin_owner"

// errNoAdmin refuses a call whose actor holds no role for it.
var errNoAdmin = errors.New("no_admin")

// adminRank is how far the acting account may go: 2 superadmin, 1 support, 0 none (the
// tech role holds no admin-data access of its own).
func (l *Local) adminRank(ctx context.Context, by int64) int {
	role, err := l.AdminRole(ctx, by)
	if err != nil {
		return 0
	}
	switch role {
	case "superadmin":
		return 2
	case "support":
		return 1
	}
	return 0
}

// AdminRole is tgID's role in the bot's admin side: "" when it has none.
func (l *Local) AdminRole(ctx context.Context, tgID int64) (string, error) {
	if tgID == 0 {
		return "", nil
	}
	if owner, _, err := settings.Get[int64](ctx, l.Settings, KeyAdminOwner); err == nil && owner == tgID {
		return "superadmin", nil
	}
	a, err := l.Store.Q.GetAdminUser(ctx, tgID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return a.Role, err
}

// AdminFindUser is the subscriber a card is asked about: an id, a payment's number, or a
// @username (a first name too, the way the chat calls it).
func (l *Local) AdminFindUser(ctx context.Context, by int64, query string) (model.User, bool, error) {
	if l.adminRank(ctx, by) < 1 {
		return model.User{}, false, errNoAdmin
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return model.User{}, false, nil
	}
	if id, err := strconv.ParseInt(strings.TrimPrefix(q, "@"), 10, 64); err == nil {
		if u, err := l.Store.Q.GetUser(ctx, id); err == nil {
			return model.User(u), true, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return model.User{}, false, err
		}
		if u, err := l.Store.Q.FindUserByPayment(ctx, id); err == nil {
			return model.User(u), true, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return model.User{}, false, err
		}
	}
	name := strings.TrimPrefix(q, "@")
	if name == "" {
		return model.User{}, false, nil
	}
	u, err := l.Store.Q.FindUserByTgUsername(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, false, nil
	}
	return model.User(u), err == nil, err
}

// AdminAddDays is the support's gift: days after the end in force (from now when it has
// passed). Unlimited stays unlimited.
func (l *Local) AdminAddDays(ctx context.Context, by, userID int64, days int) (model.User, error) {
	if l.adminRank(ctx, by) < 1 {
		return model.User{}, errNoAdmin
	}
	if days < 1 {
		days = 1
	}
	if days > 3650 {
		days = 3650
	}
	u, err := l.Store.Q.ExtendUser(ctx, db.ExtendUserParams{ID: userID, Days: int64(days), Now: l.Now().Unix()})
	return model.User(u), err
}

// AdminSetDisabled blocks (the user's apps get nothing) or lets the subscription back.
func (l *Local) AdminSetDisabled(ctx context.Context, by, userID int64, disabled bool) (model.User, error) {
	if l.adminRank(ctx, by) < 1 {
		return model.User{}, errNoAdmin
	}
	status := "active"
	if disabled {
		status = "disabled"
	}
	u, err := l.Store.Q.SetUserDisabled(ctx, db.SetUserDisabledParams{ID: userID, Status: status, Now: l.Now().Unix()})
	return model.User(u), err
}

// AdminResetSessions takes every device off the subscription and issues a new link: the old
// one stops working, the apps must be set up again.
func (l *Local) AdminResetSessions(ctx context.Context, by, userID int64) error {
	if l.adminRank(ctx, by) < 1 {
		return errNoAdmin
	}
	if _, err := l.Store.Q.DeleteUserBoundDevices(ctx, userID); err != nil {
		return err
	}
	if l.Users != nil {
		if _, err := l.Users.ReissueByOwner(ctx, userID, "admin"); err != nil {
			return err
		}
	}
	return nil
}

// AdminSleepingTrials lists the accounts that took a trial and could not connect.
func (l *Local) AdminSleepingTrials(ctx context.Context, by int64) ([]tgbot.SleepingTrial, error) {
	if l.adminRank(ctx, by) < 1 {
		return nil, errNoAdmin
	}
	rows, err := l.Store.Q.SleepingTrials(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tgbot.SleepingTrial, 0, len(rows))
	for _, r := range rows {
		name := r.FirstName
		if name == "" {
			name = r.Username
		}
		if name == "" {
			name = strconv.FormatInt(r.TgID, 10)
		}
		out = append(out, tgbot.SleepingTrial{TgID: r.TgID, UserID: r.UserID.Int64, Username: r.Username, Name: name})
	}
	return out, nil
}

// AdminSetRole names an account of the bot's admin side (or changes its role): the owner's
// call — a superadmin only, checked by the screens.
func (l *Local) AdminSetRole(ctx context.Context, tgID int64, role string, by int64) error {
	if l.adminRank(ctx, by) < 2 {
		return errNoAdmin
	}
	if role != "superadmin" && role != "support" && role != "tech" {
		return errors.New("bad role")
	}
	username := ""
	if c, err := l.Store.Q.GetTgChat(ctx, tgID); err == nil {
		username = c.Username
	}
	_, err := l.Store.Q.UpsertAdminUser(ctx, db.UpsertAdminUserParams{TgID: tgID, Username: username, Role: role,
		CreatedAt: l.Now().Unix(), CreatedBy: sql.NullInt64{Int64: by, Valid: by != 0}})
	return err
}

// AdminDelRole takes an account off the list.
func (l *Local) AdminDelRole(ctx context.Context, tgID, by int64) error {
	if l.adminRank(ctx, by) < 2 {
		return errNoAdmin
	}
	_, err := l.Store.Q.DeleteAdminUser(ctx, tgID)
	return err
}

// Admins is every account that may run the bot's admin side: the owner and the named
// superadmins. Not gated: the bot asks for who to tell itself.
func (l *Local) Admins(ctx context.Context) ([]int64, error) {
	owner, _, err := settings.Get[int64](ctx, l.Settings, KeyAdminOwner)
	if err != nil {
		return nil, err
	}
	rows, err := l.Store.Q.ListAdminUsers(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	add := func(id int64) {
		if id != 0 && !seen[id] {
			seen[id] = true
		}
	}
	add(owner)
	for _, r := range rows {
		if r.Role == "superadmin" {
			add(r.TgID)
		}
	}
	out := make([]int64, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out, nil
}

// AdminPayments is the recent payments as a superadmin reads them: the Payments page of
// the panel, in the bot. before is the last id seen (0 from the newest).
func (l *Local) AdminPayments(ctx context.Context, by, before, limit int64) ([]tgbot.PaymentRow, error) {
	if l.adminRank(ctx, by) < 2 {
		return nil, errNoAdmin
	}
	if before == 0 {
		before = 1 << 62
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	rows, err := l.Store.Q.ListPayments(ctx, db.ListPaymentsParams{BeforeID: before, Lim: limit})
	if err != nil {
		return nil, err
	}
	out := make([]tgbot.PaymentRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, tgbot.PaymentRow{
			ID: r.Payment.ID, Kind: r.Payment.Kind, Provider: r.Payment.Provider,
			Amount: r.Payment.Amount, Currency: r.Payment.Currency, TariffName: r.Payment.TariffName,
			TermDays: r.Payment.TermDays.Int64, Status: r.Payment.Status, PaidAt: r.Payment.PaidAt.Int64,
			UserName: r.UserName, TgUsername: r.TgUsername,
		})
	}
	return out, nil
}

// AdminList is the bot's admin side as the superadmin sees it.
func (l *Local) AdminList(ctx context.Context, by int64) ([]tgbot.AdminUser, error) {
	if l.adminRank(ctx, by) < 2 {
		return nil, errNoAdmin
	}
	rows, err := l.Store.Q.ListAdminUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tgbot.AdminUser, 0, len(rows))
	for _, r := range rows {
		out = append(out, tgbot.AdminUser{TgID: r.TgID, Username: r.Username, Role: r.Role})
	}
	return out, nil
}
