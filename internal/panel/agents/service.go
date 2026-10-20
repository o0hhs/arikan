// Package agents is the panel's advertising agents: the admin hands each agent a login
// and a password for a panel of their own, and links campaigns (the agent's ad
// placements) to them with a percent of the sales. The money itself is earned where the
// payments are applied — campaigns.Service commissions a linked campaign's payments on
// the billing transaction (and takes them back on a refund) — while this side keeps the
// agents: who they are, what they brought, what they earned and what of it was paid out.
package agents

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

var (
	// The login and the wished password are not of the shape the panel keeps.
	ErrLogin    = errors.New("agent_login_invalid")
	ErrPassword = errors.New("agent_password_invalid")
	// The login is somebody else's.
	ErrLoginTaken = errors.New("agent_login_taken")
	// The agent's own Telegram is somebody else's.
	ErrTgTaken = errors.New("agent_tg_taken")
	ErrNotFound = errors.New("agent_not_found")
	// The payout recorded is one the panel cannot believe: not a positive kopeck sum.
	ErrPayout = errors.New("agent_payout_amount")
	// The login or the password did not match an agent (or the agent is switched off).
	ErrCredentials = errors.New("agent_credentials")
)

const Currency = "RUB"

// Service is the advertising agents: who they are, what they brought and what they were
// paid, and their panel's sessions.
type Service struct {
	Store *store.Store
	Log   *slog.Logger
	Now   func() time.Time

	ipLimit    *auth.Limiter
	loginLimit *auth.Limiter
	// hashSem bounds the password hashes that run at once: each holds 64 MiB.
	hashSem chan struct{}
	// dummyHash keeps a wrong login's answer as slow as a right one's.
	dummyHash string
}

func New(st *store.Store, log *slog.Logger, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	dummy, err := auth.HashPassword(secure.Token(32))
	if err != nil {
		// Only a dead random source brings this about; logins still answer, just sooner
		// for an unknown name (session.go keeps the timing honest the rest of the way).
		log.Warn("agents: the dummy password hash failed", "err", err)
	}
	return &Service{
		Store: st, Log: log, Now: now,
		ipLimit:    auth.NewLimiter(10, 10*time.Minute, 15*time.Minute, 24*time.Hour),
		loginLimit: auth.NewLimiter(10, 10*time.Minute, 15*time.Minute, 24*time.Hour),
		hashSem:    make(chan struct{}, 4),
		dummyHash:  dummy,
	}
}

// Agent is the admin's side of one agent: the agent's own row with what it brought.
type Agent struct {
	db.CampaignAgent
	Stats
}

// Stats is what an agent (or the whole programme) has brought and earned, in kopecks of
// RUB: Revenue is what its links' buyers paid, Earned the commissions that stand, Paid
// what was handed over.
type Stats struct {
	Links   int64
	Visits  int64
	Bought  int64
	Renewed int64
	Revenue int64
	Earned  int64
	Paid    int64
	// Usages is how many times the agent themselves went through their own links: their
	// own activity, never an arrival.
	Usages int64
}

// Owed is what the agent still waits for: everything earned that a payout has not
// covered. A refund pulls a commission back, so the number may go below zero — the
// agent was paid for a sale the buyer took back.
func (st Stats) Owed() int64 { return st.Earned - st.Paid }

const (
	loginMin = 3
	loginMax = 64
)

// normalizeLogin trims a login and says whether it may be kept: the panel's own form of
// a name, so "Vasya" and "vasya" are one agent.
func normalizeLogin(raw string) (string, bool) {
	l := strings.ToLower(strings.TrimSpace(raw))
	if l == "" || len(l) < loginMin || len(l) > loginMax {
		return "", false
	}
	for _, r := range l {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
		default:
			return "", false
		}
	}
	return l, true
}

// tgID turns the admin's own Telegram id into the column's shape; 0 means none.
func tgID(t *int64) sql.NullInt64 {
	if t == nil || *t <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *t, Valid: true}
}

// Create makes an agent with a login and a password to give them (out of band), and a
// note the admin calls them by. tgID is the agent's own Telegram account if they have
// one: their visits of their own links then count as their own use, not as arrivals.
func (s *Service) Create(ctx context.Context, rawLogin, password, note string, tg *int64) (db.CampaignAgent, error) {
	login, ok := normalizeLogin(rawLogin)
	if !ok {
		return db.CampaignAgent{}, ErrLogin
	}
	hash, err := s.hash(ctx, password)
	if err != nil {
		return db.CampaignAgent{}, err
	}
	now := s.Now().Unix()
	a, err := s.Store.Q.CreateAgent(ctx, db.CreateAgentParams{
		Login: login, PasswordHash: hash, Note: strings.TrimSpace(note), TgID: tgID(tg), CreatedAt: now})
	if store.IsUnique(err) {
		if tg != nil && *tg > 0 {
			return db.CampaignAgent{}, ErrTgTaken
		}
		return db.CampaignAgent{}, ErrLoginTaken
	}
	return a, err
}

