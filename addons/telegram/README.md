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

Since 0.5.1.4 a panel carries this repository as an **addon source of its own**
(`addons.BuiltinSourceURL` in the panel's code): it is already in **Addons → Sources**,
marked built-in and signed with the release key, and the panel puts the bot down by itself.

- **Nothing to add by hand.** A panel whose bot is on installs `arikan-telegram` and hands
  the bot to it on its own — including on a server that was answering with the marketplace's
  `telegram` addon, where the panel put that one itself. A holder the admin chose by hand
  stays, and turning the bot off and on again hands the choice back to the panel.
- **By hand**, if one wants it another way: **Addons → Catalog → Telegram-бот Arikan →
  Install**, then **Addons → Installed → Hand the bot to it** (or
  `PUT /api/v1/telegram/bot-addon` with `{"addon":"arikan-telegram"}`). The marketplace's
  `telegram` addon, if it is installed, keeps running but answers nothing.
- The menu the bot sends is the one set in **Telegram → Menu** (colours and premium emoji
  included), and the bot picks new code up on the next update.

The source's file (`mikan-addon.json`) is read from this repository's latest release and must
be signed with the release key (`mikan-addon.json.sig`, written by `mikan-release addon
-sign` in the release workflow): a file signed with another key — or not signed — is refused,
and the panel says so. If the key is ever rotated, the panel shows the new fingerprint to
accept. The entry's image is pinned by the digest it names.

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
