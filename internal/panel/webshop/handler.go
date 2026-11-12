package webshop

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/model"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/server"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

const (
	cookieName   = "mikan_shop"
	shopHeader   = "X-Mikan-Shop"
	sessionFor   = 14 * 24 * time.Hour
	codeFor      = 10 * time.Minute
	codeAttempts = 5
)

// Deps is what the shop needs from the panel.
type Deps struct {
	Store      *store.Store
	Settings   *settings.Settings
	Billing    *billing.Service
	Promo      *promo.Service
	Now        func() time.Time
	TrustProxy bool
	Log        *slog.Logger
	// SubBase is the subscription root, no trailing slash. Empty until the panel has an address.
	SubBase func(context.Context) string
	Brand   func(context.Context) string
	// Bot is the connected bot's username and token, for the optional Telegram login.
	// The token is used only to check the widget's hash.
	Bot func(context.Context) (username, token string, ok bool)
	// Send mails a login code. Nil: SendMail.
	Send func(context.Context, ShopConfig, Message) error
}

// Handler is the public shop under /<sub path>/shop.
type Handler struct {
	d      Deps
	codes  limiter
	logins limiter
	pays   limiter
	promos limiter
}

func New(d Deps) *Handler {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Send == nil {
		d.Send = SendMail
	}
	return &Handler{d: d}
}

// On: the admin turned the shop on.
func (h *Handler) On(ctx context.Context) bool {
	c, err := Load(ctx, h.d.Settings)
	return err == nil && c.Enabled
}

// ShopURL is the shop's address, empty while it is off or the panel has no address yet.
func (h *Handler) ShopURL(ctx context.Context) string {
	if !h.On(ctx) || h.d.SubBase == nil {
		return ""
	}
	b := strings.TrimRight(h.d.SubBase(ctx), "/")
	if b == "" {
		return ""
	}
	return b + "/shop"
}

// ReturnURL is where a provider sends the browser after it pays. The query carries the
// invoice's payload so the shop can show the result; the redirect itself pays nothing.
func (h *Handler) ReturnURL(ctx context.Context, payload string) string {
	cfg, err := Load(ctx, h.d.Settings)
	if err != nil {
		return ""
	}
	raw := strings.TrimSpace(cfg.ReturnURL)
	if raw == "" {
		raw = h.ShopURL(ctx)
	}
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return raw
	}
	q := u.Query()
	q.Set("order", payload)
	u.RawQuery = q.Encode()
	return u.String()
}

