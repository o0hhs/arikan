package auth

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"mikan/internal/panel/secure"
)

const totpPeriod = 30

func NewTOTPKey(account string) (*otp.Key, error) {
	return totp.Generate(totp.GenerateOpts{Issuer: "Arikan", AccountName: account, Period: totpPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
}

// TOTPGuard validates codes and refuses to accept the same time step twice per admin
// while the panel runs. Validate gives the step it accepted; the caller spends that step
// in the store (ClaimAdminTOTPStep), and the row is what keeps a code one use across a
// restart — the memory alone would forget.
type TOTPGuard struct {
	mu   sync.Mutex
	used map[int64]int64 // admin id → last accepted step
}

func NewTOTPGuard() *TOTPGuard { return &TOTPGuard{used: map[int64]int64{}} }

// Validate returns the time step a code belongs to, ok=false when the code matches none.
// The window is the current step and its neighbours: a code typed at the edge of a
// rotation, or one from a clock a little off, still counts.
func (g *TOTPGuard) Validate(adminID int64, secret, code string, now time.Time) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return 0, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	step := now.Unix() / totpPeriod
	for _, s := range []int64{step, step - 1, step + 1} {
		if s <= g.used[adminID] {
			continue
		}
		ok, err := totp.ValidateCustom(code, secret, time.Unix(s*totpPeriod, 0).UTC(), totp.ValidateOpts{Period: totpPeriod, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err == nil && ok {
			g.used[adminID] = s
			return s, true
		}
	}
	return 0, false
}

// Recovery codes carry ~50 random bits and are rate limited like passwords, so a fast hash is enough.
func NewRecoveryCodes(n int) (plain []string, stored string) {
	hashes := make([]string, n)
	plain = make([]string, n)
	for i := range plain {
		c := strings.ToLower(secure.Token(10))
		plain[i] = c[:5] + "-" + c[5:]
		hashes[i] = secure.SHA256Hex(c)
	}
	raw, _ := json.Marshal(hashes)
	return plain, string(raw)
}

// UseRecoveryCode returns the updated list without the matched code, or ok=false.
func UseRecoveryCode(stored, code string) (rest string, ok bool) {
	var hashes []string
	if json.Unmarshal([]byte(stored), &hashes) != nil {
		return stored, false
	}
	h := secure.SHA256Hex(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(code)), "-", ""))
	for i, x := range hashes {
		if secure.Equal(x, h) {
			hashes = append(hashes[:i], hashes[i+1:]...)
			raw, _ := json.Marshal(hashes)
			return string(raw), true
		}
	}
	return stored, false
}
