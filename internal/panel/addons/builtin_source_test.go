package addons

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
	"mikan/internal/release"
)

// fakeSourceStore is the calls EnsureBuiltinSource makes, over the rows it holds.
type fakeSourceStore struct {
	mu     sync.Mutex
	rows   []db.AddonSource
	nextID int64
	// failAdd: the insert fails even with no row holding the url (a broken table).
	failAdd bool
	// failUpdate: the repair of a row already there fails (a broken table).
	failUpdate bool
	// updates: the repairs made, in order.
	updates []db.UpdateAddonSourceParams
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

func (f *fakeSourceStore) UpdateAddonSource(_ context.Context, a db.UpdateAddonSourceParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failUpdate {
		return errors.New("no room")
	}
	for i := range f.rows {
		if f.rows[i].ID == a.ID {
			f.rows[i].Key, f.rows[i].Title = a.Key, a.Title
			f.updates = append(f.updates, a)
			return nil
		}
	}
	return errors.New("no such source")
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

// A panel upgraded from a release that shipped no source of its own: the admin added the
// address by hand, so the row holds no key and no title. It is the panel's own address and is
// repaired in place — with no key the source is shown as unsigned ("встроенный без подписи")
// and its signed file cannot be verified (source_key_needed).
func TestEnsureBuiltinSourceRepairsEmptyKey(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	m := New("", "", "dev", nil, time.Now)
	q := &fakeSourceStore{rows: []db.AddonSource{{ID: 7, Url: BuiltinSourceURL, AddedAt: 1_700_000_000}}}
	if err := m.EnsureBuiltinSource(ctx, q, now); err != nil {
		t.Fatal(err)
	}
	rows, _ := q.ListAddonSources(ctx)
	if len(rows) != 1 {
		t.Fatalf("the repair added a row instead of fixing one: %+v", rows)
	}
	s := rows[0]
	if s.ID != 7 || s.Key != BuiltinSourceKey || s.Title != BuiltinSourceTitle {
		t.Fatalf("the row: %+v", s)
	}
	if s.AddedAt != 1_700_000_000 {
		t.Fatalf("the repair rewrote when the admin added it: %+v", s)
	}
	if len(q.updates) != 1 || q.updates[0].ID != 7 {
		t.Fatalf("updates: %+v", q.updates)
	}
	// A second call finds the key and the title there and writes nothing more.
	if err := m.EnsureBuiltinSource(ctx, q, now); err != nil {
		t.Fatal(err)
	}
	if len(q.updates) != 1 {
		t.Fatalf("repaired twice: %+v", q.updates)
	}
	// A key and a title the admin wrote are theirs, and both survive a call that finds them.
	title := &fakeSourceStore{rows: []db.AddonSource{{ID: 8, Url: BuiltinSourceURL, Key: "something"}}}
	if err := m.EnsureBuiltinSource(ctx, title, now); err != nil {
		t.Fatal(err)
	}
	rows, _ = title.ListAddonSources(ctx)
	if len(rows) != 1 || rows[0].Key != "something" || rows[0].Title != BuiltinSourceTitle {
		t.Fatalf("the admin's key: %+v", rows)
	}
	if len(title.updates) != 1 || title.updates[0].Key != "something" {
		t.Fatalf("updates: %+v", title.updates)
	}
	// A table that takes no repair is an error the caller logs, not a panel that cannot start.
	broken := &fakeSourceStore{failUpdate: true, rows: []db.AddonSource{{ID: 9, Url: BuiltinSourceURL}}}
	if err := m.EnsureBuiltinSource(ctx, broken, now); err == nil || !strings.Contains(err.Error(), "no room") {
		t.Fatalf("a table that takes no repair: %v", err)
	}
}

// The repaired row reads the fork's own signed file: the key it now pins is a real one, and a
// document signed with that key verifies with no pin from the admin (the source_key_needed
// answer the empty row used to get). The file here declares no key of its own, as the release
// publishes it after this fix.
func TestRepairedBuiltinSourceVerifiesSignedDoc(t *testing.T) {
	f := newFileServer(t)
	m := srcManager(t, devNet())
	ctx := t.Context()
	author := newPriv(t)
	authorKey := base64.StdEncoding.EncodeToString(author.Public().(ed25519.PublicKey))
	BuiltinSourceKey = authorKey
	t.Cleanup(func() { BuiltinSourceKey = release.PublicKey })

	data, err := json.Marshal(map[string]any{"version": 1, "adapters": []any{entryDoc("hello", "b")}})
	if err != nil {
		t.Fatal(err)
	}
	f.put("/mikan-addon.json", data)
	f.put("/mikan-addon.json.sig", []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(author, data))))

	q := &fakeSourceStore{rows: []db.AddonSource{{ID: 1, Url: BuiltinSourceURL}}}
	if err := m.EnsureBuiltinSource(ctx, q, time.Unix(1_800_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	rows, _ := q.ListAddonSources(ctx)
	if rows[0].Key != authorKey || rows[0].Title != BuiltinSourceTitle {
		t.Fatalf("the row was not repaired: %+v", rows[0])
	}
	// The row's key is what a fetch pins, and it is Signed: no source_key_needed.
	pin := SourcePin{Key: rows[0].Key, Unsigned: rows[0].Key == ""}
	doc, err := m.FetchSource(ctx, f.srv.URL+"/mikan-addon.json", pin)
	if err != nil {
		t.Fatalf("the panel's own source: %v", err)
	}
	if doc.Key != authorKey || len(doc.Entries) != 1 {
		t.Fatalf("the doc: %+v", doc)
	}
}