// Serve answers /shop/api/…. The page itself is the subscription handler's.
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request, rest string) {
	cfg, err := Load(r.Context(), h.d.Settings)
	if err != nil || !cfg.Enabled {
		server.NotFound(w)
		return
	}
	if r.Method == http.MethodOptions {
		h.cors(w, r, cfg)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !h.allowed(w, r, cfg) {
		return
	}
	h.cors(w, r, cfg)
	switch {
	case r.Method == http.MethodGet && rest == "api/config":
		h.publicConfig(w, r, cfg)
	case r.Method == http.MethodPost && rest == "api/code":
		h.sendCode(w, r, cfg)
	case r.Method == http.MethodPost && rest == "api/login":
		h.login(w, r, cfg)
	case r.Method == http.MethodPost && rest == "api/telegram":
		h.telegram(w, r, cfg)
	case r.Method == http.MethodPost && rest == "api/token":
		h.bindToken(w, r)
	case r.Method == http.MethodPost && rest == "api/logout":
		h.logout(w, r)
	case r.Method == http.MethodGet && rest == "api/me":
		h.me(w, r)
	case r.Method == http.MethodGet && rest == "api/offers":
		h.offers(w, r)
	case r.Method == http.MethodPost && rest == "api/promo":
		h.promo(w, r)
	case r.Method == http.MethodPost && rest == "api/pay":
		h.pay(w, r)
	case r.Method == http.MethodGet && rest == "api/order":
		h.order(w, r)
	default:
		server.NotFound(w)
	}
}

func (h *Handler) allowed(w http.ResponseWriter, r *http.Request, cfg ShopConfig) bool {
	origin := r.Header.Get("Origin")
	if server.FetchSite(r.Header, r.Host) == server.SiteCross && !originListed(origin, cfg) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get(shopHeader) != "1" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func originListed(origin string, cfg ShopConfig) bool {
	origin = strings.TrimRight(origin, "/")
	for _, o := range cfg.Origins {
		if origin == strings.TrimRight(o, "/") {
			return true
		}
	}
	return false
}

func (h *Handler) cors(w http.ResponseWriter, r *http.Request, cfg ShopConfig) {
	origin := strings.TrimRight(r.Header.Get("Origin"), "/")
	if origin == "" {
		return
	}
	same := server.FetchSite(r.Header, r.Host) == server.SiteSame
	if !same && !originListed(origin, cfg) {
		return
	}
	hd := w.Header()
	hd.Set("Access-Control-Allow-Origin", origin)
	hd.Add("Vary", "Origin")
	hd.Set("Access-Control-Allow-Credentials", "true")
	hd.Set("Access-Control-Allow-Headers", "Content-Type, "+shopHeader+", Authorization")
	hd.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
}

func (h *Handler) publicConfig(w http.ResponseWriter, r *http.Request, cfg ShopConfig) {
	user, token, ok := "", "", false
	if h.d.Bot != nil {
		user, token, ok = h.d.Bot(r.Context())
	}
	if !ok || token == "" {
		user = ""
	}
	brand := "VPN"
	if h.d.Brand != nil {
		if b := strings.TrimSpace(h.d.Brand(r.Context())); b != "" {
			brand = b
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"brand": brand, "smtp": cfg.SMTPOn(), "telegram_bot": user, "shop_url": h.ShopURL(r.Context()),
	})
}

type codeBody struct {
	Email string `json:"email"`
}

func (h *Handler) sendCode(w http.ResponseWriter, r *http.Request, cfg ShopConfig) {
	if !cfg.SMTPOn() {
		fail(w, http.StatusConflict, "smtp_off")
		return
	}
	var in codeBody
	if !readJSON(r, &in) {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	email, ok := parseEmail(in.Email)
	if !ok {
		fail(w, http.StatusBadRequest, "email_invalid")
		return
	}
	now := h.d.Now()
	ip := server.ClientIP(r.Header, r.RemoteAddr, h.d.TrustProxy)
	if !h.codes.allow("ip:"+ip, now, time.Hour, 20) || !h.codes.allow("email:"+email, now, time.Hour, 5) || !h.codes.allow("gap:"+email, now, 30*time.Second, 1) {
		fail(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal")
		return
	}
	code := strconv.FormatInt(n.Int64()+1000000, 10)[1:]
	ctx := r.Context()
	if err := h.d.Store.Q.CreateWebCode(ctx, db.CreateWebCodeParams{Email: email, CodeHash: secure.SHA256Hex(email + ":" + code), ExpiresAt: now.Add(codeFor).Unix(), CreatedAt: now.Unix()}); err != nil {
		h.d.Log.Warn("web shop: code not stored", "err", err)
		fail(w, http.StatusInternalServerError, "internal")
		return
	}
	link := h.ShopURL(ctx)
	if link != "" {
		link += "#email=" + url.QueryEscape(email) + "&code=" + code
	}
	body := "Code: " + code + "\nКод: " + code + "\n"
	if link != "" {
		body += "\n" + link + "\n"
	}
	if err := h.d.Send(ctx, cfg, Message{To: email, Subject: "Code / Код", Body: body}); err != nil {
		h.d.Log.Warn("web shop: code not sent", "err", err)
		fail(w, http.StatusBadGateway, "smtp_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type loginBody struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request, cfg ShopConfig) {
	var in loginBody
	if !readJSON(r, &in) {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	email, ok := parseEmail(in.Email)
	if !ok || len(in.Code) != 6 {
		fail(w, http.StatusBadRequest, "email_invalid")
		return
	}
	now := h.d.Now()
	if !h.logins.allow(email, now, 15*time.Minute, 8) {
		fail(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	ctx := r.Context()
	row, err := h.d.Store.Q.LatestWebCode(ctx, email)
	if err != nil || now.Unix() > row.ExpiresAt || row.Attempts >= codeAttempts || !secure.Equal(row.CodeHash, secure.SHA256Hex(email+":"+in.Code)) {
		if err == nil {
			_ = h.d.Store.Q.AddWebCodeAttempt(ctx, row.ID)
		}
		fail(w, http.StatusUnauthorized, "code_invalid")
		return
	}
	_ = h.d.Store.Q.DeleteWebCodes(ctx, email)
	acc, err := h.account(ctx, email, now)
	if err != nil {
		h.d.Log.Warn("web shop: account", "err", err)
		fail(w, http.StatusInternalServerError, "internal")
		return
	}
	h.startSession(w, r, cfg, db.CreateWebSessionParams{AccountID: sql.NullInt64{Int64: acc.ID, Valid: true}}, now)
}

func (h *Handler) account(ctx context.Context, email string, now time.Time) (db.WebAccount, error) {
	a, err := h.d.Store.Q.CreateWebAccount(ctx, db.CreateWebAccountParams{Email: email, CreatedAt: now.Unix()})
	if store.IsUnique(err) {
		return h.d.Store.Q.GetWebAccountByEmail(ctx, email)
	}
	return a, err
}

func (h *Handler) telegram(w http.ResponseWriter, r *http.Request, cfg ShopConfig) {
	if h.d.Bot == nil {
		fail(w, http.StatusConflict, "telegram_off")
		return
	}
	_, token, ok := h.d.Bot(r.Context())
	if !ok || token == "" {
		fail(w, http.StatusConflict, "telegram_off")
		return
	}
	var in tgLogin
	if !readJSON(r, &in) || !in.valid(token, h.d.Now()) {
		fail(w, http.StatusUnauthorized, "telegram_invalid")
		return
	}
	h.startSession(w, r, cfg, db.CreateWebSessionParams{TgID: sql.NullInt64{Int64: in.ID, Valid: true}}, h.d.Now())
}

type tokenBody struct {
	Token string `json:"token"`
}

func (h *Handler) bindToken(w http.ResponseWriter, r *http.Request) {
	var in tokenBody
	if !readJSON(r, &in) {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	u, err := h.d.Store.Q.GetUserBySubToken(r.Context(), strings.TrimSpace(in.Token))
	if err != nil {
		fail(w, http.StatusForbidden, "not_yours")
		return
	}
	now := h.d.Now()
	if sess, raw, ok := h.session(r); ok {
		if err := h.d.Store.Q.SetWebSessionUser(r.Context(), db.SetWebSessionUserParams{UserID: sql.NullInt64{Int64: u.ID, Valid: true}, TokenHash: secure.SHA256Hex(raw)}); err != nil {
			h.d.Log.Warn("web shop: token", "err", err)
			fail(w, http.StatusInternalServerError, "internal")
			return
		}
		sess.UserID = sql.NullInt64{Int64: u.ID, Valid: true}
		h.writeMe(w, r, sess, false, "")
		return
	}
	h.startSession(w, r, ShopConfig{}, db.CreateWebSessionParams{UserID: sql.NullInt64{Int64: u.ID, Valid: true}}, now)
}

func (h *Handler) startSession(w http.ResponseWriter, r *http.Request, cfg ShopConfig, p db.CreateWebSessionParams, now time.Time) {
	raw := secure.Token(32)
	p.TokenHash = secure.SHA256Hex(raw)
	p.ExpiresAt = now.Add(sessionFor).Unix()
	p.CreatedAt = now.Unix()
	if err := h.d.Store.Q.CreateWebSession(r.Context(), p); err != nil {
		h.d.Log.Warn("web shop: session", "err", err)
		fail(w, http.StatusInternalServerError, "internal")
		return
	}
	h.setCookie(w, r, raw, int(sessionFor.Seconds()))
	sess := db.WebSession{TokenHash: p.TokenHash, AccountID: p.AccountID, TgID: p.TgID, UserID: p.UserID, ExpiresAt: p.ExpiresAt}
	cross := server.FetchSite(r.Header, r.Host) == server.SiteCross && originListed(r.Header.Get("Origin"), cfg)
	token := ""
	if cross {
		token = raw
	}
	h.writeMe(w, r, sess, true, token)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if _, raw, ok := h.session(r); ok {
		_ = h.d.Store.Q.DeleteWebSession(r.Context(), secure.SHA256Hex(raw))
	}
	h.setCookie(w, r, "", -1)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	sess, _, ok := h.session(r)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"auth": false})
		return
	}
	h.writeMe(w, r, sess, true, "")
}

func (h *Handler) session(r *http.Request) (db.WebSession, string, bool) {
	raw := bearer(r)
	if raw == "" {
		if c, err := r.Cookie(cookieName); err == nil {
			raw = c.Value
		}
	}
	if raw == "" {
		return db.WebSession{}, "", false
	}
	s, err := h.d.Store.Q.GetWebSession(r.Context(), db.GetWebSessionParams{TokenHash: secure.SHA256Hex(raw), ExpiresAt: h.d.Now().Unix()})
	if err != nil {
		return db.WebSession{}, "", false
	}
	return s, raw, true
}

func bearer(r *http.Request) string {
	v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

func (h *Handler) writeMe(w http.ResponseWriter, r *http.Request, sess db.WebSession, auth bool, token string) {
	out := map[string]any{"auth": auth}
	if token != "" {
		out["token"] = token
	}
	ctx := r.Context()
	if sess.AccountID.Valid {
		if a, err := h.d.Store.Q.GetWebAccount(ctx, sess.AccountID.Int64); err == nil {
			out["email"] = a.Email
		}
	}
	if sess.TgID.Valid {
		out["telegram_id"] = sess.TgID.Int64
	}
	subs := h.subsOf(ctx, sess)
	out["subs"] = subs
	if h.d.Billing != nil && sess.TgID.Valid {
		if n, err := h.d.Billing.BalanceOf(ctx, sess.TgID.Int64); err == nil && h.d.Billing.Available(ctx).Balance.On() {
			out["balance"] = n
			out["balance_currency"] = h.d.Billing.Available(ctx).Balance.Currency
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type subView struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	TariffID int64  `json:"tariff_id,omitempty"`
	PackID   int64  `json:"device_pack,omitempty"`
}

func (h *Handler) subsOf(ctx context.Context, sess db.WebSession) []subView {
	var users []db.User
	if sess.TgID.Valid {
		if rows, err := h.d.Store.Q.ListTgLinksOf(ctx, sess.TgID.Int64); err == nil {
			users = append(users, rows...)
		}
	}
	if sess.AccountID.Valid {
		if rows, err := h.d.Store.Q.ListWebSubs(ctx, sess.AccountID.Int64); err == nil {
			users = append(users, rows...)
		}
	}
	if sess.UserID.Valid {
		if u, err := h.d.Store.Q.GetUser(ctx, sess.UserID.Int64); err == nil {
			users = append(users, u)
		}
	}
	seen := map[int64]bool{}
	out := []subView{}
	for _, u := range users {
		if seen[u.ID] {
			continue
		}
		seen[u.ID] = true
		out = append(out, subView{Token: u.SubToken, Name: u.Name, TariffID: u.TariffID.Int64, PackID: u.DevicePackID.Int64})
	}
	return out
}

func (h *Handler) offers(w http.ResponseWriter, r *http.Request) {
	if h.d.Billing == nil {
		fail(w, http.StatusConflict, "shop_off")
		return
	}
	ctx := r.Context()
	sess, _, _ := h.session(r)
	tgID := sess.TgID.Int64
	offers, av, err := h.d.Billing.Offers(ctx, tgID)
	if err != nil {
		h.d.Log.Warn("web shop: offers", "err", err)
		fail(w, http.StatusInternalServerError, "internal")
		return
	}
	lang := h.lang(ctx)
	out := map[string]any{
		"allow_new": h.d.Billing.Config(ctx).AllowNew,
		"providers": h.providers(ctx, av, sess),
		"offers":    h.offerViews(offers, lang),
		"packs":     h.packs(ctx),
		"subs":      h.subsOf(ctx, sess),
	}
	if sess.UserID.Valid {
		if o, ok, err := h.d.Billing.Renewable(ctx, sess.UserID.Int64); err == nil && ok {
			extra := int64(0)
			if u, err := h.d.Store.Q.GetUser(ctx, sess.UserID.Int64); err == nil {
				_, extra = model.RenewalExtras(h.d.Billing.Config(ctx).Model(), model.User(u), model.Tariff(o.Tariff))
			}
			out["renew"] = h.oneOffer(o, lang, extra)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) providers(ctx context.Context, av billing.Available, sess db.WebSession) []map[string]string {
	var out []map[string]string
	lang := h.lang(ctx)
	for _, id := range av.Addons {
		out = append(out, map[string]string{"id": billing.AddonPrefix + id, "name": h.d.Billing.AddonName(ctx, id, lang)})
	}
	if sess.TgID.Valid && av.Balance.On() {
		out = append(out, map[string]string{"id": billing.Balance, "name": av.Balance.Currency})
	}
	if out == nil {
		out = []map[string]string{}
	}
	return out
}

func (h *Handler) packs(ctx context.Context) []map[string]any {
	rows, err := h.d.Billing.DevicePacks(ctx)
	if err != nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		out = append(out, map[string]any{"id": p.ID, "title": p.Title, "slots": p.Slots, "rub": p.PriceRub})
	}
	return out
}

func (h *Handler) offerViews(offers []billing.Offer, lang string) []map[string]any {
	out := make([]map[string]any, 0, len(offers))
	for _, o := range offers {
		out = append(out, h.oneOffer(o, lang, 0))
	}
	return out
}

func (h *Handler) oneOffer(o billing.Offer, lang string, extraRub int64) map[string]any {
	terms := make([]map[string]any, 0, len(o.Terms))
	for _, t := range o.Terms {
		terms = append(terms, map[string]any{"days": t.Days, "rub": t.Rub + extraRub, "label": model.Describe(model.Tariff(o.Tariff), t.Days, lang)})
	}
	return map[string]any{
		"id": o.Tariff.ID, "name": o.Tariff.Name, "note": o.Tariff.Description,
		"description": model.Describe(model.Tariff(o.Tariff), o.First, lang),
		"rub":         o.Rub + extraRub, "terms": terms,
	}
}

type payBody struct {
	TariffID     int64  `json:"tariff_id"`
	TermDays     *int64 `json:"term_days"`
	DevicePackID int64  `json:"device_pack_id"`
	Provider     string `json:"provider"`
	PromoCode    string `json:"promo_code"`
	Token        string `json:"token"`
}

func (h *Handler) promo(w http.ResponseWriter, r *http.Request) {
	if h.d.Promo == nil || h.d.Billing == nil {
		fail(w, http.StatusConflict, "promo_unavailable")
		return
	}
	var in payBody
	if !readJSON(r, &in) || strings.TrimSpace(in.PromoCode) == "" {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	sess, _, _ := h.session(r)
	if !h.promos.allow(h.who(sess, r), h.d.Now(), 10*time.Minute, 8) {
		fail(w, http.StatusTooManyRequests, "promo_try_later")
		return
	}
	req, err := h.buyer(r.Context(), sess, in.Token)
	if err != nil {
		fail(w, http.StatusForbidden, "not_yours")
		return
	}
	amount, currency, err := h.price(r.Context(), req, in)
	if err != nil {
		fail(w, http.StatusConflict, "promo_unavailable")
		return
	}
	p, err := h.d.Promo.ValidateFor(r.Context(), promo.Account{TgID: req.TgID, WebID: req.WebAccountID, UserID: req.UserID}, in.TariffID, amount, currency, in.PromoCode)
	if err != nil || (p.Type != "percent" && p.Type != "fixed") {
		code := "promo_unavailable"
		if err != nil && promo.IsRefusal(err) {
			code = err.Error()
		}
		fail(w, http.StatusConflict, code)
		return
	}
	d := promo.DiscountAmount(p, amount)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "discount": d, "final_amount": amount - d, "currency": currency})
}

func (h *Handler) pay(w http.ResponseWriter, r *http.Request) {
	var in payBody
	if !readJSON(r, &in) || in.TariffID == 0 || in.Provider == "" {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	sess, _, ok := h.session(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "auth_required")
		return
	}
	if h.d.Billing == nil {
		fail(w, http.StatusConflict, "shop_off")
		return
	}
	if !h.pays.allow(h.who(sess, r), h.d.Now(), time.Hour, 30) {
		fail(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	if strings.TrimSpace(in.PromoCode) != "" && !h.promos.allow("pay:"+h.who(sess, r), h.d.Now(), 10*time.Minute, 8) {
		fail(w, http.StatusTooManyRequests, "promo_try_later")
		return
	}
	req, err := h.buyer(r.Context(), sess, in.Token)
	if err != nil {
		fail(w, http.StatusForbidden, "not_yours")
		return
	}
	req.TariffID = in.TariffID
	req.TermDays = in.TermDays
	req.DevicePackID = in.DevicePackID
	req.Provider = in.Provider
	req.PromoCode = in.PromoCode
	req.Web = true
	p, err := h.d.Billing.Invoice(r.Context(), req)
	if err != nil {
		status, code := payFailure(err)
		if status >= 500 {
			h.d.Log.Warn("web shop: invoice", "err", err)
		}
		fail(w, status, code)
		return
	}
	out := map[string]any{"provider": p.Provider, "status": p.Status, "payload": p.Payload}
	if p.PayUrl != "" {
		out["url"] = p.PayUrl
	}
	if p.Status == "applied" || p.Status == "paid" {
		if u, err := h.d.Store.Q.GetPayment(r.Context(), p.ID); err == nil && u.UserID.Valid {
			if user, err := h.d.Store.Q.GetUser(r.Context(), u.UserID.Int64); err == nil {
				out["token"] = user.SubToken
				out["name"] = user.Name
				out["status"] = u.Status
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) order(w http.ResponseWriter, r *http.Request) {
	payload := strings.TrimSpace(r.URL.Query().Get("order"))
	if payload == "" || len(payload) > 80 {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	ip := server.ClientIP(r.Header, r.RemoteAddr, h.d.TrustProxy)
	if !h.pays.allow("order:"+ip, h.d.Now(), time.Minute, 60) {
		fail(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	p, err := h.d.Store.Q.GetPaymentByPayload(r.Context(), payload)
	if err != nil || !p.Web {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	out := map[string]any{"status": p.Status, "name": p.TariffName}
	if p.Status == "applied" && p.UserID.Valid {
		if u, err := h.d.Store.Q.GetUser(r.Context(), p.UserID.Int64); err == nil {
			out["token"] = u.SubToken
			out["sub_name"] = u.Name
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// buyer is the invoice's identity. A subscription token renews that subscription; an
// email or a Telegram login buys a new one or renews one it already owns.
func (h *Handler) buyer(ctx context.Context, sess db.WebSession, token string) (billing.InvoiceRequest, error) {
	req := billing.InvoiceRequest{Web: true, TgID: sess.TgID.Int64, WebAccountID: sess.AccountID.Int64}
	token = strings.TrimSpace(token)
	if token == "" {
		if req.TgID == 0 && req.WebAccountID == 0 {
			return req, billing.ErrNotYours
		}
		return req, nil
	}
	u, err := h.d.Store.Q.GetUserBySubToken(ctx, token)
	if err != nil {
		return req, billing.ErrNotYours
	}
	req.UserID = u.ID
	if req.TgID != 0 {
		if link, err := h.d.Store.Q.GetTgLink(ctx, u.ID); err == nil && link.TgID == req.TgID {
			return req, nil
		}
	}
	if req.WebAccountID != 0 {
		if link, err := h.d.Store.Q.GetWebLink(ctx, u.ID); err == nil && link.AccountID == req.WebAccountID {
			return req, nil
		}
	}
	req.TgID, req.WebAccountID, req.TokenRenew = 0, 0, true
	return req, nil
}

func (h *Handler) price(ctx context.Context, req billing.InvoiceRequest, in payBody) (int64, string, error) {
	offers, av, err := h.d.Billing.Offers(ctx, req.TgID)
	if err != nil {
		return 0, "", err
	}
	var offer billing.Offer
	found := false
	for _, o := range offers {
		if o.Tariff.ID == in.TariffID {
			offer, found = o, true
			break
		}
	}
	if !found && req.UserID != 0 {
		if o, ok, err := h.d.Billing.Renewable(ctx, req.UserID); err != nil {
			return 0, "", err
		} else if ok && o.Tariff.ID == in.TariffID {
			offer, found = o, true
		}
	}
	if !found {
		return 0, "", billing.ErrNotForSale
	}
	term, ok := offer.Term(in.TermDays)
	if !ok || term.Rub <= 0 {
		return 0, "", billing.ErrNotForSale
	}
	rub := term.Rub
	if req.UserID != 0 {
		if u, err := h.d.Store.Q.GetUser(ctx, req.UserID); err == nil {
			_, extra := model.RenewalExtras(h.d.Billing.Config(ctx).Model(), model.User(u), model.Tariff(offer.Tariff))
			rub += extra
		}
	}
	if in.DevicePackID != 0 {
		packs, err := h.d.Billing.DevicePacks(ctx)
		if err != nil {
			return 0, "", err
		}
		var pack db.DevicePack
		for _, p := range packs {
			if p.ID == in.DevicePackID {
				pack = p
			}
		}
		if pack.ID == 0 {
			return 0, "", billing.ErrNotForSale
		}
		add, took := billing.PackTermPrice(pack, term.Days, "RUB")
		if !took {
			return 0, "", billing.ErrProviderOff
		}
		rub += add
	}
	if in.Provider == billing.Balance && req.TgID != 0 && av.Balance.On() {
		return av.Balance.FromRub(rub), av.Balance.Currency, nil
	}
	id := model.AddonID(in.Provider)
	if id != "" && slices.Contains(av.Addons, id) {
		return rub, "RUB", nil
	}
	return 0, "", billing.ErrProviderOff
}

func (h *Handler) who(sess db.WebSession, r *http.Request) string {
	switch {
	case sess.AccountID.Valid:
		return "a:" + strconv.FormatInt(sess.AccountID.Int64, 10)
	case sess.TgID.Valid:
		return "t:" + strconv.FormatInt(sess.TgID.Int64, 10)
	case sess.UserID.Valid:
		return "u:" + strconv.FormatInt(sess.UserID.Int64, 10)
	default:
		return "ip:" + server.ClientIP(r.Header, r.RemoteAddr, h.d.TrustProxy)
	}
}

func (h *Handler) lang(ctx context.Context) string {
	l, err := h.d.Settings.Lang(ctx)
	if err != nil || l == "" {
		return "en"
	}
	return l
}

func (h *Handler) setCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	secureCookie := r.TLS != nil || h.d.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: value, Path: cookiePath(r), HttpOnly: true,
		Secure: secureCookie, SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

func cookiePath(r *http.Request) string {
	p := r.URL.Path
	if i := strings.Index(p, "/shop"); i >= 0 {
		return p[:i+len("/shop")]
	}
	return "/shop"
}

func payFailure(err error) (int, string) {
	if code, known := billing.Refusal(err); known {
		return http.StatusConflict, code
	}
	if errors.Is(err, billing.ErrProviderOff) {
		return http.StatusConflict, "provider_off"
	}
	return http.StatusBadGateway, "invoice_failed"
}

func parseEmail(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || len(s) > 254 || !strings.Contains(s, "@") || !strings.Contains(strings.SplitN(s, "@", 2)[1], ".") {
		return "", false
	}
	return s, true
}

func readJSON(r *http.Request, dst any) bool {
	return json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(dst) == nil
}

func fail(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"code": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
