// Package campaigns is the ad campaign tags of the Telegram bot: an admin gives each ad
// its own deep link (t.me/<bot>?start=camp_<tag>) and the panel counts, per campaign, how
// many people arrived, how many bought for the first time and how many renewed.
//
// A tag is a public label, not a secret: anybody may follow a link with any tag, so it is
// never trusted for access — it only says where a chat came from. A chat belongs to the
// campaign of the first tagged link it opened and never moves, so an arrival is counted
// once; its later payments are counted as a first purchase and then as renewals.
package campaigns

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"

	"mikan/internal/panel/referral"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Prefix marks a campaign tag in /start, as refPrefix marks a referral code.
const Prefix = "camp_"

// Currency is the currency the campaign totals are kept in: the payments counted here are
// summed in kopecks of RUB, whatever a buyer paid with, so one column means one thing.
const Currency = "RUB"

// tagMaxLen keeps a tag inside Telegram's 64-character start payload with room to spare
// for the prefix.
const tagMaxLen = 48

var (
	ErrTagInvalid   = errors.New("camp_tag_invalid")
	ErrNotFound     = errors.New("camp_not_found")
	ErrAgentPercent = errors.New("agent_percent")
	// The agent the campaign is being linked to is gone (or was never there).
	ErrAgentNotFound = errors.New("agent_not_found")
)

// Service is the campaign tags and what they brought.
type Service struct {
	Store *store.Store
	Log   *slog.Logger
	Now   func() time.Time
	// StarsRate is how many kopecks one Telegram Star counts as, by the referral
	// programme's setting: a Stars sale's commission is taken of that worth. 0 (or nil):
	// Stars count nothing, like the campaign totals.
	StarsRate func(ctx context.Context) int64
}

func New(st *store.Store, log *slog.Logger, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{Store: st, Log: log, Now: now}
}

// Tag is one campaign: its label, what the admin calls it and where its link leads.
type Tag struct {
	ID        int64
	Tag       string
	Name      string
	Note      string
	Enabled   bool
	CreatedAt time.Time
}

// Stats is what one campaign brought.
type Stats struct {
	Visits  int64
	Bought  int64
	Renewed int64
	Rub     int64
}

// View is a campaign with its numbers, for the admin's page. Its fields are spelled out
// rather than embedding Tag, whose own Tag field would collide with the view's.
type View struct {
	ID        int64
	Tag       string
	Name      string
	Note      string
	Enabled   bool
	CreatedAt time.Time
	// The agent the campaign pays, 0 and 0 for the admin's own campaigns.
	AgentID      int64
	AgentPercent int64
	Stats
}

// Link is the deep link that carries a tag, made with the bot's username.
func Link(username, tag string) string {
	return "https://t.me/" + username + "?start=" + Prefix + tag
}

// NormalizeTag trims a tag and says whether it may be used: letters, digits, dash and
// underscore, so it survives a URL, a Telegram payload and a copy-paste.
func NormalizeTag(raw string) (string, bool) {
	t := strings.TrimSpace(raw)
	t = strings.TrimPrefix(t, Prefix)
	t = strings.TrimSpace(t)
	if t == "" || len(t) > tagMaxLen {
		return "", false
	}
	for _, r := range t {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return "", false
		}
	}
	return t, true
}

