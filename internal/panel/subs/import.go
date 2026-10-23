package subs

import (
	"net/http"
	"net/url"
	"strings"

	"mikan/internal/panel/server"
)

// The one-tap import: GET /import?client=<app>&sub_url=<the subscription's own link> answers
// a 302 to the app's own add-schema, so a button in the bot or a page can hand the whole
// subscription over without the buyer copying anything. Happ and Incy take the link as
// their add payload; the Clash family (Kabosu among them) takes an install-config.
func (h *Handler) importRedirect(w http.ResponseWriter, r *http.Request) {
	client := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("client")))
	sub := strings.TrimSpace(r.URL.Query().Get("sub_url"))
	fail := func() { server.NotFound(w) }
	if sub == "" {
		fail()
		return
	}
	parsed, err := url.Parse(sub)
	if err != nil || !parsed.IsAbs() {
		fail()
		return
	}
	// The link must be this panel's own: the token names a subscription, and the app that
	// takes the redirect would hold it otherwise.
	token := parsed.Path
	if i := strings.LastIndexAny(token, "/"); i >= 0 {
		token = token[i+1:]
	}
	if len(token) != 24 {
		fail()
		return
	}
	if _, err := h.st.Q.GetUserBySubToken(r.Context(), token); err != nil {
		fail()
		return
	}
	var to string
	brand := url.QueryEscape(r.URL.Query().Get("name"))
	switch client {
	case "happ":
		// Happ takes the subscription URL in its add schema.
		to = "happ://add/" + url.PathEscape(sub)
	case "incy":
		to = "incy://add/" + url.PathEscape(sub)
	case "kabosu", "clash", "mihomo":
		to = "clash://install-config?url=" + url.QueryEscape(sub) + "&name=" + brand
	default:
		fail()
		return
	}
	http.Redirect(w, r, to, http.StatusFound)
}
