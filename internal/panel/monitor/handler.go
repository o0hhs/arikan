package monitor

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/auth"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/server"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
)

// Nodes is the live side of the nodes, as the page needs it; the panel's own node syncer
// answers for every node it runs.
type Nodes interface {
	Health(id int64) (nodesync.HealthView, bool)
}

// Deps is what the page needs of the panel around it.
type Deps struct {
	Store *store.Store
	Set   *settings.Settings
	Now   func() time.Time
	// Online is the live view of the slots, the same the panel's dashboard counts users
	// from; nil when the panel runs without nodes.
	Online func() map[string]nodeapi.Online
	// Nodes is the running nodes; nil without them.
	Nodes Nodes
	// Brand names the service in the page's head (settings.KeyBrand), "" for none.
	Brand func(ctx context.Context) string
	// TrustProxy: the panel stands behind a proxy it trusts (MIKAN_TRUST_PROXY), so the
	// client's address is the one the proxy appended (the rate limit counts it).
	TrustProxy bool
	Log        *slog.Logger
}

// Handler serves the page: its HTML (the panel's own bundle, a third entry), its state as
// JSON and the two calls that open and close a session. Every request arrives with the
// page's own address already stripped (server.Server.forward).
type Handler struct {
	d     Deps
	page  http.Handler // the bundle's entry; nil = no page, the API alone
	fails *auth.Limiter
}

// NewHandler builds the page's handler around its bundle entry.
func NewHandler(d Deps, page http.Handler) *Handler {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	// Five wrong passwords per client, then a block that doubles: the page is public, and
	// the password is the only thing between a stranger and the service's numbers.
	return &Handler{d: d, page: page, fails: auth.NewLimiter(5, 10*time.Minute, 15*time.Minute, 24*time.Hour)}
}

// ServeHTTP: the page itself is given to anyone asking for it — it is the login form — and
// the state behind it only to a session the page's password signed.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg, err := Load(r.Context(), h.d.Set)
	if err != nil {
		h.d.Log.Error("monitor: read the document", "err", err)
		server.NotFound(w)
		return
	}
	if !cfg.Ready() {
		// Off, or without a password to open it with: the page is not there at all.
		server.NotFound(w)
		return
	}
	switch r.URL.Path {
	case "", "/":
		h.servePage(w, r)
	case "/api/state":
		h.serveState(w, r, cfg)
	case "/api/login":
		h.login(w, r, cfg)
	case "/api/logout":
		h.logout(w, r)
	default:
		// Everything else is the bundle's own: its hashed assets, the icons, and any path
		// the page itself routes (the entry is served for those, as an SPA does).
		if h.page != nil && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			h.page.ServeHTTP(w, r)
			return
		}
		server.NotFound(w)
	}
}

// servePage hands out the bundle's entry: the page itself, which is the login form until
// the API answers with a session. It carries no numbers of its own.
func (h *Handler) servePage(w http.ResponseWriter, r *http.Request) {
	if h.page == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		server.NotFound(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.page.ServeHTTP(w, r)
}

func (h *Handler) serveState(w http.ResponseWriter, r *http.Request, cfg Config) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.session(w, r, cfg) {
		h.json(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	st, err := h.state(r.Context(), cfg, Range(r.URL.Query().Get("range")))
	if err != nil {
		h.d.Log.Error("monitor: state", "err", err)
		h.json(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	h.json(w, http.StatusOK, st)
}

// session says whether the request carries one of the page's sessions, and hands a session
// close to its end a fresh cookie, so a screen left open does not ask for the password
// again in the middle of the night.
func (h *Handler) session(w http.ResponseWriter, r *http.Request, cfg Config) bool {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return false
	}
	left, ok := CheckSession(cfg.Hash, c.Value, h.d.Now())
	if !ok {
		return false
	}
	if RenewSession(left) {
		h.setCookie(w, IssueSession(cfg.Hash, h.d.Now()))
	}
	return true
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request, cfg Config) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// A browser on this page only: another site's page cannot post a password here and read
	// the answer off the visitor's screen.
	if server.FetchSite(r.Header, r.Host) != server.SiteSame {
		h.json(w, http.StatusForbidden, map[string]string{"error": "cross_origin"})
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		h.json(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	now := h.d.Now()
	key := auth.ClientKey(server.ClientIP(r.Header, r.RemoteAddr, h.d.TrustProxy))
	// The attempt is counted before the password is hashed, as the panel's own login does:
	// parallel guesses do not get past a check made first and counted after, and the attempt
	// that uses up the allowance is the one that blocks (the panel's own login does not fail
	// it again either).
	if ok, wait, _ := h.fails.Reserve(key, now); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(max(int(wait.Round(time.Second)/time.Second), 1)))
		h.json(w, http.StatusTooManyRequests, map[string]string{"error": "rate_limited"})
		return
	}
	ok, err := auth.VerifyPassword(in.Password, cfg.Hash)
	if err != nil {
		// A hash this panel cannot read (hand-edited settings): nobody gets in, and the
		// log says why rather than pretending the password was wrong.
		h.d.Log.Error("monitor: the stored password hash", "err", err)
		h.json(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	if !ok {
		h.json(w, http.StatusUnauthorized, map[string]string{"error": "bad_password"})
		return
	}
	h.fails.Reset(key)
	h.setCookie(w, IssueSession(cfg.Hash, now))
	h.json(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if server.FetchSite(r.Header, r.Host) != server.SiteSame {
		h.json(w, http.StatusForbidden, map[string]string{"error": "cross_origin"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	h.json(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) setCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: value, Path: "/",
		MaxAge: int(SessionTTL / time.Second), Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

func (h *Handler) json(w http.ResponseWriter, status int, v any) {
	hdr := w.Header()
	hdr.Set("Content-Type", "application/json; charset=utf-8")
	hdr.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Sweep drops what the rate limiter remembers of clients long gone; the panel calls it with
// the rest of its housekeeping.
func (h *Handler) Sweep() { h.fails.Sweep(h.d.Now()) }
