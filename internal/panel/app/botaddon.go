package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/addons/panelapi"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
)

// The panel has no screens of its own in the subscribers' bot: they are the bot addon's. A
// panel whose bot is on and that no addon answers for installs one by itself, with the
// permissions it asks for, and hands it the bot once it runs:
//
//   - this fork's own bot (addons.ForkBotID), from the source the panel ships with
//     (addons.BuiltinSourceURL). It is preferred, and it takes the bot over from the
//     marketplace's bot where the panel itself put that one (no admin ever chose a holder):
//     the fork's bot is the one that carries what this panel does — the referral screens,
//     the coloured buttons, the premium emoji.
//   - the marketplace's official bot (addons.OfficialBotID), from the signed catalog, when
//     this fork's is not offered (a release before it, a source that cannot be read).
//
// The admin may hand the bot to another addon (PUT /api/v1/telegram/bot-addon); then this
// leaves it alone for good (panelapi.BotChosenKey). Once the admin removes the bot addon it
// stays removed (panelapi.AutoBotKey) until they turn the bot on again or install a bot from
// the catalog.

// OfficialBot is the id of the official bot addon in the catalog.
const OfficialBot = addons.OfficialBotID

// officialBotPermissions bound what a bot addon is granted when it installs itself.
var officialBotPermissions = []string{"bot", "users:read", "users:write", "billing:invoice", "events", "kv"}

// botAddonAgain: how long a failed install waits before it is asked again.
const botAddonAgain = time.Hour

type botAddon struct {
	p     *Panel
	asked time.Time
}

func (b *botAddon) Run(ctx context.Context) {
	// The source the panel ships with — this fork's own releases — is put in before anything
	// looks for the fork's bot in it; the admin neither adds nor removes it.
	if err := b.p.Addons.EnsureBuiltinSource(ctx, b.p.st.Q, b.p.now()); err != nil {
		b.p.log.Warn("bot addon: the fork's own source", "err", err)
	}
	b.check(ctx)
	every(ctx, time.Minute, func() { b.check(ctx) })
}

func (b *botAddon) check(ctx context.Context) {
	p := b.p
	if p.Addons == nil || !p.Addons.Supported() {
		return
	}
	on, _, err := settings.Get[bool](ctx, p.Settings, tgate.KeyEnabled)
	if err != nil || !on {
		return
	}
	if auto, ok, err := settings.Get[bool](ctx, p.Settings, panelapi.AutoBotKey); err != nil || ok && !auto {
		return // the admin removed the bot addon: theirs to bring back
	}
	st, err := p.Addons.State()
	if err != nil || st.HostVersion == "" {
		return // a host before protocol 2 runs no bot addon: `arikan update` first
	}
	e, ok := b.wanted(ctx)
	if !ok {
		return // nothing offers a bot addon now
	}
	if !b.mayTake(ctx, e.ID) {
		return // another addon holds the bot: the admin's own choice
	}
	if a, ok := st.Adapters[e.ID]; ok && a.KindOf() != addons.KindPayment {
		// The copy that is installed runs the bot it was granted: a digest that differs from
		// the entry offered now is an older release, and the host installs the new one over
		// it (asked for below). Either way a running copy takes the bot over, so the panel
		// does not answer while a newer release is on its way in.
		if a.Status == "running" && b.granted(ctx, e.ID) {
			if err := settings.Set(ctx, p.Settings, panelapi.SlotKey, e.ID); err != nil {
				p.log.Error("bot addon: hand over the bot", "err", err)
				return
			}
			p.log.Info("bot addon: the bot answers in the addon now", "addon", e.ID, "version", a.Version)
			p.Gateway.Reload()
		}
		if a.Digest == e.Digest {
			return // the entry offered now is the copy that is installed
		}
		p.log.Info("bot addon: a newer release of the bot is offered", "addon", e.ID,
			"installed", a.Version, "offered", e.Version)
	}
	if slices.ContainsFunc(p.Addons.Pending(), func(r addons.Request) bool { return r.ID == e.ID }) {
		return
	}
	if l := st.Request; l != nil && l.ID == e.ID && l.State == "failed" && p.now().Sub(b.asked) < botAddonAgain {
		return
	}
	// Installed without the admin's look, so never with more than a bot needs, whatever the
	// catalog or the source says.
	if !addons.Covers(officialBotPermissions, e.Permissions) {
		p.log.Warn("bot addon: the entry asks for more than a bot may have", "addon", e.ID, "permissions", e.Permissions)
		return
	}
	if err := p.st.Q.UpsertAddonGrant(ctx, db.UpsertAddonGrantParams{AddonID: e.ID, Version: e.Version,
		Permissions: addons.EncodePermissions(e.Permissions), ApprovedAt: p.now().Unix()}); err != nil {
		p.log.Error("bot addon: grant", "err", err)
		return
	}
	// An entry of a source is recorded as coming from it: the addon keeps working only while
	// the source stays, and the admin's page says where it came from.
	if src, ok := b.sourceOf(ctx, e.ID); ok {
		if err := p.st.Q.UpsertAddonOrigin(ctx, db.UpsertAddonOriginParams{AddonID: e.ID, SourceID: src}); err != nil {
			p.log.Error("bot addon: origin", "err", err)
			return
		}
	}
	if err := p.Addons.Ask(addons.Request{Action: "install", ID: e.ID}); err != nil {
		p.log.Error("bot addon: ask the host", "err", err)
		return
	}
	b.asked = p.now()
	p.log.Info("bot addon: installing the bot", "addon", e.ID, "version", e.Version)
}

