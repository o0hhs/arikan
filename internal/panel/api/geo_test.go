package api

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"mikan/internal/panel/geoip"
)

// The check items of a node carry where the addresses they name are: the exit's and the
// node's own. The databases are the geoip package's own fixtures — the same shapes the
// release has.
func TestGeoParams(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"country-meta.mmdb", "asn.mmdb"} {
		raw, err := os.ReadFile(filepath.Join("..", "geoip", "testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		name := "country.mmdb"
		if f == "asn.mmdb" {
			name = "asn.mmdb"
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := &handlers{d: Deps{Geo: geoip.New(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))}}
	params := map[string]string{"ip": "203.0.113.7"}
	h.geoParams(params, "203.0.113.7")
	if params["country"] != "NL" || params["flag"] != "🇳🇱" || params["asn"] != "13335" || params["org"] != "Cloudflare, Inc." {
		t.Fatalf("params = %+v", params)
	}
	// An address the databases do not cover adds nothing, and neither does an empty one.
	for _, ip := range []string{"8.8.8.8", ""} {
		other := map[string]string{}
		h.geoParams(other, ip)
		if len(other) != 0 {
			t.Fatalf("params for %q = %+v", ip, other)
		}
	}
}
