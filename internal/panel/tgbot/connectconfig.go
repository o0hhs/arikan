package tgbot

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"mikan/internal/tgapi"
)

// ConnectConfig is the screen that hands the subscription to an app («Подключить»): which
// clients are offered on which platforms, where they are downloaded, and what the screen
// says over the buttons. A config saved before this option gets the built-in set on its
// next save: Happ and Incy on the phones, Kabosu on the desk.
type ConnectConfig struct {
	// OneScreen shows every app at once instead of asking the platform first.
	OneScreen bool `json:"one_screen" doc:"Показывать все приложения сразу, не спрашивая платформу"`
	// Intro is the text over the buttons; empty is the built-in one.
	Intro string `json:"intro,omitempty" doc:"Свой текст над кнопками подключения; пусто — встроенный"`
	// Apps are the offered clients in order; empty is the built-in set until the next save.
	Apps []ConnectApp `json:"apps,omitempty"`
}

// ConnectApp is one client on the connect screen: its name in the buttons, the scheme the
// panel hands the subscription over with (the /import redirect), where it is downloaded and
// on which platforms it is offered.
type ConnectApp struct {
	ID        string   `json:"id" doc:"Постоянный id: латиница, цифры, дефис и подчёркивание"`
	Name      string   `json:"name" doc:"Название в кнопках, например Happ"`
	Client    string   `json:"client" enum:"happ,incy,kabosu,clash,mihomo" doc:"Схема приложения: кнопка в один клик отдаёт подписку через неё"`
	Platforms []string `json:"platforms" doc:"Где предлагать: ios, android, desktop (Windows, macOS, Linux)"`
	Download  string   `json:"download,omitempty" doc:"Ссылка на установку; пусто — только подключение, без установки"`
	Primary   bool     `json:"primary,omitempty" doc:"Цветная кнопка подключения; у остальных обычная"`
	On        bool     `json:"on"`
}

// The platforms the connect screen knows.
const (
	PlatformIOS     = "ios"
	PlatformAndroid = "android"
	PlatformDesktop = "desktop" // Windows, macOS and Linux together
)

// connectClients are the schemes the panel's /import redirect hands a subscription to.
var connectClients = []string{"happ", "incy", "kabosu", "clash", "mihomo"}

var connectAppID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,15}$`)

const (
	maxConnectApps  = 8
	maxConnectName  = 24
	maxConnectIntro = 400
)

// Validation codes (the UI translates errors.api.<code>).
var (
	ErrConnectApps      = errors.New("tg_connect_apps")
	ErrConnectApp       = errors.New("tg_connect_app")
	ErrConnectClient    = errors.New("tg_connect_client")
	ErrConnectPlatforms = errors.New("tg_connect_platforms")
	ErrConnectURL       = errors.New("tg_connect_url")
	ErrConnectIntro     = errors.New("tg_connect_intro")
)

// DefaultConnect is the built-in screen: Happ on the phones (the App Store on iOS, Google
// Play on Android), Incy as the second phone client, Kabosu on the desk.
func DefaultConnect() ConnectConfig {
	return ConnectConfig{Apps: []ConnectApp{
		{ID: "happ", Name: "Happ", Client: "happ", Platforms: []string{PlatformIOS}, Download: happAppStore, Primary: true, On: true},
		{ID: "happ-android", Name: "Happ", Client: "happ", Platforms: []string{PlatformAndroid}, Download: happGooglePlay, Primary: true, On: true},
		{ID: "incy", Name: "Incy", Client: "incy", Platforms: []string{PlatformIOS, PlatformAndroid}, Download: incySite, On: true},
		{ID: "kabosu", Name: "Kabosu", Client: "kabosu", Platforms: []string{PlatformDesktop}, Download: kabosuSite, Primary: true, On: true},
	}}
}

// validate checks the screen as the admin saved it and fills in the built-in set for a
// config from before this option.
func (c *ConnectConfig) validate() error {
	if utf8.RuneCountInString(c.Intro) > maxConnectIntro {
		return ErrConnectIntro
	}
	if len(c.Apps) == 0 {
		c.Apps = DefaultConnect().Apps
	}
	if len(c.Apps) > maxConnectApps {
		return ErrConnectApps
	}
	ids := map[string]bool{}
	on := 0
	for i := range c.Apps {
		a := &c.Apps[i]
		a.ID = strings.ToLower(strings.TrimSpace(a.ID))
		a.Name = strings.TrimSpace(a.Name)
		a.Client = strings.ToLower(strings.TrimSpace(a.Client))
		a.Download = strings.TrimSpace(a.Download)
		if !connectAppID.MatchString(a.ID) || ids[a.ID] {
			return ErrConnectApp
		}
		ids[a.ID] = true
		if a.Name == "" || utf8.RuneCountInString(a.Name) > maxConnectName {
			return ErrConnectApp
		}
		if !slices.Contains(connectClients, a.Client) {
			return ErrConnectClient
		}
		plats := make([]string, 0, len(a.Platforms))
		for _, p := range a.Platforms {
			p = strings.ToLower(strings.TrimSpace(p))
			if p != PlatformIOS && p != PlatformAndroid && p != PlatformDesktop {
				return ErrConnectPlatforms
			}
			if !slices.Contains(plats, p) {
				plats = append(plats, p)
			}
		}
		if len(plats) == 0 {
			return ErrConnectPlatforms
		}
		a.Platforms = plats
		if a.Download != "" && !tgapi.SafeURL(a.Download) {
			return ErrConnectURL
		}
		if a.On {
			on++
		}
	}
	if on == 0 {
		return ErrConnectApps // the screen would have nothing to offer
	}
	return nil
}
