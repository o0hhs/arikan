package subs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/model"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/server"
	"mikan/internal/panel/store/db"
)

// The Telegram Mini App's JSON endpoints under "/tg/": the subscription page inside Telegram,
// its sign-in, the shop, the promo codes and their history. Every request but the page is a
// POST from the page itself (sameOrigin) whose body carries Telegram's init_data, the proof
// of who the Telegram account is; the subscriptions the account may act on are the ones
// Telegram.MiniAppUser names, and a token in the body is checked against them (ownerOf).

// miniApp serves the subscription page inside Telegram and signs its user in.
func (h *Handler) miniApp(w http.ResponseWriter, r *http.Request, rest string) {
	switch {
	case r.Method == http.MethodGet && rest == "" && h.page != nil:
		// Telegram Web opens Mini Apps in a frame; the apps open them in a web view.
		hd := w.Header()
		hd.Set("Content-Security-Policy", strings.Replace(hd.Get("Content-Security-Policy"), "frame-ancestors 'none'", "frame-ancestors https://web.telegram.org", 1))
		hd.Del("X-Frame-Options")
		cfg, _ := h.cfg(r.Context())
		h.servePage(w, r, cfg)
	case r.Method == http.MethodPost && rest == "session" && sameOrigin(r):
		h.miniAppSession(w, r)
	case r.Method == http.MethodPost && rest == "reissue" && sameOrigin(r) && h.reissuer != nil:
		h.miniAppReissue(w, r)
	case r.Method == http.MethodPost && (rest == "shop" || rest == "pay") && sameOrigin(r) && h.shop != nil:
		h.miniAppShop(w, r, rest)
	case r.Method == http.MethodPost && rest == "free" && sameOrigin(r) && h.shop != nil:
		h.miniAppFree(w, r)
	case r.Method == http.MethodPost && rest == "access" && sameOrigin(r) && h.promos != nil && h.shop != nil:
		h.miniAppAccess(w, r)
	case r.Method == http.MethodPost && (rest == "promo" || rest == "promo-history") && sameOrigin(r) && h.promos != nil:
		if rest == "promo-history" {
			h.miniAppPromoHistory(w, r)
		} else {
			h.miniAppPromo(w, r)
		}
	default:
		server.NotFound(w)
	}
}

// miniAuth is what every Mini App request starts with: Telegram's proof of who is asking.
// The requests embed it, so that mini can read it from any of them.
type miniAuth struct {
	InitData string `json:"init_data"`
}

func (a miniAuth) initData() string { return a.InitData }

// miniRequest is a request body of the Mini App: it holds the init_data.
type miniRequest interface{ initData() string }

// checkedRequest is a request that can be refused for its content alone, before Telegram is
// asked who sent it (a promo request without a code).
type checkedRequest interface{ valid() bool }

// plainRequest is a request whose unreadable body is answered with the plain-text 400 of
// http.Error, not with the JSON code the others have.
type plainRequest interface{ plainBadRequest() }

// mini is the beginning of every Mini App request that answers in JSON: the answer is not to
// be cached and is JSON, the body (16 KiB at most) is read into in, and the init_data in it is
// checked. It returns the Telegram account and the subscriptions it owns, and fail, which
// writes the JSON error {"code": code} with the status. ok is false when the request has
// been answered already: a body that cannot be read (400, bad_request) or whose init_data
// Telegram does not vouch for (401, init_data).
func (h *Handler) mini(w http.ResponseWriter, r *http.Request, in miniRequest) (tgID int64, users []db.User, fail func(int, string), ok bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	fail = func(status int, code string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
	}
	if json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(in) != nil || !requestValid(in) {
		if _, plain := in.(plainRequest); plain {
			http.Error(w, "bad request", http.StatusBadRequest)
		} else {
			fail(http.StatusBadRequest, "bad_request")
		}
		return 0, nil, fail, false
	}
	tgID, users, err := h.tg.MiniAppUser(r.Context(), in.initData())
	if err != nil {
		fail(http.StatusUnauthorized, "init_data")
		return 0, nil, fail, false
	}
	return tgID, users, fail, true
}

func requestValid(in miniRequest) bool {
	c, ok := in.(checkedRequest)
	return !ok || c.valid()
}

