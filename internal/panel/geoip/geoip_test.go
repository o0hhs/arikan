package geoip

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixtureDir lays the databases of testdata out as the lookup expects them: the file
// names it opens are fixed, whatever the database behind them is.
func fixtureDir(t *testing.T, country string) string {
	t.Helper()
	dir := t.TempDir()
	for src, dst := range map[string]string{country: countryFile, "asn.mmdb": asnFile} {
		if src == "" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("testdata", src))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, dst), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// The country and the network of an address, from the shapes the release carries: a bare
// code (MetaCubeX's own build) and the GeoIP2 one, plus GeoLite2's ASN.
func TestAddr(t *testing.T) {
	l := New(fixtureDir(t, "country-meta.mmdb"), quiet())
	got := l.Addr("203.0.113.7")
	if got.Country != "NL" || got.Flag != "🇳🇱" || got.ASN != 13335 || got.Org != "Cloudflare, Inc." {
		t.Fatalf("Addr = %+v", got)
	}
	if line := got.String(); line != "🇳🇱 NL · AS13335 Cloudflare, Inc." {
		t.Fatalf("String = %q", line)
	}
	// Nothing is said about what the databases do not cover, and about addresses no check
	// should ask about.
	for _, ip := range []string{"", "8.8.8.8", "not an ip", "127.0.0.1", "10.1.2.3", "::1", "169.254.1.1"} {
		if got := l.Addr(ip); got.Known() {
			t.Errorf("Addr(%q) = %+v, want nothing", ip, got)
		}
	}
}

func TestAddrMaxmindShape(t *testing.T) {
	l := New(fixtureDir(t, "country-maxmind.mmdb"), quiet())
	got := l.Addr("198.51.100.4")
	if got.Country != "DE" || got.Flag != "🇩🇪" || got.ASN != 0 || got.Org != "" {
		t.Fatalf("Addr = %+v", got)
	}
	if got := l.Addr("203.0.113.7"); got.Country != "" {
		t.Fatalf("the other database's range has a country: %+v", got)
	}
}

// A database on disk and fresh answers; nothing is fetched behind the panel's back.
func TestFreshDatabasesAreNotFetched(t *testing.T) {
	dir := fixtureDir(t, "country-meta.mmdb")
	l := New(dir, quiet())
	l.Addr("203.0.113.7")
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.fetched.IsZero() || l.fetching {
		t.Fatalf("a download was started with fresh databases: fetched=%v fetching=%v", l.fetched, l.fetching)
	}
}

// Old databases are fetched again — and a fetch that fails leaves the old ones in use.
func TestOldDatabasesAreFetchedAgain(t *testing.T) {
	dir := fixtureDir(t, "country-meta.mmdb")
	old := time.Now().Add(-ttl - time.Hour)
	for _, f := range []string{countryFile, asnFile} {
		if err := os.Chtimes(filepath.Join(dir, f), old, old); err != nil {
			t.Fatal(err)
		}
	}
	l := New(dir, quiet())
	// No network in a test: the fetch is what is asked for, its failure is expected.
	l.client = &http.Client{Transport: failing{}}
	l.Addr("203.0.113.7")
	l.mu.Lock()
	fetched := !l.fetched.IsZero()
	l.mu.Unlock()
	if !fetched {
		t.Fatal("old databases were not fetched again")
	}
	if got := l.Addr("203.0.113.7"); got.Country != "NL" {
		t.Fatalf("the old database stopped answering: %+v", got)
	}
}

// failing is a transport that never reaches anywhere.
type failing struct{}

func (failing) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no network in a test")
}

// What is not a database at all must not replace a working one: a captive portal answers
// a download too.
func TestNotADatabase(t *testing.T) {
	if verify([]byte("<html>login</html>")) {
		t.Fatal("a page passed as a database")
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "asn.mmdb"))
	if err != nil {
		t.Fatal(err)
	}
	if !verify(raw) {
		t.Fatal("a real database was refused")
	}
	if flag("NL") != "🇳🇱" || flag("X") != "" || flag("N1") != "" {
		t.Fatalf("flags: %q %q %q", flag("NL"), flag("X"), flag("N1"))
	}
}
