package domain

import (
	"context"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
)

// Buying another tariff is a switch, not a renewal: the trial's remaining days are not
// added to the paid term, so a 3-day trial followed by a 30-day tariff ends in 30 days
// from the purchase, not 31, and the subscription takes the paid tariff's limits.
func TestPurchaseSwitchingTariffStartsItsTermNow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	trial, std := tariffs[0], tariffs[1] // 3 days, 1 device, 5 GiB; then 30 days, 3 devices, 150 GiB

	trialUser, err := users.Create(ctx, CreateInput{Name: "trial", TariffID: trial.ID, Source: UserFromTrial})
	if err != nil {
		t.Fatal(err)
	}
	if trialUser.ExpiresAt.Int64 != now.Add(3*24*time.Hour).Unix() {
		t.Fatalf("the trial runs to %d, want %d", trialUser.ExpiresAt.Int64, now.Add(3*24*time.Hour).Unix())
	}
	// A day and a half of it gone: what is left is what must not carry into the paid term.
	now = now.Add(36 * time.Hour)

	var got db.User
	err = st.Tx(ctx, func(q *db.Queries) error {
		var err error
		got, _, err = users.Purchase(ctx, q, PurchaseInput{UserID: trialUser.ID, TariffID: std.ID})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := now.Add(30 * 24 * time.Hour).Unix()
	if got.ExpiresAt.Int64 != want {
		t.Fatalf("expiry %d, want %d: the %d days left of the trial were stacked on", got.ExpiresAt.Int64, want,
			(got.ExpiresAt.Int64-want)/86400)
	}
	if got.TariffID.Int64 != std.ID || got.TariffID.Valid != true {
		t.Fatalf("tariff %v, want %d", got.TariffID, std.ID)
	}
	if got.TrafficLimit != std.TrafficLimit || got.DeviceLimit != std.DeviceLimit || got.ResetStrategy != std.ResetStrategy {
		t.Fatalf("limits %v/%v/%q, want %v/%v/%q", got.TrafficLimit, got.DeviceLimit, got.ResetStrategy,
			std.TrafficLimit, std.DeviceLimit, std.ResetStrategy)
	}
}

// The same tariff again is a renewal and still stacks: 30 days bought over a term with 10
// days left ends in 40 days, and the user's remaining days are not dropped.
func TestPurchaseSameTariffStillStacks(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	std := tariffs[1]

	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: std.ID})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(20 * 24 * time.Hour) // 10 days of the 30 left
	var got db.User
	err = st.Tx(ctx, func(q *db.Queries) error {
		var err error
		got, _, err = users.Purchase(ctx, q, PurchaseInput{UserID: u.ID, TariffID: std.ID})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(40 * 24 * time.Hour).Unix(); got.ExpiresAt.Int64 != want {
		t.Fatalf("expiry %d, want %d: the running term was dropped", got.ExpiresAt.Int64, want)
	}
}