// ownerOf is the subscription the token in a request names, out of the ones the Telegram
// account owns: its user id. A request without a token (""), for something not tied to a
// subscription, gets 0 and ok. A token of a subscription the account does not own is not ok,
// and must be refused: the token alone is a bearer of the subscription, but what the Mini App
// does with it (buying for it) is for its owner only.
func ownerOf(users []db.User, token string) (userID int64, ok bool) {
	if token == "" {
		return 0, true
	}
	for _, u := range users {
		if u.SubToken == token {
			return u.ID, u.ID != 0
		}
	}
	return 0, false
}

// miniAppSession signs the Telegram account in: it answers with the subscriptions the
// account owns (their tokens and names), which the page then opens.
func (h *Handler) miniAppSession(w http.ResponseWriter, r *http.Request) {
	var in miniAuth
	if json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	_, users, err := h.tg.MiniAppUser(r.Context(), in.InitData)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "init_data"})
		return
	}
	type sub struct {
		Token string `json:"token"`
		Name  string `json:"name"`
	}
	out := struct {
		Subs []sub `json:"subs"`
	}{Subs: []sub{}}
	for _, u := range users {
		out.Subs = append(out.Subs, sub{Token: u.SubToken, Name: u.Name})
	}
	_ = json.NewEncoder(w).Encode(out)
}

// shopRequest is the body of "shop" and "pay".
type shopRequest struct {
	miniAuth
	TariffID     int64  `json:"tariff_id"`
	TermDays     *int64 `json:"term_days"` // the tariff's term; absent: its first
	PackageID    int64  `json:"package_id"`
	Device       bool   `json:"device"`         // one more device for the subscription
	DevicePackID int64  `json:"device_pack_id"` // the pack of extra device places beside the tariff
	Trial        bool   `json:"trial"`          // the priced trial, for an account without a subscription
	Provider     string `json:"provider"`
	Token        string `json:"token"`
	PromoCode    string `json:"promo_code"`
	// Custom is a tariff the buyer put together themselves on the constructor's base tariff
	// (custom.go); it goes with a pay of that very tariff.
	Custom *billing.CustomPlan `json:"custom,omitempty"`
}

// valid: the trial is bought for no subscription and at its own price, without a code; a
// tariff put together by the buyer is bought for a subscription or as a new one, and never
// together with a package, a device pack or the trial.
func (in shopRequest) valid() bool {
	if in.Trial && (in.Token != "" || strings.TrimSpace(in.PromoCode) != "") {
		return false
	}
	if in.Custom != nil && (in.TariffID == 0 || in.PackageID != 0 || in.DevicePackID != 0 || in.Trial) {
		return false
	}
	return true
}

// miniAppShop: "shop" lists what the Telegram account can buy (with token: the traffic
// packages of that subscription too), "pay" opens an invoice for a new subscription
// (token ""), one of the account's own, a traffic package for it (package_id), or the
// priced trial (trial).
func (h *Handler) miniAppShop(w http.ResponseWriter, r *http.Request, rest string) {
	var in shopRequest
	tgID, users, fail, ok := h.mini(w, r, &in)
	if !ok {
		return
	}
	userID, ok := ownerOf(users, in.Token)
	if !ok {
		fail(http.StatusForbidden, billing.ErrNotYours.Error())
		return
	}
	ctx := r.Context()
	if rest == "shop" {
		h.miniAppOffers(ctx, w, tgID, userID, fail)
		return
	}
	if strings.TrimSpace(in.PromoCode) != "" && !h.promoLimit.allow(tgID, h.now(), in.PromoCode) {
		fail(http.StatusTooManyRequests, "promo_try_later")
		return
	}
	var p db.Payment
	var err error
	if in.Trial {
		p, err = h.shop.TrialInvoice(ctx, tgID, billing.AdapterOf(in.Provider))
	} else if in.Device {
		p, err = h.shop.DeviceInvoice(ctx, billing.DeviceRequest{TgID: tgID, UserID: userID, Provider: billing.AdapterOf(in.Provider)})
	} else if in.PackageID != 0 {
		p, err = h.shop.PackageInvoice(ctx, billing.PackageRequest{TgID: tgID, UserID: userID, PackageID: in.PackageID, Provider: billing.AdapterOf(in.Provider), PromoCode: in.PromoCode})
	} else {
		p, err = h.shop.Invoice(ctx, billing.InvoiceRequest{TgID: tgID, UserID: userID, TariffID: in.TariffID, TermDays: in.TermDays, Provider: billing.AdapterOf(in.Provider), PromoCode: in.PromoCode, Custom: in.Custom, DevicePackID: in.DevicePackID})
	}
	if err != nil {
		if strings.TrimSpace(in.PromoCode) != "" && promoError(err) {
			fail(h.promoAttempt(err))
			return
		}
		status, code, unexplained := invoiceFailure(err)
		if unexplained {
			h.log.Warn("mini app: the invoice was not made", "provider", in.Provider, "err", err)
		}
		fail(status, code)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"url": p.PayUrl, "provider": p.Provider})
}

