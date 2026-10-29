package nodesync

import (
	"context"
	"errors"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subs"
)

// fakeSubsNode is the node API's subscription calls over the fake of sync_test.go: it keeps
// the records like the node's store does, generation and all, so a test sees what a node
// would answer.
type fakeSubsNode struct {
	*fakeNode
	subs   map[string]int64 // path → the generation it was sent with
	prunes []int64
	fail   error
}

func (f *fakeSubsNode) PutSubs(_ context.Context, gen int64, records []nodeapi.SubRecord) (nodeapi.SubsResult, error) {
	if f.fail != nil {
		return nodeapi.SubsResult{}, f.fail
	}
	if f.subs == nil {
		f.subs = map[string]int64{}
	}
	for _, r := range records {
		f.subs[r.Path] = gen
	}
	return nodeapi.SubsResult{Stored: len(records), Bytes: int64(len(f.subs))}, nil
}

func (f *fakeSubsNode) PruneSubs(_ context.Context, gen int64) (nodeapi.SubsResult, error) {
	if f.fail != nil {
		return nodeapi.SubsResult{}, f.fail
	}
	removed := 0
	for p, g := range f.subs {
		if g < gen {
			delete(f.subs, p)
			removed++
		}
	}
	f.prunes = append(f.prunes, gen)
	return nodeapi.SubsResult{Removed: removed, Bytes: int64(len(f.subs))}, nil
}

// subsTest is a syncer and a manager with a node that keeps subscriptions, the setting on and
// a renderer that answers a record per user.
func subsTest(t *testing.T) (*Syncer, *fakeSubsNode, *domain.Users, *time.Time) {
	t.Helper()
	s, node, st, users, now := setup(t)
	fake := &fakeSubsNode{fakeNode: node}
	s.node = fake
	s.m.SetSubsRenderer(func(_ context.Context, u db.User) (nodeapi.SubRecord, error) {
		return nodeapi.SubRecord{Path: u.SubToken, Expires: s.m.now().Add(time.Hour),
			Bodies: []nodeapi.SubBody{{Format: nodeapi.SubFormatURI, Body: []byte("links")}}}, nil
	})
	ctx := context.Background()
	if err := settings.Set(ctx, settings.New(st.Q), settings.KeySubsFromNodes, subs.FromNodes{Enabled: true, Nodes: []int64{LocalNode}}); err != nil {
		t.Fatal(err)
	}
	return s, fake, users, now
}

