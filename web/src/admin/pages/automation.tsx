import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus, Trash2, Workflow } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { qk, useAutomationRules, useAutomationRuns } from "../../api/hooks";
import { CardHead } from "../../components/layout";
import { Confirm, Drawer, DrawerFooter } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { Switch, SwitchRow } from "../../components/switch";
import { useToast } from "../../components/toast";
import { Button, EmptyState, Field, PageHeader, Pill, Skeleton } from "../../components/ui";
import { t, type Key } from "../../i18n";
import { fieldErrors } from "../../lib/fields";
import { ago, GiB } from "../../lib/format";

type Rule = Schemas["AutomationRuleView"];
type Run = Schemas["AutomationRunView"];
type Trigger = Rule["trigger"];
type ActionType = "notify_admin" | "notify_user" | "extend" | "grant_traffic" | "promo" | "discount";

type Conditions = {
  tariff_ids?: number[];
  states?: string[];
  sources?: string[];
  days_left_min?: number;
  days_left_max?: number;
  traffic_used_min?: number;
  traffic_used_max?: number;
};

type Action = {
  type: ActionType;
  text?: string;
  days?: number;
  bytes?: number;
  lifetime?: string;
  grant_days?: number;
  code?: string;
  percent?: number;
  discount_days?: number;
};

type Draft = {
  name: string;
  trigger: Trigger;
  enabled: boolean;
  dry_run: boolean;
  cooldown_seconds: number;
  conditions: Conditions;
  actions: Action[];
};

const TRIGGERS: { value: Trigger; label: Key }[] = [
  { value: "trial.started", label: "automation.triggers.trial" },
  { value: "user.created", label: "automation.triggers.created" },
  { value: "user.granted", label: "automation.triggers.granted" },
  { value: "user.expiring", label: "automation.triggers.expiring" },
  { value: "user.expired", label: "automation.triggers.expired" },
  { value: "user.inactive", label: "automation.triggers.inactive" },
  { value: "traffic.90", label: "automation.triggers.traffic90" },
  { value: "traffic.100", label: "automation.triggers.traffic100" },
  { value: "payment.paid", label: "automation.triggers.paid" },
  { value: "payment.failed", label: "automation.triggers.paymentFailed" },
  { value: "payment.refunded", label: "automation.triggers.refunded" },
  { value: "trial.not_given", label: "automation.triggers.trialNotGiven" },
  { value: "device.bound", label: "automation.triggers.deviceBound" },
  { value: "device.unbound", label: "automation.triggers.deviceUnbound" },
  { value: "balance.topup", label: "automation.triggers.balanceTopup" },
  { value: "discount.ending", label: "automation.triggers.discountEnding" },
  { value: "node.down", label: "automation.triggers.nodeDown" },
  { value: "node.up", label: "automation.triggers.nodeUp" },
];

const ACTION_TYPES: { value: ActionType; label: Key }[] = [
  { value: "notify_admin", label: "automation.actions.notifyAdmin" },
  { value: "notify_user", label: "automation.actions.notifyUser" },
  { value: "extend", label: "automation.actions.extend" },
  { value: "grant_traffic", label: "automation.actions.grant" },
  { value: "promo", label: "automation.actions.promo" },
  { value: "discount", label: "automation.actions.discount" },
];

const STATES = ["active", "expiring", "limited", "expired", "disabled"] as const;

const emptyDraft = (): Draft => ({
  name: "",
  trigger: "trial.started",
  enabled: true,
  dry_run: false,
  cooldown_seconds: 3600,
  conditions: {},
  actions: [{ type: "notify_admin", text: "" }],
});

const asConditions = (v: unknown): Conditions => (v && typeof v === "object" ? (v as Conditions) : {});
const asActions = (v: unknown): Action[] => (Array.isArray(v) ? (v as Action[]) : []);

function draftOf(r: Rule): Draft {
  return {
    name: r.name,
    trigger: r.trigger,
    enabled: r.enabled,
    dry_run: r.dry_run,
    cooldown_seconds: r.cooldown_seconds,
    conditions: asConditions(r.conditions),
    actions: asActions(r.actions).length ? asActions(r.actions) : [{ type: "notify_admin", text: "" }],
  };
}

function bodyOf(d: Draft): Schemas["AutomationRuleInputBody"] {
  return {
    name: d.name.trim(),
    trigger: d.trigger,
    enabled: d.enabled,
    dry_run: d.dry_run,
    cooldown_seconds: d.cooldown_seconds,
    conditions: d.conditions,
    actions: d.actions,
  };
}