// term is one term of an offer as the page reads it.
type term struct {
	Days        int64  `json:"days"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Stars       int64  `json:"stars,omitempty"`
	Rub         int64  `json:"rub,omitempty"`
}

// offer is a tariff in "shop". Note is the admin's own text about the tariff, shown under the
// line built from the limits; plain text with line breaks, never markup.
type offer struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Note        string `json:"note,omitempty"`
	Stars       int64  `json:"stars,omitempty"`
	Rub         int64  `json:"rub,omitempty"`
	Terms       []term `json:"terms"`
}

// miniAppOffers answers "shop": the tariffs on sale, the payment providers, the traffic
// packages of the subscription userID (0: none), and the priced trial when the Telegram
// account tgID may buy it.
func (h *Handler) miniAppOffers(ctx context.Context, w http.ResponseWriter, tgID, userID int64, fail func(int, string)) {
	offers, av, err := h.shop.Offers(ctx, tgID)
	if err != nil {
		fail(http.StatusInternalServerError, "internal")
		return
	}
	cfg, _ := h.cfg(ctx)
	packages, err := h.shopPackages(ctx, userID, cfg.Lang)
	if err != nil {
		fail(http.StatusInternalServerError, "internal")
		return
	}
	// A tariff's terms, the first one also in the offer itself (as before terms).
	type term struct {
		Days        int64  `json:"days"`
		Label       string `json:"label"`
		Description string `json:"description"`
		Stars       int64  `json:"stars,omitempty"`
		Rub         int64  `json:"rub,omitempty"`
	}
	type offer struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		// Note is the admin's own text about the tariff, shown under the line built from the
		// limits; plain text with line breaks, never markup.
		Note  string `json:"note,omitempty"`
		Stars int64  `json:"stars,omitempty"`
		Rub   int64  `json:"rub,omitempty"`
		Terms []term `json:"terms"`
		// Groups and Gifts say what the tariff is made of: its server groups as it has them,
		// and the packages it gives with every payment.
		Groups []billing.OfferGroup `json:"groups,omitempty"`
		Gifts  []string             `json:"gifts,omitempty"`
	}
	// Marketplace adapters take rubles; the buyer sees each by its own name.
	type addon struct {
		Provider string `json:"provider"`
		Name     string `json:"name"`
	}
	out := struct {
		AllowNew  bool            `json:"allow_new"`
		Providers map[string]bool `json:"providers"`
		Addons    []addon         `json:"addons"`
		Offers    []offer         `json:"offers"`
		Packages  []shopPackage   `json:"packages"`
		// Packs are the packs of extra device places a buyer may add to a tariff; the
		// subscription's own is DevicePack, what a renewal starts from.
		Packs      []devicePackOffer `json:"packs"`
		DevicePack int64             `json:"device_pack,omitempty"`
		// Trial is the priced trial this account may buy now, if any.
		Trial *offer `json:"trial,omitempty"`
		// Free is the free plan this account may take now, if any.
		Free *freeOffer `json:"free,omitempty"`
		// Built is the tariff constructor this account may use now, if any.
		Built *builtOffer `json:"built,omitempty"`
		// Access lists the access codes this account redeemed (the tariffs they open are
		// already among the offers).
		Access []accessItem `json:"access,omitempty"`
		// Device is one more device for the subscription on screen, if it may buy one.
		Device *offer `json:"device,omitempty"`
	}{AllowNew: h.shop.Config(ctx).AllowNew, Offers: []offer{}, Packages: packages, Packs: []devicePackOffer{}, Addons: []addon{},
		Providers: map[string]bool{billing.Stars: av.Stars}}
	if userID != 0 {
		if u, err := h.st.Q.GetUser(ctx, userID); err == nil {
			out.DevicePack = u.DevicePackID.Int64
		}
	}
	if packs, err := h.shop.DevicePacks(ctx); err == nil {
		for _, p := range packs {
			v := devicePackOffer{ID: p.ID, Title: p.Title, Slots: int64(p.Slots), Stars: p.PriceStars.Int64, Rub: p.PriceRub}
			out.Packs = append(out.Packs, v)
		}
	}
	if fo, ok := h.shop.FreeOffer(ctx, tgID); ok {
		out.Free = &freeOffer{Name: fo.Tariff.Name, Description: model.Describe(model.Tariff(fo.Tariff), fo.Tariff.DurationDays, cfg.Lang), Channel: fo.Channel}
	}
	if c, ok := h.shop.CustomConstructor(ctx); ok {
		out.Built = builtOf(c, cfg.Lang)
	}
	if accs, err := h.promos.Accesses(ctx, tgID); err == nil {
		for _, a := range accs {
			if t, err := h.st.Q.GetTariff(ctx, a.TariffID); err == nil {
				out.Access = append(out.Access, accessItem{TariffID: a.TariffID, Name: t.Name, Code: a.Code, Mode: a.Mode})
			}
		}
	}
	if to, _, ok := h.shop.TrialOffer(ctx, tgID); ok {
		out.Trial = &offer{ID: to.Tariff.ID, Name: to.Tariff.Name, Description: model.Describe(model.Tariff(to.Tariff), to.Tariff.DurationDays, cfg.Lang), Note: to.Tariff.Description, Stars: to.Stars, Rub: to.Rub, Terms: []term{}}
	}
	if userID != 0 {
		if do, _, ok, _ := h.shop.DeviceOfferFor(ctx, userID); ok {
			out.Device = &offer{ID: userID, Name: model.DescribeDevice(cfg.Lang), Description: model.DescribeDeviceOffer(do.Model(), cfg.Lang), Stars: do.Stars, Rub: do.Rub, Terms: []term{}}
		}
	}
	for _, id := range av.Addons {
		out.Addons = append(out.Addons, addon{Provider: billing.AddonPrefix + id, Name: h.shop.AddonName(ctx, id, cfg.Lang)})
	}
	for _, o := range offers {
		mo := o.Model()
		v := offer{ID: o.Tariff.ID, Name: o.Tariff.Name, Description: model.DescribeOffer(mo, cfg.Lang), Note: o.Tariff.Description, Stars: o.Stars, Rub: o.Rub}
		v.Groups = o.Groups
		for _, g := range mo.Gifts {
			v.Gifts = append(v.Gifts, model.DescribePackage(g.Package, g.Pool, cfg.Lang))
		}
		for _, t := range mo.Terms {
			v.Terms = append(v.Terms, term{Days: t.Days, Label: model.TermLabel(mo.Tariff, t.Days, cfg.Lang),
				Description: model.Describe(mo.Tariff, t.Days, cfg.Lang), Stars: t.Stars, Rub: t.Rub})
		}
		out.Offers = append(out.Offers, v)
	}
	_ = json.NewEncoder(w).Encode(out)
}

// freeOffer is the free plan in "shop": what it gives, and the channel it asks the account
// to join ("" when it asks nothing).
type freeOffer struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Channel     string `json:"channel,omitempty"`
}

// devicePackOffer is a pack of extra device places in "shop": its places and its price a
// month, in either currency it is sold for.
type devicePackOffer struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Slots int64  `json:"slots"`
	Stars int64  `json:"stars,omitempty"`
	Rub   int64  `json:"rub,omitempty"`
}

// accessItem is an access code an account redeemed, as "shop" lists it: which tariff it opens
// and with which code.
type accessItem struct {
	TariffID int64  `json:"tariff_id"`
	Name     string `json:"name"`
	Code     string `json:"code"`
	Mode     string `json:"mode"` // access_paid or access_free
}

// builtTerm is one term of the constructor's base tariff, with its prices.
type builtTerm struct {
	Days  int64 `json:"days"`
	Stars int64 `json:"stars,omitempty"`
	Rub   int64 `json:"rub,omitempty"`
}

// builtLimit is a dimension of the constructor: the tariff's own number (0: endless) and the
// bounds a plan may ask for. The page prices a preview itself with rates, the same way the
// panel does, and the panel prices the order again when it is opened.
type builtLimit struct {
	Value int64 `json:"value"`
	Min   int64 `json:"min"`
	Max   int64 `json:"max"`
}

// builtOffer is the tariff constructor as the page sees it: the base tariff, its terms and
// what may be put together.
type builtOffer struct {
	ID          int64                `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Limits      string               `json:"limits"`
	Reset       string               `json:"reset"`
	Days        builtLimit           `json:"days"`
	Traffic     builtLimit           `json:"traffic"`
	Devices     builtLimit           `json:"devices"`
	Speed       builtLimit           `json:"speed"`
	Rates       billing.Coefficients `json:"rates"`
	Terms       []builtTerm          `json:"terms"`
	Stars       int64                `json:"stars,omitempty"`
	Rub         int64                `json:"rub,omitempty"`
}

