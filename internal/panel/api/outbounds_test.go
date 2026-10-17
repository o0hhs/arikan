package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/auth"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/pgdump"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

type outboundNodes struct {
	NodeRuntime
	probe    nodeapi.ProbeResult
	probeErr error
	asked    []string
}

func (n *outboundNodes) Probe(_ context.Context, _ int64, proxy string) (nodeapi.ProbeResult, error) {
	n.asked = append(n.asked, proxy)
	return n.probe, n.probeErr
}

// A node that has not reported its health: the views say "unknown" instead of failing.
func (n *outboundNodes) Health(int64) (nodesync.HealthView, bool) {
	return nodesync.HealthView{}, false
}

type outboundFixture struct {
	t       *testing.T
	st      *store.Store
	q       *db.Queries
	nodes   *outboundNodes
	now     time.Time
	token   string
	csrf    string
	handler http.Handler
}

func newOutboundFixture(t *testing.T) *outboundFixture {
	t.Helper()
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &outboundFixture{t: t, st: st, q: st.Q, now: time.Unix(1_800_000_000, 0)}
	clock := func() time.Time { return f.now }
	if err := domain.Seed(ctx, st, f.now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(st.Q)
	if err := settings.Set(ctx, set, settings.KeyPublicHost, "203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	// A node of the panel's own, so that a link to its address is a loop too.
	if _, err := st.Q.CreateNode(ctx, db.CreateNodeParams{Name: "B", Address: "198.51.100.20:40000", PublicHost: "198.51.100.20", Domain: "b.example.com", CreatedAt: f.now.Unix(), UpdatedAt: f.now.Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Q.CreateAdmin(ctx, db.CreateAdminParams{Username: "admin", PasswordHash: "x", CreatedAt: f.now.Unix()}); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessions(st.Q, clock, nil)
	token, sess, err := sessions.Create(ctx, 1, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	f.token, f.csrf = token, sess.CsrfToken
	f.nodes = &outboundNodes{probe: nodeapi.ProbeResult{OK: true, IP: "198.51.100.77"}}
	pool := domain.NewPool(st, clock)
	handler, _, err := New(Deps{
		Version: "test", Store: st, Sessions: sessions, Now: clock, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Settings: set, IPLimit: auth.NewLimiter(10, time.Minute, time.Minute, time.Hour), UserLimit: auth.NewLimiter(10, time.Minute, time.Minute, time.Hour),
		TOTP: auth.NewTOTPGuard(), Users: domain.NewUsers(st, pool, noChanges{}, clock), Pool: pool, Changes: noChanges{}, Nodes: f.nodes,
		Inbounds: domain.NewInbounds(st, nil, clock),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.handler = handler
	return f
}

func (f *outboundFixture) call(method, path, body string) (int, string) {
	f.t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: f.token})
	req.Header.Set("X-CSRF-Token", f.csrf)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (f *outboundFixture) view(t *testing.T, body string) OutboundView {
	t.Helper()
	var v OutboundView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("not an outbound: %v (%s)", err, body)
	}
	return v
}

// A link is added, chosen by an inbound, checked on a node and only then deleted; a link
// to this panel's own server is refused, and a check says which node answered.
func TestOutbounds(t *testing.T) {
	f := newOutboundFixture(t)
	ctx := context.Background()

	code, body := f.call(http.MethodPost, "/api/v1/outbounds", `{"link":"vless://11111111-2222-3333-4444-555555555555@198.51.100.30:443?security=tls#VPS","name":""}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	o := f.view(t, body)
	if o.Name != "VPS" || o.Kind != "vless" || !o.Enabled || len(o.Uses) != 0 {
		t.Fatalf("created as %+v", o)
	}
	// The panel's own address, and one of its nodes', are not an outbound of anyone's.
	for _, link := range []string{"vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443", "trojan://secret@198.51.100.20:443", "vless://11111111-2222-3333-4444-555555555555@b.example.com:443"} {
		if code, body := f.call(http.MethodPost, "/api/v1/outbounds", `{"link":"`+link+`"}`); code != http.StatusUnprocessableEntity || !strings.Contains(body, "own_server") {
			t.Fatalf("a link to our own server: %d %s", code, body)
		}
	}
	if code, body := f.call(http.MethodPost, "/api/v1/outbounds", `{"link":"nonsense"}`); code != http.StatusUnprocessableEntity || !strings.Contains(body, "bad_link") {
		t.Fatalf("a link that is not one: %d %s", code, body)
	}

	// An inbound leaves through it.
	ins, err := f.q.ListInbounds(ctx)
	if err != nil || len(ins) == 0 {
		t.Fatal(err)
	}
	in := ins[0]
	code, body = f.call(http.MethodPatch, "/api/v1/inbounds/"+strconv.FormatInt(in.ID, 10),
		`{"outbound":"link","outbound_id":`+strconv.FormatInt(o.ID, 10)+`}`)
	if code != http.StatusOK {
		t.Fatalf("patch inbound: %d %s", code, body)
	}
	var inbound struct {
		Outbound   string `json:"outbound"`
		OutboundID *int64 `json:"outbound_id"`
	}
	if err := json.Unmarshal([]byte(body), &inbound); err != nil {
		t.Fatal(err)
	}
	if inbound.Outbound != "link" || inbound.OutboundID == nil || *inbound.OutboundID != o.ID {
		t.Fatalf("inbound reads %+v", inbound)
	}

	code, body = f.call(http.MethodGet, "/api/v1/outbounds", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	var list struct {
		Outbounds []OutboundView `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Outbounds) != 1 || len(list.Outbounds[0].Uses) != 1 || list.Outbounds[0].Uses[0].InboundID != in.ID {
		t.Fatalf("list = %+v", list.Outbounds)
	}

	// The check asks the node that carries it, and says which node answered.
	code, body = f.call(http.MethodPost, "/api/v1/outbounds/"+strconv.FormatInt(o.ID, 10)+"/check", "")
	if code != http.StatusOK {
		t.Fatalf("check: %d %s", code, body)
	}
	var check OutboundCheck
	if err := json.Unmarshal([]byte(body), &check); err != nil {
		t.Fatal(err)
	}
	if !check.OK || check.IP != "198.51.100.77" || check.Node == 0 {
		t.Fatalf("check = %+v", check)
	}
	if len(f.nodes.asked) != 1 || f.nodes.asked[0] != nodeapi.OutboundName(o.ID) {
		t.Fatalf("asked the node about %v", f.nodes.asked)
	}
	// A node that does not carry it yet says so instead of failing the check.
	f.nodes.probe, f.nodes.probeErr = nodeapi.ProbeResult{}, &nodeapi.StatusError{Method: "GET", Path: "/v1/probe", Status: http.StatusNotFound}
	code, body = f.call(http.MethodPost, "/api/v1/outbounds/"+strconv.FormatInt(o.ID, 10)+"/check", "")
	if code != http.StatusOK {
		t.Fatalf("check: %d %s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &check); err != nil {
		t.Fatal(err)
	}
	if check.Code != "not_applied" || check.OK {
		t.Fatalf("check of an unapplied outbound = %+v", check)
	}

	// While an inbound leaves through it, it stays.
	if code, body := f.call(http.MethodDelete, "/api/v1/outbounds/"+strconv.FormatInt(o.ID, 10), ""); code != http.StatusConflict || !strings.Contains(body, "outbound_used") {
		t.Fatalf("delete while used: %d %s", code, body)
	}
	code, _ = f.call(http.MethodPatch, "/api/v1/inbounds/"+strconv.FormatInt(in.ID, 10), `{"outbound":"direct"}`)
	if code != http.StatusOK {
		t.Fatalf("inbound back to direct: %d", code)
	}
	if code, body := f.call(http.MethodDelete, "/api/v1/outbounds/"+strconv.FormatInt(o.ID, 10), ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", code, body)
	}
	if code, _ := f.call(http.MethodGet, "/api/v1/outbounds/"+strconv.FormatInt(o.ID, 10)+"/check", ""); code != http.StatusMethodNotAllowed {
		t.Fatalf("check of a gone outbound: %d", code)
	}
}

// The database download is the archive "mikan database backup" writes, streamed: the admin
// gets a file pg_restore reads, and it is a session's own request.
func TestDatabaseDownload(t *testing.T) {
	f := newOutboundFixture(t)
	ctx := context.Background()
	// The fixture's database is the installation's: the panel dumps the one its
	// MIKAN_DATABASE_URL names.
	var database string
	if err := f.st.DB.QueryRowContext(ctx, "SELECT current_database()").Scan(&database); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv("MIKAN_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + database
	t.Setenv("MIKAN_DATABASE_URL", u.String())

	code, body := f.call(http.MethodGet, "/api/v1/database/backup", "")
	if code != http.StatusOK {
		t.Fatalf("download: %d %s", code, body)
	}
	if !strings.HasPrefix(body, "PGDMP") {
		t.Fatalf("not a pg_dump archive: %q", body[:min(16, len(body))])
	}
	path := filepath.Join(t.TempDir(), "download.dump")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// What the panel itself checks before trusting any backup, including this one.
	if err := pgdump.Check(ctx, path); err != nil {
		t.Fatalf("the downloaded archive does not pass the panel's own check: %v", err)
	}
	// A request without a session never gets the database.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/database/backup", nil)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("a request without a session: %d", rec.Code)
	}
}
