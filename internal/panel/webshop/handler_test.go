package webshop

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

func TestShopHTTP(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := domain.Seed(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(st.Q)
	var mailed Message
	h := New(Deps{Store: st, Settings: set, Now: func() time.Time { return now }, Log: slog.New(slog.DiscardHandler),
		SubBase: func(context.Context) string { return "https://vpn.example/s" },
		Brand:   func(context.Context) string { return "North" },
		Send:    func(_ context.Context, _ ShopConfig, m Message) error { mailed = m; return nil },
	})
	if h.On(ctx) || h.ShopURL(ctx) != "" {
		t.Fatal("the shop is on before the admin turns it on")
	}
	off := httptest.NewRequest(http.MethodGet, "/s/shop/api/config", nil)
	rec := httptest.NewRecorder()
	h.Serve(rec, off, "api/config")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("off: %d", rec.Code)
	}

	cfg := ShopConfig{Enabled: true, SMTPHost: "mail.example", SMTPPort: 587, SMTPFrom: "shop@example.com", Origins: []string{"https://operator.example"}}
	if err := Save(ctx, set, cfg); err != nil {
		t.Fatal(err)
	}
	if h.ShopURL(ctx) != "https://vpn.example/s/shop" {
		t.Fatalf("url %q", h.ShopURL(ctx))
	}
	back := h.ReturnURL(ctx, "abc")
	if back != "https://vpn.example/s/shop?order=abc" {
		t.Fatalf("return %q", back)
	}
	if err := Save(ctx, set, ShopConfig{Enabled: true, ReturnURL: "https://pay.example/done", SMTPHost: "mail.example", SMTPPort: 587, SMTPFrom: "shop@example.com", SMTPPass: "secret", Origins: []string{"https://operator.example"}}); err != nil {
		t.Fatal(err)
	}
	if got := h.ReturnURL(ctx, "abc"); got != "https://pay.example/done?order=abc" {
		t.Fatalf("custom return %q", got)
	}
	kept, err := Load(ctx, set)
	if err != nil || kept.SMTPPass != "secret" {
		t.Fatalf("password: %+v %v", kept, err)
	}
	if err := Save(ctx, set, ShopConfig{Enabled: true, ReturnURL: "https://pay.example/done", SMTPHost: "mail.example", SMTPPort: 587, SMTPFrom: "shop@example.com", Origins: []string{"https://operator.example"}}); err != nil {
		t.Fatal(err)
	}
	if kept, err = Load(ctx, set); err != nil || kept.SMTPPass != "secret" {
		t.Fatalf("empty password wiped it: %+v %v", kept, err)
	}

	noHeader := post("/s/shop/api/code", `{"email":"a@example.com"}`, "")
	h.Serve(noHeader.rec, noHeader.req, "api/code")
	if noHeader.rec.Code != http.StatusForbidden {
		t.Fatalf("no header: %d", noHeader.rec.Code)
	}
	cross := post("/s/shop/api/code", `{"email":"a@example.com"}`, "1")
	cross.req.Header.Set("Origin", "https://evil.example")
	cross.req.Header.Set("Sec-Fetch-Site", "cross-site")
	h.Serve(cross.rec, cross.req, "api/code")
	if cross.rec.Code != http.StatusForbidden {
		t.Fatalf("evil origin: %d", cross.rec.Code)
	}

	ask := post("/s/shop/api/code", `{"email":"Buyer@Example.com"}`, "1")
	h.Serve(ask.rec, ask.req, "api/code")
	if ask.rec.Code != http.StatusOK || !strings.Contains(mailed.Body, "Code: ") {
		t.Fatalf("code: %d %q", ask.rec.Code, mailed.Body)
	}
	code := codeFrom(mailed.Body)
	in := post("/s/shop/api/login", `{"email":"buyer@example.com","code":"`+code+`"}`, "1")
	h.Serve(in.rec, in.req, "api/login")
	if in.rec.Code != http.StatusOK || !strings.Contains(in.rec.Header().Get("Set-Cookie"), "mikan_shop=") {
		t.Fatalf("login: %d %q %s", in.rec.Code, in.rec.Header().Get("Set-Cookie"), in.rec.Body.String())
	}
	me := httptest.NewRequest(http.MethodGet, "/s/shop/api/me", nil)
	me.Header.Set("Cookie", in.rec.Header().Get("Set-Cookie"))
	meRec := httptest.NewRecorder()
	h.Serve(meRec, me, "api/me")
	if meRec.Code != http.StatusOK || !strings.Contains(meRec.Body.String(), "buyer@example.com") {
		t.Fatalf("me: %d %s", meRec.Code, meRec.Body.String())
	}

	// A listed origin may call the order API. A pending payment is not applied by reading it.
	ts, err := st.Q.ListTariffs(ctx)
	if err != nil || len(ts) == 0 {
		t.Fatal(err)
	}
	pay, err := st.Q.CreatePayment(ctx, db.CreatePaymentParams{Provider: "addon:fake", Payload: "ord-1", TgID: 0, Kind: "new", TariffID: sql.NullInt64{Int64: ts[0].ID, Valid: true}, TariffName: ts[0].Name, Amount: 100, Currency: "RUB", CreatedAt: now.Unix(), Web: true})
	if err != nil {
		t.Fatal(err)
	}
	order := httptest.NewRequest(http.MethodGet, "/s/shop/api/order?order=ord-1", nil)
	order.Header.Set("Origin", "https://operator.example")
	order.Header.Set("Sec-Fetch-Site", "cross-site")
	orderRec := httptest.NewRecorder()
	h.Serve(orderRec, order, "api/order")
	if orderRec.Code != http.StatusOK || !strings.Contains(orderRec.Body.String(), `"status":"pending"`) || strings.Contains(orderRec.Body.String(), "token") {
		t.Fatalf("order: %d %s", orderRec.Code, orderRec.Body.String())
	}
	if orderRec.Header().Get("Access-Control-Allow-Origin") != "https://operator.example" {
		t.Fatalf("cors: %q", orderRec.Header().Get("Access-Control-Allow-Origin"))
	}
	if got, err := st.Q.GetPayment(ctx, pay.ID); err != nil || got.Status != "pending" {
		t.Fatalf("reading the order paid it: %+v %v", got, err)
	}
	payReq := post("/s/shop/api/pay", `{"tariff_id":1,"provider":"addon:fake"}`, "1")
	h.Serve(payReq.rec, payReq.req, "api/pay")
	if payReq.rec.Code != http.StatusUnauthorized {
		t.Fatalf("pay signed out: %d %s", payReq.rec.Code, payReq.rec.Body.String())
	}
}

