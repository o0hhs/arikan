package billing

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// Platega and RollyPay are built into the panel. CryptoBot stays the marketplace
// adapter "cryptobot": its container already speaks the adapter protocol. These two
// follow the same rule as an adapter: a callback is only a nudge, and the payment is
// applied after the provider's own status call confirms the amount and the currency.

const (
	keyPlatega = "pay_platega"
	keyRolly   = "pay_rollypay"
)

// Platega methods from the provider's create-payment reference.
const (
	PlategaSBP      = 2
	PlategaERIP     = 3
	PlategaCard     = 11
	PlategaIntlCard = 12
	PlategaCrypto   = 13
	PlategaSberPay  = 14
)

// PlategaSetup is what the admin saves for Platega. Secret is write-only.
type PlategaSetup struct {
	Enabled    bool   `json:"enabled"`
	MerchantID string `json:"merchant_id"`
	Secret     string `json:"secret,omitempty"`
	Method     int    `json:"method"`
}

// RollySetup is what the admin saves for RollyPay. The API key and the signing
// secret are write-only. TerminalID is optional: with an API key the provider
// does not require it.
type RollySetup struct {
	Enabled       bool   `json:"enabled"`
	APIKey        string `json:"api_key,omitempty"`
	SigningSecret string `json:"signing_secret,omitempty"`
	TerminalID    string `json:"terminal_id"`
}

// PlategaPublic is PlategaSetup without the secret.
type PlategaPublic struct {
	Enabled    bool   `json:"enabled"`
	MerchantID string `json:"merchant_id"`
	SecretSet  bool   `json:"secret_set"`
	Method     int    `json:"method"`
	WebhookURL string `json:"webhook_url"`
}

// RollyPublic is RollySetup without the secrets.
type RollyPublic struct {
	Enabled    bool   `json:"enabled"`
	APIKeySet  bool   `json:"api_key_set"`
	SecretSet  bool   `json:"signing_secret_set"`
	TerminalID string `json:"terminal_id"`
	WebhookURL string `json:"webhook_url"`
}

// useDirect is true when the panel's own client should take this provider.
// A marketplace add-on of the same id (the Platega add-on already published)
// keeps the invoice, the webhook and the status call while this card is off.
func (s *Service) useDirect(ctx context.Context, id string) bool {
	switch id {
	case "platega":
		return s.plategaOn(ctx)
	case "rollypay":
		return s.rollyOn(ctx)
	default:
		return false
	}
}

func (s *Service) loadPlatega(ctx context.Context) (PlategaSetup, error) {
	c, _, err := settings.Get[PlategaSetup](ctx, s.d.Settings, keyPlatega)
	if c.Method == 0 {
		c.Method = PlategaSBP
	}
	return c, err
}

func (s *Service) loadRolly(ctx context.Context) (RollySetup, error) {
	c, _, err := settings.Get[RollySetup](ctx, s.d.Settings, keyRolly)
	return c, err
}

func (s *Service) plategaOn(ctx context.Context) bool {
	c, err := s.loadPlatega(ctx)
	return err == nil && c.Enabled && c.MerchantID != "" && c.Secret != "" && plategaMethod(c.Method)
}

func (s *Service) rollyOn(ctx context.Context) bool {
	c, err := s.loadRolly(ctx)
	return err == nil && c.Enabled && c.APIKey != "" && c.SigningSecret != ""
}

// directAddons are the built-in ruble providers that can take a payment now.
func (s *Service) directAddons(ctx context.Context) []string {
	var out []string
	if s.plategaOn(ctx) {
		out = append(out, "platega")
	}
	if s.rollyOn(ctx) {
		out = append(out, "rollypay")
	}
	return out
}

// PlategaView is the admin's Platega card. The webhook address is where Platega
// posts callbacks; the secret in that path is the panel's, and the provider's
// own secret is checked again on the request.
func (s *Service) PlategaView(ctx context.Context, subBase string) (PlategaPublic, error) {
	c, err := s.loadPlatega(ctx)
	if err != nil {
		return PlategaPublic{}, err
	}
	return PlategaPublic{Enabled: c.Enabled, MerchantID: c.MerchantID, SecretSet: c.Secret != "", Method: c.Method,
		WebhookURL: s.AddonWebhookURL(ctx, "platega", subBase)}, nil
}

