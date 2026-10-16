// Package geoip says where an address is: its country and the network it belongs to, read
// from the same databases mihomo itself reads for its GEOIP rules. The panel asks for an
// exit's address when it checks a node, and a country with an ASN says in one line what
// the address alone cannot: a node advertised as one country leaving through another, or
// an exit that is a cloud provider's address rather than an ISP's.
//
// The databases live in the panel's data directory and are downloaded once, in the
// background: a country database is a few megabytes — more than a binary wants to carry —
// and it ages (MetaCubeX's release is rebuilt monthly). Nothing waits for the download: an
// address with no database yet simply reports less.
package geoip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang"

	"mikan/internal/fsutil"
)

// Where the databases come from: the release mihomo's own geox-url points at. Both are in
// MaxMind's format; the country one carries only codes (MetaCubeX's own build), the ASN
// one MaxMind's (GeoLite2, the free mirror).
const (
	countryURL = "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.metadb"
	asnURL     = "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/GeoLite2-ASN.mmdb"

	countryFile = "country.mmdb"
	asnFile     = "asn.mmdb"

	// ttl is how long a database is used before it is fetched again: the release moves
	// monthly, and a country rarely does.
	ttl = 14 * 24 * time.Hour
	// retryGap bounds how often a download is retried after a failure or with nothing on
	// disk, however many addresses are asked about meanwhile.
	retryGap = 30 * time.Minute

	downloadTimeout = 3 * time.Minute
	maxSize         = 128 << 20
)

// Info is what the databases say about an address. Everything is left out that is not
// known: an address no database covers says nothing at all.
type Info struct {
	// Country is the ISO 3166-1 alpha-2 code in upper case, and Flag its flag: the letters
	// as regional indicators, which is how a flag is spelled. No table of names is kept —
	// the codes are the same everywhere, names are not.
	Country string
	Flag    string
	// ASN is the autonomous system number, 0 when it is not known, and Org whose it is.
	ASN int
	Org string
}

// Known is true when the databases had something to say.
func (i Info) Known() bool { return i.Country != "" || i.ASN != 0 }

// String is the one line the checks and the CLI show.
func (i Info) String() string {
	var parts []string
	if i.Country != "" {
		parts = append(parts, strings.TrimSpace(i.Flag+" "+i.Country))
	}
	if i.ASN != 0 {
		as := "AS" + fmt.Sprint(i.ASN)
		if i.Org != "" {
			as += " " + i.Org
		}
		parts = append(parts, as)
	}
	return strings.Join(parts, " · ")
}

// Lookup reads the databases of one directory. The zero value is not usable: New.
type Lookup struct {
	dir string
	log *slog.Logger

	mu       sync.Mutex
	country  *maxminddb.Reader
	asn      *maxminddb.Reader
	opened   time.Time // when the files were read
	fetched  time.Time // when a download was last started or given up on
	fetching bool
	client   *http.Client
}

// New returns a lookup over dir; the files in it are read on first use, and what is
// missing or older than their ttl is fetched in the background.
func New(dir string, log *slog.Logger) *Lookup {
	return &Lookup{dir: dir, log: log, client: &http.Client{Timeout: downloadTimeout}}
}

