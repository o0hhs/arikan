package tgbot

import (
	"errors"
	"strings"
	"testing"
)

// A config from before the screen existed gets the built-in set: Happ and Incy on the
// phones, Kabosu on the desk, the recommended one primary.
func TestConnectConfigDefaults(t *testing.T) {
	var c ConnectConfig
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if len(c.Apps) != 4 || c.Apps[0].ID != "happ" || !c.Apps[0].Primary || c.Apps[3].Client != "kabosu" {
		t.Fatalf("the built-in set: %+v", c.Apps)
	}
	if !c.Apps[0].On || !strings.HasPrefix(c.Apps[0].Download, "https://") {
		t.Fatalf("a default app is off or has no download: %+v", c.Apps[0])
	}
}

// What the admin saves is checked: the codes cross to the UI and say which field is wrong.
func TestConnectConfigValidate(t *testing.T) {
	app := func(patch func(*ConnectApp)) ConnectConfig {
		a := ConnectApp{ID: "happ", Name: "Happ", Client: "happ", Platforms: []string{PlatformIOS}, Download: "https://example.com/app", On: true}
		patch(&a)
		return ConnectConfig{Apps: []ConnectApp{a}}
	}
	for name, tc := range map[string]struct {
		cfg  ConnectConfig
		want error
	}{
		"an unknown scheme": {app(func(a *ConnectApp) { a.Client = "nope" }), ErrConnectClient},
		"no platforms":      {app(func(a *ConnectApp) { a.Platforms = nil }), ErrConnectPlatforms},
		"a bad platform":    {app(func(a *ConnectApp) { a.Platforms = []string{"phone"} }), ErrConnectPlatforms},
		"a bad download":    {app(func(a *ConnectApp) { a.Download = "http://example.com/app" }), ErrConnectURL},
		"an empty name":     {app(func(a *ConnectApp) { a.Name = "  " }), ErrConnectApp},
		"a bad id":          {app(func(a *ConnectApp) { a.ID = "Happ!" }), ErrConnectApp},
		"nothing on":        {app(func(a *ConnectApp) { a.On = false }), ErrConnectApps},
		"too many":          {ConnectConfig{Apps: manyApps(9)}, ErrConnectApps},
		"a long intro":      {ConnectConfig{Intro: strings.Repeat("я", 401)}, ErrConnectIntro},
	} {
		t.Run(name, func(t *testing.T) {
			if err := tc.cfg.validate(); !errors.Is(err, tc.want) {
				t.Fatalf("%v, want %v", err, tc.want)
			}
		})
	}
	// Two apps with one id are refused: the id names the app's buttons.
	two := app(func(a *ConnectApp) {})
	two.Apps = append(two.Apps, two.Apps[0])
	if err := two.validate(); !errors.Is(err, ErrConnectApp) {
		t.Fatalf("a duplicate id: %v", err)
	}
	// The screen may show every app at once; that choice is kept as saved.
	one := app(func(a *ConnectApp) {})
	one.OneScreen = true
	if err := one.validate(); err != nil || !one.OneScreen {
		t.Fatalf("one screen: %v %+v", err, one)
	}
}

func manyApps(n int) []ConnectApp {
	apps := make([]ConnectApp, 0, n)
	for i := 0; i < n; i++ {
		apps = append(apps, ConnectApp{ID: "a" + string(rune('0'+i)), Name: "A", Client: "happ", Platforms: []string{PlatformIOS}, On: true})
	}
	return apps
}
