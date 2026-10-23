// Package botpanel is the panel's side of the Telegram bot: Local answers the bot addon's calls
// from the panel's store, settings and billing in the bot's model (served over the Panel API by
// RPCHandler), and Panel tells the addon of the notices that are due and sends the admin's
// broadcasts. The bot's screens are the addon's (tgbot).
package botpanel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/campaigns"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/model"
	"mikan/internal/panel/panelimport"
	"mikan/internal/panel/partners"
	"mikan/internal/panel/referral"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subs"
	"mikan/internal/panel/tgate"
	"mikan/internal/panel/tgbot"
)

// Backend is tgbot.Backend and what only the panel's side of the bot reads: the notices about
// the term and the traffic (which were told, tg_notices) and the broadcasts' targets. The
// addon never asks for these, so tgbot.Remote has none of them and the Panel API opens none:
// a list of every subscriber's account is not the addon's to read.
type Backend interface {
	tgbot.Backend
	// Notices are the subscriptions that may get a notice now.
	Notices(ctx context.Context, now time.Time) ([]db.ListNoticeSubscriptionsRow, error)
	NoticeSent(ctx context.Context, userID int64, kind string, period int64) (bool, error)
	NoticeDelivered(ctx context.Context, userID int64, kind string, period int64, at time.Time) error
	PruneNotices(ctx context.Context, before time.Time) error
	// BroadcastTargets are the accounts a broadcast goes to.
	BroadcastTargets(ctx context.Context) ([]int64, error)
}

// Local is the bot's Backend inside the panel's process: its store, settings, users, devices
// and billing. It is the only part of the bot that touches them; the bot addon reaches it over
// the Panel API (RPCHandler), and the panel's side of the bot (Panel) calls it directly.
type Local struct {
	Store    *store.Store
	Settings *settings.Settings
	// Binder unbinds devices at their subscriber's request.
	Binder *domain.Devices
	// Users reissues a subscription's link at its subscriber's request.
	Users *domain.Users
	// Billing sells tariffs in the menu.
	Billing *billing.Service
	// Partners binds an account to a partner's code.
	Partners *partners.Service
	// Gateway reads the link codes it signed and asks who is in the channel.
	Gateway *tgate.Gateway
	// SubBase is https://host:port/<sub path>, "" while the panel has no public address.
	SubBase func(ctx context.Context) string
	// MiniApp: the panel's certificate is one Telegram apps accept (not self-signed).
	MiniApp func() bool
	// ReferralProgram is the referral programme the screens show (the method Referral
	// reads it); nil: none.
	ReferralProgram *referral.Service
	// Campaigns are the ad campaign tags a tagged /start counts; nil: none.
	Campaigns *campaigns.Service
	Now       func() time.Time
}

var _ Backend = (*Local)(nil)

// nativeToken is the panel's own subscription token: 24 letters and digits.
var nativeToken = regexp.MustCompile(`^[A-Za-z0-9]{24}$`)

// referralHistory is how many rewards the history screen lists.
const referralHistory = 10

func (l *Local) Config(ctx context.Context) tgbot.Config {
	// A setup saved before an option existed gets that option's default. A saved menu
	// replaces the default one whole.
	cfg, lang, saved := tgate.ReadSetup(ctx, l.Settings, func(lang string) tgbot.Config {
		def := tgbot.Default(lang)
		def.Buttons = nil
		return def
	})
	if !saved {
		return tgbot.Default(lang)
	}
	_ = cfg.Validate()
	return cfg
}

// Member is the channel check, for the bot's own channel only: the screens cannot probe
// other chats or reset what the gateway knows about members.
func (l *Local) Member(ctx context.Context, chat string, tgID int64, fresh bool) (bool, error) {
	if ch := l.Gateway.ChannelConfig(ctx).Chat; ch == "" || chat != ch {
		return false, model.ErrNotChannel
	}
	return l.Gateway.Member(ctx, chat, tgID, fresh)
}

func (l *Local) Brand(ctx context.Context) string {
	s, _ := l.Settings.String(ctx, settings.KeyBrand)
	return s
}

func (l *Local) SupportURL(ctx context.Context) string {
	s, _ := l.Settings.String(ctx, settings.KeySupportURL)
	return s
}

func (l *Local) SubURL(ctx context.Context, u model.User) string {
	if base := l.SubBase(ctx); base != "" {
		return base + "/" + u.SubToken
	}
	return ""
}