// builtOf is the constructor in "shop". A dimension the tariff leaves endless has no range to
// build in: min and max are 0 and the page shows it as fixed.
func builtOf(c billing.Constructor, lang string) *builtOffer {
	out := &builtOffer{ID: c.Tariff.ID, Name: c.Tariff.Name, Description: billing.DescribeOffer(billing.Offer{Tariff: c.Tariff}, lang),
		Limits: billing.DescribeLimits(c.Tariff, lang), Reset: c.Tariff.ResetStrategy, Rates: c.Rates, Stars: c.Stars, Rub: c.Rub,
		Days:    builtLimit{Value: c.Days, Min: 1, Max: c.MaxDays},
		Traffic: builtLimit{Value: c.Traffic},
		Devices: builtLimit{Value: c.Devices},
		Speed:   builtLimit{Value: c.Speed},
	}
	if c.Traffic > 0 {
		out.Traffic.Min, out.Traffic.Max = c.TrafficMin, c.TrafficMax
	}
	if c.Devices > 0 {
		out.Devices.Min, out.Devices.Max = c.DevicesMin, c.DevicesMax
	}
	if c.Speed > 0 {
		out.Speed.Min, out.Speed.Max = c.SpeedMin, c.SpeedMax
	}
	for _, t := range c.Terms {
		out.Terms = append(out.Terms, builtTerm{Days: t.Days, Stars: t.Stars, Rub: t.Rub})
	}
	return out
}

