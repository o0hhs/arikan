package referral

import (
	"database/sql"
	"slices"
	"testing"

	"mikan/internal/panel/store/db"
)

// These are the parts of the days queue that are pure arithmetic and ordering: they run in
// any environment, without a database, so the ordering rule the user gave ("сначала будет
// идти самая дорогая и так до самой дешёвой") is checked even where the DB-backed suite
// cannot run.

// The queue is ordered by the bought tariff's price, dearest first, and by arrival between
// equals: a later cheaper purchase must never come before a running dearer one.
func TestGrantQueueOrder(t *testing.T) {
	grants := []Grant{
		{ID: 1, Price: 7000},
		{ID: 2, Price: 15000},
		{ID: 3, Price: 15000},
		{ID: 4, Price: 3000},
	}
	slices.SortStableFunc(grants, CompareGrants)
	got := make([]int64, len(grants))
	for i, g := range grants {
		got[i] = g.ID
	}
	// Dearest first; between equals the earlier arrival (the lower id) comes first.
	want := []int64{2, 3, 1, 4}
	if !slices.Equal(got, want) {
		t.Fatalf("the queue runs %v, wanted %v", got, want)
	}
}

// A tariff's price is the ruble price, else the Stars price, else nothing: a free or
// unpriced tariff sorts last rather than jumping the queue.
func TestTariffPrice(t *testing.T) {
	for _, c := range []struct {
		name string
		in   db.Tariff
		want int64
	}{
		{"rubles", db.Tariff{PriceRub: sql.NullInt64{Int64: 7000, Valid: true}, PriceStars: sql.NullInt64{Int64: 70, Valid: true}}, 7000},
		{"stars only", db.Tariff{PriceStars: sql.NullInt64{Int64: 150, Valid: true}}, 150},
		{"none", db.Tariff{}, 0},
		{"zero rubles", db.Tariff{PriceRub: sql.NullInt64{Int64: 0, Valid: true}}, 0},
	} {
		if got := tariffPrice(c.in); got != c.want {
			t.Errorf("%s: price %d, wanted %d", c.name, got, c.want)
		}
	}
}

// The reward's days are whole days from the moment it starts.
func TestGrantEndsAt(t *testing.T) {
	if got := grantEnd(1000, 10); got != 1000+10*daySeconds {
		t.Fatalf("10 days from 1000 ends at %d", got)
	}
	if daySeconds != 86400 {
		t.Fatalf("a day is %d seconds", daySeconds)
	}
}