func (l *Local) MiniAppURL(ctx context.Context) string {
	base := l.SubBase(ctx)
	if base == "" || l.MiniApp == nil || !l.MiniApp() {
		return ""
	}
	return base + "/tg"
}

func (l *Local) Context(ctx context.Context, tgID int64) (tgbot.Account, error) {
	list, err := l.Store.Q.ListTgLinksOf(ctx, tgID)
	if err != nil {
		return tgbot.Account{}, err
	}
	acc := tgbot.Account{Links: model.Each(list, func(u db.User) model.User { return model.User(u) })}
	if ch, err := l.Store.Q.GetTgChat(ctx, tgID); err == nil {
		acc.Chat, acc.HasChat = model.TgChat(ch), true
	}
	return acc, nil
}

func (l *Local) Chat(ctx context.Context, tgID int64) (model.TgChat, error) {
	ch, err := l.Store.Q.GetTgChat(ctx, tgID)
	return model.TgChat(ch), err
}

func (l *Local) SetMenu(ctx context.Context, tgID, msgID int64) error {
	return l.Store.Q.SetTgMenu(ctx, db.SetTgMenuParams{MenuMsgID: msgID, TgID: tgID})
}

func (l *Local) SetCurrent(ctx context.Context, tgID, userID int64) error {
	return l.Store.Q.SetTgCurrent(ctx, db.SetTgCurrentParams{Current: userID, TgID: tgID})
}

func (l *Local) UserOfCode(ctx context.Context, code string) (int64, error) {
	return l.Gateway.UserOfCode(ctx, code)
}

// UserOfAddress: the own token is a path segment of 24 letters and digits. An old panel's
// address is taken as the panel serves it (panelimport.LinkToken), and only while old links
// are set up; the signature of a token Marzban or PasarGuard signed is checked as there
// (Verifier.User).
func (l *Local) UserOfAddress(ctx context.Context, urlPath string) (int64, bool) {
	for seg := range strings.SplitSeq(urlPath, "/") {
		if !nativeToken.MatchString(seg) {
			continue
		}
		if u, err := l.Store.Q.GetUserBySubToken(ctx, seg); err == nil {
			return u.ID, true
		}
	}
	legacyPath, verifier, err := panelimport.LoadLegacy(ctx, l.Settings)
	if err != nil {
		return 0, false
	}
	token, _, ok := panelimport.LinkToken(urlPath, legacyPath)
	if !ok {
		return 0, false
	}
	if u, ok := verifier.User(ctx, l.Store.Q, token); ok {
		return u.ID, true
	}
	return 0, false
}

// Link: a subscription has one owner. An owner who cannot be asked, as they blocked the
// bot, is not in the way.
func (l *Local) Link(ctx context.Context, tgID, userID int64) (tgbot.Linked, error) {
	row, err := l.Store.Q.GetUser(ctx, userID)
	if err != nil {
		return tgbot.Linked{}, err
	}
	u := model.User(row)
	prev, err := l.Store.Q.GetTgLink(ctx, userID)
	switch {
	case err == nil && prev.TgID == tgID:
		_ = l.SetCurrent(ctx, tgID, userID)
		return tgbot.Linked{Status: tgbot.LinkAlready, User: u}, nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return tgbot.Linked{}, err
	}
	// The check and the link are one transaction under the account's and the subscription's
	// locks (domain.LinkTg): two links at once do not pass the limit together, nor take a
	// subscription from each other unseen.
	owner := prev.TgID
	err = l.Store.TxRC(ctx, func(q *db.Queries) error {
		return domain.LinkTg(ctx, q, tgID, userID, model.MaxLinks, l.Now(), func(p int64) bool {
			owner = p
			return l.reachable(ctx, p)
		})
	})
	switch {
	case errors.Is(err, domain.ErrLinkLimit):
		return tgbot.Linked{Status: tgbot.LinkLimit, User: u}, nil
	case errors.Is(err, domain.ErrLinkTaken):
		return tgbot.Linked{Status: tgbot.LinkTaken, User: u, Owner: owner}, nil
	case err != nil:
		return tgbot.Linked{}, err
	}
	_ = l.SetCurrent(ctx, tgID, userID)
	return tgbot.Linked{Status: tgbot.LinkDone, User: u}, nil
}

// reachable: the account has a chat with the bot and has not blocked it, so it can be asked.
func (l *Local) reachable(ctx context.Context, tgID int64) bool {
	ch, err := l.Store.Q.GetTgChat(ctx, tgID)
	return err == nil && ch.Blocked == 0
}