// accessRequest is the body of "access": the code the buyer typed.
type accessRequest struct {
	miniAuth
	Token string `json:"token"`
	Code  string `json:"code"`
}

// valid: a request without a code is refused before Telegram is asked who sent it.
func (in *accessRequest) valid() bool { return strings.TrimSpace(in.Code) != "" }

// miniAppAccess takes an access code (promo access_paid or access_free) for the Telegram
// account. Paid: the account may now see and buy the tariff, and the answer says so (the page
// offers it). Free: a subscription on the tariff is given right away and the answer carries
// its link. The reason a code does not work is not told ("promo_unavailable" for all of it),
// so codes cannot be probed for existence or audience.
func (h *Handler) miniAppAccess(w http.ResponseWriter, r *http.Request) {
	var in accessRequest
	tgID, users, fail, ok := h.mini(w, r, &in)
	if !ok {
		return
	}
	if !h.promoLimit.allow(tgID, h.now(), in.Code) {
		fail(http.StatusTooManyRequests, "promo_try_later")
		return
	}
	userID, ok := ownerOf(users, in.Token)
	if !ok {
		fail(http.StatusForbidden, billing.ErrNotYours.Error())
		return
	}
	ctx := r.Context()
	p, err := h.promos.Check(ctx, tgID, userID, in.Code)
	if err != nil {
		fail(h.promoAttempt(err))
		return
	}
	tariffID, ok := promo.TariffOf(p)
	if !ok {
		fail(http.StatusConflict, "promo_unavailable")
		return
	}
	access, err := h.promos.GrantAccess(ctx, tgID, userID, in.Code)
	if err != nil {
		fail(h.promoAttempt(err))
		return
	}
	t, err := h.st.Q.GetTariff(ctx, tariffID)
	if err != nil {
		h.log.Warn("mini app: the tariff of an access code is gone", "tariff", tariffID, "err", err)
		fail(http.StatusInternalServerError, "internal")
		return
	}
	if access.Mode != promo.TypeAccessFree {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "type": access.Mode, "tariff_id": t.ID, "name": t.Name,
			"tariff": h.accessOffer(ctx, tgID, t, userID)})
		return
	}
	u, err := h.shop.AccessPlan(ctx, tgID, t, nil)
	switch {
	case errors.Is(err, billing.ErrTooManySubs):
		fail(http.StatusConflict, err.Error())
		return
	case errors.Is(err, billing.ErrNotForSale), errors.Is(err, billing.ErrBadOptions):
		fail(http.StatusConflict, "promo_unavailable")
		return
	case err != nil:
		h.log.Warn("mini app: the subscription of an access code was not given", "err", err)
		fail(http.StatusInternalServerError, "internal")
		return
	}
	_ = h.promos.SetAccessUser(ctx, access.ID, u.ID)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "type": access.Mode, "tariff_id": t.ID, "name": u.Name, "token": u.SubToken})
}

