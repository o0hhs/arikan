// Package webshop is the browser checkout: a buyer pays on the operator's site, with an
// email or a subscription token, and never needs Telegram.
package webshop

import (
	"context"
	"net/url"
	"strings"

	"mikan/internal/panel/settings"
)

// Key is the settings row of the browser shop.
const Key = "web_shop"

// Config is what the admin sets. Off until they turn it on. The SMTP password is a
// secret and is never sent back to the browser.
type ShopConfig struct {
	Enabled   bool     `json:"enabled"`
	ReturnURL string   `json:"return_url"`
	Origins   []string `json:"origins"`
	SMTPHost  string   `json:"smtp_host"`
	SMTPPort  int      `json:"smtp_port"`
	SMTPUser  string   `json:"smtp_user"`
	SMTPPass  string   `json:"smtp_password,omitempty"`
	SMTPFrom  string   `json:"smtp_from"`
	// SMTPTLS is starttls (port 587), tls (port 465) or none.
	SMTPTLS string `json:"smtp_tls"`
}

// SMTPOn: a code can be mailed.
func (c ShopConfig) SMTPOn() bool {
	return strings.TrimSpace(c.SMTPHost) != "" && c.SMTPPort > 0 && strings.TrimSpace(c.SMTPFrom) != ""
}

// Load reads the shop settings. Missing is off.
func Load(ctx context.Context, set *settings.Settings) (ShopConfig, error) {
	c, _, err := settings.Get[ShopConfig](ctx, set, Key)
	if err != nil {
		return ShopConfig{}, err
	}
	if c.Origins == nil {
		c.Origins = []string{}
	}
	if c.SMTPTLS == "" {
		c.SMTPTLS = "starttls"
	}
	return c, nil
}

// Save writes c. An empty password keeps the one already stored.
func Save(ctx context.Context, set *settings.Settings, c ShopConfig) error {
	if strings.TrimSpace(c.SMTPPass) == "" {
		prev, _, err := settings.Get[ShopConfig](ctx, set, Key)
		if err != nil {
			return err
		}
		c.SMTPPass = prev.SMTPPass
	}
	c = Normalize(c)
	return settings.Set(ctx, set, Key, c)
}

// Normalize trims what the admin typed.
func Normalize(c ShopConfig) ShopConfig {
	c.ReturnURL = strings.TrimSpace(c.ReturnURL)
	c.SMTPHost = strings.TrimSpace(c.SMTPHost)
	c.SMTPUser = strings.TrimSpace(c.SMTPUser)
	c.SMTPFrom = strings.TrimSpace(c.SMTPFrom)
	switch c.SMTPTLS {
	case "tls", "none":
	default:
		c.SMTPTLS = "starttls"
	}
	var origins []string
	seen := map[string]bool{}
	for _, o := range c.Origins {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o == "" || seen[o] {
			continue
		}
		seen[o] = true
		origins = append(origins, o)
	}
	if origins == nil {
		origins = []string{}
	}
	c.Origins = origins
	return c
}

// Problems are the fields that cannot be saved, as the settings API names them.
func Problems(c ShopConfig) (field, code string) {
	c = Normalize(c)
	if c.ReturnURL != "" && !httpsURL(c.ReturnURL) {
		return "return_url", "web_shop_return_url"
	}
	if len(c.Origins) > 20 {
		return "origins", "web_shop_origins"
	}
	for _, o := range c.Origins {
		if !originOK(o) {
			return "origins", "web_shop_origins"
		}
	}
	if c.SMTPHost != "" || c.SMTPFrom != "" || c.SMTPPort != 0 || c.SMTPUser != "" {
		if c.SMTPHost == "" || c.SMTPFrom == "" || c.SMTPPort < 1 || c.SMTPPort > 65535 {
			return "smtp_host", "web_shop_smtp"
		}
		if !strings.Contains(c.SMTPFrom, "@") {
			return "smtp_from", "web_shop_smtp"
		}
	}
	return "", ""
}

func httpsURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

func originOK(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Path != "" && u.Path != "/" || u.User != nil || u.RawQuery != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	host := u.Hostname()
	return u.Scheme == "http" && (host == "localhost" || host == "127.0.0.1")
}

// ShopView is ShopConfig without the password.
type ShopView struct {
	Enabled         bool     `json:"enabled"`
	ReturnURL       string   `json:"return_url"`
	Origins         []string `json:"origins"`
	SMTPHost        string   `json:"smtp_host"`
	SMTPPort        int      `json:"smtp_port"`
	SMTPUser        string   `json:"smtp_user"`
	SMTPFrom        string   `json:"smtp_from"`
	SMTPTLS         string   `json:"smtp_tls"`
	SMTPPasswordSet bool     `json:"smtp_password_set"`
}

// Public is c as the admin panel shows it.
func Public(c ShopConfig) ShopView {
	c = Normalize(c)
	return ShopView{Enabled: c.Enabled, ReturnURL: c.ReturnURL, Origins: c.Origins, SMTPHost: c.SMTPHost, SMTPPort: c.SMTPPort,
		SMTPUser: c.SMTPUser, SMTPFrom: c.SMTPFrom, SMTPTLS: c.SMTPTLS, SMTPPasswordSet: c.SMTPPass != ""}
}