// Addr says where the address is. It never waits for a download: what is on disk answers,
// and a database that is missing or old is fetched for the next call.
func (l *Lookup) Addr(ip string) Info {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil || !a.IsValid() || a.Is4In6() || !a.IsGlobalUnicast() {
		return Info{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.open()
	var out Info
	if l.country != nil {
		out.Country = country(l.country, net.IP(a.AsSlice()))
		out.Flag = flag(out.Country)
	}
	if l.asn != nil {
		out.ASN, out.Org = autonomousSystem(l.asn, net.IP(a.AsSlice()))
	}
	l.fetchLater()
	return out
}

// open reads the files once, and again after a fetch replaced them.
func (l *Lookup) open() {
	if l.country != nil || l.asn != nil {
		if time.Since(l.opened) < ttl {
			return
		}
		l.close()
	}
	l.opened = time.Now()
	for file, into := range map[string]**maxminddb.Reader{countryFile: &l.country, asnFile: &l.asn} {
		r, err := maxminddb.Open(filepath.Join(l.dir, file))
		if err == nil {
			*into = r
		}
	}
}

func (l *Lookup) close() {
	for _, r := range []*maxminddb.Reader{l.country, l.asn} {
		if r != nil {
			_ = r.Close()
		}
	}
	l.country, l.asn = nil, nil
}

// fetchLater starts a download when one is owed — a file that is missing or older than its
// ttl — at most one at a time, and not more often than retryGap however many addresses are
// asked about. Called with mu held.
func (l *Lookup) fetchLater() {
	if l.fetching || time.Since(l.fetched) < retryGap {
		return
	}
	if age := l.age(countryFile); age >= 0 && age < ttl {
		if age := l.age(asnFile); age >= 0 && age < ttl {
			return
		}
	}
	l.fetching, l.fetched = true, time.Now()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
		defer cancel()
		if err := l.Refresh(ctx); err != nil && l.log != nil {
			l.log.Warn("geo databases", "err", err)
		}
		l.mu.Lock()
		l.fetching = false
		l.mu.Unlock()
	}()
}

// Refresh fetches what is missing or older than its ttl and reads it. A failure of one
// file does not stop the other, and an old file stays in use until its replacement is
// whole: the checks should say less, not nothing.
func (l *Lookup) Refresh(ctx context.Context) error {
	var errs []error
	for _, db := range []struct {
		url, file string
		age       time.Duration
	}{{countryURL, countryFile, l.age(countryFile)}, {asnURL, asnFile, l.age(asnFile)}} {
		if db.age >= 0 && db.age < ttl {
			continue
		}
		if err := l.fetch(ctx, db.url, db.file); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", db.file, err))
		}
	}
	l.mu.Lock()
	l.close()
	l.open()
	l.mu.Unlock()
	return errors.Join(errs...)
}

// age is how old the file is; negative when it is not there.
func (l *Lookup) age(file string) time.Duration {
	st, err := os.Stat(filepath.Join(l.dir, file))
	if err != nil {
		return -1
	}
	return time.Since(st.ModTime())
}

func (l *Lookup) fetch(ctx context.Context, url, file string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return err
	}
	if len(body) > maxSize {
		return errors.New("too large")
	}
	// What is not a database at all (a captive portal's page) must not replace a good file.
	if !verify(body) {
		return errors.New("not an mmdb")
	}
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(l.dir, file), body, 0o644)
}

func verify(raw []byte) bool {
	r, err := maxminddb.FromBytes(raw)
	if err != nil {
		return false
	}
	return r.Close() == nil
}

// countryRecord is the GeoLite2/GeoIP2 shape: what a MaxMind build of the country database
// holds. MetaCubeX's own build holds the code alone (or a list of them) — see country.
type countryRecord struct {
	Country struct {
		IsoCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
}

func country(r *maxminddb.Reader, ip net.IP) string {
	var rec countryRecord
	if err := r.Lookup(ip, &rec); err == nil && rec.Country.IsoCode != "" {
		return code(rec.Country.IsoCode)
	}
	// MetaCubeX's build: the record is the code itself, or a list of them where the ranges
	// of two countries meet.
	var record any
	if err := r.Lookup(ip, &record); err != nil {
		return ""
	}
	switch v := record.(type) {
	case string:
		return code(v)
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				return code(s)
			}
		}
	case map[string]any: // sing-geoip's {"code": "nl"}
		if s, ok := v["code"].(string); ok {
			return code(s)
		}
	}
	return ""
}

func code(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// asnRecord is the GeoLite2-ASN shape.
type asnRecord struct {
	Number uint32 `maxminddb:"autonomous_system_number"`
	Org    string `maxminddb:"autonomous_system_organization"`
}

func autonomousSystem(r *maxminddb.Reader, ip net.IP) (int, string) {
	var rec asnRecord
	if err := r.Lookup(ip, &rec); err != nil {
		return 0, ""
	}
	return int(rec.Number), strings.TrimSpace(rec.Org)
}

// flag spells a country code as its flag: the two letters as regional indicators.
func flag(code string) string {
	if len(code) != 2 {
		return ""
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		if r < 'A' || r > 'Z' {
			return ""
		}
		b.WriteRune(rune(0x1F1E6 + (r - 'A')))
	}
	return b.String()
}
