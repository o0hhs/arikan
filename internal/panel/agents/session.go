package agents

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/store/db"
)

// CookieName is the agents' panel session cookie. The __Host- prefix keeps it off other
// subdomains of the panel's host, as the admin's own cookie is kept.
const CookieName = "__Host-mikan-agent"

// Login checks a login and a password and opens a session: the raw cookie token is
// returned, only its SHA-256 is kept. The attempt is counted on both the address and the
// login before the password is hashed, so parallel guesses get no free rides.
//
// A wrong answer is one answer: an agent that is switched off, an agent that is not
// there and a password that is not theirs all read the same, and an unknown login takes
// as long as a known one (the dummy hash is run for it).
func (s *Service) Login(ctx context.Context, rawLogin, password, ip, userAgent string) (string, db.AgentSession, error) {
	login := strings.ToLower(strings.TrimSpace(rawLogin))
	now := s.Now()
	ipKey, loginKey := "ip:"+auth.ClientKey(ip), "login:"+login
	okIP, waitIP, _ := s.ipLimit.Reserve(ipKey, now)
	if !okIP {
		return "", db.AgentSession{}, waitErr(waitIP)
	}
	okLogin, waitLogin, _ := s.loginLimit.Reserve(loginKey, now)
	if !okLogin {
		return "", db.AgentSession{}, waitErr(waitLogin)
	}

	a, err := s.Store.Q.GetAgentByLogin(ctx, login)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", db.AgentSession{}, err
	}
	hash := s.dummyHash
	if found {
		hash = a.PasswordHash
	}
	ok, err := s.verify(ctx, password, hash)
	if err != nil && found {
		return "", db.AgentSession{}, err
	}
	if !found || !ok || a.Disabled != 0 {
		if s.Log != nil {
			s.Log.Warn("agents: login refused", "login", loginName(found, login), "ip", ip)
		}
		return "", db.AgentSession{}, ErrCredentials
	}

	s.ipLimit.Reset(ipKey)
	s.loginLimit.Reset(loginKey)
	token, sess, err := s.createSession(ctx, a.ID, ip, userAgent)
	if err != nil {
		return "", db.AgentSession{}, err
	}
	return token, sess, nil
}

// loginName says what a refused login answered for in the log: a known login as itself,
// an unknown one as a short hash of what was typed (often a password pasted into the
// wrong field, and the log stays readable but does not leak the attempt).
func loginName(found bool, login string) string {
	if found {
		return login
	}
	return "unknown:" + secure.SHA256Hex(login)[:8]
}

func waitErr(wait time.Duration) error {
	return &WaitError{Wait: wait}
}

// WaitError says the login was refused for a moment; the API answers it as a 429 with a
// Retry-After.
type WaitError struct{ Wait time.Duration }

func (e *WaitError) Error() string { return "too many attempts, wait " + e.Wait.String() }

// createSession is the agents' own sessions table: the same shape as the admin's, so the
// same lifetimes hold — an hour of silence kills a session, the week ends it in any case.
func (s *Service) createSession(ctx context.Context, agentID int64, ip, userAgent string) (string, db.AgentSession, error) {
	token := secure.Token(43)
	now := s.Now().Unix()
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}
	p := db.CreateAgentSessionParams{
		IDHash:     secure.SHA256Hex(token),
		AgentID:    agentID,
		CsrfToken:  secure.Token(32),
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now + int64(auth.MaxTTL/time.Second),
		Ip:         ip,
		UserAgent:  userAgent,
	}
	if err := s.Store.Q.CreateAgentSession(ctx, p); err != nil {
		return "", db.AgentSession{}, err
	}
	return token, db.AgentSession(p), nil
}

// LookupSession is the token's session, if it lives: not past its expiry, not idle past
// the same hour as the admin's sessions. It saves the last-seen time at most once a
// minute.
func (s *Service) LookupSession(ctx context.Context, token string) (db.AgentSession, error) {
	if token == "" {
		return db.AgentSession{}, ErrCredentials
	}
	sess, err := s.Store.Q.GetAgentSession(ctx, secure.SHA256Hex(token))
	if errors.Is(err, sql.ErrNoRows) {
		return db.AgentSession{}, ErrCredentials
	}
	if err != nil {
		return db.AgentSession{}, err
	}
	now := s.Now()
	if now.Unix() >= sess.ExpiresAt || now.Sub(time.Unix(sess.LastSeenAt, 0)) > auth.IdleTTL {
		_ = s.Store.Q.DeleteAgentSession(ctx, sess.IDHash)
		return db.AgentSession{}, ErrCredentials
	}
	if now.Sub(time.Unix(sess.LastSeenAt, 0)) > time.Minute {
		if _, err := s.Store.Q.TouchAgentSession(ctx, db.TouchAgentSessionParams{LastSeenAt: now.Unix(), IDHash: sess.IDHash}); err != nil && s.Log != nil {
			s.Log.Warn("agents: session last seen not saved", "err", err)
		}
	}
	return sess, nil
}

// Logout ends the session the token belongs to.
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.Store.Q.DeleteAgentSession(ctx, secure.SHA256Hex(token))
}
