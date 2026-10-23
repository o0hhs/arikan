package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikan/internal/panel/addons"
	"mikan/internal/release"
)

// The file the release publishes is one the panel takes as an addon source: the entry
// passes the same checks a source's entries go through (checkSourceEntry), the id is not the
// official bot's, and the id is the one the fork's image is installed as.
func TestAddonFile(t *testing.T) {
	dir := t.TempDir()
	const digest = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := makeAddon([]string{"-version", "0.5.1.3", "-image", "ghcr.io/o0hhs/arikan", "-digest", digest, "-out", dir}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, addonFile))
	if err != nil {
		t.Fatal(err)
	}
	var e struct {
		ID          string            `json:"id"`
		Category    string            `json:"category"`
		Name        map[string]string `json:"name"`
		Description map[string]string `json:"description"`
		Version     string            `json:"version"`
		Protocol    int               `json:"protocol"`
		Kind        string            `json:"kind"`
		Mode        string            `json:"mode"`
		Permissions []string          `json:"permissions"`
		Resources   struct {
			MemMB int `json:"mem_mb"`
		} `json:"resources"`
		Image    string `json:"image"`
		Digest   string `json:"digest"`
		MinPanel string `json:"min_panel"`
		Homepage string `json:"homepage"`
		Key      string `json:"key"`
	}
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatal(err)
	}
	switch {
	case e.ID != addonID:
		t.Errorf("id: %q", e.ID)
	case e.ID == addons.OfficialBotID:
		t.Error("the official bot's id is not a source's to take")
	case !addons.ValidID(e.ID):
		t.Errorf("the panel would refuse the id: %q", e.ID)
	case e.Protocol != addons.Protocol2 || e.Kind != addons.KindBot || e.Mode != addons.ModeClient:
		t.Errorf("protocol/kind/mode: %d %q %q", e.Protocol, e.Kind, e.Mode)
	case e.Version != "0.5.1.3" || e.MinPanel == "":
		t.Errorf("version %q, min_panel %q", e.Version, e.MinPanel)
	case !addons.ValidImage(e.Image) || !release.ValidDigest(e.Digest) || e.Digest != digest:
		t.Errorf("image %q, digest %q", e.Image, e.Digest)
	case e.Resources.MemMB != 0 && (e.Resources.MemMB < addons.MinMemMB || e.Resources.MemMB > addons.MaxMemMB):
		t.Errorf("memory: %d", e.Resources.MemMB)
	case e.Homepage != "https://github.com/o0hhs/arikan":
		t.Errorf("homepage: %q", e.Homepage)
	case e.Key != release.PublicKey:
		t.Errorf("key: %q, want the release key %q", e.Key, release.PublicKey)
	}
	// The key the file declares is one the panel reads: a document signed by the release key
	// verifies against the key inside it, with no pin from the admin (a panel whose row for
	// the source holds no key would otherwise answer source_key_needed on a signed file).
	if _, ok := addons.ParseSourceKey(e.Key); !ok {
		t.Errorf("the panel cannot parse the key the file declares: %q", e.Key)
	}
	// The permissions are the bot's, and each is one the panel knows.
	seen := map[string]bool{}
	for _, p := range e.Permissions {
		if seen[p] || !strings.Contains(" "+strings.Join(addons.Permissions, " ")+" ", " "+p+" ") {
			t.Errorf("permission %q", p)
		}
		seen[p] = true
	}
	if !seen["bot"] {
		t.Errorf("without the bot permission the addon cannot hold the bot: %v", e.Permissions)
	}
	// Both languages the panel shows, and a description that says what it is.
	for _, lang := range []string{"ru", "en"} {
		if strings.TrimSpace(e.Name[lang]) == "" || strings.TrimSpace(e.Description[lang]) == "" {
			t.Errorf("%s: name %q, description %q", lang, e.Name[lang], e.Description[lang])
		}
	}

	// The panel's own reading of it: the same entry rules apply to a source's file (the
	// source parser keeps an entry a catalog would keep), so the file is put through
	// ParseCatalog — signed, as a catalog is — and must come out whole.
	catalog := []byte(`{"version":1,"updated":"2026-01-01T00:00:00Z","adapters":[` + strings.TrimSpace(string(data)) + `]}`)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := addons.ParseCatalog(catalog, release.SignIn(release.CatalogContext, catalog, priv), pub, e.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Adapters) != 1 {
		t.Fatalf("the panel would skip the fork's bot: %+v", cat.Adapters)
	}
	kept := cat.Adapters[0]
	if kept.ID != addonID || kept.Version != e.Version || kept.Image != e.Image || kept.Digest != e.Digest ||
		kept.Kind != addons.KindBot || kept.Mode != addons.ModeClient || kept.MinPanel != e.MinPanel {
		t.Fatalf("kept: %+v", kept)
	}
}