func TestProblems(t *testing.T) {
	if f, c := Problems(ShopConfig{ReturnURL: "http://shop.example"}); c != "web_shop_return_url" || f != "return_url" {
		t.Fatalf("%s %s", f, c)
	}
	if _, c := Problems(ShopConfig{Origins: []string{"https://ok.example", "javascript:alert(1)"}}); c != "web_shop_origins" {
		t.Fatal(c)
	}
	if _, c := Problems(ShopConfig{SMTPHost: "mail.example"}); c != "web_shop_smtp" {
		t.Fatal(c)
	}
	if _, c := Problems(ShopConfig{Enabled: true, Origins: []string{"http://localhost:5173"}}); c != "" {
		t.Fatal(c)
	}
}

func TestTelegramLoginHash(t *testing.T) {
	token := "123456:ABC"
	in := tgLogin{ID: 42, FirstName: "Ann", Username: "ann", AuthDate: 1_700_000_000, Hash: "00"}
	pairs := "auth_date=1700000000\nfirst_name=Ann\nid=42\nusername=ann"
	secret := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(pairs))
	in.Hash = hex.EncodeToString(mac.Sum(nil))
	if !in.valid(token, time.Unix(1_700_000_000, 0)) {
		t.Fatal("a genuine widget was refused")
	}
	in.FirstName = "Other"
	if in.valid(token, time.Unix(1_700_000_000, 0)) {
		t.Fatal("a changed name still matched")
	}
}

func codeFrom(body string) string {
	_, rest, _ := strings.Cut(body, "Code: ")
	return strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0])
}

type posted struct {
	req *http.Request
	rec *httptest.ResponseRecorder
}

func post(url, body, header string) posted {
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if header != "" {
		req.Header.Set("X-Mikan-Shop", header)
	}
	return posted{req: req, rec: httptest.NewRecorder()}
}
