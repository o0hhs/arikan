// Package webhooks tells the admin's own site about sold and renewed subscriptions: a
// delivery is queued in the payment's transaction and sent by the worker, signed with the
// admin's secret. A site that is down is retried; a purchase never waits for it.
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// The event of a sold or renewed subscription, and the one a test sends.
const (
	EventSubscriptionPaid = "subscription.paid"
	EventTest             = "subscription.test"
)

// Retention is how long delivered and failed rows are kept, and DeliveryTimeout how long
// one site is given to answer.
const (
	Retention       = 7 * 24 * time.Hour
	DeliveryTimeout = 10 * time.Second
	// maxAttempts is where the retries stop growing: a site down for a day is still tried
	// every few hours, and the row is pruned with the retention.
	maxAttempts = 10
	dueBatch    = 50
)

// Payload is what the site gets, as it is: the subscription's id and the key its apps use
// (uuid of the slot, the token of the link), the term, and the payment that bought it.
type Payload struct {
	Event        string           `json:"event"`
	At           time.Time        `json:"at"`
	Subscription SubscriptionInfo `json:"subscription"`
	Payment      PaymentInfo      `json:"payment"`
	TelegramID   int64            `json:"telegram_id,omitempty"`
}

// SubscriptionInfo is the subscription as the payment left it.
type SubscriptionInfo struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	UUID         string     `json:"uuid,omitempty"`
	Slot         string     `json:"slot,omitempty"`
	Token        string     `json:"token,omitempty"`
	TariffID     int64      `json:"tariff_id,omitempty"`
	Tariff       string     `json:"tariff,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	TermDays     int64      `json:"term_days,omitempty"`
	TrafficLimit *int64     `json:"traffic_limit,omitempty"`
	DeviceLimit  *int64     `json:"device_limit,omitempty"`
}

// PaymentInfo is the order that made the change.
type PaymentInfo struct {
	ID       int64  `json:"id"`
	Provider string `json:"provider"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	Kind     string `json:"kind"`
	Created  bool   `json:"created"`
}

// Service queues and delivers the panel's webhooks.
type Service struct {
	Store    *store.Store
	Settings *settings.Settings
	Log      *slog.Logger
	Now      func() time.Time
	Client   *http.Client
}

func New(st *store.Store, set *settings.Settings, log *slog.Logger, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{Store: st, Settings: set, Log: log, Now: now, Client: &http.Client{Timeout: DeliveryTimeout}}
}

// ValidURL: where a signed JSON may go to. https only, no credentials in the address.
func ValidURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && len(raw) <= 500
}

// Enabled: there is an address to send to.
func (s *Service) Enabled(ctx context.Context) (string, bool, error) {
	u, err := s.Settings.String(ctx, settings.KeyWebhookURL)
	if err != nil {
		return "", false, err
	}
	return u, u != "", nil
}

// Paid queues the subscription sale or renewal the payment made, in the payment's own
// transaction. Nothing is queued while no address is set; a read of the address that fails
// is the caller's to log, never the purchase's to fail.
func (s *Service) Paid(ctx context.Context, q *db.Queries, p db.Payment, u db.User, created bool) error {
	_, on, err := s.Enabled(ctx)
	if err != nil || !on {
		return err
	}
	body, err := json.Marshal(s.payload(ctx, q, p, u, created))
	if err != nil {
		return err
	}
	now := s.Now()
	return q.AddWebhookDelivery(ctx, db.AddWebhookDeliveryParams{Event: EventSubscriptionPaid, Payload: string(body), NextAt: now.Unix(), CreatedAt: now.Unix()})
}

func (s *Service) payload(ctx context.Context, q *db.Queries, p db.Payment, u db.User, created bool) Payload {
	out := Payload{
		Event: EventSubscriptionPaid, At: s.Now(), TelegramID: p.TgID,
		Subscription: SubscriptionInfo{
			ID: u.ID, Name: u.Name, Token: u.SubToken, TariffID: p.TariffID.Int64, Tariff: p.TariffName, TermDays: p.TermDays.Int64,
		},
		Payment: PaymentInfo{ID: p.ID, Provider: p.Provider, Amount: p.Amount, Currency: p.Currency, Kind: p.Kind, Created: created},
	}
	if u.ExpiresAt.Valid {
		t := time.Unix(u.ExpiresAt.Int64, 0).UTC()
		out.Subscription.ExpiresAt = &t
	}
	if u.TrafficLimit.Valid {
		n := u.TrafficLimit.Int64
		out.Subscription.TrafficLimit = &n
	}
	if u.DeviceLimit.Valid {
		n := u.DeviceLimit.Int64
		out.Subscription.DeviceLimit = &n
	}
	if u.SlotID.Valid {
		if slot, err := q.GetSlot(ctx, u.SlotID.Int64); err == nil {
			out.Subscription.UUID, out.Subscription.Slot = slot.Uuid, slot.Name
		}
	}
	return out
}

