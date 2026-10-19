import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Pencil } from "lucide-react";
import { useState, type FormEvent } from "react";
import { errorText } from "../../api/client";
import { qk, rawJson } from "../../api/hooks";
import { Confirm } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { Switch } from "../../components/switch";
import { FormActions } from "../../components/layout";
import { useToast } from "../../components/toast";
import { Button, EmptyState, ErrorState, Field, Pill, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { useCopy } from "../../lib/copy";
import { useDraft } from "../../lib/draft";
import { dateShort, money, num } from "../../lib/format";

type Agent = {
  id: number;
  login: string;
  note?: string;
  disabled: boolean;
  created_at: string;
  links: number;
  visits: number;
  bought: number;
  renewed: number;
  revenue: number;
  earned: number;
  paid: number;
  owed: number;
  currency: string;
};

type Stats = { agents: number; visits: number; bought: number; renewed: number; revenue: number; earned: number; paid: number; owed: number };

type List = { items: Agent[]; total: number; stats: Stats };

type Payout = { id: number; amount: number; note?: string; created_at: string };

export function Agents() {
  const list = useQuery({
    queryKey: qk.agents,
    queryFn: ({ signal }) => rawJson<List>("/api/v1/agents?limit=100", { signal }),
  });
  return (
    <QueryBoundary query={list} pending={<Skeleton style={{ height: 360, borderRadius: 20 }} />}>
      {(v) => <AgentCards v={v} />}
    </QueryBoundary>
  );
}

function AgentCards({ v }: { v: List }) {
  return (
    <div className="flex flex-col gap-4">
      <PanelPath />
      <Header stats={v.stats} />
      <AddCard />
      {v.items.length === 0 ? (
        <section className="card glass reveal">
          <EmptyState title={t("agents.empty")} text={t("agents.emptyText")} />
        </section>
      ) : (
        <ul className="flex flex-col gap-3">
          {v.items.map((a) => (
            <AgentRow key={a.id} a={a} />
          ))}
        </ul>
      )}
    </div>
  );
}

function Header({ stats }: { stats: Stats }) {
  const items: [string, string][] = [
    [t("agents.statAgents"), num(stats.agents)],
    [t("agents.visits"), num(stats.visits)],
    [t("agents.bought"), num(stats.bought)],
    [t("agents.revenueStat"), money(stats.revenue, "RUB")],
    [t("agents.earned"), money(stats.earned, "RUB")],
    [t("agents.owed"), money(stats.owed, "RUB")],
  ];
  return (
    <section className="card glass reveal">
      <div className="card-head">
        <div className="min-w-0">
          <h2 className="card-title">{t("agents.title")}</h2>
          <div className="card-sub">{t("agents.subtitle")}</div>
        </div>
        <dl className="flex flex-wrap gap-x-5 gap-y-1" aria-label={t("agents.stats")}>
          {items.map(([label, value]) => (
            <div key={label}>
              <dt className="text-[11px] text-[var(--md-on-surface-variant)]">{label}</dt>
              <dd className="num text-[15px] font-semibold">{value}</dd>
            </div>
          ))}
        </dl>
      </div>
    </section>
  );
}

/** The agents' panel: where it lives now, where to move it, and the link to hand over. */
function PanelPath() {
  const qc = useQueryClient();
  const toast = useToast();
  const copyText = useCopy();
  const panel = useQuery({
    queryKey: [...qk.agents, "panel"],
    queryFn: ({ signal }) => rawJson<{ path: string; url?: string }>("/api/v1/agents/panel", { signal }),
  });
  const [confirmOff, setConfirmOff] = useState(false);
  const save = useMutation({
    mutationFn: (path: string) => rawJson<{ path: string; url?: string }>("/api/v1/agents/panel", { method: "PUT", body: JSON.stringify({ path }), headers: { "Content-Type": "application/json" } }),
    onSuccess: (v) => {
      void qc.invalidateQueries({ queryKey: [...qk.agents, "panel"] });
      toast.ok(t("agents.panel.saved"));
      if (v.url) void copyText(v.url, t("common.linkCopied"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const [path, setPath] = useState("");
  if (panel.isPending) return <Skeleton style={{ height: 120, borderRadius: 20 }} />;
  if (panel.isError) return <ErrorState text={errorText(panel.error)} onRetry={() => void panel.refetch()} />;
  const p = panel.data;
  return (
    <section className="card glass reveal">
      <div className="card-head">
        <div className="min-w-0">
          <h2 className="card-title">{t("agents.panel.title")}</h2>
          <div className="card-sub">{t("agents.panel.text")}</div>
        </div>
        {p.url ? (
          <Pill tone="ok">{t("agents.panel.on")}</Pill>
        ) : (
          <Pill tone="off">{t("agents.panel.off")}</Pill>
        )}
      </div>
      {p.url ? (
        <div className="mb-3 flex flex-wrap items-center gap-2">
          <span className="text-xs text-[var(--md-on-surface-variant)]">{t("agents.panel.link")}</span>
          <div className="link-field min-w-[240px] flex-1">
            <span className="mono" title={p.url}>
              {p.url}
            </span>
            <button type="button" className="icon-btn" onClick={() => void copyText(p.url!, t("common.linkCopied"))} aria-label={t("common.copyLink")}>
              <Copy size={16} />
            </button>
          </div>
        </div>
      ) : null}
      <div className="flex flex-wrap items-end gap-2">
        <div className="min-w-[240px] flex-1">
          <Field label={t("agents.panel.path")} hint={t("agents.panel.pathHint")}>
            <input className="input mono" maxLength={64} value={path} onChange={(e) => setPath(e.target.value)} placeholder="partners-panel" />
          </Field>
        </div>
        <Button size="sm" variant="glass" disabled={save.isPending || path.trim() === p.path} onClick={() => save.mutate(path.trim())}>
          {t("agents.panel.save")}
        </Button>
        {p.path ? (
          <Button size="sm" variant="ghost" disabled={save.isPending} onClick={() => setConfirmOff(true)}>
            {t("agents.panel.turnOff")}
          </Button>
        ) : null}
      </div>
      <Confirm
        open={confirmOff}
        onOpenChange={(v) => !v && setConfirmOff(false)}
        title={t("agents.panel.offTitle")}
        text={t("agents.panel.offText")}
        confirm={t("common.disable")}
        danger
        loading={save.isPending}
        onConfirm={() => {
          setPath("");
          save.mutate("");
        }}
      />
    </section>
  );
}

/** An agent the admin makes by hand: a login, a password to hand over, a note. */
function AddCard() {
  const qc = useQueryClient();
  const toast = useToast();
  const create = useMutation({
    mutationFn: (body: { login: string; password: string; note: string }) =>
      rawJson<Agent>("/api/v1/agents", { method: "POST", body: JSON.stringify(body), headers: { "Content-Type": "application/json" } }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.agents });
      toast.ok(t("agents.created"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const { draft: f, setDraft: setF, dirty, reset } = useDraft({ login: "", password: "", note: "" });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!f.login.trim() || !f.password) return;
    create.mutate({ login: f.login.trim(), password: f.password, note: f.note }, { onSuccess: () => reset() });
  };
  return (
    <section className="card glass reveal">
      <form onSubmit={submit} noValidate>
        <div className="card-head">
          <div className="min-w-0">
            <h2 className="card-title">{t("agents.newTitle")}</h2>
          </div>
        </div>
        {create.error ? <div className="banner err mb-4">{errorText(create.error)}</div> : null}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <Field label={t("agents.login")} hint={t("agents.loginHint")}>
            <input className="input mono" maxLength={64} value={f.login} onChange={(e) => setF((c) => ({ ...c, login: e.target.value }))} placeholder="vasya-ads" />
          </Field>
          <Field label={t("agents.password")} hint={t("agents.passwordHint")}>
            <input className="input mono" type="password" maxLength={256} value={f.password} onChange={(e) => setF((c) => ({ ...c, password: e.target.value }))} placeholder={t("agents.passwordPlaceholder")} />
          </Field>
          <Field label={t("agents.note")} hint={t("agents.noteHint")}>
            <input className="input" maxLength={500} value={f.note} onChange={(e) => setF((c) => ({ ...c, note: e.target.value }))} />
          </Field>
        </div>
        <FormActions dirty={dirty || !!f.login} saving={create.isPending} onReset={reset} label={t("agents.add")} />
      </form>
    </section>
  );
}

function AgentRow({ a }: { a: Agent }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [openEdit, setOpenEdit] = useState(false);
  const [openPayout, setOpenPayout] = useState(false);
  const [confirmPayout, setConfirmPayout] = useState(false);
  const refresh = () => void qc.invalidateQueries({ queryKey: qk.agents });
  const save = useMutation({
    mutationFn: (body: { login: string; note: string; disabled: boolean; password?: string }) =>
      rawJson<Agent>(`/api/v1/agents/${a.id}`, { method: "PUT", body: JSON.stringify(body), headers: { "Content-Type": "application/json" } }),
    onSuccess: () => {
      refresh();
      toast.ok(t("agents.saved"));
      setOpenEdit(false);
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const pay = useMutation({
    mutationFn: (body: { amount: number; note: string }) =>
      rawJson<Payout>(`/api/v1/agents/${a.id}/payouts`, { method: "POST", body: JSON.stringify(body), headers: { "Content-Type": "application/json" } }),
    onSuccess: () => {
      refresh();
      setOpenPayout(false);
      setConfirmPayout(false);
      toast.ok(t("agents.payoutDone"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const { draft: f, setDraft: setF, dirty, reset } = useDraft({ login: a.login, note: a.note ?? "", password: "" });
  const [payoutAmount, setPayoutAmount] = useState(a.owed > 0 ? String(a.owed) : "");
  const [payoutNote, setPayoutNote] = useState("");
  const payDirty = payoutAmount !== (a.owed > 0 ? String(a.owed) : "") || payoutNote !== "";
  const conversion = a.visits > 0 ? Math.round((a.bought / a.visits) * 100) : 0;
  return (
    <li className="card glass reveal">
      <div className="card-head">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="card-title">{a.note || a.login}</h3>
            <code className="pill mono">{a.login}</code>
            {a.disabled ? <Pill tone="off">{t("agents.off")}</Pill> : null}
          </div>
          <div className="mt-1 text-xs text-[var(--md-on-surface-variant)]">{dateShort(a.created_at)}</div>
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="glass" onClick={() => setOpenPayout((v) => !v)} aria-label={t("agents.payout")}>
            {t("agents.payout")}
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setOpenEdit((v) => !v)} aria-label={t("agents.edit")}>
            <Pencil size={16} aria-hidden />
          </Button>
        </div>
      </div>

      <dl className="mb-3 flex flex-wrap gap-x-6 gap-y-2" aria-label={t("agents.stats")}>
        <Stat label={t("agents.links")} value={num(a.links)} />
        <Stat label={t("agents.visits")} value={num(a.visits)} />
        <Stat label={t("agents.bought")} value={num(a.bought)} />
        <Stat label={t("agents.renewed")} value={num(a.renewed)} />
        <Stat label={t("agents.conversion")} value={`${conversion}%`} />
        <Stat label={t("agents.revenue")} value={money(a.revenue, a.currency)} />
        <Stat label={t("agents.earned")} value={money(a.earned, a.currency)} />
        <Stat label={t("agents.paid")} value={money(a.paid, a.currency)} />
        <Stat label={t("agents.owed")} value={money(a.owed, a.currency)} />
      </dl>

      {openEdit ? (
        <form
          className="mb-3 border-t border-[var(--hairline)] pt-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!f.login.trim()) return;
            save.mutate({ login: f.login.trim(), note: f.note, disabled: a.disabled, password: f.password || undefined });
          }}
          noValidate
        >
          {save.error ? <div className="banner err mb-4">{errorText(save.error)}</div> : null}
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <Field label={t("agents.login")}>
              <input className="input mono" maxLength={64} value={f.login} onChange={(e) => setF((c) => ({ ...c, login: e.target.value }))} />
            </Field>
            <Field label={t("agents.newPassword")} hint={t("agents.newPasswordHint")}>
              <input className="input mono" type="password" maxLength={256} value={f.password} onChange={(e) => setF((c) => ({ ...c, password: e.target.value }))} />
            </Field>
            <Field label={t("agents.note")}>
              <input className="input" maxLength={500} value={f.note} onChange={(e) => setF((c) => ({ ...c, note: e.target.value }))} />
            </Field>
          </div>
          <div className="mt-2 flex items-center justify-between gap-3">
            <Switch checked={!a.disabled} label={t("agents.on")} onChange={(on) => save.mutate({ login: f.login.trim(), note: f.note, disabled: !on })} />
            <FormActions dirty={dirty} saving={save.isPending} onReset={reset} />
          </div>
        </form>
      ) : null}

      {openPayout ? (
        <form
          className="mb-3 border-t border-[var(--hairline)] pt-3"
          onSubmit={(e) => {
            e.preventDefault();
            const amount = Number(p.amount);
            if (!Number.isFinite(amount) || amount <= 0) return;
            setConfirmPayout(true);
          }}
          noValidate
        >
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label={t("agents.payoutAmount")} hint={t("agents.payoutAmountHint")}>
              <input className="input mono" inputMode="numeric" value={payoutAmount} onChange={(e) => setPayoutAmount(e.target.value)} />
            </Field>
            <Field label={t("agents.payoutNote")}>
              <input className="input" maxLength={200} value={payoutNote} onChange={(e) => setPayoutNote(e.target.value)} />
            </Field>
          </div>
          <FormActions dirty={payDirty} saving={pay.isPending} onReset={() => setOpenPayout(false)} />
          <Confirm
            open={confirmPayout}
            onOpenChange={(v) => !v && setConfirmPayout(false)}
            title={t("agents.payoutTitle", { login: a.login })}
            text={t("agents.payoutText")}
            confirm={t("agents.payout")}
            danger
            loading={pay.isPending}
            onConfirm={() => pay.mutate({ amount: Number(payoutAmount), note: payoutNote })}
          />
        </form>
      ) : null}
    </li>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-[11px] text-[var(--md-on-surface-variant)]">{label}</dt>
      <dd className="num text-[15px] font-semibold">{value}</dd>
    </div>
  );
}