function triggerLabel(trigger: string): string {
  const item = TRIGGERS.find((t) => t.value === trigger);
  return item ? t(item.label) : trigger;
}

function actionLabel(type: string): string {
  const item = ACTION_TYPES.find((a) => a.value === type);
  return item ? t(item.label) : type;
}

function idsOf(s: string): number[] {
  return s
    .split(/[,\s]+/)
    .map(Number)
    .filter((n) => Number.isSafeInteger(n) && n > 0);
}

export function AutomationPage() {
  const rules = useAutomationRules();
  const runs = useAutomationRuns();
  const qc = useQueryClient();
  const toast = useToast();
  const [editing, setEditing] = useState<Rule | "new" | null>(null);
  const [removing, setRemoving] = useState<Rule | null>(null);
  const refresh = () => Promise.all([qc.invalidateQueries({ queryKey: qk.automationRules }), qc.invalidateQueries({ queryKey: qk.automationRuns })]);
  const save = useMutation({
    mutationFn: ({ id, body }: { id?: number; body: Schemas["AutomationRuleInputBody"] }) =>
      id ? unwrap(api.PUT("/api/v1/automation/rules/{id}", { params: { path: { id } }, body })) : unwrap(api.POST("/api/v1/automation/rules", { body })),
    onSuccess: () => {
      setEditing(null);
      void refresh();
      toast.ok(t("automation.saved"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const toggle = useMutation({
    mutationFn: (r: Rule) => unwrap(api.PUT("/api/v1/automation/rules/{id}", { params: { path: { id: r.id } }, body: bodyOf({ ...draftOf(r), enabled: !r.enabled }) })),
    onSuccess: () => void refresh(),
    onError: (e) => toast.error(errorText(e)),
  });
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/automation/rules/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      setRemoving(null);
      void refresh();
      toast.ok(t("automation.removed"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  return (
    <>
      <PageHeader
        title={t("automation.title")}
        sub={t("automation.subtitle")}
        actions={
          <Button variant="primary" onClick={() => setEditing("new")}>
            <Plus size={18} aria-hidden /> {t("automation.new")}
          </Button>
        }
      />
      <QueryBoundary
        query={rules}
        pending={<Skeleton style={{ height: 260, borderRadius: 20 }} />}
        wrap={(state) => <section className="card glass">{state}</section>}
      >
        {(rows) => (
          <section className="card glass reveal">
            <CardHead title={t("automation.rules")} sub={t("automation.rulesSub")} />
            {rows.length === 0 ? (
              <EmptyState icon={Workflow} title={t("automation.emptyTitle")} text={t("automation.emptyText")}>
                <Button variant="primary" onClick={() => setEditing("new")}>
                  <Plus size={18} aria-hidden /> {t("automation.new")}
                </Button>
              </EmptyState>
            ) : (
              <ul className="row-list">
                {rows.map((r) => (
                  <li key={r.id} className="flex flex-wrap items-start justify-between gap-3 py-3">
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-[13px] font-semibold">{r.name}</span>
                        <Pill tone={r.enabled ? "ok" : "off"}>{r.enabled ? t("common.enable") : t("common.disable")}</Pill>
                        {r.dry_run ? <Pill tone="warn">{t("automation.dryRun")}</Pill> : null}
                      </div>
                      <div className="mt-1 text-xs text-[var(--md-on-surface-variant)]">{triggerLabel(r.trigger)} · {t("automation.cooldownShort", { s: r.cooldown_seconds })}</div>
                      <div className="mt-1 text-xs text-[var(--md-on-surface-variant)]">
                        {asActions(r.actions).map((a) => actionLabel(a.type)).join(" → ")} · {t("automation.runsCount", { n: r.runs_count })}
                        {r.last_run_at ? ` · ${t("automation.lastRun", { ago: ago(r.last_run_at) })}` : ""}
                      </div>
                    </div>
                    <div className="flex items-center gap-1">
                      <Switch checked={r.enabled} label={t("automation.toggle", { name: r.name })} onChange={() => toggle.mutate(r)} disabled={toggle.isPending} />
                      <button type="button" className="icon-btn" aria-label={t("automation.editLabel", { name: r.name })} onClick={() => setEditing(r)}>
                        <Pencil size={16} aria-hidden />
                      </button>
                      <button type="button" className="icon-btn" aria-label={t("automation.removeLabel", { name: r.name })} onClick={() => setRemoving(r)}>
                        <Trash2 size={16} aria-hidden />
                      </button>
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </section>
        )}
      </QueryBoundary>
      <QueryBoundary
        query={runs}
        pending={<Skeleton style={{ height: 220, borderRadius: 20, marginTop: 16 }} />}
        wrap={(state) => <section className="card glass mt-4">{state}</section>}
      >
        {(data) => <RunsCard rows={data.items} rules={rules.data ?? []} />}
      </QueryBoundary>
      <Drawer
        wide
        open={editing !== null}
        onOpenChange={(v) => !v && setEditing(null)}
        title={editing === "new" ? t("automation.new") : t("automation.edit")}
        footer={<DrawerFooter form="automation-form" onCancel={() => setEditing(null)} loading={save.isPending}>{t("common.save")}</DrawerFooter>}
      >
        {editing ? (
          <RuleForm
            key={editing === "new" ? "new" : editing.id}
            initial={editing === "new" ? emptyDraft() : draftOf(editing)}
            errors={fieldErrors(save.error)}
            saving={save.isPending}
            onSubmit={(d) => save.mutate({ id: editing === "new" ? undefined : editing.id, body: bodyOf(d) })}
          />
        ) : null}
      </Drawer>
      <Confirm
        open={!!removing}
        onOpenChange={(v) => !v && setRemoving(null)}
        title={t("automation.removeTitle", { name: removing?.name ?? "" })}
        text={t("automation.removeText")}
        confirm={t("common.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing.id)}
      />
    </>
  );
}

function RunsCard({ rows, rules }: { rows: Run[]; rules: Rule[] }) {
  const names = new Map(rules.map((r) => [r.id, r.name]));
  const tone = (s: Run["status"]) => (s === "ok" ? "ok" : s === "failed" ? "bad" : "warn");
  return (
    <section className="card glass reveal">
      <CardHead title={t("automation.journal")} sub={t("automation.journalSub")} />
      {rows.length === 0 ? (
        <p className="py-6 text-center text-[13px] text-[var(--md-on-surface-variant)]">{t("automation.noRuns")}</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="utable">
            <thead>
              <tr>
                <th>{t("automation.run.rule")}</th>
                <th>{t("automation.run.event")}</th>
                <th>{t("automation.run.status")}</th>
                <th>{t("automation.run.detail")}</th>
                <th>{t("automation.run.at")}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.id} style={{ cursor: "default" }}>
                  <td className="font-medium">{names.get(r.rule_id) ?? `#${r.rule_id}`}</td>
                  <td className="mono text-xs">{r.event_type}{r.user_id ? ` · #${r.user_id}` : ""}</td>
                  <td><Pill tone={tone(r.status)}>{t(`automation.run.${r.status}` as Key)}</Pill>{r.dry_run ? <span className="ml-1 text-xs">dry</span> : null}</td>
                  <td className="max-w-[360px] break-words text-xs">{r.detail}</td>
                  <td className="whitespace-nowrap text-xs">{ago(r.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function RuleForm({ initial, errors, saving, onSubmit }: { initial: Draft; errors: Record<string, string>; saving: boolean; onSubmit: (d: Draft) => void }) {
  const [d, setD] = useState(initial);
  const set = <K extends keyof Draft,>(k: K, v: Draft[K]) => setD((x) => ({ ...x, [k]: v }));
  const setCond = <K extends keyof Conditions,>(k: K, v: Conditions[K]) => setD((x) => ({ ...x, conditions: { ...x.conditions, [k]: v } }));
  const setAction = (i: number, patch: Partial<Action>) => setD((x) => ({ ...x, actions: x.actions.map((a, j) => (j === i ? { ...a, ...patch } : a)) }));
  const addAction = () => setD((x) => ({ ...x, actions: [...x.actions, { type: "notify_admin", text: "" }] }));
  const removeAction = (i: number) => setD((x) => ({ ...x, actions: x.actions.filter((_, j) => j !== i) }));
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onSubmit(d);
  };
  const toggleState = (s: string) => {
    const cur = d.conditions.states ?? [];
    setCond("states", cur.includes(s) ? cur.filter((x) => x !== s) : [...cur, s]);
  };
  return (
    <form id="automation-form" onSubmit={submit} className="flex flex-col gap-4 pt-4" noValidate>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={t("automation.name")} htmlFor="au-name" error={errors.name}>
          <input id="au-name" className="input" value={d.name} onChange={(e) => set("name", e.target.value)} maxLength={100} aria-invalid={!!errors.name} />
        </Field>
        <Field label={t("automation.trigger")} htmlFor="au-trigger">
          <select id="au-trigger" className="input" value={d.trigger} onChange={(e) => set("trigger", e.target.value as Trigger)}>
            {TRIGGERS.map((x) => <option key={x.value} value={x.value}>{t(x.label)}</option>)}
          </select>
        </Field>
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <SwitchRow label={t("automation.enabled")} sub={t("automation.enabledSub")} checked={d.enabled} onChange={(v) => set("enabled", v)} />
        <SwitchRow label={t("automation.dryRun")} sub={t("automation.dryRunSub")} checked={d.dry_run} onChange={(v) => set("dry_run", v)} />
      </div>
      <Field label={t("automation.cooldown")} htmlFor="au-cooldown" hint={t("automation.cooldownHint")} error={errors.cooldown_seconds}>
        <input id="au-cooldown" className="input max-w-[220px]" type="number" min={0} step={1} value={d.cooldown_seconds} onChange={(e) => set("cooldown_seconds", Number(e.target.value) || 0)} />
      </Field>

      <section className="rounded-2xl border border-[var(--hairline)] p-3">
        <h3 className="text-[14px] font-semibold">{t("automation.conditions")}</h3>
        <p className="mt-1 text-xs text-[var(--md-on-surface-variant)]">{t("automation.conditionsSub")}</p>
        <div className="mt-3 grid gap-3 sm:grid-cols-2">
          <Field label={t("automation.daysMin")} htmlFor="au-days-min">
            <input id="au-days-min" className="input" type="number" min={0} value={d.conditions.days_left_min ?? ""} onChange={(e) => setCond("days_left_min", e.target.value === "" ? undefined : Number(e.target.value))} />
          </Field>
          <Field label={t("automation.daysMax")} htmlFor="au-days-max">
            <input id="au-days-max" className="input" type="number" min={0} value={d.conditions.days_left_max ?? ""} onChange={(e) => setCond("days_left_max", e.target.value === "" ? undefined : Number(e.target.value))} />
          </Field>
          <Field label={t("automation.trafficMin")} htmlFor="au-tr-min">
            <input id="au-tr-min" className="input" type="number" min={0} max={100} value={d.conditions.traffic_used_min ?? ""} onChange={(e) => setCond("traffic_used_min", e.target.value === "" ? undefined : Number(e.target.value))} />
          </Field>
          <Field label={t("automation.trafficMax")} htmlFor="au-tr-max">
            <input id="au-tr-max" className="input" type="number" min={0} max={100} value={d.conditions.traffic_used_max ?? ""} onChange={(e) => setCond("traffic_used_max", e.target.value === "" ? undefined : Number(e.target.value))} />
          </Field>
          <Field label={t("automation.tariffs")} htmlFor="au-tariffs" hint={t("automation.idsHint")}>
            <input id="au-tariffs" className="input mono" value={(d.conditions.tariff_ids ?? []).join(", ")} onChange={(e) => setCond("tariff_ids", idsOf(e.target.value))} placeholder="1, 4, 7" />
          </Field>
          <Field label={t("automation.sources")} htmlFor="au-sources" hint={t("automation.sourcesHint")}>
            <input id="au-sources" className="input mono" value={(d.conditions.sources ?? []).join(", ")} onChange={(e) => setCond("sources", e.target.value.split(",").map((s) => s.trim()).filter(Boolean))} placeholder="bot, partner" />
          </Field>
        </div>
        <fieldset className="mt-3">
          <legend className="mb-2 text-xs font-medium text-[var(--md-on-surface-variant)]">{t("automation.states")}</legend>
          <div className="flex flex-wrap gap-3">
            {STATES.map((s) => (
              <label key={s} className="flex items-center gap-2 text-[13px]">
                <input type="checkbox" className="check" checked={(d.conditions.states ?? []).includes(s)} onChange={() => toggleState(s)} />
                {t(`states.${s}` as Key)}
              </label>
            ))}
          </div>
        </fieldset>
      </section>

      <section className="rounded-2xl border border-[var(--hairline)] p-3">
        <div className="flex items-center justify-between gap-2">
          <div>
            <h3 className="text-[14px] font-semibold">{t("automation.actionsTitle")}</h3>
            <p className="mt-1 text-xs text-[var(--md-on-surface-variant)]">{t("automation.actionsSub")}</p>
          </div>
          <Button size="sm" onClick={addAction}><Plus size={16} aria-hidden /> {t("automation.addAction")}</Button>
        </div>
        {errors.actions ? <p className="mt-2 text-xs text-[var(--md-error)]">{errors.actions}</p> : null}
        <div className="mt-3 flex flex-col gap-3">
          {d.actions.map((a, i) => (
            <div key={i} className="rounded-xl bg-[var(--md-surface-container)] p-3">
              <div className="flex items-center gap-2">
                <select
                  className="input max-w-[260px]"
                  value={a.type}
                  onChange={(e) => {
                    const type = e.target.value as ActionType;
                    const defaults: Partial<Action> =
                      type === "extend" ? { days: a.days ?? 30 }
                      : type === "grant_traffic" ? { bytes: a.bytes ?? 10 * GiB, lifetime: a.lifetime ?? "used", grant_days: a.grant_days ?? 30 }
                      : type === "discount" ? { percent: a.percent ?? 10, discount_days: a.discount_days ?? 7 }
                      : {};
                    setAction(i, { ...defaults, type });
                  }}
                  aria-label={t("automation.actionType")}
                >
                  {ACTION_TYPES.map((x) => <option key={x.value} value={x.value}>{t(x.label)}</option>)}
                </select>
                <button type="button" className="icon-btn ml-auto" onClick={() => removeAction(i)} aria-label={t("automation.removeAction")} disabled={d.actions.length === 1}>
                  <Trash2 size={16} aria-hidden />
                </button>
              </div>
              {a.type === "extend" ? (
                <Field label={t("automation.days")} htmlFor={`au-days-${i}`} hint={t("automation.daysHint")}>
                  <input id={`au-days-${i}`} className="input max-w-[180px]" type="number" min={1} value={a.days ?? 30} onChange={(e) => setAction(i, { days: Number(e.target.value) || 0 })} />
                </Field>
              ) : null}
              {a.type === "grant_traffic" ? (
                <div className="mt-2 grid gap-2 sm:grid-cols-3">
                  <Field label={t("automation.gb")} htmlFor={`au-gb-${i}`}>
                    <input id={`au-gb-${i}`} className="input" type="number" min={0.1} step={0.1} value={a.bytes ? (a.bytes / GiB).toFixed(2) : ""} onChange={(e) => setAction(i, { bytes: Math.round((Number(e.target.value) || 0) * GiB) })} />
                  </Field>
                  <Field label={t("packages.lifetimeLabel")} htmlFor={`au-life-${i}`}>
                    <select id={`au-life-${i}`} className="input" value={a.lifetime ?? "used"} onChange={(e) => setAction(i, { lifetime: e.target.value })}>
                      <option value="used">{t("packages.lifetime.used")}</option>
                      <option value="period">{t("packages.lifetime.period")}</option>
                      <option value="days">{t("packages.lifetimeDays")}</option>
                    </select>
                  </Field>
                  {(a.lifetime ?? "used") === "days" ? (
                    <Field label={t("automation.grantDays")} htmlFor={`au-gdays-${i}`}>
                      <input id={`au-gdays-${i}`} className="input" type="number" min={1} value={a.grant_days ?? 30} onChange={(e) => setAction(i, { grant_days: Number(e.target.value) || 0 })} />
                    </Field>
                  ) : null}
                </div>
              ) : null}
              {a.type === "promo" ? (
                <Field label={t("automation.promoCode")} htmlFor={`au-code-${i}`} hint={t("automation.promoCodeHint")}>
                  <input id={`au-code-${i}`} className="input mono max-w-[260px]" value={a.code ?? ""} onChange={(e) => setAction(i, { code: e.target.value })} />
                </Field>
              ) : null}
              {a.type === "discount" ? (
                <div className="mt-2 grid gap-2 sm:grid-cols-2">
                  <Field label={t("automation.percent")} htmlFor={`au-percent-${i}`} hint={t("automation.percentHint")}>
                    <input id={`au-percent-${i}`} className="input" type="number" min={1} max={100} value={a.percent ?? 10} onChange={(e) => setAction(i, { percent: Number(e.target.value) || 0 })} />
                  </Field>
                  <Field label={t("automation.discountDays")} htmlFor={`au-discount-days-${i}`} hint={t("automation.discountDaysHint")}>
                    <input id={`au-discount-days-${i}`} className="input" type="number" min={1} max={3650} value={a.discount_days ?? 7} onChange={(e) => setAction(i, { discount_days: Number(e.target.value) || 0 })} />
                  </Field>
                </div>
              ) : null}
              {a.type !== "extend" && a.type !== "grant_traffic" ? (
                <Field label={t("automation.text")} htmlFor={`au-text-${i}`} hint={t("automation.textHint")}>
                  <textarea id={`au-text-${i}`} className="input min-h-[80px]" value={a.text ?? ""} onChange={(e) => setAction(i, { text: e.target.value })} maxLength={2000} />
                </Field>
              ) : null}
            </div>
          ))}
        </div>
      </section>
      <p className="text-xs text-[var(--md-on-surface-variant)]">{saving ? t("common.loading") : t("automation.formHint")}</p>
    </form>
  );
}