// Visit binds a chat to the campaign whose tagged link it followed. A chat that already
// belongs to a campaign is left where it is, so a second link — another campaign's — only
// counts as an arrival for the first. An unknown tag is created on the spot with an empty
// name, so nothing an admin forgot to make in advance is lost; the admin names it later.
func (s *Service) Visit(ctx context.Context, chat int64, rawTag string) (db.CampaignTag, bool, error) {
	tag, ok := NormalizeTag(rawTag)
	if !ok {
		return db.CampaignTag{}, false, ErrTagInvalid
	}
	c, err := s.Store.Q.GetCampaignTagByTag(ctx, tag)
	if errors.Is(err, sql.ErrNoRows) {
		c, err = s.ensure(ctx, tag)
		if err != nil {
			return db.CampaignTag{}, false, err
		}
	} else if err != nil {
		return db.CampaignTag{}, false, err
	}
	if c.Deleted != 0 || c.Enabled == 0 {
		// A campaign the admin switched off or deleted still answers the link, but is not
		// counted: nothing of it appears in the stats.
		return c, false, nil
	}
	n, err := s.Store.Q.TakeCampaignVisit(ctx, db.TakeCampaignVisitParams{
		CampaignID: c.ID, ChatID: chat, Tag: tag, FirstSeen: s.Now().Unix()})
	if err != nil {
		return db.CampaignTag{}, false, err
	}
	return c, n == 1, nil
}

func (s *Service) ensure(ctx context.Context, tag string) (db.CampaignTag, error) {
	now := s.Now().Unix()
	c, err := s.Store.Q.CreateCampaignTag(ctx, db.CreateCampaignTagParams{Tag: tag, CreatedAt: now})
	if err == nil {
		return c, nil
	}
	// Another request made it between the read and the insert; take that one.
	if store.IsUnique(err) {
		return s.Store.Q.GetCampaignTagByTag(ctx, tag)
	}
	return db.CampaignTag{}, err
}