// Update changes the login, the note, the agent's own Telegram and the on/off of an
// agent. Switching an agent off ends their sessions and refuses logins, but keeps every
// number of theirs.
func (s *Service) Update(ctx context.Context, id int64, rawLogin, note string, disabled bool, tg *int64) (db.CampaignAgent, error) {
	login, ok := normalizeLogin(rawLogin)
	if !ok {
		return db.CampaignAgent{}, ErrLogin
	}
	dis := int32(0)
	if disabled {
		dis = 1
	}
	n, err := s.Store.Q.UpdateAgent(ctx, db.UpdateAgentParams{
		Login: login, Note: strings.TrimSpace(note), Disabled: dis, TgID: tgID(tg), UpdatedAt: s.Now().Unix(), ID: id})
	if store.IsUnique(err) {
		return db.CampaignAgent{}, ErrTgTaken
	}
	if err != nil {
		return db.CampaignAgent{}, err
	}
	if n == 0 {
		return db.CampaignAgent{}, ErrNotFound
	}
	if disabled {
		if err := s.Store.Q.DeleteAgentSessions(ctx, id); err != nil {
			s.Log.Warn("agents: leaving sessions behind", "err", err)
		}
	}
	return s.Store.Q.GetAgent(ctx, id)
}

// SetPassword changes an agent's password: the new one, out of band, in the panel's
// login. Every session of the agent ends with it.
func (s *Service) SetPassword(ctx context.Context, id int64, password string) error {
	hash, err := s.hash(ctx, password)
	if err != nil {
		return err
	}
	n, err := s.Store.Q.UpdateAgentPassword(ctx, db.UpdateAgentPasswordParams{
		PasswordHash: hash, UpdatedAt: s.Now().Unix(), ID: id})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return s.Store.Q.DeleteAgentSessions(ctx, id)
}

// hash checks a password's shape and takes a hash of it (argon2id) for the agent's row.
func (s *Service) hash(ctx context.Context, password string) (string, error) {
	if len(password) < auth.MinPassword {
		return "", ErrPassword
	}
	select {
	case s.hashSem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.hashSem }()
	return auth.HashPassword(password)
}

// verify runs a password against a hash under one hashSem's slot.
func (s *Service) verify(ctx context.Context, password, hash string) (bool, error) {
	select {
	case s.hashSem <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-s.hashSem }()
	return auth.VerifyPassword(password, hash)
}

// List is every agent with its numbers, newest first.
func (s *Service) List(ctx context.Context, lim, offset int64) ([]Agent, int64, error) {
	rows, err := s.Store.Q.ListAgentsWithStats(ctx, db.ListAgentsWithStatsParams{Lim: lim, RowOffset: offset})
	if err != nil {
		return nil, 0, err
	}
	total, err := s.Store.Q.CountAgents(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Agent, 0, len(rows))
	for _, r := range rows {
		out = append(out, Agent{
			CampaignAgent: db.CampaignAgent{ID: r.ID, Login: r.Login, PasswordHash: r.PasswordHash, Note: r.Note, Disabled: r.Disabled, TgID: r.TgID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt},
			Stats:         Stats{Links: r.Links, Visits: r.Visits, Bought: r.Bought, Renewed: r.Renewed, Revenue: r.Revenue, Earned: r.Earned, Paid: r.Paid, Usages: r.Usages},
		})
	}
	return out, total, nil
}

// Summary adds every agent's numbers up, over the programme's header.
func (s *Service) Summary(ctx context.Context) (Stats, error) {
	rows, _, err := s.List(ctx, 1000, 0)
	if err != nil {
		return Stats{}, err
	}
	out := Stats{}
	for _, a := range rows {
		out.Links += a.Links
		out.Visits += a.Visits
		out.Bought += a.Bought
		out.Renewed += a.Renewed
		out.Revenue += a.Revenue
		out.Earned += a.Earned
		out.Paid += a.Paid
		out.Usages += a.Usages
	}
	return out, nil
}

// StatsOf is one agent's numbers, for the admin's card and the agent's own panel.
func (s *Service) StatsOf(ctx context.Context, agentID int64) (Stats, error) {
	r, err := s.Store.Q.AgentOverview(ctx, sql.NullInt64{Int64: agentID, Valid: true})
	if err != nil {
		return Stats{}, err
	}
	return Stats{Visits: r.Visits, Bought: r.Bought, Renewed: r.Renewed, Revenue: r.Revenue, Earned: r.Earned, Paid: r.Paid, Usages: r.Usages}, nil
}

// Link is one of an agent's campaigns: the placement the admin linked to them, with what
// it brought. Percent is the agent's share of the sales it counts.
type Link struct {
	ID        int64
	Tag       string
	Name      string
	Enabled   bool
	CreatedAt time.Time
	Percent   int64
	Visits    int64
	Bought    int64
	Renewed   int64
	Rub       int64
	Earned    int64
}

