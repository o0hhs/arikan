package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mikan/internal/panel/promo"
	"mikan/internal/panel/store/db"
)

func TestPlategaAndRollyPay(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	platega := "pending"
	rollyAmount := "199.00"
	rollyStatus := "created"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/transaction/process":
			if r.Header.Get("X-MerchantId") != "merchant-1" || r.Header.Get("X-Secret") != "sekret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var in struct {
				Details struct {
					Amount   float64 `json:"amount"`
					Currency string  `json:"currency"`
				} `json:"paymentDetails"`
				Meta map[string]string `json:"metadata"`
			}
			if json.Unmarshal(body, &in) != nil || in.Details.Amount != 199 || in.Details.Currency != "RUB" || in.Meta["user_email"] != "555@telegram.org" {
				t.Errorf("platega invoice: %s", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"transactionId": "tx-1", "redirect": "https://pay.platega.io/tx-1", "status": "PENDING"})
		case strings.HasPrefix(r.URL.Path, "/transaction/"):
			amount, currency := 199.0, "RUB"
			switch platega {
			case "short":
				amount = 1
			case "usd":
				currency = "USD"
			}
			status := "PENDING"
			if platega != "pending" {
				status = "CONFIRMED"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "paymentDetails": map[string]any{"amount": amount, "currency": currency}})
		case r.URL.Path == "/api/v1/payments" && r.Method == http.MethodPost:
			if r.Header.Get("X-API-Key") != "key-1" || r.Header.Get("X-Nonce") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var in struct {
				Amount   string `json:"amount"`
				Currency string `json:"payment_currency"`
			}
			if json.Unmarshal(body, &in) != nil || in.Amount != "199.00" || in.Currency != "RUB" {
				t.Errorf("rolly invoice: %s", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"payment_id": "pay-1", "pay_url": "https://pay.rollypay.io/pay/tok", "status": "created"})
		case strings.HasPrefix(r.URL.Path, "/api/v1/payments/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"status": rollyStatus, "amount": rollyAmount, "payment_currency": "RUB"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	e.s.httpGate = gateHTTP{client: srv.Client(), plategaBase: srv.URL, rollyBase: srv.URL}
	if err := SaveProvider(ctx, e.s.d.Settings, PlategaSetup{Enabled: true, MerchantID: "merchant-1", Method: 99}); !errors.Is(err, ErrPlategaConfig) {
		t.Fatalf("bad method: %v", err)
	}
	if err := SaveProvider(ctx, e.s.d.Settings, PlategaSetup{Enabled: true, MerchantID: "merchant-1", Secret: "sekret", Method: PlategaSBP}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProvider(ctx, e.s.d.Settings, PlategaSetup{Enabled: true, MerchantID: "merchant-1", Method: PlategaSBP}); err != nil {
		t.Fatal(err)
	}
	if err := SaveRollyProvider(ctx, e.s.d.Settings, RollySetup{Enabled: true, APIKey: "key-1", SigningSecret: "sign-1"}); err != nil {
		t.Fatal(err)
	}
	if got := e.s.Available(ctx).Addons; !containsAll(got, "platega", "rollypay") {
		t.Fatalf("providers: %v", got)
	}
	view, err := e.s.PlategaView(ctx, "https://panel.example/sub")
	if err != nil || !view.SecretSet || !strings.Contains(view.WebhookURL, "/pay/addon/platega/") {
		t.Fatalf("platega view: %+v %v", view, err)
	}

	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: AddonPrefix + "platega"})
	if err != nil || p.PayUrl != "https://pay.platega.io/tx-1" || p.ExternalID.String != "tx-1" {
		t.Fatalf("platega invoice: %+v %v", p, err)
	}
	body := []byte(`{"id":"tx-1","status":"CONFIRMED","amount":199,"currency":"RUB"}`)
	if code := e.hook("addon/platega", e.token, "203.0.113.8", body, map[string]string{"X-MerchantId": "merchant-1", "X-Secret": "nope"}); code != http.StatusForbidden || e.payment(p.ID).Status != PaymentPending {
		t.Fatalf("bad secret: %d %s", code, e.payment(p.ID).Status)
	}
	if code := e.hook("addon/platega", e.token, "203.0.113.8", body, map[string]string{"X-MerchantId": "merchant-1", "X-Secret": "sekret"}); code != http.StatusOK || e.payment(p.ID).Status != PaymentPending {
		t.Fatalf("still pending: %d %s", code, e.payment(p.ID).Status)
	}
	platega = "short"
	if code := e.hook("addon/platega", e.token, "203.0.113.8", body, map[string]string{"X-MerchantId": "merchant-1", "X-Secret": "sekret"}); code != http.StatusOK || e.payment(p.ID).Status != PaymentPending {
		t.Fatalf("short amount: %d %s", code, e.payment(p.ID).Status)
	}
	platega = "usd"
	if code := e.hook("addon/platega", e.token, "203.0.113.8", body, map[string]string{"X-MerchantId": "merchant-1", "X-Secret": "sekret"}); code != http.StatusOK || e.payment(p.ID).Status != PaymentPending {
		t.Fatalf("wrong currency: %d %s", code, e.payment(p.ID).Status)
	}
	platega = "paid"
	if code := e.hook("addon/platega", e.token, "203.0.113.8", body, map[string]string{"X-MerchantId": "merchant-1", "X-Secret": "sekret"}); code != http.StatusOK || e.payment(p.ID).Status != PaymentApplied {
		t.Fatalf("confirmed: %d %s", code, e.payment(p.ID).Status)
	}
	users := e.users()
	if code := e.hook("addon/platega", e.token, "203.0.113.8", body, map[string]string{"X-MerchantId": "merchant-1", "X-Secret": "sekret"}); code != http.StatusOK || e.users() != users {
		t.Fatalf("applied twice: %d users %d want %d", code, e.users(), users)
	}

	acc, err := e.st.Q.CreateWebAccount(ctx, db.CreateWebAccountParams{Email: "buyer@example.com", CreatedAt: e.now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.s.Invoice(ctx, InvoiceRequest{Web: true, WebAccountID: acc.ID, TariffID: e.sale.ID, Provider: AddonPrefix + "rollypay"})
	if err != nil || !strings.HasPrefix(r.PayUrl, "https://pay.rollypay.io/") {
		t.Fatalf("rolly invoice: %+v %v", r, err)
	}
	raw := []byte(`{"event_type":"payment.paid","payment_id":"pay-1","status":"paid","amount":"1.00","currency":"USD"}`)
	sig := rollySig("sign-1", "1700000000", raw)
	if code := e.hook("addon/rollypay", e.token, "203.0.113.9", raw, map[string]string{"X-Timestamp": "1700000000", "X-Signature": sig}); code != http.StatusOK || e.payment(r.ID).Status != PaymentPending {
		t.Fatalf("rolly not paid yet: %d %s", code, e.payment(r.ID).Status)
	}
	last := byte('1')
	if sig[len(sig)-1] == '1' {
		last = '0'
	}
	bad := sig[:len(sig)-1] + string(last)
	rollyStatus = "paid"
	if code := e.hook("addon/rollypay", e.token, "203.0.113.9", raw, map[string]string{"X-Timestamp": "1700000000", "X-Signature": bad}); code != http.StatusForbidden || e.payment(r.ID).Status != PaymentPending {
		t.Fatalf("bad signature: %d %s", code, e.payment(r.ID).Status)
	}
	rollyAmount = "1.00"
	if code := e.hook("addon/rollypay", e.token, "203.0.113.9", raw, map[string]string{"X-Timestamp": "1700000000", "X-Signature": sig}); code != http.StatusOK || e.payment(r.ID).Status != PaymentPending {
		t.Fatalf("rolly short: %d %s", code, e.payment(r.ID).Status)
	}
	rollyAmount = "199.00"
	if code := e.hook("addon/rollypay", e.token, "203.0.113.9", raw, map[string]string{"X-Timestamp": "1700000000", "X-Signature": strings.ToUpper(sig)}); code != http.StatusOK || e.payment(r.ID).Status != PaymentApplied {
		t.Fatalf("rolly paid: %d %s", code, e.payment(r.ID).Status)
	}
	if err := e.s.validatePromoProvider(ctx, AddonPrefix+"platega"); !errors.Is(err, promo.ErrRefundUnsupported) {
		t.Fatal(err)
	}
	if err := e.s.validatePromoProvider(ctx, AddonPrefix+"rollypay"); !errors.Is(err, promo.ErrRefundUnsupported) {
		t.Fatal(err)
	}
}

func rollySig(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func containsAll(got []string, want ...string) bool {
	for _, w := range want {
		ok := false
		for _, g := range got {
			if g == w {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}
