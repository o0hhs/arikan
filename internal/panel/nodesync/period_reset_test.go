package nodesync

import (
	"context"
	"testing"
	"time"

	"mikan/internal/panel/domain"
)

// A monthly or periodic reset is the start of a new paid period. A subscription that has
// ended has no new period: the scheduled reset must leave its counters as they are, so the
// list and the card keep showing the traffic that was really used.
func TestResetPeriodsSkipsExpiredUsers(t *testing.T) {
	s1, _, st, users, now := setup(t)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, err := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID, Options: &domain.Options{ResetStrategy: "period"}})
	if err != nil {
		t.Fatal(err)
	}
	old := now.Add(-40 * 24 * time.Hour).Unix()
	if _, err := st.DB.ExecContext(ctx, "UPDATE users SET period_start = $1, used_up = 500, used_down = 100, expires_at = $2 WHERE id = $3",
		old, now.Add(-time.Hour).Unix(), u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s1.m.resetPeriods(ctx, *now); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Q.GetUser(ctx, u.ID)
	if got.UsedUp != 500 || got.UsedDown != 100 || got.PeriodStart != old {
		t.Fatalf("an expired user was reset: used %d/%d, period_start %d (want %d)", got.UsedUp, got.UsedDown, got.PeriodStart, old)
	}
	// The same period still running for a live subscription resets.
	if _, err := st.DB.ExecContext(ctx, "UPDATE users SET expires_at = $1 WHERE id = $2",
		now.Add(30*24*time.Hour).Unix(), u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s1.m.resetPeriods(ctx, *now); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Q.GetUser(ctx, u.ID)
	if got.UsedUp != 0 || got.UsedDown != 0 {
		t.Fatalf("a live user was not reset: used %d/%d", got.UsedUp, got.UsedDown)
	}
}
