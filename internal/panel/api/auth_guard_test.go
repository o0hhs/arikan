package api

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/pquerna/otp/totp"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// A stolen session must not guess the admin's password through the password form, the
// 2FA switch or the code of a pending 2FA setup: wrong answers there count against the
// same allowance as every reauth.
func TestPasswordFormsCountWrongAnswers(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hash, err := auth.HashPassword("right-password-123")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := st.Q.CreateAdmin(ctx, db.CreateAdminParams{Username: "admin", PasswordHash: hash, CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	h := &handlers{d: Deps{Store: st, Now: func() time.Time { return now }, TOTP: auth.NewTOTPGuard(),
		UserLimit: auth.NewLimiter(3, 10*time.Minute, 15*time.Minute, time.Hour)}, hashSem: make(chan struct{}, 4)}
	sess := context.WithValue(ctx, keySession, db.Session{AdminID: admin.ID})

	status := func(err error) int {
		var he huma.StatusError
		if errors.As(err, &he) {
			return he.GetStatus()
		}
		return 0
	}
	for i := range 3 {
		in := &passwordInput{}
		in.Body.Current, in.Body.New = "wrong", "new-password-12345"
		if _, err := h.changePassword(sess, in); status(err) != 422 {
			t.Fatalf("wrong password %d: %v", i, err)
		}
	}
	// The allowance is spent: even the right password waits now, and so does the 2FA form.
	in := &passwordInput{}
	in.Body.Current, in.Body.New = "right-password-123", "new-password-12345"
	if _, err := h.changePassword(sess, in); status(err) != 429 {
		t.Fatalf("password form after the allowance: %v", err)
	}
	if err := st.Q.SetAdminTOTP(ctx, db.SetAdminTOTPParams{ID: admin.ID, TotpSecret: sql.NullString{String: "JBSWY3DPEHPK3PXP", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	off := &totpDisableInput{}
	off.Body.Password, off.Body.Code = "right-password-123", "000000"
	if _, err := h.totpDisable(sess, off); status(err) != 429 {
		t.Fatalf("2FA form after the allowance: %v", err)
	}
	// So does the code that would turn a pending setup on.
	h.pending = map[int64]pendingTOTP{admin.ID: {secret: "JBSWY3DPEHPK3PXP", expires: now.Add(time.Minute)}}
	on := &totpCodeInput{}
	on.Body.Code = "000000"
	if _, err := h.totpEnable(sess, on); status(err) != 429 {
		t.Fatalf("2FA setup code after the allowance: %v", err)
	}
}

// A code is spent in the admin's row, not in the process: a restart between two tries does
// not give the same code a second life.
func TestTOTPCodeIsSpentAcrossRestarts(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	admin, err := st.Q.CreateAdmin(ctx, db.CreateAdminParams{Username: "admin", PasswordHash: "x", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	key, err := auth.NewTOTPKey("admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Q.SetAdminTOTP(ctx, db.SetAdminTOTPParams{TotpSecret: sql.NullString{String: key.Secret(), Valid: true}, ID: admin.ID}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	code, err := totp.GenerateCode(key.Secret(), now)
	if err != nil {
		t.Fatal(err)
	}
	// Every process gets its own guard: the second one knows nothing of the first.
	restart := func() *handlers {
		return &handlers{d: Deps{Store: st, Now: func() time.Time { return now }, TOTP: auth.NewTOTPGuard()}}
	}
	if ok, err := restart().spendTOTPCode(ctx, admin.ID, key.Secret(), code, now); err != nil || !ok {
		t.Fatalf("the first use: %v %v", ok, err)
	}
	if ok, err := restart().spendTOTPCode(ctx, admin.ID, key.Secret(), code, now); err != nil || ok {
		t.Fatalf("the same code after a restart: %v %v, want a refusal", ok, err)
	}
	// The next step's code is a new code and works.
	next, err := totp.GenerateCode(key.Secret(), now.Add(31*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := restart().spendTOTPCode(ctx, admin.ID, key.Secret(), next, now.Add(31*time.Second)); err != nil || !ok {
		t.Fatalf("the next code: %v %v", ok, err)
	}
}
