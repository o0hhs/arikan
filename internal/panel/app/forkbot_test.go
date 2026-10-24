package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"mikan/internal/release"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/addons/panelapi"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
)

// The panel's own bot: a source every panel ships with (addons.BuiltinSourceURL) offers this
// fork's bot, and the panel installs it and takes the bot over from the marketplace's bot
// where the panel itself put that one — the fork's bot is the one that carries what this
// panel does. A holder the admin picked by hand is left alone, and without the fork's entry
// the marketplace's bot is still what installs.
func TestForkBotIsPreferred(t *testing.T) {
	srcPub, srcPriv, _ := ed25519.GenerateKey(rand.Reader)
	forkEntry := map[string]any{"id": addons.ForkBotID, "name": map[string]string{"en": "Arikan bot"}, "version": "0.5.1.4",
		"protocol": 2, "kind": addons.KindBot, "mode": addons.ModeClient,
		"permissions": []string{"bot", "users:read", "users:write", "billing:invoice", "events", "kv"},
		"image":       "ghcr.io/o0hhs/arikan", "digest": "sha256:" + strings.Repeat("a", 64)}
	var forkFile, forkSig []byte
	writeFork := func(e map[string]any) {
		data, _ := json.Marshal(e)
		forkFile, forkSig = data, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(srcPriv, data)))
	}
	writeFork(forkEntry)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mikan-addon.json":
			_, _ = w.Write(forkFile)
		case "/mikan-addon.json.sig":
			_, _ = w.Write(forkSig)
		}
	}))
	t.Cleanup(source.Close)

	// The marketplace's catalog, with the official bot in it: the fallback.
	catPub, catPriv, _ := ed25519.GenerateKey(rand.Reader)
	entry := addons.Entry{ID: OfficialBot, Version: "0.5.1.0-rc.11", Protocol: 2, Kind: addons.KindBot, Mode: addons.ModeClient,
		Permissions: []string{"bot", "users:read", "users:write", "billing:invoice", "events", "kv"},
		Image:       "ghcr.io/getmikan/tool-telegram", Digest: "sha256:" + strings.Repeat("b", 64)}
	index, _ := json.Marshal(addons.Catalog{Version: 1, Adapters: []addons.Entry{entry}})
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			_, _ = w.Write(index)
		case "/index.json" + release.SigSuffix:
			_, _ = io.WriteString(w, release.SignIn(release.CatalogContext, index, catPriv))
		}
	}))
	t.Cleanup(catalog.Close)

	// The source the panel ships with, pointed at this server for the test.
	builtinURL, builtinKey := addons.BuiltinSourceURL, addons.BuiltinSourceKey
	addons.BuiltinSourceURL, addons.BuiltinSourceKey = source.URL+"/mikan-addon.json", base64.StdEncoding.EncodeToString(srcPub)
	t.Cleanup(func() { addons.BuiltinSourceURL, addons.BuiltinSourceKey = builtinURL, builtinKey })

	dir := t.TempDir()
	h := newHarness(t, func(o *Options) { o.DataDir, o.AddonsCatalog, o.Dev = dir, catalog.URL+"/index.json", true })
	h.p.Addons.SetKeys(catPub, catPub)
	ctx := context.Background()
	host := func(st addons.State) {
		t.Helper()
		data, _ := json.Marshal(st)
		_ = os.RemoveAll(filepath.Join(dir, "addons", "requests"))
		_ = os.MkdirAll(filepath.Join(dir, "addons"), 0o755)
		if err := os.WriteFile(filepath.Join(dir, "addons", "state.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	asked := func(id string) bool {
		return slices.ContainsFunc(h.p.Addons.Pending(), func(r addons.Request) bool { return r.ID == id })
	}
	slot := func() string {
		id, _, _ := settings.Get[string](ctx, h.p.Settings, panelapi.SlotKey)
		return id
	}
	// What the panel does at its start (botAddon.Run): the source is put in, then it looks.
	if err := h.p.Addons.EnsureBuiltinSource(ctx, h.st.Q, h.p.now()); err != nil {
		t.Fatal(err)
	}
	rows, err := h.st.Q.ListAddonSources(ctx)
	if err != nil || len(rows) != 1 || !addons.IsBuiltinSource(rows[0].Url) || rows[0].Key != addons.BuiltinSourceKey {
		t.Fatalf("the panel's own source: %+v %v", rows, err)
	}
	b := &botAddon{p: h.p}

	// The bot is on, nothing holds it: this fork's bot is asked for, not the marketplace's.
	if err := settings.Set(ctx, h.p.Settings, tgate.KeyEnabled, true); err != nil {
		t.Fatal(err)
	}
	host(addons.State{Adapters: map[string]addons.Installed{}, HostVersion: "0.5.1.0"})
	b.check(ctx)
	if !asked(addons.ForkBotID) || asked(OfficialBot) {
		t.Fatalf("not this fork's bot: %+v", h.p.Addons.Pending())
	}
	// The host is told what the source says (its image and digest), not just an id: this
	// fork's bot is not in the marketplace catalog, and an install by id there fails.
	var askedReq addons.Request
	for _, r := range h.p.Addons.Pending() {
		if r.ID == addons.ForkBotID {
			askedReq = r
		}
	}
	if askedReq.Source != addons.SourceCustom || askedReq.Image != "ghcr.io/o0hhs/arikan" || !strings.HasPrefix(askedReq.Digest, "sha256:a") ||
		askedReq.Kind != addons.KindBot || askedReq.Mode != addons.ModeClient {
		t.Fatalf("the host is asked by id from the catalog: %+v", askedReq)
	}
	want := []string{"bot", "users:read", "users:write", "billing:invoice", "events", "kv"}
	if perms, _, err := addons.LoadGrant(ctx, h.st.Q, addons.ForkBotID); err != nil || !addons.SamePermissions(perms, want) {
		t.Fatalf("grant: %v %v", perms, err)
	}
	// It came from the panel's own source, and that is recorded: the source stays while it is
	// installed, and the admin's page says where it came from.
	if origins, err := h.st.Q.ListAddonOrigins(ctx); err != nil || len(origins) != 1 ||
		origins[0].AddonID != addons.ForkBotID || origins[0].SourceID != rows[0].ID {
		t.Fatalf("origin: %+v %v", origins, err)
	}

	// The host ran it: the bot is the fork's addon's now.
	host(addons.State{HostVersion: "0.5.1.0", Adapters: map[string]addons.Installed{
		addons.ForkBotID: {Protocol: 2, Kind: addons.KindBot, Status: "running", PanelToken: strings.Repeat("p", 48)}}})
	b.check(ctx)
	if slot() != addons.ForkBotID {
		t.Fatalf("the bot was not handed over: %q", slot())
	}

	// The server of this user: the marketplace's bot holds it, put there by the panel (no
	// admin ever chose a holder). This fork's bot takes it over.
	if err := h.st.Q.UpsertAddonGrant(ctx, db.UpsertAddonGrantParams{AddonID: OfficialBot, Version: "0.5.1.0-rc.11",
		Permissions: `["bot","users:read","users:write","billing:invoice","events","kv"]`, ApprovedAt: h.p.now().Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, h.p.Settings, panelapi.SlotKey, OfficialBot); err != nil {
		t.Fatal(err)
	}
	host(addons.State{HostVersion: "0.5.1.0", Adapters: map[string]addons.Installed{
		OfficialBot:      {Protocol: 2, Kind: addons.KindBot, Status: "running", PanelToken: strings.Repeat("p", 48)},
		addons.ForkBotID: {Protocol: 2, Kind: addons.KindBot, Status: "running", PanelToken: strings.Repeat("p", 48)}}})
	b.check(ctx)
	if slot() != addons.ForkBotID {
		t.Fatalf("the marketplace's bot was not taken over: %q", slot())
	}

	// The admin hands it back by hand: left alone for good.
	if err := settings.Set(ctx, h.p.Settings, panelapi.SlotKey, OfficialBot); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, h.p.Settings, panelapi.BotChosenKey, true); err != nil {
		t.Fatal(err)
	}
	if held := h.p.AddonsAPI.Slot(ctx); held != OfficialBot {
		t.Fatalf("the marketplace's bot holds the bot here: %q", held)
	}
	b.check(ctx)
	if slot() != OfficialBot {
		t.Fatalf("the admin's choice was overwritten: %q", slot())
	}

	// The fork's file stops offering a bot (an old release, a source that cannot be read):
	// the marketplace's bot is what installs, as before.
	if err := settings.Set(ctx, h.p.Settings, panelapi.SlotKey, ""); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, h.p.Settings, panelapi.BotChosenKey, false); err != nil {
		t.Fatal(err)
	}
	writeFork(map[string]any{"id": "something-else", "name": map[string]string{"en": "x"}, "version": "1.0.0",
		"protocol": 2, "kind": addons.KindGeneric, "mode": addons.ModeClient, "permissions": []string{"kv"},
		"image": "ghcr.io/example/other", "digest": "sha256:" + strings.Repeat("c", 64)})
	h.p.Addons.ForgetSource(rows[0].ID)
	host(addons.State{Adapters: map[string]addons.Installed{}, HostVersion: "0.5.1.0"})
	b.check(ctx)
	if !asked(OfficialBot) {
		t.Fatalf("the official bot is the fallback: %+v", h.p.Addons.Pending())
	}
}
