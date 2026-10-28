// Package automation runs the admin's event-driven rules: a feed event (a trial, an
// expiring subscription, a payment, a node going down) is matched against the enabled
// rules, optional conditions are checked against the subscriber, and Telegram actions are
// performed. Every run is recorded, so a rule that did nothing says why.
package automation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/events"
	"mikan/internal/panel/events/feed"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/tgapi"
)

const (
	TriggerTrialStarted  = events.TrialStarted
	TriggerUserCreated   = events.UserCreated
	TriggerUserGranted   = events.UserGranted
	TriggerUserExpiring  = events.UserExpiring
	TriggerUserExpired   = events.UserExpired
	TriggerUserInactive  = events.UserInactive
	TriggerTraffic90     = events.Traffic90
	TriggerTraffic100    = events.Traffic100
	TriggerPaymentPaid   = events.PaymentPaid
	TriggerPaymentFailed = events.PaymentFailed
	TriggerPaymentRefund = events.PaymentRefunded
	TriggerTrialNotGiven = events.TrialNotGiven
	TriggerDeviceBound   = events.DeviceBound
	TriggerDeviceUnbound = events.DeviceUnbound
	TriggerBalanceTopup  = events.BalanceTopup
	TriggerDiscountEnd   = events.DiscountEnding
	TriggerNodeDown      = events.NodeDown
	TriggerNodeUp        = events.NodeUp
)

// Triggers are the events a rule may listen to.
var Triggers = []string{
	TriggerTrialStarted, TriggerUserCreated, TriggerUserGranted, TriggerUserExpiring, TriggerUserExpired, TriggerUserInactive,
	TriggerTraffic90, TriggerTraffic100, TriggerPaymentPaid, TriggerPaymentFailed, TriggerPaymentRefund, TriggerTrialNotGiven,
	TriggerDeviceBound, TriggerDeviceUnbound, TriggerBalanceTopup, TriggerDiscountEnd, TriggerNodeDown, TriggerNodeUp,
}

// inactiveAfter is how long a subscription may go without a connection before the
// win-back event is emitted. The event is emitted at most once a month per subscription.
const inactiveAfter = 7 * 24 * time.Hour

// Action types. Only Telegram notifications, the panel's own grants, promo codes and
// generated discounts are here: an automation cannot call an arbitrary URL or run a command.
const (
	ActionNotifyAdmin = "notify_admin"
	ActionNotifyUser  = "notify_user"
	ActionExtend      = "extend"
	ActionGrant       = "grant_traffic"
	ActionPromo       = "promo"
	ActionDiscount    = "discount"
)

var ActionTypes = []string{ActionNotifyAdmin, ActionNotifyUser, ActionExtend, ActionGrant, ActionPromo, ActionDiscount}

// Conditions narrow a trigger to the subscribers it is about. Empty lists and nil limits
// mean "any".
type Conditions struct {
	TariffIDs      []int64  `json:"tariff_ids,omitempty"`
	States         []string `json:"states,omitempty"`
	Sources        []string `json:"sources,omitempty"`
	DaysLeftMin    *int     `json:"days_left_min,omitempty"`
	DaysLeftMax    *int     `json:"days_left_max,omitempty"`
	TrafficUsedMin *float64 `json:"traffic_used_min,omitempty"`
	TrafficUsedMax *float64 `json:"traffic_used_max,omitempty"`
}

// Action is one step of a rule. Text is expanded with the event's facts; the grant fields
// follow domain.GrantInput, and Percent/DiscountDays describe a generated personal code.
type Action struct {
	Type         string `json:"type"`
	Text         string `json:"text,omitempty"`
	Days         int64  `json:"days,omitempty"`
	Bytes        int64  `json:"bytes,omitempty"`
	Lifetime     string `json:"lifetime,omitempty"`
	GrantDays    int64  `json:"grant_days,omitempty"`
	Code         string `json:"code,omitempty"`
	Percent      int64  `json:"percent,omitempty"`
	DiscountDays int64  `json:"discount_days,omitempty"`
}

// Bot is the Telegram side the rules use: the running outbox and the admin's chat.
type Bot interface {
	Outbox() *tgapi.Outbox
	InfrastructureAdminChat(ctx context.Context) (int64, bool, error)
}

type Service struct {
	st     *store.Store
	users  *domain.Users
	bot    Bot
	promos *promo.Service
	now    func() time.Time
	log    *slog.Logger
	mu     sync.Mutex
}