// accessOffer is the tariff an access code opened as "shop" offers it, so the page can show
// it without asking for the whole shop again.
func (h *Handler) accessOffer(ctx context.Context, tgID int64, t db.Tariff, userID int64) *offer {
	_, av, err := h.shop.Offers(ctx, tgID)
	if err != nil {
		return nil
	}
	cfg, _ := h.cfg(ctx)
	terms, err := domain.TermsOf(ctx, h.st.Q, t)
	if err != nil {
		return nil
	}
	v := &offer{ID: t.ID, Name: t.Name, Description: billing.Describe(t, t.DurationDays, cfg.Lang), Note: t.Description, Terms: []term{}}
	for _, tr := range terms {
		stars, rub := av.Prices(tr.PriceStars, tr.PriceRub)
		if stars == 0 && rub == 0 {
			continue
		}
		v.Terms = append(v.Terms, term{Days: tr.Days, Label: billing.TermLabel(t, tr.Days, cfg.Lang),
			Description: billing.Describe(t, tr.Days, cfg.Lang), Stars: stars, Rub: rub})
		if v.Stars == 0 && v.Rub == 0 {
			v.Stars, v.Rub = stars, rub
		}
	}
	if len(v.Terms) == 0 {
		return nil
	}
	return v
}

// miniAppFree takes the free plan for the Telegram account: the token of the new
// subscription, or the code of why not (free_off, free_active, free_channel with the
// channel to join, too_many_subs).
func (h *Handler) miniAppFree(w http.ResponseWriter, r *http.Request) {
	var in miniAuth
	tgID, _, fail, ok := h.mini(w, r, &in)
	if !ok {
		return
	}
	ctx := r.Context()
	u, err := h.shop.FreePlan(ctx, tgID)
	switch {
	case errors.Is(err, billing.ErrFreeChannel):
		link, _ := h.shop.FreeOffer(ctx, tgID)
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": err.Error(), "channel": link.Channel})
		return
	case errors.Is(err, billing.ErrFreeOff), errors.Is(err, billing.ErrFreeActive), errors.Is(err, billing.ErrTooManySubs):
		fail(http.StatusConflict, err.Error())
		return
	case err != nil:
		h.log.Warn("mini app: the free plan was not given", "err", err)
		fail(http.StatusInternalServerError, "internal")
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"token": u.SubToken, "name": u.Name})
}

// promoError is a code the buyer sent that cannot be used, as opposed to the panel failing.
func promoError(err error) bool { return promo.IsRefusal(err) }

// promoRequest is the body of "promo".
type promoRequest struct {
	miniAuth
	Token        string `json:"token"`
	Code         string `json:"code"`
	TariffID     int64  `json:"tariff_id"`
	TermDays     *int64 `json:"term_days"`
	PackageID    int64  `json:"package_id"`
	Provider     string `json:"provider"`
	ValidateOnly bool   `json:"validate_only"`
}

// valid: a request without a code is refused before Telegram is asked who sent it.
func (in *promoRequest) valid() bool { return strings.TrimSpace(in.Code) != "" }

