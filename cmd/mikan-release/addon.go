package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mikan/internal/release"
)

// The bot addon this fork publishes: the release builds cmd/mikan-telegram into an image of
// its own and uploads the file this command writes beside it. The panel takes it as an
// addon source (github.com/<owner>/<repo> reads mikan-addon.json of the latest release,
// internal/panel/addons/sources.go), and the admin hands the bot to it.
//
// The id is not "telegram": that one belongs to the official catalog's bot, and a source's
// entry claiming it is skipped (id_taken).

// addonID is the id the fork's bot addon installs as.
const addonID = "arikan-telegram"

// addonFile is what the source lookup reads: mikan-addon.json first, index.json after it.
const addonFile = "mikan-addon.json"

// addonMinPanel: the panel that runs a protocol 2 bot addon at all. The two RPC methods
// this fork's bot adds (AdminChat, NoteEmojiRefused) are newer; a panel without them
// answers them with a refusal, and the bot only loses the emoji help and the note.
const addonMinPanel = "0.5.1.0-rc.11"

// addonPermissions are the bot's: the same the official bot holds, and no more. The host
// hands them to the addon when it installs it from a source (the admin approves them).
var addonPermissions = []string{"bot", "users:read", "users:write", "billing:invoice", "events", "kv"}

// addonEntry is the source file: one entry, the shape the catalog uses (addons.Entry). It is
// written by hand here so the file this fork publishes does not depend on the panel's types.
type addonEntry struct {
	ID          string            `json:"id"`
	Category    string            `json:"category"`
	Name        map[string]string `json:"name"`
	Description map[string]string `json:"description"`
	Version     string            `json:"version"`
	Protocol    int               `json:"protocol"`
	Kind        string            `json:"kind"`
	Mode        string            `json:"mode"`
	Permissions []string          `json:"permissions"`
	Resources   map[string]int    `json:"resources"`
	Image       string            `json:"image"`
	Digest      string            `json:"digest"`
	MinPanel    string            `json:"min_panel"`
	Homepage    string            `json:"homepage"`
}

func makeAddon(args []string) error {
	fs := flag.NewFlagSet("addon", flag.ContinueOnError)
	version := fs.String("version", "", "release version, e.g. 0.5.1.3")
	image := fs.String("image", "", "image repository the bot was pushed to, e.g. ghcr.io/o0hhs/arikan")
	digest := fs.String("digest", "", "sha256 digest of the pushed multi-arch bot image")
	homepage := fs.String("homepage", "https://github.com/o0hhs/arikan", "where the addon comes from")
	sign := fs.Bool("sign", false, "sign the file with RELEASE_SIGNING_KEY (file.sig); the panel's own source pins that key")
	out := fs.String("out", "dist", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case *version == "":
		return errors.New("-version is required")
	case *image == "":
		return errors.New("-image is required")
	case !release.ValidDigest(*digest):
		return fmt.Errorf("-digest %q: want sha256:…", *digest)
	case !strings.Contains(*image, "/"):
		return fmt.Errorf("-image %q: want host/path/name", *image)
	}
	e := addonEntry{
		ID:       addonID,
		Category: "tools",
		Name: map[string]string{
			"ru": "Telegram-бот Arikan",
			"en": "Arikan Telegram bot",
		},
		Description: map[string]string{
			"ru": "Бот подписчиков панели Arikan: меню, покупка, устройства, уведомления. Неофициальный форк mikan",
			"en": "The subscriber bot of the Arikan panel: menu, purchases, devices, notices. An unofficial fork of mikan",
		},
		Version:     *version,
		Protocol:    2,
		Kind:        "bot",
		Mode:        "client",
		Permissions: addonPermissions,
		Resources:   map[string]int{"mem_mb": 128},
		Image:       *image,
		Digest:      *digest,
		MinPanel:    addonMinPanel,
		Homepage:    *homepage,
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(*out, addonFile)
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("%s: %s %s %s@%s\n", path, e.ID, e.Version, e.Image, e.Digest)
	if *sign {
		key, err := privateKey(os.Getenv("RELEASE_SIGNING_KEY"))
		if err != nil {
			return err
		}
		// The signature is over the exact bytes: the panel's own source carries the release
		// key's public half, so a file signed with another key (or none) is refused there.
		if err := os.WriteFile(path+".sig", []byte(release.Sign(data, key)+"\n"), 0o644); err != nil {
			return err
		}
		fmt.Printf("%s.sig: signed with the release key\n", path)
	}
	return nil
}