// RollyView is the admin's RollyPay card. The callback address is set on the
// cash register; the panel shows the address to paste there.
func (s *Service) RollyView(ctx context.Context, subBase string) (RollyPublic, error) {
	c, err := s.loadRolly(ctx)
	if err != nil {
		return RollyPublic{}, err
	}
	return RollyPublic{Enabled: c.Enabled, APIKeySet: c.APIKey != "", SecretSet: c.SigningSecret != "", TerminalID: c.TerminalID,
		WebhookURL: s.AddonWebhookURL(ctx, "rollypay", subBase)}, nil
}

// SavePlatega keeps the setup. An empty secret leaves the saved one. Enabling
// it needs a merchant id, a secret and a method the provider documents.
func SaveProvider(ctx context.Context, set *settings.Settings, in PlategaSetup) error {
	old, _, err := settings.Get[PlategaSetup](ctx, set, keyPlatega)
	if err != nil {
		return err
	}
	in.MerchantID = strings.TrimSpace(in.MerchantID)
	in.Secret = strings.TrimSpace(in.Secret)
	if in.Secret == "" {
		in.Secret = old.Secret
	}
	if in.Method == 0 {
		in.Method = PlategaSBP
	}
	if in.Enabled && (in.MerchantID == "" || in.Secret == "" || !plategaMethod(in.Method)) {
		return ErrPlategaConfig
	}
	if !plategaMethod(in.Method) && in.Method != 0 {
		return ErrPlategaConfig
	}
	return settings.Set(ctx, set, keyPlatega, in)
}

// SaveRolly keeps the setup. Empty secrets leave the saved ones.
func SaveRollyProvider(ctx context.Context, set *settings.Settings, in RollySetup) error {
	old, _, err := settings.Get[RollySetup](ctx, set, keyRolly)
	if err != nil {
		return err
	}
	in.APIKey = strings.TrimSpace(in.APIKey)
	in.SigningSecret = strings.TrimSpace(in.SigningSecret)
	in.TerminalID = strings.TrimSpace(in.TerminalID)
	if in.APIKey == "" {
		in.APIKey = old.APIKey
	}
	if in.SigningSecret == "" {
		in.SigningSecret = old.SigningSecret
	}
	if in.Enabled && (in.APIKey == "" || in.SigningSecret == "") {
		return ErrRollyConfig
	}
	return settings.Set(ctx, set, keyRolly, in)
}

func plategaMethod(m int) bool {
	switch m {
	case PlategaSBP, PlategaERIP, PlategaCard, PlategaIntlCard, PlategaCrypto, PlategaSberPay:
		return true
	}
	return false
}

// ErrPlategaConfig and ErrRollyConfig: the setup cannot be switched on as it is.
var (
	ErrPlategaConfig = errors.New("platega_config")
	ErrRollyConfig   = errors.New("rollypay_config")
)

type gateHTTP struct {
	client      *http.Client
	plategaBase string
	rollyBase   string
}

func (s *Service) gate() gateHTTP {
	g := s.httpGate
	if g.client == nil {
		g.client = &http.Client{Timeout: 20 * time.Second}
	}
	if g.plategaBase == "" {
		g.plategaBase = "https://app.platega.io"
	}
	if g.rollyBase == "" {
		g.rollyBase = "https://rollypay.io"
	}
	return g
}

func (s *Service) directInvoice(ctx context.Context, id string, p db.Payment, description, ret string) (sql.NullString, string, error) {
	switch id {
	case "platega":
		return s.plategaInvoice(ctx, p, description, ret)
	case "rollypay":
		return s.rollyInvoice(ctx, p, description, ret)
	}
	return sql.NullString{}, "", errAddonOff
}

func (s *Service) plategaInvoice(ctx context.Context, p db.Payment, description, ret string) (sql.NullString, string, error) {
	cfg, err := s.loadPlatega(ctx)
	if err != nil || !s.plategaOn(ctx) {
		return sql.NullString{}, "", errAddonOff
	}
	rub, ok := majorRUB(p.Amount)
	if !ok || p.Currency != "RUB" {
		return sql.NullString{}, "", ErrProviderOff
	}
	mail := s.payerMail(ctx, p)
	body := map[string]any{
		"paymentMethod":  cfg.Method,
		"paymentDetails": map[string]any{"amount": rub, "currency": "RUB"},
		"description":    clip(description, 200),
		"return":         ret,
		"failedUrl":      ret,
		"orderId":        strconv.FormatInt(p.ID, 10),
		"payload":        p.Payload,
		"metadata": map[string]string{
			"user_email": mail,
			"userId":     payerID(p),
		},
	}
	var out struct {
		TransactionID string `json:"transactionId"`
		Redirect      string `json:"redirect"`
	}
	if err := s.gateJSON(ctx, http.MethodPost, s.gate().plategaBase+"/transaction/process", plategaHeaders(cfg), body, &out); err != nil {
		return sql.NullString{}, "", err
	}
	if out.TransactionID == "" || !strings.HasPrefix(out.Redirect, "https://") {
		return sql.NullString{}, "", errors.New("platega_bad_invoice")
	}
	return sql.NullString{String: out.TransactionID, Valid: true}, out.Redirect, nil
}