func TestSubsPushedAndRevoked(t *testing.T) {
	s, fake, users, _ := subsTest(t)
	ctx := context.Background()
	m := s.m
	tariffs, err := m.st.Q.ListTariffs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var tokens []string
	for _, name := range []string{"a", "b"} {
		u, err := users.Create(ctx, domain.CreateInput{Name: name, TariffID: tariffs[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, u.SubToken)
	}
	m.pushSubs(ctx)
	if len(fake.subs) != 2 {
		t.Fatalf("the node keeps %d records, want 2: %v", len(fake.subs), fake.subs)
	}
	for _, tok := range tokens {
		if _, ok := fake.subs[tok]; !ok {
			t.Fatalf("a user's path was not pushed")
		}
	}
	p := m.SubsStatus()[LocalNode]
	if p.Err != "" || p.Records != 2 || p.At.IsZero() || p.Generation == 0 {
		t.Fatalf("the push status: %+v", p)
	}
	// A user deleted in the panel is gone from the node at the next push: the prune drops
	// every record not sent with its generation.
	all, err := m.st.Q.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := users.Delete(ctx, all[0].ID); err != nil {
		t.Fatal(err)
	}
	m.pushSubs(ctx)
	if len(fake.subs) != 1 {
		t.Fatalf("after the delete the node keeps %d records: %v", len(fake.subs), fake.subs)
	}
	// A block is a new policy and a new record: the node gets both, the path stays.
	off := true
	if _, err := users.Update(ctx, all[1].ID, domain.Patch{Disabled: &off}); err != nil {
		t.Fatal(err)
	}
	m.pushSubs(ctx)
	if len(fake.subs) != 1 {
		t.Fatalf("after a block: %v", fake.subs)
	}
	if p := m.SubsStatus()[LocalNode]; p.Err != "" || p.At.Before(m.now().Add(-time.Minute)) {
		t.Fatalf("the block's push: %+v", p)
	}
}

func TestSubsPushFailsWholeWithoutPrune(t *testing.T) {
	s, fake, users, _ := subsTest(t)
	ctx := context.Background()
	m := s.m
	tariffs, _ := m.st.Q.ListTariffs(ctx)
	if _, err := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID}); err != nil {
		t.Fatal(err)
	}
	m.pushSubs(ctx)
	if len(fake.subs) != 1 {
		t.Fatalf("the first push: %v", fake.subs)
	}
	// The node stops taking records halfway: nothing is pruned, so the node keeps answering
	// what it has — which is the point of the cache.
	fake.fail = errors.New("node unavailable")
	m.pushSubs(ctx)
	if len(fake.subs) != 1 || len(fake.prunes) != 1 {
		t.Fatalf("a failed push pruned: %v prunes %v", fake.subs, fake.prunes)
	}
	if p := m.SubsStatus()[LocalNode]; p.Err == "" {
		t.Fatalf("a failed push is not reported: %+v", p)
	}
	fake.fail = nil
	m.pushSubs(ctx)
	if len(fake.subs) != 1 || len(fake.prunes) != 2 {
		t.Fatalf("the push after the failure: %v prunes %v", fake.subs, fake.prunes)
	}
	if p := m.SubsStatus()[LocalNode]; p.Err != "" || p.Records != 1 {
		t.Fatalf("the push after the failure: %+v", p)
	}
}

func TestSubsPushOffWhenSettingOffOrNodeNotChosen(t *testing.T) {
	s, fake, users, _ := subsTest(t)
	ctx := context.Background()
	m := s.m
	tariffs, _ := m.st.Q.ListTariffs(ctx)
	if _, err := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID}); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []subs.FromNodes{{Enabled: false, Nodes: []int64{LocalNode}}, {Enabled: true, Nodes: []int64{LocalNode + 1}}} {
		if err := settings.Set(ctx, settings.New(m.st.Q), settings.KeySubsFromNodes, cfg); err != nil {
			t.Fatal(err)
		}
		m.pushSubs(ctx)
		if len(fake.subs) != 0 {
			t.Fatalf("%+v pushed: %v", cfg, fake.subs)
		}
	}
}

// A user without a slot (imported halfway, say) must not stop the others: the push skips it.
func TestSubsPushSkipsAUserItCannotRender(t *testing.T) {
	s, fake, users, _ := subsTest(t)
	ctx := context.Background()
	m := s.m
	tariffs, _ := m.st.Q.ListTariffs(ctx)
	if _, err := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID}); err != nil {
		t.Fatal(err)
	}
	s.m.SetSubsRenderer(func(_ context.Context, u db.User) (nodeapi.SubRecord, error) {
		if u.Name == "a" {
			return nodeapi.SubRecord{}, subs.ErrNoSlot
		}
		return nodeapi.SubRecord{Path: u.SubToken, Expires: m.now().Add(time.Hour), Bodies: []nodeapi.SubBody{{Format: "uri", Body: []byte("x")}}}, nil
	})
	if _, err := users.Create(ctx, domain.CreateInput{Name: "b", TariffID: tariffs[1].ID}); err != nil {
		t.Fatal(err)
	}
	m.pushSubs(ctx)
	if len(fake.subs) != 1 {
		t.Fatalf("the renderable user was not pushed: %v", fake.subs)
	}
	if p := m.SubsStatus()[LocalNode]; p.Records != 1 {
		t.Fatalf("status: %+v", p)
	}
}
