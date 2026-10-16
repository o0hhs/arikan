package addons

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
)

// fakeSourceStore is the two calls EnsureBuiltinSource makes, over the rows it holds.
type fakeSourceStore struct {
	mu     sync.Mutex
	rows   []db.AddonSource
	nextID int64
	// failAdd: the insert fails even with no row holding the url (a broken table).
	failAdd bool
}

func (f *fakeSourceStore) ListAddonSources(context.Context) ([]db.AddonSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]db.AddonSource(nil), f.rows...), nil
}

func (f *fakeSourceStore) AddAddonSource(_ context.Context, a db.AddAddonSourceParams) (db.AddonSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAdd {
		return db.AddonSource{}, errors.New("no room")
	}
	for _, r := range f.rows {
		if r.Url == a.Url {
			return db.AddonSource{}, errors.New("addons: duplicate key value (url)=(...)")
		}
	}
	f.nextID++
	s := db.AddonSource{ID: f.nextID, Url: a.Url, Key: a.Key, Title: a.Title, AddedBy: a.AddedBy, AddedAt: a.AddedAt}
	f.rows = append(f.rows, s)
	return s, nil
}

// The fork's own source is put in once, with the release key pinned and no admin behind it;
// a row the admin already added under the same address stands, and a second call changes
// nothing.
func TestEnsureBuiltinSource(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	q := &fakeSourceStore{}
	m := New("", "", "dev", nil, time.Now)
	if err := m.EnsureBuiltinSource(ctx, q, now); err != nil {
		t.Fatal(err)
	}
	rows, _ := q.ListAddonSources(ctx)
	if len(rows) != 1 {
		t.Fatalf("rows: %+v", rows)
	}
	s := rows[0]
	if s.Url != BuiltinSourceURL || s.Key != BuiltinSourceKey || s.Title != BuiltinSourceTitle || s.AddedAt != now.Unix() {
		t.Fatalf("the row: %+v", s)
	}
	if s.AddedBy.Valid {
		t.Fatalf("the panel's own source has no admin behind it: %+v", s.AddedBy)
	}
	if !IsBuiltinSource(s.Url) || IsBuiltinSource("github.com/someone/else") {
		t.Fatal("IsBuiltinSource")
	}
	// A second call finds it and adds nothing.
	if err := m.EnsureBuiltinSource(ctx, q, now); err != nil {
		t.Fatal(err)
	}
	if rows, _ = q.ListAddonSources(ctx); len(rows) != 1 {
		t.Fatalf("doubled: %+v", rows)
	}
	// One the admin added by hand, with a key of their own, is left as it is.
	own := &fakeSourceStore{rows: []db.AddonSource{{ID: 7, Url: BuiltinSourceURL, Key: "something", Title: "mine"}}}
	if err := m.EnsureBuiltinSource(ctx, own, now); err != nil {
		t.Fatal(err)
	}
	if rows, _ = own.ListAddonSources(ctx); len(rows) != 1 || rows[0].Key != "something" || rows[0].Title != "mine" {
		t.Fatalf("the admin's row was touched: %+v", rows)
	}
	// An insert that fails while the row is there (a race) is no error; one that fails with
	// nothing to show for it is.
	race := &fakeSourceStore{rows: []db.AddonSource{{ID: 1, Url: BuiltinSourceURL}}}
	if err := m.EnsureBuiltinSource(ctx, race, now); err != nil {
		t.Fatalf("a row that appeared meanwhile: %v", err)
	}
	if err := m.EnsureBuiltinSource(ctx, &fakeSourceStore{failAdd: true}, now); err == nil || !strings.Contains(err.Error(), "no room") {
		t.Fatalf("a table that takes nothing: %v", err)
	}
}