func (s *Service) rollyInvoice(ctx context.Context, p db.Payment, description, ret string) (sql.NullString, string, error) {
	cfg, err := s.loadRolly(ctx)
	if err != nil || !s.rollyOn(ctx) {
		return sql.NullString{}, "", errAddonOff
	}
	text, ok := rubText(p.Amount)
	if !ok || p.Currency != "RUB" {
		return sql.NullString{}, "", ErrProviderOff
	}
	body := map[string]any{
		"amount": text, "payment_currency": "RUB", "order_id": p.Payload,
		"description": clip(description, 200), "customer_id": payerID(p),
		"success_redirect_url": ret, "fail_redirect_url": ret,
	}
	if cfg.TerminalID != "" {
		body["terminal_id"] = cfg.TerminalID
	}
	var out struct {
		PaymentID string `json:"payment_id"`
		PayURL    string `json:"pay_url"`
	}
	if err := s.gateJSON(ctx, http.MethodPost, s.gate().rollyBase+"/api/v1/payments", rollyHeaders(cfg), body, &out); err != nil {
		return sql.NullString{}, "", err
	}
	if out.PaymentID == "" || !strings.HasPrefix(out.PayURL, "https://") {
		return sql.NullString{}, "", errors.New("rollypay_bad_invoice")
	}
	return sql.NullString{String: out.PaymentID, Valid: true}, out.PayURL, nil
}

func (s *Service) directStatus(ctx context.Context, id, ext string) (addons.Status, error) {
	switch id {
	case "platega":
		return s.plategaStatus(ctx, ext)
	case "rollypay":
		return s.rollyStatus(ctx, ext)
	}
	return addons.Status{}, errAddonOff
}

func (s *Service) plategaStatus(ctx context.Context, id string) (addons.Status, error) {
	cfg, err := s.loadPlatega(ctx)
	if err != nil {
		return addons.Status{}, err
	}
	var out struct {
		Status  string `json:"status"`
		Details struct {
			Amount   float64 `json:"amount"`
			Currency string  `json:"currency"`
		} `json:"paymentDetails"`
	}
	if err := s.gateJSON(ctx, http.MethodGet, s.gate().plategaBase+"/transaction/"+id, plategaHeaders(cfg), nil, &out); err != nil {
		return addons.Status{}, err
	}
	amount, ok := kopecks(out.Details.Amount)
	if !ok {
		return addons.Status{}, errors.New("platega_bad_amount")
	}
	return addons.Status{Status: plategaState(out.Status), Amount: amount, Currency: strings.ToUpper(out.Details.Currency)}, nil
}

func (s *Service) rollyStatus(ctx context.Context, id string) (addons.Status, error) {
	cfg, err := s.loadRolly(ctx)
	if err != nil {
		return addons.Status{}, err
	}
	var out struct {
		Status   string `json:"status"`
		Amount   string `json:"amount"`
		Currency string `json:"payment_currency"`
	}
	if err := s.gateJSON(ctx, http.MethodGet, s.gate().rollyBase+"/api/v1/payments/"+id, rollyHeaders(cfg), nil, &out); err != nil {
		return addons.Status{}, err
	}
	amount, ok := kopecksText(out.Amount)
	if !ok {
		return addons.Status{}, errors.New("rollypay_bad_amount")
	}
	return addons.Status{Status: rollyState(out.Status), Amount: amount, Currency: strings.ToUpper(out.Currency)}, nil
}

// directWebhook checks that the callback is the provider's and returns the
// invoice id to re-query. The body is not what marks the payment paid.
func (s *Service) directWebhook(ctx context.Context, id string, header http.Header, body []byte) (string, error) {
	switch id {
	case "platega":
		return s.plategaHook(ctx, header, body)
	case "rollypay":
		return s.rollyHook(ctx, header, body)
	}
	return "", errAddonOff
}