// The file the release signs: the panel's own source (addons.BuiltinSourceURL) pins the
// release key, so the .sig must verify with it — and with nothing else.
func TestAddonFileSigned(t *testing.T) {
	dir := t.TempDir()
	const digest = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	key := keypair(t)
	trust(t, key.pub)
	t.Setenv("RELEASE_SIGNING_KEY", key.pem)
	if err := makeAddon([]string{"-version", "0.5.1.4", "-image", "ghcr.io/o0hhs/arikan", "-digest", digest, "-sign", "-out", dir}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, addonFile))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(filepath.Join(dir, addonFile+".sig"))
	if err != nil {
		t.Fatal(err)
	}
	if err := release.Verify(data, strings.TrimSpace(string(sig)), key.pub); err != nil {
		t.Fatalf("the release key must verify it: %v", err)
	}
	if err := release.Verify(data, strings.TrimSpace(string(sig)), keypair(t).pub); err == nil {
		t.Fatal("another key must not")
	}
	// Without the flag nothing is signed; without a key the flag is an error, not a file that
	// the panel would refuse in silence.
	plain := t.TempDir()
	if err := makeAddon([]string{"-version", "0.5.1.4", "-image", "ghcr.io/o0hhs/arikan", "-digest", digest, "-out", plain}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(plain, addonFile+".sig")); !os.IsNotExist(err) {
		t.Fatalf("signed without being asked: %v", err)
	}
	t.Setenv("RELEASE_SIGNING_KEY", "")
	if err := makeAddon([]string{"-version", "0.5.1.4", "-image", "ghcr.io/o0hhs/arikan", "-digest", digest, "-sign", "-out", t.TempDir()}); err == nil {
		t.Fatal("signing without a key is an error")
	}
}

// keypair is an Ed25519 key and its PEM, as RELEASE_SIGNING_KEY carries one.
type testKey struct {
	pub ed25519.PublicKey
	pem string
}

func keypair(t *testing.T) testKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{pub: pub, pem: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))}
}

// What the command refuses: a version, an image or a digest the panel would not take.
func TestAddonFileRefusesBadInput(t *testing.T) {
	ok := []string{"-version", "0.5.1.3", "-image", "ghcr.io/o0hhs/arikan", "-digest", "sha256:" + strings.Repeat("a", 64), "-out", t.TempDir()}
	for name, args := range map[string][]string{
		"no version": drop(ok, "-version"),
		"no image":   drop(ok, "-image"),
		"no digest":  drop(ok, "-digest"),
		"bad digest": replace(ok, "-digest", "sha256:xyz"),
		"bare image": replace(ok, "-image", "arikan"),
	} {
		if err := makeAddon(args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// drop takes a flag and its value out of args.
func drop(args []string, flag string) []string {
	out := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == flag {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// replace gives a flag a new value.
func replace(args []string, flag, value string) []string {
	out := append([]string(nil), args...)
	for i := range out {
		if out[i] == flag && i+1 < len(out) {
			out[i+1] = value
		}
	}
	return out
}