// Test sends a synthetic delivery now and says what the site answered: the admin's button
// behind the address and the secret.
type TestResult struct {
	Status  int    `json:"status"`
	MS      int64  `json:"ms"`
	Error   string `json:"error,omitempty"`
	Payload string `json:"payload" doc:"Что именно ушло: сайт может сверить подпись"`
}

// Test posts a test event to the saved address with the saved secret.
func (s *Service) Test(ctx context.Context) (TestResult, error) {
	url, on, err := s.Enabled(ctx)
	if err != nil {
		return TestResult{}, err
	}
	if !on || !ValidURL(url) {
		return TestResult{}, errors.New("webhook_not_configured")
	}
	body, err := json.Marshal(Payload{Event: EventTest, At: s.Now(), Subscription: SubscriptionInfo{ID: 1, Name: "Test", UUID: "00000000-0000-4000-8000-000000000000",
		Tariff: "Test plan", TermDays: 30}, Payment: PaymentInfo{ID: 1, Provider: "test", Amount: 100, Currency: "XTR", Kind: "new", Created: true}})
	if err != nil {
		return TestResult{}, err
	}
	start := s.Now()
	status, err := s.deliver(ctx, url, 0, EventTest, body)
	res := TestResult{Status: status, MS: s.Now().Sub(start).Milliseconds(), Payload: string(body)}
	if err != nil {
		res.Error = err.Error()
	}
	return res, nil
}

// Run delivers what is due until ctx is done.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	pruned := time.Time{}
	for {
		if err := s.sweep(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("webhooks", "err", err)
		}
		if now := s.Now(); now.Sub(pruned) > time.Hour {
			if err := s.Store.Q.PruneWebhookDeliveries(ctx, now.Add(-Retention).Unix()); err != nil && ctx.Err() == nil {
				s.Log.Error("webhooks: prune", "err", err)
			}
			pruned = now
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) sweep(ctx context.Context) error {
	url, on, err := s.Enabled(ctx)
	if err != nil || !on || !ValidURL(url) {
		return err
	}
	rows, err := s.Store.Q.DueWebhookDeliveries(ctx, db.DueWebhookDeliveriesParams{NextAt: s.Now().Unix(), Limit: dueBatch})
	if err != nil {
		return err
	}
	for _, row := range rows {
		status, err := s.deliver(ctx, url, row.ID, row.Event, []byte(row.Payload))
		if err != nil && status >= 200 && status < 300 {
			err = nil
		}
		if err == nil {
			if e := s.Store.Q.WebhookDelivered(ctx, db.WebhookDeliveredParams{Now: sql.NullInt64{Int64: s.Now().Unix(), Valid: true}, ID: row.ID}); e != nil {
				return e
			}
			continue
		}
		next := s.Now().Add(backoff(row.Attempts))
		if e := s.Store.Q.WebhookRetry(ctx, db.WebhookRetryParams{NextAt: next.Unix(), Err: clip(err.Error(), 500), ID: row.ID}); e != nil {
			return e
		}
	}
	return nil
}

// backoff: a minute, then wider; a site down for long is still tried, not dropped.
func backoff(attempts int32) time.Duration {
	steps := []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour}
	if int(attempts) >= len(steps) {
		if attempts >= maxAttempts {
			return 12 * time.Hour
		}
		return steps[len(steps)-1]
	}
	return steps[attempts]
}

// deliver posts the body; the site's own error text is part of the error, its status stays.
func (s *Service) deliver(ctx context.Context, url string, id int64, event string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	ts := s.Now().Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Arikan-Webhooks/1")
	req.Header.Set("X-Arikan-Event", event)
	req.Header.Set("X-Arikan-Timestamp", strconv.FormatInt(ts, 10))
	if id > 0 {
		req.Header.Set("X-Arikan-Delivery", strconv.FormatInt(id, 10))
	}
	if secret, err := s.Settings.String(ctx, settings.KeyWebhookSecret); err == nil && secret != "" {
		req.Header.Set("X-Arikan-Signature", Signature(secret, ts, body))
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// Signature is what the site checks: HMAC-SHA256 of "timestamp.body" with the secret, as
// "sha256=<hex>"; the timestamp is the header of the same name.
func Signature(secret string, ts int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(strconv.FormatInt(ts, 10)))
	m.Write([]byte("."))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
