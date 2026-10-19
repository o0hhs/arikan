package monitor

import (
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/auth"
)

// A session is signed with a key of the stored password: the same password opens it, a new
// password ends it, and nothing else passes.
func TestSessionsAreSignedByThePassword(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var c Config
	if err := SetPassword(&c, "monitor-password-1"); err != nil {
		t.Fatal(err)
	}
	cookie := IssueSession(c.Hash, now)
	left, ok := CheckSession(c.Hash, cookie, now)
	if !ok || left > SessionTTL || left < SessionTTL-time.Minute {
		t.Fatalf("a fresh session: %v %v", left, ok)
	}
	// Past halfway: the page is handed a new one rather than being asked again; a fresh one
	// is left alone.
	old := IssueSession(c.Hash, now.Add(-20*time.Hour))
	if near, ok := CheckSession(c.Hash, old, now); !ok || !RenewSession(near) {
		t.Fatalf("a session four hours from its end: %v %v", near, ok)
	}
	if RenewSession(left) {
		t.Fatal("a fresh session is renewed")
	}
	// Expired: the cookie is worthless.
	if _, ok := CheckSession(c.Hash, cookie, now.Add(SessionTTL)); ok {
		t.Fatal("an expired session was accepted")
	}
	// Another password: the same cookie is worthless, without anything to look up.
	var other Config
	if err := SetPassword(&other, "monitor-password-2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := CheckSession(other.Hash, cookie, now); ok {
		t.Fatal("a session survived the password it was signed with")
	}
	// Anything hand-made is worthless too.
	for _, bad := range []string{"", "x", "1.2.3", cookie + "0", "99999999999.abcdef." + strings.Repeat("0", 64), cookie[:len(cookie)-1]} {
		if _, ok := CheckSession(c.Hash, bad, now); ok {
			t.Fatalf("a made-up session was accepted: %q", bad)
		}
	}
}

func TestSetPasswordHashesAndRefusesShort(t *testing.T) {
	var c Config
	if err := SetPassword(&c, strings.Repeat("a", auth.MinPassword-1)); err != ErrWeakPassword {
		t.Fatalf("a short password: %v", err)
	}
	if c.Hash != "" {
		t.Fatalf("a refused password was stored: %q", c.Hash)
	}
	if err := SetPassword(&c, "monitor-password-1"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.Hash, "$argon2id$") || strings.Contains(c.Hash, "monitor-password-1") {
		t.Fatalf("the stored hash: %q", c.Hash)
	}
	if ok, err := auth.VerifyPassword("monitor-password-1", c.Hash); err != nil || !ok {
		t.Fatalf("the password does not verify: %v %v", ok, err)
	}
	if ok, _ := auth.VerifyPassword("monitor-password-2", c.Hash); ok {
		t.Fatal("another password verified")
	}
}

// The link's segment goes into the panel's own routing: nothing that could stand for
// another path, and nothing short enough to guess.
func TestValidPath(t *testing.T) {
	for _, ok := range []string{"abcdefgh", "Monitor-Link_1234", strings.Repeat("a", 64)} {
		if !ValidPath(ok) {
			t.Errorf("ValidPath(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "short", "with/slash", "with.dot", "with space", "with%20", strings.Repeat("a", 65), "ключ"} {
		if ValidPath(bad) {
			t.Errorf("ValidPath(%q) = true", bad)
		}
	}
}

// What is stored is what the page shows, in the panel's own order; a block this panel does
// not know is dropped rather than shown.
func TestKnownBlocks(t *testing.T) {
	got := knownBlocks([]string{"payments", "summary", "nonsense", "nodes"})
	want := []string{"summary", "nodes", "payments"}
	if len(got) != len(want) {
		t.Fatalf("knownBlocks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("knownBlocks = %v, want %v", got, want)
		}
	}
	if len(knownBlocks(nil)) != 0 {
		t.Fatal("a page the admin emptied shows nothing")
	}
}

// A page nobody set up shows the service's traffic and its nodes, and is not ready to open
// until it has both a password and a link.
func TestDefaultIsOffAndGuarded(t *testing.T) {
	d := Default()
	if d.Enabled || d.Ready() || d.Hash != "" {
		t.Fatalf("a page nobody set up: %+v", d)
	}
	if !d.Show("summary") || !d.Show("traffic") || !d.Show("nodes") || d.Show("payments") || d.Show("sites") {
		t.Fatalf("what it would show: %v", d.Blocks)
	}
	// A link without a password opens nothing, and a password without a link is nowhere.
	d.Enabled, d.Path = true, "monitor-link-1234"
	if d.Ready() {
		t.Fatal("a page turned on without a password is ready")
	}
	if err := SetPassword(&d, "monitor-password-1"); err != nil {
		t.Fatal(err)
	}
	if !d.Ready() {
		t.Fatal("a page with a link and a password is not ready")
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	d.Blocks = []string{"summary", "nonsense"}
	if err := d.Validate(); err != ErrBadBlock {
		t.Fatalf("an unknown block: %v", err)
	}
	d.Blocks = []string{"summary"}
	d.Path = "with/slash"
	if err := d.Validate(); err != ErrBadPath {
		t.Fatalf("an unsafe link: %v", err)
	}
}