func New(st *store.Store, users *domain.Users, bot Bot, promos *promo.Service, now func() time.Time, log *slog.Logger) *Service {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{st: st, users: users, bot: bot, promos: promos, now: now, log: log}
}

// Run processes new events every few seconds until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.Process(ctx)
	s.emitInactive(ctx)
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	slow := time.NewTicker(time.Hour)
	defer slow.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Process(ctx); err != nil {
				s.log.Warn("automation", "err", err)
			}
		case <-slow.C:
			s.emitInactive(ctx)
		}
	}
}

// emitInactive tells the feed about subscriptions that have not connected for a week. The
// event is written once a month per subscription, so a win-back rule does not repeat its
// message every hour. The scan is skipped when no enabled rule listens to it.
func (s *Service) emitInactive(ctx context.Context) {
	rules, err := s.st.Q.ListEnabledAutomationRules(ctx)
	if err != nil {
		s.log.Warn("automation: inactive rules", "err", err)
		return
	}
	if !slices.ContainsFunc(rules, func(r db.AutomationRule) bool { return r.Trigger == TriggerUserInactive }) {
		return
	}
	now := s.now()
	rows, err := s.st.Q.ListInactiveUsers(ctx, db.ListInactiveUsersParams{
		Now: now.Unix(), Before: now.Add(-inactiveAfter).Unix(), Lim: 500,
	})
	if err != nil {
		s.log.Warn("automation: inactive users", "err", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	month := now.UTC().Format("2006-01")
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		for _, u := range rows {
			days := int((now.Unix() - u.LastSeen) / 86400)
			dedupe := "user.inactive:" + strconv.FormatInt(u.ID, 10) + ":" + month
			if err := feed.Emit(ctx, q, now, events.UserInactive, u.ID, u.TgID, events.Inactive{Days: days, LastSeen: u.LastSeen}, dedupe); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.log.Warn("automation: inactive events", "err", err)
	}
}

// Process reads the feed after the automation cursor and runs every matching rule once.
func (s *Service) Process(ctx context.Context) error {
	if !s.mu.TryLock() {
		return nil
	}
	defer s.mu.Unlock()
	cursor, err := s.st.Q.GetAddonCursor(ctx, "automation")
	if err != nil {
		return err
	}
	evs, err := s.st.Q.ListAddonEvents(ctx, db.ListAddonEventsParams{After: cursor, Types: nil, Lim: 200})
	if err != nil {
		return err
	}
	if len(evs) == 0 {
		return nil
	}
	rules, err := s.st.Q.ListEnabledAutomationRules(ctx)
	if err != nil {
		return err
	}
	for _, ev := range evs {
		for _, rule := range rules {
			if rule.Trigger != ev.Type {
				continue
			}
			if err := s.run(ctx, rule, ev); err != nil {
				s.log.Warn("automation rule", "rule", rule.ID, "event", ev.ID, "err", err)
			}
		}
		if err := s.st.Q.AckAddonEvents(ctx, db.AckAddonEventsParams{AddonID: "automation", LastID: ev.ID, UpdatedAt: s.now().Unix()}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) run(ctx context.Context, rule db.AutomationRule, ev db.AddonEvent) error {
	var cond Conditions
	if err := json.Unmarshal([]byte(rule.Conditions), &cond); err != nil {
		return s.record(ctx, rule, ev, "failed", "bad_conditions: "+err.Error())
	}
	var acts []Action
	if err := json.Unmarshal([]byte(rule.Actions), &acts); err != nil {
		return s.record(ctx, rule, ev, "failed", "bad_actions: "+err.Error())
	}
	if len(acts) == 0 {
		return s.record(ctx, rule, ev, "skipped", "no_actions")
	}
	var u db.User
	hasUser := ev.UserID.Valid && ev.UserID.Int64 != 0
	if hasUser {
		var err error
		u, err = s.users.Get(ctx, ev.UserID.Int64)
		if err != nil {
			return s.record(ctx, rule, ev, "skipped", "user_not_found")
		}
		if !s.matches(ctx, cond, u) {
			return s.record(ctx, rule, ev, "skipped", "conditions")
		}
		if rule.CooldownSeconds > 0 {
			last, err := s.st.Q.LastAutomationRunForUser(ctx, db.LastAutomationRunForUserParams{RuleID: rule.ID, UserID: ev.UserID})
			if err != nil {
				return err
			}
			if last > 0 && s.now().Unix()-last < rule.CooldownSeconds {
				return s.record(ctx, rule, ev, "skipped", "cooldown")
			}
		}
	} else if rule.CooldownSeconds > 0 && rule.LastRunAt.Valid && s.now().Unix()-rule.LastRunAt.Int64 < rule.CooldownSeconds {
		return s.record(ctx, rule, ev, "skipped", "cooldown")
	}

	// Reserve the event before doing anything: if the cursor is retried (a restart, a
	// concurrent worker) the unique (rule, event) row makes the second pass a no-op. The
	// reservation itself is the journal entry; the result is written over it afterwards.
	reserved, err := s.st.Q.ReserveAutomationRun(ctx, db.ReserveAutomationRunParams{
		RuleID: rule.ID, EventID: ev.ID, EventType: ev.Type, UserID: ev.UserID, TgID: ev.TgID,
		DryRun: rule.DryRun, CreatedAt: s.now().Unix(),
	})
	if err != nil {
		return err
	}
	if reserved == 0 {
		return nil
	}

	var details []string
	failed := false
	for _, a := range acts {
		detail, err := s.act(ctx, rule, ev, u, hasUser, a)
		if err != nil {
			failed = true
			details = append(details, a.Type+": "+err.Error())
			continue
		}
		if detail != "" {
			details = append(details, a.Type+": "+detail)
		}
	}
	status := "ok"
	if failed {
		status = "failed"
	}
	text := strings.Join(details, "; ")
	if text == "" {
		text = "ok"
	}
	if _, err := s.st.Q.FinishAutomationRun(ctx, db.FinishAutomationRunParams{
		RuleID: rule.ID, EventID: ev.ID, Status: status, Detail: text,
	}); err != nil {
		return err
	}
	if status == "ok" {
		if err := s.st.Q.MarkAutomationRuleRun(ctx, db.MarkAutomationRuleRunParams{ID: rule.ID, LastRunAt: nullInt(s.now().Unix())}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) matches(ctx context.Context, c Conditions, u db.User) bool {
	if len(c.TariffIDs) > 0 && (!u.TariffID.Valid || !slices.Contains(c.TariffIDs, u.TariffID.Int64)) {
		return false
	}
	if len(c.Sources) > 0 && !slices.Contains(c.Sources, u.Source) {
		return false
	}
	grants, err := domain.UserGrantsLeft(ctx, s.st.Q, u.ID, s.now())
	if err != nil {
		return false
	}
	state := domain.State(u, grants.Main(u.ID), s.now())
	if len(c.States) > 0 && !slices.Contains(c.States, state) {
		return false
	}
	if c.DaysLeftMin != nil || c.DaysLeftMax != nil {
		if !u.ExpiresAt.Valid {
			return false
		}
		days := int((u.ExpiresAt.Int64 - s.now().Unix()) / 86400)
		if c.DaysLeftMin != nil && days < *c.DaysLeftMin {
			return false
		}
		if c.DaysLeftMax != nil && days > *c.DaysLeftMax {
			return false
		}
	}
	if c.TrafficUsedMin != nil || c.TrafficUsedMax != nil {
		var used float64
		if u.TrafficLimit.Valid && u.TrafficLimit.Int64 > 0 {
			used = float64(u.UsedUp+u.UsedDown) / float64(u.TrafficLimit.Int64) * 100
		}
		if c.TrafficUsedMin != nil && used < *c.TrafficUsedMin {
			return false
		}
		if c.TrafficUsedMax != nil && used > *c.TrafficUsedMax {
			return false
		}
	}
	return true
}

func (s *Service) act(ctx context.Context, rule db.AutomationRule, ev db.AddonEvent, u db.User, hasUser bool, a Action) (string, error) {
	if rule.DryRun != 0 {
		return "dry-run", nil
	}
	switch a.Type {
	case ActionNotifyAdmin:
		return "", s.notifyAdmin(ctx, s.expand(a.Text, ev, u, hasUser))
	case ActionNotifyUser:
		if !hasUser && (!ev.TgID.Valid || ev.TgID.Int64 == 0) {
			return "", errors.New("no_telegram")
		}
		return "", s.notifyUser(ctx, ev, u, s.expand(a.Text, ev, u, hasUser))
	case ActionExtend:
		if !hasUser {
			return "", errors.New("no_user")
		}
		if a.Days < 1 || a.Days > domain.MaxExtendDays {
			return "", errors.New("bad_days")
		}
		if _, err := s.users.Extend(ctx, u.ID, a.Days); err != nil {
			return "", err
		}
		return fmt.Sprintf("+%d days", a.Days), nil
	case ActionGrant:
		if !hasUser {
			return "", errors.New("no_user")
		}
		lifetime := a.Lifetime
		if lifetime == "" {
			lifetime = domain.LifetimeUsed
		}
		if _, err := s.users.Grant(ctx, u.ID, domain.GrantInput{Bytes: a.Bytes, Lifetime: lifetime, Days: a.GrantDays, Note: "automation: " + rule.Name}); err != nil {
			return "", err
		}
		return "granted", nil
	case ActionPromo:
		if a.Code == "" {
			return "", errors.New("no_code")
		}
		text := s.expand(a.Text, ev, u, hasUser)
		if text == "" {
			text = "Промокод: " + a.Code
		} else if !strings.Contains(text, a.Code) {
			text += "\n" + a.Code
		}
		if !hasUser && (!ev.TgID.Valid || ev.TgID.Int64 == 0) {
			return "", s.notifyAdmin(ctx, text)
		}
		return "", s.notifyUser(ctx, ev, u, text)
	case ActionDiscount:
		if !hasUser {
			return "", errors.New("no_user")
		}
		if s.promos == nil {
			return "", errors.New("promo_unavailable")
		}
		p, err := s.promos.CreatePersonalDiscount(ctx, a.Percent, a.DiscountDays, rule.Name)
		if err != nil {
			return "", err
		}
		text := strings.ReplaceAll(s.expand(a.Text, ev, u, hasUser), "{code}", p.Code)
		if text == "" {
			text = fmt.Sprintf("Скидка %d%% на подписку: %s", a.Percent, p.Code)
		} else if !strings.Contains(text, p.Code) {
			text += "\n" + p.Code
		}
		if !ev.TgID.Valid || ev.TgID.Int64 == 0 {
			if err := s.notifyAdmin(ctx, text); err != nil {
				return "", err
			}
		} else if err := s.notifyUser(ctx, ev, u, text); err != nil {
			return "", err
		}
		return "code=" + p.Code, nil
	default:
		return "", errors.New("bad_action")
	}
}

func (s *Service) notifyAdmin(ctx context.Context, text string) error {
	if s.bot == nil || text == "" {
		return errors.New("bot_unavailable")
	}
	chat, ok, err := s.bot.InfrastructureAdminChat(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("no_admin_chat")
	}
	out := s.bot.Outbox()
	if out == nil {
		return errors.New("bot_unavailable")
	}
	out.Notice(chat, func(ctx context.Context, c *tgapi.Client) error {
		_, err := c.Send(ctx, chat, text, nil, false)
		return err
	}, nil)
	return nil
}

func (s *Service) notifyUser(ctx context.Context, ev db.AddonEvent, u db.User, text string) error {
	if s.bot == nil || text == "" {
		return errors.New("bot_unavailable")
	}
	chat := ev.TgID.Int64
	if chat == 0 {
		return errors.New("no_telegram")
	}
	out := s.bot.Outbox()
	if out == nil {
		return errors.New("bot_unavailable")
	}
	out.Notice(chat, func(ctx context.Context, c *tgapi.Client) error {
		_, err := c.Send(ctx, chat, text, nil, false)
		return err
	}, nil)
	return nil
}

func (s *Service) expand(text string, ev db.AddonEvent, u db.User, hasUser bool) string {
	if text == "" {
		return ""
	}
	name := "клиент"
	if hasUser && u.Name != "" {
		name = u.Name
	}
	days := ""
	if hasUser && u.ExpiresAt.Valid {
		days = strconv.FormatInt((u.ExpiresAt.Int64-s.now().Unix())/86400, 10)
	}
	id := ev.UserID.Int64
	if id == 0 && ev.TgID.Valid {
		id = ev.TgID.Int64
	}
	repl := strings.NewReplacer(
		"{name}", name, "{id}", strconv.FormatInt(id, 10), "{event}", ev.Type,
		"{days_left}", days, "{tariff}", tariffName(u),
	)
	return repl.Replace(text)
}

func tariffName(u db.User) string {
	if u.TariffID.Valid {
		return strconv.FormatInt(u.TariffID.Int64, 10)
	}
	return ""
}

func (s *Service) record(ctx context.Context, rule db.AutomationRule, ev db.AddonEvent, status, detail string) error {
	n, err := s.st.Q.AddAutomationRun(ctx, db.AddAutomationRunParams{
		RuleID: rule.ID, EventID: ev.ID, EventType: ev.Type, UserID: ev.UserID, TgID: ev.TgID,
		Status: status, DryRun: rule.DryRun, Detail: detail, CreatedAt: s.now().Unix(),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	return nil
}

func nullInt(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
