# The Telegram bot of this fork

`cmd/mikan-telegram` — the addon that answers in the subscribers' bot: the menu, the shop,
the devices, the notices. This directory holds what turns it into an image a panel installs
as an addon of its own.

## Why it is not in the marketplace catalog

The catalog the panel reads by default (`getmikan/marketplace`) builds its bot from
`Miroshka000/mikan` at the version its entry names (`tools/telegram/Dockerfile` of that
repository clones it and builds `cmd/mikan-telegram`). A release of this fork therefore
cannot put its bot there: the image would carry upstream's code, whatever this repository
holds. So each release of this fork publishes its own bot:

- the workflow job `addon` (`.github/workflows/release.yml`) builds
  `addons/telegram/Dockerfile` for amd64 and arm64 and pushes it as a second tag of the
  panel's own package — `ghcr.io/<owner>/arikan:addon-<version>` (one package to keep
  public, no second one to publish);
- the same job writes `mikan-addon.json` and uploads it beside the installers, naming that
  image by its digest (`arikan-release addon`).

Nothing of this is automatic in the panel: the addon's id is `arikan-telegram`, not
`telegram`, because that one belongs to the official catalog's bot, and a source's entry
claiming it is skipped (`id_taken`).

## Installing it on a panel

1. **Addons → Sources → Add** `github.com/o0hhs/arikan` (the repository, not a file: the
   latest release's `mikan-addon.json` is read). The source shows up as *Arikan*.
2. **Addons → Catalog → Telegram-бот Arikan → Install.** The image is pulled by the root
   host on the source's word, pinned by the digest the file names.
3. **Addons → Installed → Telegram-бот Arikan → Hand the bot to it** (or
   `PUT /api/v1/telegram/bot-addon` with `{"addon":"arikan-telegram"}`). The official
   `telegram` addon, if it is installed, keeps running but answers nothing: the bot's
   updates go to the holder alone.
4. The bot picks the new code up by itself on the next update; its menu is the one the
   admin set in **Telegram → Menu** (colours and premium emoji included).

The bot addon of a panel older than the release it was built from is skipped as
`min_panel`; a bot addon refuses to be installed by a panel that runs no bot addons at all
(anything before 0.5.1.0).

## Building and trying it by hand

```sh
docker buildx build --platform linux/amd64,linux/arm64 \
  --build-arg VERSION=0.5.1.3 --build-arg TAG=addon-0.5.1.3 \
  -f addons/telegram/Dockerfile .
```

A single-architecture build is enough to try it: the image only needs the environment the
host hands an addon (`MIKAN_PANEL_URL`, `MIKAN_PANEL_TOKEN`, `MIKAN_BOT_API_URL`,
`MIKAN_BOT_TOKEN`) and reaches both the panel and Telegram over HTTPS. It listens on
nothing and writes no files.