// Attribute counts the applied payment p for the campaign the buyer arrived through, on
// q's transaction — the one that applied the payment. A first subscription is "bought",
// anything on a tariff after it is "renewed". Nothing is counted twice: the event row's
// payment id is unique and a payment is applied once.
func (s *Service) Attribute(ctx context.Context, q *db.Queries, p db.Payment) error {
	if p.Kind != "new" && p.Kind != "renew" {
		return nil
	}
	v, err := q.GetCampaignVisit(ctx, p.TgID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	what := "renewed"
	if p.Kind == "new" {
		what = "bought"
	}
	_, err = q.CreateCampaignEvent(ctx, db.CreateCampaignEventParams{
		CampaignID: v.CampaignID, ChatID: p.TgID, PaymentID: sql.NullInt64{Int64: p.ID, Valid: true},
		Kind: what, Amount: p.Amount, Currency: p.Currency, CreatedAt: s.Now().Unix()})
	if err != nil {
		if store.IsUnique(err) {
			// Already counted, by a reconcile pass that ran the same payment again; the
			// commission stood behind that counting and exists too.
			return nil
		}
		return err
	}
	return s.commission(ctx, q, v.CampaignID, p)
}

// commission credits the campaign's agent for the payment, on q's transaction — the one
// that applied the payment, so a sale pays out once or not at all. The percent is read
// off the campaign as it stands now and saved with the commission, so a later change of
// the link's percent does not rewrite what was earned. A campaign without an agent, or a
// sale whose worth comes out to nothing (Stars with no rate set), earns nothing.
func (s *Service) commission(ctx context.Context, q *db.Queries, campaignID int64, p db.Payment) error {
	c, err := q.GetCampaignTag(ctx, campaignID)
	if err != nil {
		return err
	}
	if !c.AgentID.Valid || !c.AgentPercent.Valid || c.AgentPercent.Int64 < 1 {
		return nil
	}
	sale := referral.Rub(p.Amount, p.Currency, s.starsRate(ctx))
	if sale <= 0 {
		return nil
	}
	_, err = q.CreateAgentCommission(ctx, db.CreateAgentCommissionParams{
		AgentID: c.AgentID.Int64, CampaignID: c.ID, PaymentID: sql.NullInt64{Int64: p.ID, Valid: true},
		Percent: c.AgentPercent.Int64, Kind: whatOf(p.Kind), SaleAmount: sale,
		Amount: share(sale, c.AgentPercent.Int64), Currency: Currency, CreatedAt: s.Now().Unix()})
	if err != nil && store.IsUnique(err) {
		// The payment paid once already, by a reconcile pass.
		return nil
	}
	return err
}

// starsRate is what one Telegram Star counts as in kopecks, 0 when nobody told us.
func (s *Service) starsRate(ctx context.Context) int64 {
	if s.StarsRate == nil {
		return 0
	}
	return max(0, s.StarsRate(ctx))
}

// share is percent of an amount without losing anything to integer division, the way the
// referral programme's share does it.
func share(amount, percent int64) int64 {
	if amount <= 0 || percent <= 0 {
		return 0
	}
	if percent > 100 {
		percent = 100
	}
	v := (amount/100)*percent + (amount%100)*percent/100
	return max(0, v)
}

// whatOf names a payment's kind as the commission rows spell it.
func whatOf(kind string) string {
	if kind == "new" {
		return "bought"
	}
	return "renewed"
}

// Revert takes back what a refunded payment counted, on q's transaction (the refund's):
// the campaign's event, and with it the agent's commission — a refunded sale earns
// nothing.
func (s *Service) Revert(ctx context.Context, q *db.Queries, paymentID int64) error {
	if paymentID == 0 {
		return nil
	}
	_, err := q.RevertCampaignEventsByPayment(ctx, db.RevertCampaignEventsByPaymentParams{
		RevertedAt: sql.NullInt64{Int64: s.Now().Unix(), Valid: true},
		PaymentID:  sql.NullInt64{Int64: paymentID, Valid: true}})
	if err != nil {
		return err
	}
	_, err = q.RevertAgentCommissionsByPayment(ctx, db.RevertAgentCommissionsByPaymentParams{
		RevertedAt: sql.NullInt64{Int64: s.Now().Unix(), Valid: true},
		PaymentID:  sql.NullInt64{Int64: paymentID, Valid: true}})
	return err
}

// Assign binds a campaign to an agent with its sales percent, or (agentID 0) unbinds it.
// The percent applies to payments applied from now on: each commission keeps the percent
// it was earned for.
func (s *Service) Assign(ctx context.Context, id, agentID, percent int64) error {
	var agent, pct sql.NullInt64
	if agentID != 0 {
		if percent < 1 || percent > 100 {
			return ErrAgentPercent
		}
		if _, err := s.Store.Q.GetAgent(ctx, agentID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrAgentNotFound
			}
			return err
		}
		agent, pct = sql.NullInt64{Int64: agentID, Valid: true}, sql.NullInt64{Int64: percent, Valid: true}
	}
	n, err := s.Store.Q.SetCampaignAgent(ctx, db.SetCampaignAgentParams{
		AgentID: agent, AgentPercent: pct, UpdatedAt: s.Now().Unix(), ID: id})
	if err != nil {
		if store.IsForeignKey(err) && agentID != 0 {
			return ErrAgentNotFound
		}
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Views is every campaign with its numbers, newest first.
func (s *Service) Views(ctx context.Context, lim, offset int64) ([]View, int64, error) {
	rows, err := s.Store.Q.ListCampaignTagsWithStats(ctx, db.ListCampaignTagsWithStatsParams{Lim: lim, RowOffset: offset})
	if err != nil {
		return nil, 0, err
	}
	total, err := s.Store.Q.CountCampaignTags(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]View, 0, len(rows))
	for _, r := range rows {
		var agentID, percent int64
		if r.AgentID.Valid {
			agentID = r.AgentID.Int64
		}
		if r.AgentPercent.Valid {
			percent = r.AgentPercent.Int64
		}
		out = append(out, View{
			ID: r.ID, Tag: r.Tag, Name: r.Name, Note: r.Note, Enabled: r.Enabled != 0, CreatedAt: time.Unix(r.CreatedAt, 0),
			AgentID: agentID, AgentPercent: percent,
			Stats: Stats{Visits: r.Visits, Bought: r.Bought, Renewed: r.Renewed, Rub: r.Rub},
		})
	}
	return out, total, nil
}

// Create makes a campaign with a hand-picked tag.
func (s *Service) Create(ctx context.Context, rawTag, name, note string) (db.CampaignTag, error) {
	tag, ok := NormalizeTag(rawTag)
	if !ok {
		return db.CampaignTag{}, ErrTagInvalid
	}
	if _, err := s.Store.Q.GetCampaignTagByTag(ctx, tag); err == nil {
		return db.CampaignTag{}, ErrTagInvalid
	} else if !errors.Is(err, sql.ErrNoRows) {
		return db.CampaignTag{}, err
	}
	now := s.Now().Unix()
	return s.Store.Q.CreateCampaignTag(ctx, db.CreateCampaignTagParams{
		Tag: tag, Name: strings.TrimSpace(name), Note: strings.TrimSpace(note), CreatedAt: now})
}

// Update changes what the admin calls a campaign and whether it is counted.
func (s *Service) Update(ctx context.Context, id int64, name, note string, enabled bool) (db.CampaignTag, error) {
	now := s.Now().Unix()
	en := int32(0)
	if enabled {
		en = 1
	}
	n, err := s.Store.Q.UpdateCampaignTag(ctx, db.UpdateCampaignTagParams{
		ID: id, Name: strings.TrimSpace(name), Note: strings.TrimSpace(note), Enabled: en, UpdatedAt: now})
	if err != nil {
		return db.CampaignTag{}, err
	}
	if n == 0 {
		return db.CampaignTag{}, ErrNotFound
	}
	return s.Store.Q.GetCampaignTag(ctx, id)
}

// SetEnabled turns counting of a campaign on or off without touching its tags or links.
func (s *Service) SetEnabled(ctx context.Context, id int64, enabled bool) error {
	en := int32(0)
	if enabled {
		en = 1
	}
	n, err := s.Store.Q.SetCampaignTagEnabled(ctx, db.SetCampaignTagEnabledParams{ID: id, Enabled: en, UpdatedAt: s.Now().Unix()})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete hides a campaign from the list and stops counting it; its rows stay, so the
// numbers of the past are not lost.
func (s *Service) Delete(ctx context.Context, id int64) error {
	n, err := s.Store.Q.DeleteCampaignTag(ctx, db.DeleteCampaignTagParams{ID: id, UpdatedAt: s.Now().Unix()})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// VisitView is one arrival, for the admin's list under a campaign.
type VisitView struct {
	ChatID    int64
	Username  string
	FirstName string
	Tag       string
	FirstSeen time.Time
}

// Visits is who arrived through a campaign, newest first.
func (s *Service) Visits(ctx context.Context, id, lim, offset int64) ([]VisitView, int64, error) {
	rows, err := s.Store.Q.ListCampaignVisits(ctx, db.ListCampaignVisitsParams{CampaignID: id, Lim: lim, RowOffset: offset})
	if err != nil {
		return nil, 0, err
	}
	total, err := s.Store.Q.CountCampaignVisitsOf(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	out := make([]VisitView, 0, len(rows))
	for _, r := range rows {
		out = append(out, VisitView{ChatID: r.ChatID, Username: r.Username, FirstName: r.FirstName, Tag: r.Tag, FirstSeen: time.Unix(r.FirstSeen, 0)})
	}
	return out, total, nil
}

// Overall is the whole programme's totals, above the campaign list.
type Overall struct {
	Campaigns int64
	Visits    int64
	Bought    int64
	Renewed   int64
}

// Summary is every campaign's numbers added up.
func (s *Service) Summary(ctx context.Context) (Overall, error) {
	views, total, err := s.Views(ctx, 1000, 0)
	if err != nil {
		return Overall{}, err
	}
	out := Overall{Campaigns: total}
	for _, v := range views {
		out.Visits += v.Visits
		out.Bought += v.Bought
		out.Renewed += v.Renewed
	}
	return out, nil
}
