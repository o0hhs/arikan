package agents

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"mikan/internal/panel/secure"
	"mikan/internal/panel/server"
	"mikan/internal/panel/store/db"
)

// APIDeps is what the agents' panel API runs on.
type APIDeps struct {
	Service *Service
	// BotUsername is the bot's @name, for the deep links of the agent's placements;
	// "" while the bot is off — the links are then not given out.
	BotUsername func() string
	TrustProxy  bool
	Log         *slog.Logger
	Now         func() time.Time
}

// apiH is the huma API of the agents' panel: its own cookie, its own sessions and four
// numbers' worth of their work, under the panel's agent path.
type apiH struct {
	d   APIDeps
	api huma.API
}

type ctxKey int

const (
	keyClient ctxKey = iota
	keySession
)

type client struct{ IP, UserAgent string }

// Handler builds the agents' panel API: mount it under the panel's agent path, so that
// its relative /api/agent/... routes live there alone.
func Handler(d APIDeps) (http.Handler, error) {
	cfg := huma.DefaultConfig("Arikan — панель рекламных агентов", "")
	// No spec endpoint of its own: the agents' panel needs no OpenAPI client.
	cfg.DocsPath = ""
	cfg.OpenAPIPath = ""
	cfg.SchemasPath = ""
	// Handlers always return non-nil slices (the admin API's rule, held here too).
	huma.DefaultArrayNullable = false
	mux := http.NewServeMux()
	api := humago.New(mux, cfg)
	h := &apiH{d: d, api: api}
	api.UseMiddleware(h.middleware)
	h.register()
	return noStore(mux), nil
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (h *apiH) middleware(ctx huma.Context, next func(huma.Context)) {
	c := client{IP: server.ClientIP(http.Header{"X-Forwarded-For": {ctx.Header("X-Forwarded-For")}}, ctx.RemoteAddr(), h.d.TrustProxy), UserAgent: ctx.Header("User-Agent")}
	ctx = huma.WithValue(ctx, keyClient, c)
	op := ctx.Operation()
	mutating := op.Method != http.MethodGet && op.Method != http.MethodHead
	if mutating && !h.sameOrigin(ctx) {
		_ = huma.WriteErr(h.api, ctx, http.StatusForbidden, "csrf")
		return
	}
	if public, _ := op.Metadata["public"].(bool); public {
		next(ctx)
		return
	}
	ck, err := huma.ReadCookie(ctx, CookieName)
	if err != nil {
		_ = huma.WriteErr(h.api, ctx, http.StatusUnauthorized, "unauthorized")
		return
	}
	sess, err := h.d.Service.LookupSession(ctx.Context(), ck.Value)
	if errors.Is(err, ErrCredentials) {
		_ = huma.WriteErr(h.api, ctx, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err != nil {
		h.d.Log.Error("agents: session lookup", "err", err)
		_ = huma.WriteErr(h.api, ctx, http.StatusInternalServerError, "internal error")
		return
	}
	if mutating && !secure.Equal(ctx.Header("X-CSRF-Token"), sess.CsrfToken) {
		_ = huma.WriteErr(h.api, ctx, http.StatusForbidden, "csrf")
		return
	}
	next(huma.WithValue(ctx, keySession, sess))
}

// sameOrigin refuses cross-site browser requests, as the admin API does.
func (h *apiH) sameOrigin(ctx huma.Context) bool {
	head := http.Header{"Sec-Fetch-Site": {ctx.Header("Sec-Fetch-Site")}, "Origin": {ctx.Header("Origin")}}
	return server.FetchSite(head, ctx.Host()) != server.SiteCross
}

func clientOf(ctx context.Context) client {
	c, _ := ctx.Value(keyClient).(client)
	return c
}

func sessionOf(ctx context.Context) db.AgentSession {
	s, _ := ctx.Value(keySession).(db.AgentSession)
	return s
}

// agentCookie is the panel's session cookie on the way in and the empty one on the way
// out (maxAge 0).
func agentCookie(value string, maxAge time.Duration) http.Cookie {
	c := http.Cookie{Name: CookieName, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}
	if maxAge > 0 {
		c.MaxAge = int(maxAge / time.Second)
	} else {
		c.MaxAge = -1
	}
	return c
}
