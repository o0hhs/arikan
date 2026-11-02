// Package monitor is the public monitoring page: one address on the panel's own host, open
// without the panel's password, behind a password of its own (settings.KeyMonitor). It
// shows what the service is doing — its traffic, its nodes — and never a subscriber: no
// names, no links, no addresses, no single connection. Off until the admin sets a password.
package monitor

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/settings"
)

// Blocks are what the page may show, in the order it lays them out.
var Blocks = []string{"summary", "traffic", "nodes", "sites", "payments"}

// defaultBlocks is what a page shows the moment it is turned on: the service's own traffic
// and its nodes. Where the clients go and what they paid are visible to whoever holds the
// password, so the admin turns those on themselves.
var defaultBlocks = []string{"summary", "traffic", "nodes"}

// PathLen is how long the page's own address segment is: long enough that nobody finds the
// page by asking for it, and no substitute for the password.
const PathLen = 16

// ErrWeakPassword: shorter than the panel's own (auth.MinPassword, in bytes).
var ErrWeakPassword = errors.New("password too short")

// Config is the page's settings document. The API takes and gives it whole but for Hash,
// which never leaves the panel: it answers with Set instead.
type Config struct {
	Enabled bool `json:"enabled" doc:"Показывать страницу мониторинга по публичной ссылке"`
	// Path is the page's own segment: the link is /<path>/. Rotating it breaks the old link.
	Path string `json:"path" doc:"Свой адрес страницы в ссылке"`
	// Hash is the argon2id hash of the page's password (auth.HashPassword). Its value keys
	// the sessions, so a new password ends every open one.
	Hash string `json:"hash"`
	// Blocks: what the page shows, and nothing else. A block this panel does not know is
	// refused; one the admin switched off is off for good, a block a later version adds
	// stays off until they turn it on: this page is public, and what is on it is a choice.
	Blocks []string `json:"blocks" doc:"Что показывает страница"`
}

// Default is the document of a page nobody set up: off, and showing the service's own
// traffic and nodes the moment the admin turns it on with a password.
func Default() Config { return Config{Blocks: slices.Clone(defaultBlocks)} }

// Load reads the document and keeps the blocks this panel has, in the order the page lays
// them out. Reading changes nothing and writes nothing: the page's address is made where
// the admin turns it on (the API), never here — a request from a stranger reads the same
// document as the Apply loop, and a link that changed with every read would be no link.
func Load(ctx context.Context, set *settings.Settings) (Config, error) {
	c, ok, err := settings.Get[Config](ctx, set, settings.KeyMonitor)
	if err != nil {
		return Default(), err
	}
	if !ok {
		return Default(), nil
	}
	c.Blocks = knownBlocks(c.Blocks)
	return c, nil
}

// Save writes the document as it is; the caller has validated it (Validate).
func Save(ctx context.Context, set *settings.Settings, c Config) error {
	return settings.Set(ctx, set, settings.KeyMonitor, c)
}

// knownBlocks keeps the blocks this panel has, in the order the page lays them out.
func knownBlocks(stored []string) []string {
	out := make([]string, 0, len(stored))
	for _, b := range Blocks {
		if slices.Contains(stored, b) {
			out = append(out, b)
		}
	}
	return out
}

// Show says whether the page shows the named block.
func (c Config) Show(block string) bool { return slices.Contains(c.Blocks, block) }

// Ready: there is a link and a password to open it with.
func (c Config) Ready() bool { return c.Enabled && c.Path != "" && c.Hash != "" }

// Validate refuses what the page cannot be: a block this panel does not know, a path from
// outside (it goes into the panel's own routing), a short or missing password.
func (c Config) Validate() error {
	for _, b := range c.Blocks {
		if !slices.Contains(Blocks, b) {
			return BadBlock{Block: b}
		}
	}
	if c.Path != "" && !ValidPath(c.Path) {
		return ErrBadPath
	}
	return nil
}

var (
	// ErrBadBlock is answered when the document asks for a block this panel does not have;
	// BadBlock names it. The text is the API's error code.
	ErrBadBlock = errors.New("monitor_block_unknown")
	// ErrBadPath: the link's segment is not one (unsafe in a URL path or too short).
	ErrBadPath = errors.New("monitor_path_bad")
)

// BadBlock is the block the document asked for and this panel does not have: the API
// answers with this error's text at body.blocks and this name as the value.
type BadBlock struct{ Block string }

func (e BadBlock) Error() string { return ErrBadBlock.Error() }
func (e BadBlock) Unwrap() error { return ErrBadBlock }

// ValidPath: what may stand in a URL path segment of its own and is not worth guessing.
func ValidPath(p string) bool {
	if len(p) < 8 || len(p) > 64 {
		return false
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// SetPassword puts a new password in the document; the old one and every session signed
// with it stop working at once. The caller saves the document after it.
func SetPassword(c *Config, password string) error {
	if len(password) < auth.MinPassword {
		return ErrWeakPassword
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	c.Hash = hash
	return nil
}

const (
	// CookieName is the page's own cookie. It shares the __Host- rules of the panel's
	// (Secure, Path=/, no Domain), so it is never sent anywhere but here.
	CookieName = "__Host-mikan-monitor"
	// SessionTTL is how long one login lasts; a session with less than half left is handed
	// a fresh one on the next request, so a screen left open stays.
	SessionTTL = 24 * time.Hour
)

// sessionKey is what the page's sessions are signed with: it comes from the stored
// password, so there is nothing extra to keep, a new password ends every session, and a
// restart of the panel does not log anyone out.
func sessionKey(hash string) []byte {
	sum := sha256.Sum256([]byte("mikan-monitor-session\x00" + hash))
	return sum[:]
}

// IssueSession is the cookie's value for a login that just happened.
func IssueSession(hash string, now time.Time) string {
	exp := strconv.FormatInt(now.Add(SessionTTL).Unix(), 10)
	id := secure.Token(16)
	return exp + "." + id + "." + mac(sessionKey(hash), exp, id)
}

// CheckSession says whether the cookie's value is one this password signed, and how long
// it has left. Anything else (another password, a hand-written value, an expired one) is
// no session.
func CheckSession(hash, value string, now time.Time) (left time.Duration, ok bool) {
	exp, id, sig, found := split3(value, ".")
	if !found || len(sig) != 64 {
		return 0, false
	}
	if !hmac.Equal([]byte(sig), []byte(mac(sessionKey(hash), exp, id))) {
		return 0, false
	}
	secs, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return 0, false
	}
	until := time.Unix(secs, 0)
	if !now.Before(until) {
		return 0, false
	}
	return until.Sub(now), true
}

// RenewSession: a session close to its end is handed a new one, so a page left open on a
// wall does not ask for the password again in the middle of a night.
func RenewSession(left time.Duration) bool { return left < SessionTTL/2 }

func mac(key []byte, parts ...string) string {
	h := hmac.New(sha256.New, key)
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func split3(s, sep string) (a, b, c string, ok bool) {
	i := strings.Index(s, sep)
	if i < 0 {
		return "", "", "", false
	}
	j := strings.Index(s[i+1:], sep)
	if j < 0 {
		return "", "", "", false
	}
	j += i + 1
	return s[:i], s[i+1 : j], s[j+1:], true
}
