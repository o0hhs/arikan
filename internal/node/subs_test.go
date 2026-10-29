package node

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/nodeapi"
)

// subClock is the store's and the server's time, moved by a test.
type subClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *subClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *subClock) add(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

func testSubs(t *testing.T) (*subStore, *subClock, string) {
	t.Helper()
	clock := &subClock{at: time.Unix(1_800_000_000, 0)}
	dir := filepath.Join(t.TempDir(), SubsDir)
	return openSubs(dir, clock.now, slog.New(slog.DiscardHandler)), clock, dir
}

// testRecord is one subscription with the three profile bodies and the page.
func testRecord(path string, expires time.Time) nodeapi.SubRecord {
	return nodeapi.SubRecord{Path: path, Expires: expires, Headers: map[string]string{"Subscription-Userinfo": "upload=1; download=2; total=3; expire=0"},
		Bodies: []nodeapi.SubBody{
			{Format: nodeapi.SubFormatURI, Type: "text/plain; charset=utf-8", Body: []byte("dmxlc3M6Ly9saW5r")},
			{Format: nodeapi.SubFormatClash, Type: "text/yaml; charset=utf-8", Name: "VPN.yaml", Body: []byte("proxies: []\n")},
			{Format: nodeapi.SubFormatSingBox, Type: "application/json; charset=utf-8", Name: "VPN.json", Body: []byte(`{"outbounds":[]}`)},
			{Format: nodeapi.SubFormatHTML, Type: "text/html; charset=utf-8", Body: []byte("<!doctype html><html></html>")},
		}}
}

const testToken = "AbCdEf0123456789xyzXYZ"

func TestSubStoreKeepsWhatThePanelSent(t *testing.T) {
	store, clock, dir := testSubs(t)
	if err := store.put(7, testRecord(testToken, clock.now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	lk, ok := store.lookup(testToken)
	if !ok || lk.expired || len(lk.rec.Bodies) != 4 || lk.rec.Headers["Subscription-Userinfo"] == "" {
		t.Fatalf("the record: %+v %v", lk, ok)
	}
	// The file is named after the token's hash, never the token, and only its owner reads it.
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("dir: %v %v", entries, err)
	}
	if strings.Contains(entries[0].Name(), testToken) || entries[0].Name() != subFileOf(testToken) {
		t.Fatalf("the file is named %q", entries[0].Name())
	}
	info, err := entries[0].Info()
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info.Mode().Perm(), err)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v %v", st.Mode().Perm(), err)
	}
	// A node restarted keeps answering: the store reads its files back.
	again := openSubs(dir, clock.now, slog.New(slog.DiscardHandler))
	if _, ok := again.lookup(testToken); !ok {
		t.Fatal("a restarted store lost the record")
	}
	// Nothing about a subscriber is in the file's name, and the status counts without paths.
	raw, _ := json.Marshal(again.status())
	if bytes.Contains(raw, []byte(testToken)) || strings.Contains(string(raw), testToken) {
		t.Fatalf("the status names a path: %s", raw)
	}
}