// Links is the agent's own links, newest first.
func (s *Service) Links(ctx context.Context, agentID, lim, offset int64) ([]Link, int64, error) {
	rows, err := s.Store.Q.ListAgentCampaignsWithStats(ctx, db.ListAgentCampaignsWithStatsParams{
		AgentID: sql.NullInt64{Int64: agentID, Valid: true}, Lim: lim, RowOffset: offset})
	if err != nil {
		return nil, 0, err
	}
	total, err := s.Store.Q.CountAgentCampaigns(ctx, sql.NullInt64{Int64: agentID, Valid: true})
	if err != nil {
		return nil, 0, err
	}
	out := make([]Link, 0, len(rows))
	for _, r := range rows {
		percent := int64(0)
		if r.AgentPercent.Valid {
			percent = r.AgentPercent.Int64
		}
		out = append(out, Link{
			ID: r.ID, Tag: r.Tag, Name: r.Name, Enabled: r.Enabled != 0, CreatedAt: time.Unix(r.CreatedAt, 0),
			Percent: percent, Visits: r.Visits, Bought: r.Bought, Renewed: r.Renewed, Rub: r.Rub, Earned: r.Earned,
		})
	}
	return out, total, nil
}

// Referral is one person an agent's links brought: the chat, the link it came through and
// what that person has paid.
type Referral struct {
	ChatID    int64
	Name      string
	Tag       string
	FirstSeen time.Time
	Bought    int64
	Renewed   int64
	Rub       int64
}

// Referrals is the people an agent's links brought, newest first.
func (s *Service) Referrals(ctx context.Context, agentID, lim, offset int64) ([]Referral, int64, error) {
	rows, err := s.Store.Q.ListAgentReferrals(ctx, db.ListAgentReferralsParams{
		AgentID: sql.NullInt64{Int64: agentID, Valid: true}, Lim: lim, RowOffset: offset})
	if err != nil {
		return nil, 0, err
	}
	total, err := s.Store.Q.CountAgentReferrals(ctx, sql.NullInt64{Int64: agentID, Valid: true})
	if err != nil {
		return nil, 0, err
	}
	out := make([]Referral, 0, len(rows))
	for _, r := range rows {
		name := r.FirstName
		if r.Username != "" {
			name = "@" + r.Username
		}
		out = append(out, Referral{
			ChatID: r.ChatID, Name: name, Tag: r.Tag, FirstSeen: time.Unix(r.FirstSeen, 0),
			Bought: r.Bought, Renewed: r.Renewed, Rub: r.Rub,
		})
	}
	return out, total, nil
}

// RemoveReferral takes a person out of an agent's arrivals by hand: a chat that was
// counted by a mistake (an agent's own click of their own link, say) stops being an
// arrival at once. Only a visit that belongs to this agent can be removed.
func (s *Service) RemoveReferral(ctx context.Context, agentID, chatID int64) error {
	v, err := s.Store.Q.GetCampaignVisit(ctx, chatID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	c, err := s.Store.Q.GetCampaignTag(ctx, v.CampaignID)
	if err != nil {
		return err
	}
	if !c.AgentID.Valid || c.AgentID.Int64 != agentID {
		return ErrNotFound
	}
	n, err := s.Store.Q.DeleteCampaignVisit(ctx, chatID)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Payout records that the admin handed the agent money outside the panel: the number
// comes off the agent's owed at once. There is no undo — the admin writes a note, "was
// made in cash", say, and the list keeps it.
func (s *Service) Payout(ctx context.Context, agentID, amount int64, note string) (db.AgentPayout, error) {
	if amount <= 0 || amount > 1_000_000_000_00 {
		return db.AgentPayout{}, ErrPayout
	}
	p, err := s.Store.Q.CreateAgentPayout(ctx, db.CreateAgentPayoutParams{
		AgentID: agentID, Amount: amount, Note: strings.TrimSpace(note), CreatedAt: s.Now().Unix()})
	if err != nil {
		if store.IsForeignKey(err) {
			return db.AgentPayout{}, ErrNotFound
		}
		return db.AgentPayout{}, err
	}
	return p, nil
}

// Payouts is an agent's payouts, newest first.
func (s *Service) Payouts(ctx context.Context, agentID, lim, offset int64) ([]db.AgentPayout, int64, error) {
	rows, err := s.Store.Q.ListAgentPayouts(ctx, db.ListAgentPayoutsParams{AgentID: agentID, Lim: lim, RowOffset: offset})
	if err != nil {
		return nil, 0, err
	}
	total, err := s.Store.Q.CountAgentPayouts(ctx, agentID)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// Cleanup drops the expired sessions, for the panel's slow sweep.
func (s *Service) Cleanup(ctx context.Context) error {
	now := s.Now()
	return s.Store.Q.DeleteStaleAgentSessions(ctx, db.DeleteStaleAgentSessionsParams{
		Now:        now.Unix(),
		IdleBefore: now.Add(-auth.IdleTTL).Unix(),
	})
}
