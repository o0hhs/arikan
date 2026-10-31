import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { FormEvent } from "react";
import { api, errorText, type Schemas, unwrap } from "../../../api/client";
import { qk, useDashboard } from "../../../api/hooks";
import { FormActions } from "../../../components/layout";
import { SwitchRow } from "../../../components/switch";
import { useToast } from "../../../components/toast";
import { Field, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { tickStep, useTick } from "../../../lib/clock";
import { useDraft } from "../../../lib/draft";
import { dashDay, dashTime } from "../../../lib/format";

type Layout = Schemas["Layout"];

/** The cards the dashboard may show, as the page names them, with what each one is. */
const CARDS = [
  { id: "kpis", label: "settings.dash.card.kpis", sub: "settings.dash.card.kpisSub" },
  { id: "traffic", label: "settings.dash.card.traffic", sub: "settings.dash.card.trafficSub" },
  { id: "attention", label: "settings.dash.card.attention", sub: "settings.dash.card.attentionSub" },
  { id: "top", label: "settings.dash.card.top", sub: "settings.dash.card.topSub" },
  { id: "server", label: "settings.dash.card.server", sub: "settings.dash.card.serverSub" },
  { id: "nodes", label: "settings.dash.card.nodes", sub: "settings.dash.card.nodesSub" },
] as const;

/** The dates the dashboard may write, with what each one looks like. */
const DATES = [
  { id: "compact", label: "settings.dash.dateOp.compact" },
  { id: "full", label: "settings.dash.dateOp.full" },
  { id: "large", label: "settings.dash.dateOp.large" },
] as const;

/**
 * What the dashboard shows and how it dates it. The cards are the page's own; the clock is
 * the reader's — the format is chosen here, the time zone and the language come from the
 * device the panel is open on, so the same dashboard shows everyone their own time.
 */
export function DashboardCard() {
  const q = useDashboard();
  if (!q.data) {
    return q.isError ? (
      <section className="card glass">
        <div className="banner err">{errorText(q.error)}</div>
      </section>
    ) : (
      <Skeleton style={{ height: 320, borderRadius: 20 }} />
    );
  }
  return <DashboardForm v={q.data} />;
}

function DashboardForm({ v }: { v: Layout }) {
  const qc = useQueryClient();
  const toast = useToast();
  const { draft, setDraft, dirty, reset } = useDraft({ cards: v.cards, date: v.date, seconds: v.seconds });
  const save = useMutation({
    mutationFn: () => unwrap(api.PUT("/api/v1/dashboard", { body: draft })),
    onSuccess: (nv) => {
      qc.setQueryData(qk.dashboard, nv);
      setDraft({ cards: nv.cards, date: nv.date, seconds: nv.seconds });
      toast.ok(t("settings.saved"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate();
  };
  const style = draft.date;
  const now = useTick(tickStep(style, draft.seconds));
  return (
    <section className="card glass reveal">
      <form onSubmit={submit} noValidate>
        <div className="card-head">
          <div>
            <h2 className="card-title">{t("settings.dash.title")}</h2>
            <div className="card-sub">{t("settings.dash.sub")}</div>
          </div>
        </div>
        <Field label={t("settings.dash.cards")} hint={t("settings.dash.cardsHint")}>
          <ul className="row-list">
            {CARDS.map((c) => (
              <li key={c.id}>
                <SwitchRow label={t(c.label)} sub={t(c.sub)} checked={draft.cards[c.id] !== false} onChange={(on) => setDraft((d) => ({ ...d, cards: { ...d.cards, [c.id]: on } }))} />
              </li>
            ))}
          </ul>
        </Field>
        <Field label={t("settings.dash.date")} hint={t("settings.dash.dateHint")}>
          <div className="date-choices" role="radiogroup" aria-label={t("settings.dash.date")}>
            {DATES.map((d) => (
              <button key={d.id} type="button" role="radio" className="opt opt-center" aria-checked={style === d.id} onClick={() => setDraft((draft) => ({ ...draft, date: d.id }))}>
                {t(d.label)}
              </button>
            ))}
          </div>
          {/* What the choice looks like, now, in the reader's own time zone: the same two
              strings the dashboard writes, so the admin sees exactly what they will get. */}
          <div className="mt-2 flex flex-wrap items-baseline gap-x-3 gap-y-1">
            <span className={style === "large" ? "num text-[clamp(22px,2.6vw,32px)] leading-none font-semibold text-[var(--md-on-surface)]" : "text-[15px] font-medium text-[var(--md-on-surface)]"}>{dashDay(now, style)}</span>
            {style === "compact" ? null : (
              <span className={style === "large" ? "num text-[clamp(18px,2.2vw,26px)] leading-none font-medium text-[var(--md-primary)]" : "num text-[15px] text-[var(--md-primary)]"}>
                {dashTime(now, draft.seconds)}
              </span>
            )}
          </div>
        </Field>
        {style === "compact" ? null : <SwitchRow label={t("settings.dash.seconds")} sub={t("settings.dash.secondsSub")} checked={draft.seconds} onChange={(on) => setDraft((d) => ({ ...d, seconds: on }))} />}
        <FormActions dirty={dirty} saving={save.isPending} onReset={reset} />
      </form>
    </section>
  );
}