func TestSubStoreExpiryRevocationAndPrune(t *testing.T) {
	store, clock, _ := testSubs(t)
	short := testRecord("ShortLivedToken00000001", clock.now().Add(time.Minute))
	long := testRecord("LongLivedToken000000001", clock.now().Add(time.Hour))
	for _, r := range []nodeapi.SubRecord{short, long} {
		if err := store.put(1, r); err != nil {
			t.Fatal(err)
		}
	}
	clock.add(2 * time.Minute)
	if lk, ok := store.lookup(short.Path); !ok || !lk.expired {
		t.Fatalf("the expired record: %+v %v", lk, ok)
	}
	if lk, ok := store.lookup(long.Path); !ok || lk.expired {
		t.Fatalf("the live record: %+v %v", lk, ok)
	}
	if st := store.status(); st.Records != 2 || st.Expired != 1 {
		t.Fatalf("status: %+v", st)
	}
	// Removal is at once; the prune drops everything not of its generation.
	if n := store.remove([]string{long.Path}); n != 1 {
		t.Fatalf("removed %d", n)
	}
	if _, ok := store.lookup(long.Path); ok {
		t.Fatal("a removed record is still served")
	}
	if n := store.remove([]string{"neverThere00000000000"}); n != 0 {
		t.Fatalf("removing an unknown path removed %d", n)
	}
	if err := store.put(5, long); err != nil {
		t.Fatal(err)
	}
	if err := store.put(6, testRecord("AnotherToken00000000001", clock.now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if n := store.prune(6); n != 2 { // the short-lived one of generation 1 and the long one of 5
		t.Fatalf("pruned %d", n)
	}
	if _, ok := store.lookup(long.Path); ok {
		t.Fatal("a record of an older generation survived the prune")
	}
	if st := store.status(); st.Records != 1 || st.Generation != 6 || st.SyncedAt.IsZero() {
		t.Fatalf("after the prune: %+v", st)
	}
	if got := store.paths(); len(got) != 1 || got[0] != "AnotherToken00000000001" {
		t.Fatalf("paths: %v", got)
	}
}

func TestSubStoreRefusesWhatItCannotServe(t *testing.T) {
	store, clock, _ := testSubs(t)
	for _, path := range []string{"", "..", "../etc/passwd", "a/b", "with space", strings.Repeat("x", nodeapi.MaxSubPath+1)} {
		if err := store.put(1, testRecord(path, clock.now().Add(time.Hour))); err != ErrSubBadPath {
			t.Errorf("path %q: %v", path, err)
		}
	}
	rec := testRecord(testToken, clock.now().Add(time.Hour))
	rec.Bodies = []nodeapi.SubBody{{Format: nodeapi.SubFormatURI, Body: make([]byte, nodeapi.MaxSubRecordBytes+1)}}
	if err := store.put(1, rec); err != ErrSubTooBig {
		t.Fatalf("a record past the limit: %v", err)
	}
	if st := store.status(); st.Records != 0 {
		t.Fatalf("a refused record was stored: %+v", st)
	}
}

// testSubsHTTP serves the subscription handler over plain HTTP: what a client gets is the
// handler's answer, the HTTPS part is the router's own test below.
func testSubsHTTP(t *testing.T, store *subStore) *httptest.Server {
	t.Helper()
	srv := newSubServer(store, slog.New(slog.DiscardHandler))
	srv.apply(&nodeapi.SubsServer{Names: []string{"sub.example.com"}}, nil)
	srv.place("router", "")
	ts := httptest.NewServer(http.HandlerFunc(srv.serve))
	t.Cleanup(func() {
		ts.Close()
		_ = srv.srv.Close()
		_ = srv.ln.Close()
	})
	return ts
}

func TestSubsAnsweredByFormat(t *testing.T) {
	store, clock, _ := testSubs(t)
	if err := store.put(1, testRecord(testToken, clock.now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	ts := testSubsHTTP(t, store)
	get := func(path, ua, accept string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if ua != "" {
			req.Header.Set("User-Agent", ua)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(body)
	}
	resp, body := get("/"+testToken, "v2rayNG/1.9", "")
	if resp.StatusCode != http.StatusOK || body != "dmxlc3M6Ly9saW5r" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("links: %d %q %q", resp.StatusCode, body, resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("Subscription-Userinfo") != "upload=1; download=2; total=3; expire=0" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers: %v", resp.Header)
	}
	if resp, body := get("/"+testToken, "ClashMetaForAndroid/2.11", ""); resp.StatusCode != http.StatusOK || !strings.HasPrefix(body, "proxies:") ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "VPN.yaml") {
		t.Fatalf("clash: %d %q %q", resp.StatusCode, body, resp.Header.Get("Content-Disposition"))
	}
	if resp, body := get("/"+testToken, "SFI/1.11 sing-box/1.11", ""); resp.StatusCode != http.StatusOK || !strings.HasPrefix(body, `{"outbounds"`) {
		t.Fatalf("sing-box: %d %q", resp.StatusCode, body)
	}
	if resp, body := get("/"+testToken, "Mozilla/5.0", "text/html"); resp.StatusCode != http.StatusOK || !strings.HasPrefix(body, "<!doctype html>") {
		t.Fatalf("the page: %d %q", resp.StatusCode, body)
	}
	if _, body := get("/"+testToken+"?format=clash", "v2rayNG", ""); !strings.HasPrefix(body, "proxies:") {
		t.Fatalf("?format=clash: %q", body)
	}
	// A path the node was never given and a dropped one answer the same: nothing about who
	// ever had it.
	respA, bodyA := get("/NeverGivenToken000000001", "", "")
	respB, bodyB := get("/Never/../NeverGivenToken000000001", "", "")
	if respA.StatusCode != http.StatusNotFound || bodyA != bodyB || respB.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown: %d %q / %d %q", respA.StatusCode, bodyA, respB.StatusCode, bodyB)
	}
	if resp, _ := get("/"+testToken+"/info", "", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("a sub-path: %d", resp.StatusCode)
	}
	// POST is no way to ask for a profile.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/"+testToken, nil)
	if resp, err := ts.Client().Do(req); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST: %v %v", resp, err)
	}
	// HEAD carries the headers and the length, no body.
	req, _ = http.NewRequest(http.MethodHead, ts.URL+"/"+testToken, nil)
	resp, err := ts.Client().Do(req)
	if err != nil || resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Length") != "16" {
		t.Fatalf("HEAD: %v %v len %q", resp, err, resp.Header.Get("Content-Length"))
	}
}

func TestSubsExpiredAnswersGone(t *testing.T) {
	store, clock, _ := testSubs(t)
	if err := store.put(1, testRecord(testToken, clock.now().Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	ts := testSubsHTTP(t, store)
	if resp, err := ts.Client().Get(ts.URL + "/" + testToken); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("before the expiry: %v %v", resp, err)
	}
	clock.add(2 * time.Minute)
	resp, err := ts.Client().Get(ts.URL + "/" + testToken)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone || !strings.Contains(string(body), "expired") {
		t.Fatalf("after the expiry: %d %q", resp.StatusCode, body)
	}
	// The panel came back with a renewal: the same path answers again.
	if err := store.put(2, testRecord(testToken, clock.now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if resp, err := ts.Client().Get(ts.URL + "/" + testToken); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("after the renewal: %v %v", resp, err)
	}
}

// TestSubsAPI is the panel's side of the exchange: a chunk of records, the generation prune
// and the status — the three calls the panel makes every sync.
func TestSubsAPI(t *testing.T) {
	store, clock, _ := testSubs(t)
	srv := newSubServer(store, slog.New(slog.DiscardHandler))
	srv.apply(&nodeapi.SubsServer{Names: []string{"sub.example.com"}}, nil)
	srv.place("router", "")
	mux := http.NewServeMux()
	subsMux(mux, srv)
	call := func(method, path string, in any, out any) int {
		t.Helper()
		var body io.Reader
		if in != nil {
			raw, _ := json.Marshal(in)
			body = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, path, body)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if out != nil {
			_ = json.Unmarshal(rec.Body.Bytes(), out)
		}
		return rec.Code
	}
	var res nodeapi.SubsResult
	if code := call(http.MethodPut, "/v1/subs", nodeapi.SubsRequest{Generation: 100, Records: []nodeapi.SubRecord{
		testRecord(testToken, clock.now().Add(time.Hour)),
		{Path: "bad path", Expires: clock.now(), Bodies: []nodeapi.SubBody{{Format: "uri", Body: []byte("x")}}},
	}}, &res); code != http.StatusOK || res.Stored != 1 || res.Rejected != 1 || res.Reason == "" {
		t.Fatalf("put: %d %+v", code, res)
	}
	var st nodeapi.SubsStatus
	if code := call(http.MethodGet, "/v1/subs", nil, &st); code != http.StatusOK || st.Records != 1 || st.Generation != 100 || !st.OK {
		t.Fatalf("status: %d %+v", code, st)
	}
	// A chunk of another generation and the prune: what the first one sent goes.
	if code := call(http.MethodPut, "/v1/subs", nodeapi.SubsRequest{Generation: 101, Records: []nodeapi.SubRecord{
		testRecord("AnotherToken00000000001", clock.now().Add(time.Hour)),
	}}, &res); code != http.StatusOK || res.Stored != 1 {
		t.Fatalf("put 2: %d %+v", code, res)
	}
	if code := call(http.MethodPost, "/v1/subs/prune", nodeapi.SubsPruneRequest{Generation: 101}, &res); code != http.StatusOK || res.Removed != 1 {
		t.Fatalf("prune: %d %+v", code, res)
	}
	if _, ok := store.lookup(testToken); ok {
		t.Fatal("the prune kept a record of the old generation")
	}
	if code := call(http.MethodPost, "/v1/subs/remove", nodeapi.SubsRemoveRequest{Generation: 102, Paths: []string{"AnotherToken00000000001"}}, &res); code != http.StatusOK || res.Removed != 1 {
		t.Fatalf("remove: %d %+v", code, res)
	}
	if st := store.status(); st.Records != 0 {
		t.Fatalf("after the remove: %+v", st)
	}
}

// TestSubsOnTheRouterOverTLS is the real way in: the SNI router holds 443, reads the
// ClientHello, and a connection of the subscription domain's goes to the store instead of an
// inbound or the front page. The certificate is the subscription domain's own.
func TestSubsOnTheRouterOverTLS(t *testing.T) {
	certPEM, keyPEM := testCertPEM(t, "sub.example.com")
	store, clock, _ := testSubs(t)
	if err := store.put(1, testRecord(testToken, clock.now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	srv := newSubServer(store, slog.New(slog.DiscardHandler))
	srv.apply(&nodeapi.SubsServer{Names: []string{"SUB.example.com"}, TLS: &nodeapi.TLSFiles{CertPEM: certPEM, KeyPEM: keyPEM}}, nil)
	srv.place("router", "")
	defer func() { _ = srv.srv.Close(); _ = srv.ln.Close() }()

	aPort, aHits := backend(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &sniRouter{ln: ln, log: slog.New(slog.DiscardHandler), subsrv: srv}
	r.set(&nodeapi.SNIRouter{Routes: []nodeapi.SNIRoute{{Inbound: "a", Names: []string{"a.example.com"}}}, Default: "a"},
		map[string]string{"a": aPort}, map[string]bool{"sub.example.com": true})
	go r.serve(nil)
	defer r.close()

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(certPEM))
	get := func(host, path string) (*http.Response, string) {
		t.Helper()
		c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool},
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
			}}}
		resp, err := c.Get("https://" + host + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(body)
	}
	resp, body := get("sub.example.com", "/"+testToken)
	if resp.StatusCode != http.StatusOK || body != "dmxlc3M6Ly9saW5r" || resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 ||
		resp.TLS.PeerCertificates[0].Subject.CommonName != "sub.example.com" {
		t.Fatalf("over TLS: %d %q %+v", resp.StatusCode, body, resp.TLS)
	}
	// Another name is still the inbound's business: the store is not the default.
	c := helloTo(t, ln.Addr().String(), "a.example.com")
	hitBy(t, aHits, c, "a routed name")
	c.Close()
	// Turned off, the subscription domain goes to the default inbound like any other name.
	r.set(&nodeapi.SNIRouter{Routes: []nodeapi.SNIRoute{{Inbound: "a", Names: []string{"a.example.com"}}}, Default: "a"},
		map[string]string{"a": aPort}, nil)
	c = helloTo(t, ln.Addr().String(), "sub.example.com")
	hitBy(t, aHits, c, "the feature off")
	c.Close()
}
