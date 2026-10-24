// Partners: the franchise's books. A partner's bot has a code its subscribers come to the
// panel's bot with (/start partner_<code>); every paid order of such an account credits the
// partner the percentage kept here. One card per partner, the bots inside it.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Handshake, Pencil, Plus, Trash2 } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { Confirm, Drawer, DrawerFooter } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { Switch } from "../../components/switch";
import { useToast } from "../../components/toast";
import { Button, EmptyState, Field, Pill, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { useCopy } from "../../lib/copy";
import { rubles } from "../../lib/format";

type Partner = Schemas["PartnerView"];
type PartnerBot = Schemas["PartnerBotView"];

const qk = { partners: ["partners"] as const };

function usePartners() {
  return useQuery({ queryKey: qk.partners, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/partners", { signal })) });
}

/** The partners' section of the payments page: the cards, the drawers and the confirmations. */
export function PartnersSection() {
  const partners = usePartners();
  const qc = useQueryClient();
  const [edit, setEdit] = useState<Partner | "new" | null>(null);
  const [remove, setRemove] = useState<Partner | null>(null);
  const refresh = () => void qc.invalidateQueries({ queryKey: qk.partners });
  const toast = useToast();
  const del = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/partners/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      refresh();
      setRemove(null);
      toast.ok(t("partners.removed"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  return (
    <section className="card glass reveal">
      <div className="card-head">
        <div className="min-w-0">
          <h2 className="card-title">{t("partners.title")}</h2>
          <div className="card-sub">{t("partners.sub")}</div>
        </div>
        <Button variant="primary" onClick={() => setEdit("new")}>
          <Plus size={18} aria-hidden /> {t("partners.add")}
        </Button>
      </div>
      <QueryBoundary query={partners} pending={<Skeleton style={{ height: 96, borderRadius: 20 }} />}>
        {(list) =>
          list.length === 0 ? (
            <EmptyState title={t("partners.emptyTitle")} text={t("partners.emptyText")} icon={Handshake}>
              <Button variant="primary" onClick={() => setEdit("new")}>
                <Plus size={18} aria-hidden /> {t("partners.add")}
              </Button>
            </EmptyState>
          ) : (
            <ul className="grid gap-4 xl:grid-cols-2">
              {list.map((p) => (
                <PartnerCard key={p.id} p={p} onEdit={() => setEdit(p)} onRemove={() => setRemove(p)} onChanged={refresh} />
              ))}
            </ul>
          )
        }
      </QueryBoundary>
      <PartnerDrawer partner={edit} onClose={() => setEdit(null)} onSaved={refresh} />
      <Confirm
        open={!!remove}
        onOpenChange={(v) => !v && setRemove(null)}
        title={t("partners.removeTitle", { name: remove?.name ?? "" })}
        text={t("partners.removeText")}
        confirm={t("partners.remove")}
        danger
        loading={del.isPending}
        onConfirm={() => remove && del.mutate(remove.id)}
      />
    </section>
  );
}

function PartnerCard({ p, onEdit, onRemove, onChanged }: { p: Partner; onEdit: () => void; onRemove: () => void; onChanged: () => void }) {
  const [addBot, setAddBot] = useState(false);
  const total = p.bots.reduce((sum, b) => sum + b.turnover_kop, 0);
  return (
    <li className="card glass reveal flex flex-col">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <Handshake size={16} className="shrink-0 text-[var(--md-primary)]" aria-hidden />
            <h3 className="truncate font-display text-lg font-medium tracking-tight">{p.name}</h3>
          </div>
          {p.note ? <p className="mt-1 whitespace-pre-line text-[13px] text-[var(--md-on-surface-variant)]">{p.note}</p> : null}
          {p.owner_tg_id ? <p className="mt-1 text-xs text-[var(--md-on-surface-variant)]">{t("partners.ownerLine", { id: p.owner_tg_id })}</p> : null}
        </div>
        <div className="flex gap-1">
          <button type="button" className="icon-btn" aria-label={t("partners.editLabel", { name: p.name })} onClick={onEdit}>
            <Pencil size={16} />
          </button>
          <button type="button" className="icon-btn" aria-label={t("partners.removeLabel", { name: p.name })} onClick={onRemove}>
            <Trash2 size={16} />
          </button>
        </div>
      </div>
      <dl className="mt-4 grid grid-cols-3 gap-3 text-xs text-[var(--md-on-surface-variant)]">
        <div>
          <dt>{t("partners.balance")}</dt>
          <dd className="num mt-0.5 text-sm font-semibold text-[var(--md-on-surface)]">{rubles(p.balance_kop)}</dd>
        </div>
        <div>
          <dt>{t("partners.turnover")}</dt>
          <dd className="num mt-0.5 text-sm font-medium text-[var(--md-on-surface)]">{rubles(total)}</dd>
        </div>
        <div>
          <dt>{t("partners.bots")}</dt>
          <dd className="num mt-0.5 text-sm font-medium text-[var(--md-on-surface)]">{p.bots.length}</dd>
        </div>
      </dl>
      {p.bots.length ? (
        <ul className="mt-4 flex flex-col gap-2">
          {p.bots.map((b) => (
            <BotRow key={b.id} bot={b} onChanged={onChanged} />
          ))}
        </ul>
      ) : (
        <p className="mt-4 text-xs text-[var(--md-on-surface-variant)]">{t("partners.noBots")}</p>
      )}
      <div className="mt-3 flex">
        <Button size="sm" variant="ghost" onClick={() => setAddBot(true)}>
          <Plus size={16} aria-hidden /> {t("partners.addBot")}
        </Button>
      </div>
      <BotDrawer partner={p} open={addBot} onClose={() => setAddBot(false)} onSaved={onChanged} />
    </li>
  );
}

function BotRow({ bot, onChanged }: { bot: PartnerBot; onChanged: () => void }) {
  const toast = useToast();
  const copyText = useCopy();
  const [edit, setEdit] = useState(false);
  const [confirmOff, setConfirmOff] = useState(false);
  const del = useMutation({
    mutationFn: () => unwrap(api.DELETE("/api/v1/partners/bots/{bot}", { params: { path: { bot: bot.id } } })),
    onSuccess: () => {
      onChanged();
      setConfirmOff(false);
      toast.ok(t("partners.botRemoved"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const name = bot.bot_username ? "@" + bot.bot_username : t("partners.botNoName");
  return (
    <li className="rounded-2xl border border-[var(--hairline)] p-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2 text-[13px] font-medium">
            <span className="truncate">{name}</span>
            {bot.enabled ? <Pill tone="ok">{t("partners.on")}</Pill> : <Pill tone="off">{t("partners.off")}</Pill>}
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-[var(--md-on-surface-variant)]">
            <span>{t("partners.commission", { percent: bot.commission_percent })}</span>
            <span>·</span>
            <span className="num">{t("partners.earned")}: {rubles(bot.turnover_kop)}</span>
          </div>
        </div>
        <div className="flex gap-1">
          <button type="button" className="icon-btn" aria-label={t("partners.editBotLabel", { name })} onClick={() => setEdit(true)}>
            <Pencil size={16} />
          </button>
          <button type="button" className="icon-btn" aria-label={t("partners.removeBotLabel", { name })} onClick={() => setConfirmOff(true)}>
            <Trash2 size={16} />
          </button>
        </div>
      </div>
      <div className="link-field mt-2">
        <span className="mono" title={"/start partner_" + bot.code}>
          /start partner_{bot.code}
        </span>
        <button type="button" className="icon-btn" onClick={() => void copyText("/start partner_" + bot.code, t("common.linkCopied"))} aria-label={t("common.copyLink")}>
          <Copy size={16} />
        </button>
      </div>
      <BotEditDrawer bot={bot} open={edit} onClose={() => setEdit(false)} onSaved={onChanged} name={name} />
      <Confirm
        open={confirmOff}
        onOpenChange={(v) => !v && setConfirmOff(false)}
        title={t("partners.removeBotTitle", { name })}
        text={t("partners.removeBotText")}
        confirm={t("partners.remove")}
        danger
        loading={del.isPending}
        onConfirm={() => del.mutate()}
      />
    </li>
  );
}

/** The partner's own card: the name and the note. */
function PartnerDrawer({ partner, onClose, onSaved }: { partner: Partner | "new" | null; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const [name, setName] = useState("");
  const [note, setNote] = useState("");
  const [owner, setOwner] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  useEffect(() => {
    if (!partner) return;
    setName(partner === "new" ? "" : partner.name);
    setNote(partner === "new" ? "" : partner.note);
    setOwner(partner === "new" || !partner.owner_tg_id ? "" : String(partner.owner_tg_id));
    setErrors({});
  }, [partner]);
  const save = useMutation({
    mutationFn: () => {
      const body = { name: name.trim(), note: note.trim(), owner_tg_id: owner.trim() ? Number(owner.trim()) : 0 };
      return partner === "new" || !partner
        ? unwrap(api.POST("/api/v1/partners", { body }))
        : unwrap(api.PUT("/api/v1/partners/{id}", { params: { path: { id: partner.id } }, body }));
    },
    onSuccess: () => {
      onSaved();
      toast.ok(partner === "new" ? t("partners.created") : t("partners.saved"));
      onClose();
    },
    onError: (e) => setErrors({ name: errorText(e) }),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      setErrors({ name: t("partners.errName") });
      return;
    }
    if (owner.trim() && !Number.isInteger(Number(owner.trim()))) {
      setErrors({ owner: t("partners.errOwner") });
      return;
    }
    save.mutate();
  };
  return (
    <Drawer
      open={!!partner}
      onOpenChange={(v) => !v && onClose()}
      title={partner === "new" ? t("partners.new") : t("partners.edit")}
      meta={t("partners.drawerMeta")}
      footer={
        <DrawerFooter form="partner-form" onCancel={onClose} loading={save.isPending}>
          {t("common.save")}
        </DrawerFooter>
      }
    >
      <form id="partner-form" onSubmit={submit} className="pt-5" noValidate>
        <Field label={t("partners.name")} htmlFor="p-name" error={errors.name}>
          <input id="p-name" className="input" value={name} onChange={(e) => setName(e.target.value)} maxLength={120} placeholder={t("partners.namePlaceholder")} autoFocus />
        </Field>
        <Field label={t("partners.note")} htmlFor="p-note" hint={t("partners.noteHint")}>
          <input id="p-note" className="input" value={note} onChange={(e) => setNote(e.target.value)} maxLength={500} />
        </Field>
        <Field label={t("partners.owner")} htmlFor="p-owner" hint={t("partners.ownerHint")} error={errors.owner}>
          <input id="p-owner" className="input mono max-w-[220px]" inputMode="numeric" value={owner} onChange={(e) => setOwner(e.target.value.replace(/[^0-9]/g, ""))} placeholder="123456789" maxLength={20} />
        </Field>
      </form>
    </Drawer>
  );
}

/** A new bot of a partner: who it is, its token, the share. The code is given by the panel. */
function BotDrawer({ partner, open, onClose, onSaved }: { partner: Partner; open: boolean; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const [username, setUsername] = useState("");
  const [token, setToken] = useState("");
  const [percent, setPercent] = useState("10");
  const [errors, setErrors] = useState<Record<string, string>>({});
  useEffect(() => {
    if (open) {
      setUsername("");
      setToken("");
      setPercent("10");
      setErrors({});
    }
  }, [open]);
  const create = useMutation({
    mutationFn: () =>
      unwrap(api.POST("/api/v1/partners/{id}/bots", {
        params: { path: { id: partner.id } },
        body: { bot_username: username.trim(), bot_token: token.trim(), commission_percent: Number(percent), enabled: true },
      })),
    onSuccess: (v) => {
      onSaved();
      const code = v.bots[0]?.code;
      toast.ok(code ? t("partners.botAddedCode", { code }) : t("partners.botAdded"));
      onClose();
    },
    onError: (e) => setErrors({ percent: errorText(e) }),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const p = Number(percent);
    if (!Number.isInteger(p) || p < 0 || p > 100) {
      setErrors({ percent: t("partners.errPercent") });
      return;
    }
    create.mutate();
  };
  return (
    <Drawer
      open={open}
      onOpenChange={(v) => !v && onClose()}
      title={t("partners.newBot")}
      meta={partner.name}
      footer={
        <DrawerFooter form="bot-form" onCancel={onClose} loading={create.isPending}>
          {t("partners.addBot")}
        </DrawerFooter>
      }
    >
      <form id="bot-form" onSubmit={submit} className="pt-5" noValidate>
        <Field label={t("partners.botUsername")} htmlFor="b-name" hint={t("partners.botUsernameHint")}>
          <input id="b-name" className="input" value={username} onChange={(e) => setUsername(e.target.value)} maxLength={64} placeholder="ivan_partner_bot" autoFocus />
        </Field>
        <Field label={t("partners.botToken")} htmlFor="b-token" hint={t("partners.botTokenHint")}>
          <input id="b-token" className="input mono" type="password" value={token} onChange={(e) => setToken(e.target.value)} maxLength={200} autoComplete="new-password" />
        </Field>
        <Field label={t("partners.percent")} htmlFor="b-percent" hint={t("partners.percentHint")} error={errors.percent}>
          <input id="b-percent" className="input max-w-[120px]" inputMode="numeric" value={percent} onChange={(e) => setPercent(e.target.value)} maxLength={3} />
        </Field>
        <p className="text-xs text-[var(--md-on-surface-variant)]">{t("partners.codeHint")}</p>
      </form>
    </Drawer>
  );
}

/** The bot as it was saved: its name, its token (empty keeps the saved one), the share. */
function BotEditDrawer({
  bot,
  open,
  onClose,
  onSaved,
  name,
}: {
  bot: PartnerBot;
  open: boolean;
  onClose: () => void;
  onSaved: () => void;
  name: string;
}) {
  const toast = useToast();
  const [username, setUsername] = useState("");
  const [token, setToken] = useState("");
  const [percent, setPercent] = useState("");
  const [enabled, setEnabled] = useState(false);
  useEffect(() => {
    if (!open) return;
    setUsername(bot.bot_username);
    setToken("");
    setPercent(String(bot.commission_percent));
    setEnabled(bot.enabled);
  }, [open, bot]);
  const save = useMutation({
    mutationFn: (body: { bot_username: string; bot_token: string; commission_percent: number; enabled: boolean }) =>
      unwrap(api.PUT("/api/v1/partners/bots/{bot}", { params: { path: { bot: bot.id } }, body })),
    onSuccess: () => {
      onSaved();
      toast.ok(t("partners.saved"));
      onClose();
    },
    onError: (e) => toast.error(errorText(e)),
  });
  return (
    <Drawer
      open={open}
      onOpenChange={(v) => !v && onClose()}
      title={t("partners.editBot")}
      meta={name}
      footer={
        <DrawerFooter form="bot-edit-form" onCancel={onClose} loading={save.isPending}>
          {t("common.save")}
        </DrawerFooter>
      }
    >
      <form
        id="bot-edit-form"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate({ bot_username: username.trim(), bot_token: token.trim(), commission_percent: Number(percent), enabled });
        }}
        className="pt-5"
        noValidate
      >
        <Field label={t("partners.botUsername")} htmlFor="be-name">
          <input id="be-name" className="input" value={username} onChange={(e) => setUsername(e.target.value)} maxLength={64} autoFocus />
        </Field>
        <Field label={t("partners.botToken")} htmlFor="be-token" hint={t("partners.botTokenKeep")}>
          <input id="be-token" className="input mono" type="password" value={token} onChange={(e) => setToken(e.target.value)} maxLength={200} autoComplete="new-password" />
        </Field>
        <Field label={t("partners.percent")} htmlFor="be-percent" hint={t("partners.percentHint")}>
          <input id="be-percent" className="input max-w-[120px]" inputMode="numeric" value={percent} onChange={(e) => setPercent(e.target.value)} maxLength={3} />
        </Field>
        <div className="flex items-center justify-between gap-3 py-3">
          <div>
            <div className="text-[13px] font-medium">{t("partners.enabled")}</div>
            <div className="text-xs text-[var(--md-on-surface-variant)]">{t("partners.enabledHint")}</div>
          </div>
          <Switch checked={enabled} onChange={setEnabled} label={t("partners.enabled")} />
        </div>
      </form>
    </Drawer>
  );
}