func (l *Local) Owner(ctx context.Context, userID int64) (int64, error) {
	link, err := l.Store.Q.GetTgLink(ctx, userID)
	return link.TgID, err
}

func (l *Local) Transfer(ctx context.Context, userID, to int64) error {
	// The owner asked for it: whoever owned it lets it go (keep nil).
	err := l.Store.TxRC(ctx, func(q *db.Queries) error {
		return domain.LinkTg(ctx, q, to, userID, model.MaxLinks, l.Now(), nil)
	})
	if errors.Is(err, domain.ErrLinkLimit) {
		return model.ErrLinkLimit
	}
	if err != nil {
		return err
	}
	_ = l.SetCurrent(ctx, to, userID)
	return nil
}

func (l *Local) Unlink(ctx context.Context, userID int64) error {
	return l.Store.Q.UnlinkTg(ctx, userID)
}

func (l *Local) User(ctx context.Context, id int64) (model.User, error) {
	u, err := l.Store.Q.GetUser(ctx, id)
	return model.User(u), err
}

// GrantsLeft is what is left of the subscription's traffic packages, of the main traffic and
// of each group.
func (l *Local) GrantsLeft(ctx context.Context, userID int64, now time.Time) (model.GrantsLeft, error) {
	g, err := domain.UserGrantsLeft(ctx, l.Store.Q, userID, now)
	if err != nil {
		return model.GrantsLeft{}, err
	}
	out := model.GrantsLeft{Main: g.Main(userID)}
	for k, left := range g {
		if k[0] != userID || k[1] == 0 {
			continue
		}
		if out.Pools == nil {
			out.Pools = map[int64]int64{}
		}
		out.Pools[k[1]] = left
	}
	return out, nil
}