func (s *Service) plategaHook(ctx context.Context, header http.Header, body []byte) (string, error) {
	cfg, err := s.loadPlatega(ctx)
	if err != nil || cfg.Secret == "" {
		return "", errAddonOff
	}
	if !sameSecret(header.Get("X-MerchantId"), cfg.MerchantID) || !sameSecret(header.Get("X-Secret"), cfg.Secret) {
		return "", errHookAuth
	}
	var in struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(body, &in) != nil || in.ID == "" {
		return "", errHookAuth
	}
	return in.ID, nil
}

func (s *Service) rollyHook(ctx context.Context, header http.Header, body []byte) (string, error) {
	cfg, err := s.loadRolly(ctx)
	if err != nil || cfg.SigningSecret == "" {
		return "", errAddonOff
	}
	ts, sig := header.Get("X-Timestamp"), header.Get("X-Signature")
	mac := hmac.New(sha256.New, []byte(cfg.SigningSecret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(strings.ToLower(sig))) {
		return "", errHookAuth
	}
	var in struct {
		PaymentID string `json:"payment_id"`
	}
	if json.Unmarshal(body, &in) != nil || in.PaymentID == "" {
		return "", errHookAuth
	}
	return in.PaymentID, nil
}

var errHookAuth = errors.New("hook_auth")

func plategaState(s string) string {
	switch strings.ToUpper(s) {
	case "CONFIRMED":
		return "paid"
	case "CANCELED":
		return "canceled"
	case "CHARGEBACKED":
		return "refunded"
	default:
		return "pending"
	}
}

func rollyState(s string) string {
	switch strings.ToLower(s) {
	case "paid":
		return "paid"
	case "canceled", "expired":
		return "canceled"
	case "chargeback", "refunded":
		return "refunded"
	default:
		return "pending"
	}
}

func plategaHeaders(c PlategaSetup) http.Header {
	h := make(http.Header)
	h.Set("X-MerchantId", c.MerchantID)
	h.Set("X-Secret", c.Secret)
	return h
}

func rollyHeaders(c RollySetup) http.Header {
	h := make(http.Header)
	h.Set("X-API-Key", c.APIKey)
	h.Set("X-Nonce", nonce())
	return h
}

func (s *Service) gateJSON(ctx context.Context, method, url string, header http.Header, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.gate().client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", addons.ErrUnreachable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: %v", addons.ErrUnreachable, err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("provider %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (s *Service) payerMail(ctx context.Context, p db.Payment) string {
	if p.WebAccountID.Valid {
		if a, err := s.d.Store.Q.GetWebAccount(ctx, p.WebAccountID.Int64); err == nil && strings.Contains(a.Email, "@") {
			return a.Email
		}
	}
	if p.TgID != 0 {
		return fmt.Sprintf("%d@telegram.org", p.TgID)
	}
	return payerID(p) + "@mail.ru"
}

func payerID(p db.Payment) string {
	switch {
	case p.TgID != 0:
		return strconv.FormatInt(p.TgID, 10)
	case p.WebAccountID.Valid:
		return "web-" + strconv.FormatInt(p.WebAccountID.Int64, 10)
	case p.UserID.Valid:
		return "user-" + strconv.FormatInt(p.UserID.Int64, 10)
	default:
		return "pay-" + strconv.FormatInt(p.ID, 10)
	}
}

func sameSecret(got, want string) bool {
	return hmac.Equal([]byte(got), []byte(want))
}

func majorRUB(kopecks int64) (float64, bool) {
	if kopecks <= 0 {
		return 0, false
	}
	return float64(kopecks) / 100, true
}

func rubText(kopecks int64) (string, bool) {
	if kopecks <= 0 {
		return "", false
	}
	return fmt.Sprintf("%d.%02d", kopecks/100, kopecks%100), true
}

func kopecks(major float64) (int64, bool) {
	if major < 0 || math.IsNaN(major) || math.IsInf(major, 0) {
		return 0, false
	}
	return int64(math.Round(major * 100)), true
}

func kopecksText(s string) (int64, bool) {
	whole, frac, ok := strings.Cut(s, ".")
	if !ok {
		frac = "00"
	}
	if len(frac) == 1 {
		frac += "0"
	}
	if len(frac) < 2 {
		return 0, false
	}
	w, err1 := strconv.ParseInt(whole, 10, 64)
	f, err2 := strconv.ParseInt(frac[:2], 10, 64)
	if err1 != nil || err2 != nil || w < 0 {
		return 0, false
	}
	return w*100 + f, true
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func nonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// directName is the buyer-facing name of a built-in provider, "" for a marketplace adapter.
func directName(id string) string {
	switch id {
	case "platega":
		return "Platega"
	case "rollypay":
		return "RollyPay"
	case "cryptobot":
		return "CryptoBot"
	}
	return ""
}