// miniAppPromo validates a code or immediately redeems a bonus. Discount codes are only
// validated here; they are reserved atomically with the payment when the order is opened.
func (h *Handler) miniAppPromo(w http.ResponseWriter, r *http.Request) {
	var in promoRequest
	tgID, users, fail, ok := h.mini(w, r, &in)
	if !ok {
		return
	}
	if !h.promoLimit.allow(tgID, h.now(), in.Code) {
		fail(http.StatusTooManyRequests, "promo_try_later")
		return
	}
	userID, ok := ownerOf(users, in.Token)
	if !ok {
		fail(http.StatusForbidden, billing.ErrNotYours.Error())
		return
	}
	if in.ValidateOnly {
		tariffID, amount, currency, err := h.miniAppPromoOrder(r.Context(), tgID, userID, in.TariffID, in.TermDays, in.PackageID, billing.AdapterOf(in.Provider))
		if err != nil {
			fail(http.StatusConflict, "promo_unavailable")
			return
		}
		p, err := h.promos.Validate(r.Context(), tgID, userID, tariffID, amount, currency, in.Code)
		if err == nil && p.Type != "percent" && p.Type != "fixed" {
			err = promo.ErrNotDiscount
		}
		if err != nil {
			fail(h.promoAttempt(err))
			return
		}
		discount := promo.DiscountAmount(p, amount)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "type": p.Type, "discount": discount, "final_amount": amount - discount, "currency": currency})
		return
	}
	p, err := h.promos.Check(r.Context(), tgID, userID, in.Code)
	if err != nil {
		fail(h.promoAttempt(err))
		return
	}
	switch p.Type {
	case "days", "traffic":
		r, err := h.promos.RedeemBonus(r.Context(), tgID, userID, in.Code)
		if err != nil {
			fail(h.promoAttempt(err))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "type": p.Type, "days": r.Days, "bytes": r.Bytes, "message": "promo_applied"})
	case "percent", "fixed":
		// A discount needs an order: the page keeps the code for the checkout, where it is
		// checked against the price and reserved with the payment.
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "type": p.Type, "code": p.Code})
	default:
		fail(http.StatusConflict, "promo_unavailable")
	}
}

// miniAppPromoOrder is the order a discount code is checked against: the tariff, and the
// price of the chosen term (days nil: the first) or of the package, with provider.
func (h *Handler) miniAppPromoOrder(ctx context.Context, tgID, userID, tariffID int64, days *int64, packageID int64, provider string) (int64, int64, string, error) {
	if h.shop == nil || provider == "" {
		return 0, 0, "", promo.ErrUnavailable
	}
	if packageID != 0 {
		offers, _, err := h.shop.PackageOffers(ctx, userID)
		if err != nil {
			return 0, 0, "", err
		}
		user, err := h.st.Q.GetUser(ctx, userID)
		if err != nil || !user.TariffID.Valid {
			return 0, 0, "", promo.ErrTariff
		}
		for _, offer := range offers {
			if offer.Package.ID == packageID {
				if amount, currency, ok := promoOrderPrice(provider, offer.Stars, offer.Rub); ok {
					return user.TariffID.Int64, amount, currency, nil
				}
			}
		}
		return 0, 0, "", promo.ErrUnavailable
	}
	offers, _, err := h.shop.Offers(ctx, tgID)
	if err != nil {
		return 0, 0, "", err
	}
	for _, offer := range offers {
		if offer.Tariff.ID == tariffID {
			t, ok := offer.Term(days)
			if !ok {
				break
			}
			if amount, currency, ok := promoOrderPrice(provider, t.Stars, t.Rub); ok {
				return offer.Tariff.ID, amount, currency, nil
			}
		}
	}
	return 0, 0, "", promo.ErrUnavailable
}

func promoOrderPrice(provider string, stars, rub int64) (int64, string, bool) {
	switch {
	case provider == billing.Stars && stars > 0:
		return stars, "XTR", true
	case strings.HasPrefix(provider, billing.AddonPrefix) && rub > 0:
		return rub, "RUB", true
	default:
		return 0, "", false
	}
}

// historyRequest is the body of "promo-history". Its unreadable body is answered in plain
// text, as it always was (plainRequest).
type historyRequest struct{ miniAuth }

func (historyRequest) plainBadRequest() {}

// miniAppPromoHistory lists the promo codes the Telegram account has used, the latest first.
func (h *Handler) miniAppPromoHistory(w http.ResponseWriter, r *http.Request) {
	var in historyRequest
	tgID, _, fail, ok := h.mini(w, r, &in)
	if !ok {
		return
	}
	rs, err := h.st.Q.ListPromoRedemptionsByTgWithCode(r.Context(), db.ListPromoRedemptionsByTgWithCodeParams{TgID: tgID, Lim: 100})
	if err != nil {
		fail(http.StatusInternalServerError, "internal")
		return
	}
	type item struct {
		Code     string `json:"code"`
		Days     int64  `json:"days"`
		Bytes    int64  `json:"bytes"`
		Discount int64  `json:"discount"`
		Currency string `json:"currency"`
		At       int64  `json:"at"`
	}
	out := struct {
		Items []item `json:"items"`
	}{Items: []item{}}
	for _, row := range rs {
		redemption := row
		out.Items = append(out.Items, item{Code: row.Code, Days: redemption.Days, Bytes: redemption.Bytes, Discount: redemption.DiscountAmount, Currency: redemption.Currency, At: redemption.RedeemedAt})
	}
	_ = json.NewEncoder(w).Encode(out)
}