// Pools are the user's groups of their own limit, among the groups the user reaches, as the
// subscription's page shows them (domain.UserGroups: a group none of whose servers the user
// may use says nothing).
func (l *Local) Pools(ctx context.Context, userID int64, now time.Time) ([]tgbot.Pool, error) {
	q := l.Store.Q
	rows, err := q.ListUserPools(ctx, userID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	u, err := q.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	reach, err := domain.UserGroups(ctx, q, u)
	if err != nil {
		return nil, err
	}
	names, err := domain.PoolNames(ctx, q)
	if err != nil {
		return nil, err
	}
	grants, err := domain.UserGrantsLeft(ctx, q, userID, now)
	if err != nil {
		return nil, err
	}
	var out []tgbot.Pool
	for _, r := range rows {
		if r.Mode != domain.PoolOwn || !reach[r.PoolID] {
			continue // in the main traffic, without a limit, closed or out of reach: nothing to count down
		}
		extra := grants.Pool(userID, r.PoolID)
		out = append(out, tgbot.Pool{Name: names[r.PoolID], Used: r.UsedUp + r.UsedDown, Limit: r.TrafficLimit.Int64, Extra: extra,
			Exhausted: domain.PoolExhausted(r, extra)})
	}
	return out, nil
}

func (l *Local) DeviceBinding(ctx context.Context) bool {
	on, _ := l.Settings.On(ctx, settings.DeviceBinding)
	return on
}

func (l *Local) DeviceCount(ctx context.Context, userID int64) (int64, error) {
	return l.Store.Q.CountBoundDevices(ctx, userID)
}

func (l *Local) Devices(ctx context.Context, userID int64) ([]model.BoundDevice, error) {
	list, err := l.Store.Q.ListBoundDevices(ctx, userID)
	return model.Each(list, func(d db.BoundDevice) model.BoundDevice { return model.BoundDevice(d) }), err
}

func (l *Local) DeviceTraffic(ctx context.Context, userID int64) (map[int64]int64, error) {
	rows, err := l.Store.Q.ListBoundDeviceTraffic(ctx, userID)
	if err != nil {
		return nil, err
	}
	used := map[int64]int64{}
	for _, r := range rows {
		used[r.DeviceID] = r.Up + r.Down
	}
	return used, nil
}

func (l *Local) UnbindState(ctx context.Context, userID int64) (model.UnbindState, error) {
	s, err := l.Binder.UnbindState(ctx, userID)
	return s.Model(), err
}

func (l *Local) Unbind(ctx context.Context, userID, deviceID int64) error {
	return l.Binder.Unbind(ctx, userID, deviceID, true)
}

func (l *Local) ReissueState(ctx context.Context, userID int64) (model.ReissueState, error) {
	s, err := l.Users.ReissueState(ctx, userID)
	return s.Model(), err
}

func (l *Local) Reissue(ctx context.Context, userID int64) error {
	_, err := l.Users.ReissueByOwner(ctx, userID, "bot")
	return err
}

func (l *Local) Notices(ctx context.Context, now time.Time) ([]db.ListNoticeSubscriptionsRow, error) {
	return l.Store.Q.ListNoticeSubscriptions(ctx, now.Unix())
}

func (l *Local) NoticeSent(ctx context.Context, userID int64, kind string, period int64) (bool, error) {
	return l.Store.Q.TgNoticeSent(ctx, db.TgNoticeSentParams{UserID: userID, Kind: kind, Period: period})
}

func (l *Local) NoticeDelivered(ctx context.Context, userID int64, kind string, period int64, at time.Time) error {
	_, err := l.Store.Q.AddTgNotice(ctx, db.AddTgNoticeParams{UserID: userID, Kind: kind, Period: period, SentAt: at.Unix()})
	return err
}

func (l *Local) PruneNotices(ctx context.Context, before time.Time) error {
	return l.Store.Q.PruneTgNotices(ctx, before.Unix())
}

func (l *Local) BroadcastTargets(ctx context.Context) ([]int64, error) {
	return l.Store.Q.BroadcastTargets(ctx)
}

func (l *Local) Shop() tgbot.Shop { return shop{l.Billing} }

func (l *Local) Tariff(ctx context.Context, id int64) (model.Tariff, error) {
	t, err := l.Store.Q.GetTariff(ctx, id)
	return model.Tariff(t), err
}

func (l *Local) MTProto(ctx context.Context) []tgbot.MTLink {
	raw, err := l.Settings.String(ctx, settings.KeyMTProto)
	if err != nil || raw == "" {
		return nil
	}
	var ps []subs.MTProxy
	if json.Unmarshal([]byte(raw), &ps) != nil {
		return nil
	}
	var out []tgbot.MTLink
	for _, p := range ps {
		if p.Server == "" || p.Port <= 0 || p.Secret == "" {
			continue
		}
		out = append(out, tgbot.MTLink{Name: p.Name, Link: p.Link(), TG: p.TGLink()})
	}
	return out
}

// ReferralOn: the programme is the admin's to turn on.
func (l *Local) ReferralOn(ctx context.Context) bool {
	return l.ReferralProgram != nil && l.ReferralProgram.Setup(ctx).Enabled
}

// BotUsername is the bot's own @name, from the gateway's last getMe; "" while it is off.
func (l *Local) BotUsername(ctx context.Context) string {
	if l.Gateway == nil {
		return ""
	}
	st := l.Gateway.Status()
	if !st.Running {
		return ""
	}
	return st.Bot.Username
}

// AdminChat is the chat the panel's alerts go to; 0 when none is connected.
func (l *Local) AdminChat(ctx context.Context) int64 {
	if l.Gateway == nil {
		return 0
	}
	id, ok, err := l.Gateway.InfrastructureAdminChat(ctx)
	if err != nil || !ok {
		return 0
	}
	return id
}

// Referral is the programme as this account sees it: off when the admin has not turned it
// on, else its code, what it pays and what it has brought.
func (l *Local) Referral(ctx context.Context, tgID int64) (tgbot.ReferralView, error) {
	if l.ReferralProgram == nil {
		return tgbot.ReferralView{}, nil
	}
	cfg := l.ReferralProgram.Setup(ctx)
	v := tgbot.ReferralView{Enabled: cfg.Enabled, Spend: cfg.Spend}
	if !cfg.Enabled {
		return v, nil
	}
	code, err := l.ReferralProgram.Code(ctx, tgID)
	if err != nil {
		return v, err
	}
	v.Code = code
	st, err := l.ReferralProgram.StatsOf(ctx, tgID)
	if err != nil {
		return v, err
	}
	v.Invited, v.Paid, v.Balance = st.Invited, st.Paid, st.Balance
	for _, r := range cfg.Rules {
		if !r.Enabled {
			continue
		}
		v.Rules = append(v.Rules, tgbot.ReferralRule{Kind: r.Kind, Value: r.Value, Every: r.Every,
			Count: st.Progress[r.ID], Names: l.ruleNames(ctx, r)})
	}
	rows, err := l.Store.Q.ListReferralRewardsOf(ctx, db.ListReferralRewardsOfParams{ReferrerTgID: tgID, Lim: referralHistory})
	if err != nil {
		return v, err
	}
	for _, r := range rows {
		kind := referral.KindPercent
		if r.Kind == referral.KindDays {
			kind = referral.KindDays
		}
		v.Rewards = append(v.Rewards, tgbot.ReferralReward{Kind: kind, Rub: r.Amount, Days: r.Days, At: time.Unix(r.CreatedAt, 0).UTC()})
	}
	return v, nil
}

// ruleNames is how a rule names the tariffs it counts, for the words of the screen: the
// rule's own name first when the admin gave one, then the tariffs'.
func (l *Local) ruleNames(ctx context.Context, r referral.Rule) []string {
	var names []string
	if name := strings.TrimSpace(r.Name); name != "" {
		names = append(names, name)
	}
	for _, id := range r.TariffIDs {
		t, err := l.Store.Q.GetTariff(ctx, id)
		if err != nil {
			return names
		}
		names = append(names, t.Name)
	}
	return names
}

// ReferralJoin follows an invite code: the account is bound to one inviter, the first whose
// link it opened, and the status says what came of this one.
func (l *Local) ReferralJoin(ctx context.Context, tgID int64, code string) (tgbot.ReferralJoined, error) {
	if l.ReferralProgram == nil {
		return tgbot.ReferralJoined{Status: tgbot.ReferralOff}, nil
	}
	ref, err := l.ReferralProgram.Link(ctx, tgID, code)
	switch {
	case errors.Is(err, referral.ErrOff):
		return tgbot.ReferralJoined{Status: tgbot.ReferralOff}, nil
	case errors.Is(err, referral.ErrSelf):
		return tgbot.ReferralJoined{Status: tgbot.ReferralSelf}, nil
	case errors.Is(err, referral.ErrCode):
		return tgbot.ReferralJoined{Status: tgbot.ReferralInvalid}, nil
	case errors.Is(err, referral.ErrBound):
		return tgbot.ReferralJoined{Status: tgbot.ReferralBound, ReferrerName: l.referrerName(ctx, ref.ReferrerTgID)}, nil
	case err != nil:
		return tgbot.ReferralJoined{}, err
	}
	return tgbot.ReferralJoined{Status: tgbot.ReferralDone, ReferrerName: l.referrerName(ctx, ref.ReferrerTgID)}, nil
}

// referrerName is how the programme calls an account that invited somebody: its @username,
// else its first name.
func (l *Local) referrerName(ctx context.Context, tgID int64) string {
	ch, err := l.Store.Q.GetTgChat(ctx, tgID)
	if err != nil {
		return ""
	}
	switch {
	case ch.Username != "":
		return "@" + ch.Username
	case strings.TrimSpace(ch.FirstName) != "":
		return ch.FirstName
	}
	return ""
}

// CampaignVisit counts a /start that came through a tag: once per chat, on the first tagged
// link it follows.
func (l *Local) CampaignVisit(ctx context.Context, tgID int64, tag string) (tgbot.CampaignGreeting, error) {
	if l.Campaigns == nil {
		return tgbot.CampaignGreeting{}, nil
	}
	c, counted, err := l.Campaigns.Visit(ctx, tgID, tag)
	if err != nil {
		return tgbot.CampaignGreeting{}, err
	}
	return tgbot.CampaignGreeting{Counted: counted, Name: c.Name}, nil
}

// NoteEmojiRefused keeps Telegram's refusal for the admin's page (POST /tg/emoji-refused).
func (l *Local) NoteEmojiRefused(ctx context.Context, ids []string, errText string) error {
	if len(ids) == 0 {
		return nil
	}
	return settings.Set(ctx, l.Settings, tgbot.KeyEmojiRefused, tgbot.EmojiRefusal{IDs: func(in []string) (out []string) {
		seen := map[string]bool{}
		for _, id := range in {
			if !seen[id] {
				seen[id], out = true, append(out, id)
			}
		}
		return out
	}(ids), Error: errText, At: l.Now().Unix()})
}

// PartnerCode binds the account to the partner's code (the first code stays) and names the
// partner for the greeting; ok false: no such code.
func (l *Local) PartnerCode(ctx context.Context, tgID int64, code string) (string, bool, error) {
	if l.Partners == nil {
		return "", false, nil
	}
	return l.Partners.Code(ctx, tgID, code)
}