// wanted is the bot addon to run: this fork's own from the source the panel ships with,
// preferred; the marketplace's official one otherwise.
func (b *botAddon) wanted(ctx context.Context) (addons.Entry, bool) {
	if e, ok := b.forkBot(ctx); ok {
		return e, true
	}
	c, err := b.p.Addons.Catalog(ctx)
	if err != nil {
		return addons.Entry{}, false
	}
	i := slices.IndexFunc(c.Adapters, func(e addons.Entry) bool { return e.ID == OfficialBot && e.Kind != addons.KindPayment })
	if i < 0 {
		return addons.Entry{}, false // not in the catalog yet
	}
	return c.Adapters[i], true
}

// forkBot is the bot this fork publishes, when the source the panel ships with offers one to
// run: the keys of that source are pinned to the fork's release key, so what it offers is what
// the fork released.
func (b *botAddon) forkBot(ctx context.Context) (addons.Entry, bool) {
	for _, s := range b.builtinSources(ctx) {
		st := b.p.Addons.RefreshSource(ctx, b.p.st.Q, s, false, "")
		if !st.Offered() {
			continue // no entries to install: a fetch that failed, or a file offering none
		}
		for _, e := range st.Doc.Entries {
			if e.ID == addons.ForkBotID && e.Kind == addons.KindBot {
				return e, true
			}
		}
	}
	return addons.Entry{}, false
}

// sourceOf is the source offering id, for the origin of an addon installed by itself.
func (b *botAddon) sourceOf(ctx context.Context, id string) (int64, bool) {
	for _, s := range b.builtinSources(ctx) {
		st := b.p.Addons.RefreshSource(ctx, b.p.st.Q, s, false, "")
		if !st.Offered() {
			continue
		}
		if slices.ContainsFunc(st.Doc.Entries, func(e addons.Entry) bool { return e.ID == id }) {
			return s.ID, true
		}
	}
	return 0, false
}

// builtinSources are the rows of the source the panel ships with.
func (b *botAddon) builtinSources(ctx context.Context) []db.AddonSource {
	rows, err := b.p.st.Q.ListAddonSources(ctx)
	if err != nil {
		return nil
	}
	return slices.DeleteFunc(rows, func(s db.AddonSource) bool { return !addons.IsBuiltinSource(s.Url) })
}

// mayTake: the bot is nobody's, is already this addon's, or is the marketplace's official bot
// where the panel itself put it — no admin ever chose a holder, so this fork's bot takes the
// bot over as the panel's own. Anything else is the admin's choice and left alone.
func (b *botAddon) mayTake(ctx context.Context, id string) bool {
	slot := b.p.AddonsAPI.Slot(ctx)
	switch {
	case slot == "" || slot == id:
		return true
	case slot != OfficialBot || id != addons.ForkBotID:
		return false
	}
	_, chosen, err := settings.Get[bool](ctx, b.p.Settings, panelapi.BotChosenKey)
	return err == nil && !chosen
}

// granted: the addon may hold the bot.
func (b *botAddon) granted(ctx context.Context, id string) bool {
	perms, _, err := addons.LoadGrant(ctx, b.p.st.Q, id)
	if err != nil && !errors.Is(err, addons.ErrNoGrant) {
		b.p.log.Warn("bot addon: grant", "err", err)
	}
	return err == nil && addons.HasPerm(perms, "bot")
}