// promoAttempt is the answer to a code that could not be used. Why a code does not apply is
// not told: "promo_unavailable" for all of it, so codes cannot be probed (the buyer must not
// learn which codes exist or who they are for); the admin finds the reason in the log.
// Anything else is the panel's trouble, logged and answered as such.
func (h *Handler) promoAttempt(err error) (int, string) {
	if code := promoAttemptCode(err); code != "" {
		h.log.Info("mini app: promo code refused", "reason", err)
		return http.StatusConflict, code
	}
	h.log.Error("mini app: promo code failed", "err", err)
	return http.StatusInternalServerError, "internal"
}

func promoAttemptCode(err error) string {
	if promoError(err) {
		return "promo_unavailable"
	}
	return ""
}

// invoiceFailure is what the Mini App answers when no invoice could be made. What the buyer
// can act on is told as it is. A provider that is switched off is "provider_off". Anything
// else, a provider that failed, a database that was busy, is the panel's trouble: the buyer
// is told it did not work, not that payment is off, and unexplained says nobody has logged
// the cause yet (billing logs a provider's failure itself).
func invoiceFailure(err error) (status int, code string, unexplained bool) {
	if code, known := billing.Refusal(err); known {
		return http.StatusConflict, code, false
	}
	// The fork's constructor: its refusals are the buyer's own doing too.
	for _, e := range []error{billing.ErrBadOptions, billing.ErrNoConstructor} {
		if errors.Is(err, e) {
			return http.StatusConflict, e.Error(), false
		}
	}
	if errors.Is(err, billing.ErrProviderOff) { // the provider failed
		return http.StatusBadGateway, "invoice_failed", false
	}
	return http.StatusBadGateway, "invoice_failed", true
}

// reissueRequest is the body of "reissue": the subscription to give a new link.
type reissueRequest struct {
	miniAuth
	Token string `json:"token"`
}

// valid: a new link is for one subscription, which has to be named.
func (in *reissueRequest) valid() bool { return in.Token != "" }

// miniAppReissue replaces the link of one of the account's subscriptions within the admin's
// rules (domain.ReissueRules) and answers with the new token, which the page opens at once:
// the old link, the keys and every bound device stop working. Unlike the unbind of the page
// itself, which the link alone authorises, this takes Telegram's proof of who asks: the link
// is what a leak exposes, and whoever holds a leaked link must not be able to cut its owner
// off by reissuing it.
func (h *Handler) miniAppReissue(w http.ResponseWriter, r *http.Request) {
	var in reissueRequest
	_, users, fail, ok := h.mini(w, r, &in)
	if !ok {
		return
	}
	userID, ok := ownerOf(users, in.Token)
	if !ok || userID == 0 {
		fail(http.StatusForbidden, billing.ErrNotYours.Error())
		return
	}
	u, err := h.reissuer.ReissueByOwner(r.Context(), userID, "miniapp")
	switch {
	case err == nil:
		h.log.Info("subs: the subscriber reissued their link", "user", userID)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": u.SubToken})
	case errors.Is(err, domain.ErrReissueCooldown):
		w.WriteHeader(http.StatusTooManyRequests)
		out := map[string]any{"code": "reissue_cooldown"}
		// Without the moment the page says only that it cannot: never a date of year one.
		if st, err := h.reissuer.ReissueState(r.Context(), userID); err == nil && !st.Next.IsZero() {
			out["reissue_after"] = st.Next
		}
		_ = json.NewEncoder(w).Encode(out)
	case errors.Is(err, domain.ErrReissueOff):
		fail(http.StatusForbidden, "reissue_off")
	case errors.Is(err, domain.ErrNotFound):
		fail(http.StatusNotFound, "not_found")
	default:
		h.log.Warn("subs: the link was not reissued", "user", userID, "err", err)
		fail(http.StatusInternalServerError, "internal")
	}
}
