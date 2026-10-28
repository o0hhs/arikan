package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

func newTest(t *testing.T) (*Service, *settings.Settings) {
	t.Helper()
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	set := settings.New(st.Q)
	now := time.Unix(1_800_000_000, 0)
	return New(st, set, nil, func() time.Time { return now }), set
}

func TestValidURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://example.com/hook":       true,
		"http://example.com/hook":        false,
		"https://user:pass@example.com/": false,
		"https://":                       false,
		"tg://resolve?domain=x":          false,
		"":                               false,
	} {
		if got := ValidURL(raw); got != want {
			t.Errorf("ValidURL(%q) = %v, want %v", raw, got, want)
		}
	}
}

// The site checks the signature the panel sends: HMAC over "timestamp.body", hex.
func TestSignature(t *testing.T) {
	body := []byte(`{"event":"subscription.paid"}`)
	m := hmac.New(sha256.New, []byte("s3cret-key"))
	m.Write([]byte("1800000000."))
	m.Write(body)
	want := "sha256=" + hex.EncodeToString(m.Sum(nil))
	if got := Signature("s3cret-key", 1800000000, body); got != want {
		t.Fatalf("signature %q, want %q", got, want)
	}
}

// A paid subscription is queued in the payment's transaction; the worker posts it to the
// site with the event, the delivery number, the timestamp and the signature, and marks it
// delivered on 2xx.
func TestPaidIsDelivered(t *testing.T) {
	s, set := newTest(t)
	ctx := context.Background()
	var (
		gotBody []byte
		gotHead http.Header
	)
	site := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHead = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer site.Close()
	s.Client = site.Client()
	if err := settings.Set(ctx, set, settings.KeyWebhookURL, site.URL+"/hook"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, set, settings.KeyWebhookSecret, "s3cret-key"); err != nil {
		t.Fatal(err)
	}
	pay := db.Payment{ID: 7, Provider: "stars", Amount: 150, Currency: "XTR", Kind: "new", TariffName: "Плюс", TgID: 555,
		TariffID: nullInt(2), TermDays: nullInt(30)}
	u := db.User{ID: 101, Name: "Анна", SubToken: "AbCdEf", ExpiresAt: nullInt(1_802_592_000)}
	if err := s.Paid(ctx, s.Store.Q, pay, u, true); err != nil {
		t.Fatal(err)
	}
	if err := s.sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if gotHead.Get("X-Arikan-Event") != EventSubscriptionPaid || gotHead.Get("X-Arikan-Delivery") == "" {
		t.Fatalf("headers: %v", gotHead)
	}
	ts := gotHead.Get("X-Arikan-Timestamp")
	want := Signature("s3cret-key", s.Now().Unix(), gotBody)
	if gotHead.Get("X-Arikan-Signature") != want || ts != "1800000000" {
		t.Fatalf("signature %q (ts %q), want %q", gotHead.Get("X-Arikan-Signature"), ts, want)
	}
	var p Payload
	if err := json.Unmarshal(gotBody, &p); err != nil {
		t.Fatal(err)
	}
	if p.Event != EventSubscriptionPaid || p.Subscription.ID != 101 || p.Subscription.Token != "AbCdEf" ||
		p.Subscription.ExpiresAt == nil || p.Payment.ID != 7 || p.TelegramID != 555 {
		t.Fatalf("payload: %+v", p)
	}
	due, err := s.Store.Q.DueWebhookDeliveries(ctx, db.DueWebhookDeliveriesParams{NextAt: s.Now().Add(48 * time.Hour).Unix(), Limit: 10})
	if err != nil || len(due) != 0 {
		t.Fatalf("a delivered row is due again: %+v %v", due, err)
	}
}

// A site that answers an error keeps the row for a retry, with the reason kept.
func TestFailedDeliveryIsRetried(t *testing.T) {
	s, set := newTest(t)
	ctx := context.Background()
	site := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer site.Close()
	s.Client = site.Client()
	if err := settings.Set(ctx, set, settings.KeyWebhookURL, site.URL); err != nil {
		t.Fatal(err)
	}
	if err := s.Paid(ctx, s.Store.Q, db.Payment{ID: 8, Kind: "renew", TgID: 1}, db.User{ID: 2}, false); err != nil {
		t.Fatal(err)
	}
	if err := s.sweep(ctx); err != nil {
		t.Fatal(err)
	}
	due, err := s.Store.Q.DueWebhookDeliveries(ctx, db.DueWebhookDeliveriesParams{NextAt: s.Now().Add(2 * time.Minute).Unix(), Limit: 10})
	if err != nil || len(due) != 1 || due[0].Attempts != 1 || !strings.Contains(due[0].LastError, "502") {
		t.Fatalf("a failed delivery: %+v %v", due, err)
	}
}

// With no address, nothing is queued at all.
func TestNoURLNoDelivery(t *testing.T) {
	s, _ := newTest(t)
	if err := s.Paid(context.Background(), s.Store.Q, db.Payment{ID: 9, Kind: "new"}, db.User{ID: 3}, true); err != nil {
		t.Fatal(err)
	}
	due, err := s.Store.Q.DueWebhookDeliveries(context.Background(), db.DueWebhookDeliveriesParams{NextAt: s.Now().Add(time.Hour).Unix(), Limit: 10})
	if err != nil || len(due) != 0 {
		t.Fatalf("queued without an address: %+v %v", due, err)
	}
}

func TestFeedPayloadKeepsTheEventData(t *testing.T) {
	ev := db.AddonEvent{ID: 42, Type: events.UserCreated, UserID: nullInt(7), TgID: nullInt(555),
		Data: `{"source":"bot","tariff_id":3}`, CreatedAt: 1_800_000_000}
	p := feedPayload(ev)
	if p.Event != events.UserCreated || p.ID != 42 || p.UserID != 7 || p.TelegramID != 555 || !p.At.Equal(time.Unix(1_800_000_000, 0).UTC()) {
		t.Fatalf("payload: %+v", p)
	}
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"data":{"source":"bot","tariff_id":3}`) {
		t.Fatalf("data is not the feed's own JSON: %s", body)
	}
}

func TestSkipFeedEventLeavesSalesToPaid(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   db.AddonEvent
		want bool
	}{
		{"new sale", db.AddonEvent{Type: events.PaymentPaid, Data: `{"kind":"new"}`}, true},
		{"renewal", db.AddonEvent{Type: events.PaymentPaid, Data: `{"kind":"renew"}`}, true},
		{"package", db.AddonEvent{Type: events.PaymentPaid, Data: `{"kind":"package"}`}, false},
		{"trial", db.AddonEvent{Type: events.PaymentPaid, Data: `{"kind":"trial"}`}, false},
		{"other event", db.AddonEvent{Type: events.UserCreated, Data: `{}`}, false},
		{"broken data", db.AddonEvent{Type: events.PaymentPaid, Data: `{`}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := skipFeedEvent(tc.ev); got != tc.want {
				t.Fatalf("skipFeedEvent() = %v, want %v", got, tc.want)
			}
		})
	}
}

func nullInt(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
