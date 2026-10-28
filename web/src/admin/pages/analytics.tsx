import { BarChart3, CircleDollarSign, Megaphone, Percent, ReceiptText, TrendingUp, UserMinus, Users, type LucideIcon } from "lucide-react";
import { useState } from "react";
import type { Schemas } from "../../api/client";
import { useAnalytics } from "../../api/hooks";
import { CardHead } from "../../components/layout";
import { QueryBoundary } from "../../components/query";
import { Bar, EmptyState, PageHeader, Segmented, Skeleton } from "../../components/ui";
import { getLocale, t, type Key } from "../../i18n";
import { dateShort, money, num } from "../../lib/format";

type View = Schemas["AnalyticsOutputBody"];
type Summary = Schemas["AnalyticsSummary"];
type Range = "30d" | "90d" | "365d";

const RANGES: { value: Range; label: Key }[] = [
  { value: "30d", label: "analytics.ranges.30d" },
  { value: "90d", label: "analytics.ranges.90d" },
  { value: "365d", label: "analytics.ranges.365d" },
];

function pct(n: number): string {
  const v = Math.round(n * 10) / 10;
  return `${v.toLocaleString(getLocale())}%`;
}

export function AnalyticsPage() {
  const [range, setRange] = useState<Range>("30d");
  const analytics = useAnalytics(range);
  return (
    <>
      <PageHeader
        title={t("analytics.title")}
        sub={t("analytics.subtitle")}
        actions={<Segmented label={t("analytics.range")} value={range} onChange={setRange} options={RANGES.map((r) => ({ value: r.value, label: t(r.label) }))} />}
      />
      <QueryBoundary
        query={analytics}
        pending={
          <div className="grid grid-cols-2 gap-4 xl:grid-cols-4" role="status" aria-busy aria-label={t("common.loading")}>
            {[0, 1, 2, 3, 4, 5, 6, 7].map((i) => (
              <section key={i} className="card glass kpi">
                <Skeleton style={{ width: "55%" }} />
                <Skeleton style={{ width: "45%", height: 28, marginTop: 8 }} />
              </section>
            ))}
          </div>
        }
        wrap={(state) => <section className="card glass">{state}</section>}
      >
        {(v) => <AnalyticsBody v={v} />}
      </QueryBoundary>
    </>
  );
}

function AnalyticsBody({ v }: { v: View }) {
  const s = v.summary;
  return (
    <div className="flex flex-col gap-4">
      <Kpis s={s} />
      <RevenueCard v={v} />
      <div className="grid grid-cols-[minmax(0,1fr)] gap-4 xl:grid-cols-2 xl:items-start">
        <TrialsCard s={s} />
        <ChurnCard s={s} />
      </div>
      <div className="grid grid-cols-[minmax(0,1fr)] gap-4 xl:grid-cols-2 xl:items-start">
        <TariffCard rows={v.tariffs} />
        <ProviderCard rows={v.providers} />
      </div>
      <CohortCard rows={v.cohorts} />
      <CampaignCard rows={v.campaigns} />
      <p className="text-xs text-[var(--md-on-surface-variant)]">{t("analytics.note")}</p>
    </div>
  );
}

function Kpis({ s }: { s: Summary }) {
  return (
    <div className="grid grid-cols-2 gap-4 xl:grid-cols-4">
      <Kpi i={0} icon={TrendingUp} label={t("analytics.kpi.mrr")} value={money(s.mrr_rub, "RUB")} foot={t("analytics.kpi.mrrHint")} />
      <Kpi i={1} icon={CircleDollarSign} label={t("analytics.kpi.revenue")} value={money(s.revenue_rub, "RUB")} foot={t("analytics.kpi.revenueHint")} />
      <Kpi i={2} icon={Users} label={t("analytics.kpi.arpu")} value={money(s.arpu_rub, "RUB")} foot={t("analytics.kpi.arpuHint")} />
      <Kpi i={3} icon={ReceiptText} label={t("analytics.kpi.ltv")} value={money(s.ltv_rub, "RUB")} foot={t("analytics.kpi.ltvHint")} />
      <Kpi i={4} icon={Percent} label={t("analytics.kpi.trial")} value={pct(s.trial_conversion)} foot={t("analytics.kpi.trialHint", { converted: num(s.trial_converted), started: num(s.trial_started) })} />
      <Kpi i={5} icon={UserMinus} label={t("analytics.kpi.churn")} value={pct(s.churn)} foot={t("analytics.kpi.churnHint", { churned: num(s.churned), expired: num(s.expired) })} />
      <Kpi i={6} icon={BarChart3} label={t("analytics.kpi.stars")} value={money(s.revenue_stars, "XTR")} foot={t("analytics.kpi.starsHint")} />
      <Kpi i={7} icon={Users} label={t("analytics.kpi.paying")} value={num(s.paying_users)} foot={t("analytics.kpi.payingHint", { active: num(s.active_users) })} />
    </div>
  );
}

function Kpi({ i, icon: Icon, label, value, foot }: { i: number; icon: LucideIcon; label: string; value: string; foot: string }) {
  return (
    <section className="card glass kpi reveal" style={{ "--i": i } as React.CSSProperties}>
      <div className="kpi-label">
        <span>{label}</span>
        <Icon size={16} className="text-[var(--md-primary)]" aria-hidden />
      </div>
      <div className="kpi-value num">{value}</div>
      <div className="kpi-foot">
        <span>{foot}</span>
      </div>
    </section>
  );
}

function RevenueCard({ v }: { v: View }) {
  const s = v.summary;
  const hasRevenue = v.revenue.some((p) => p.rub > 0 || p.stars > 0);
  return (
    <section className="card glass reveal" aria-labelledby="analytics-revenue-title">
      <CardHead
        title={t("analytics.revenue.title")}
        sub={t("analytics.revenue.sub")}
        id="analytics-revenue-title"
        aside={
          <span className="text-xs text-[var(--md-on-surface-variant)]">
            {t("analytics.revenue.payments", { n: num(s.payments) })} · {t("analytics.kpi.newUsers", { n: num(s.new_users) })}
          </span>
        }
      />
      {hasRevenue ? <RevenueChart points={v.revenue} /> : <p className="py-10 text-center text-[13px] text-[var(--md-on-surface-variant)]">{t("analytics.revenue.empty")}</p>}
      <div className="mt-3 flex flex-wrap items-center gap-x-6 gap-y-2 text-[13px] text-[var(--md-on-surface-variant)]">
        <span>
          {t("analytics.revenue.total")} <b className="num font-semibold text-[var(--md-on-surface)]">{money(s.revenue_rub, "RUB")}</b>
        </span>
        <span>
          {t("analytics.revenue.stars")} <b className="num font-semibold text-[var(--md-on-surface)]">{money(s.revenue_stars, "XTR")}</b>
        </span>
      </div>
      <p className="mt-2 text-xs text-[var(--md-on-surface-variant)]">{t("analytics.revenue.note")}</p>
    </section>
  );
}

function RevenueChart({ points }: { points: View["revenue"] }) {
  if (points.length === 0) return null;
  const max = Math.max(1, ...points.map((p) => p.rub));
  const from = dateShort(points[0]!.day);
  const to = dateShort(points[points.length - 1]!.day);
  return (
    <div>
      <div className="flex h-[220px] items-end gap-[1px]" role="img" aria-label={t("analytics.revenue.chartLabel", { from, to })}>
        {points.map((p) => (
          <div key={p.day} className="flex h-full min-w-0 flex-1 items-end" title={`${dateShort(p.day)}: ${money(p.rub, "RUB")}`}>
            <div className="w-full rounded-t-[3px] bg-[var(--mikan-500)]" style={{ height: `${(p.rub / max) * 100}%`, minHeight: p.rub > 0 ? 2 : 0 }} />
          </div>
        ))}
      </div>
      <div className="mt-2 flex justify-between text-xs text-[var(--md-on-surface-variant)]">
        <span>{from}</span>
        <span>{to}</span>
      </div>
    </div>
  );
}

function TrialsCard({ s }: { s: Summary }) {
  return (
    <section className="card glass reveal" aria-labelledby="analytics-trials-title">
      <CardHead title={t("analytics.trials.title")} sub={t("analytics.trials.sub")} id="analytics-trials-title" />
      {s.trial_started === 0 ? (
        <p className="py-6 text-center text-[13px] text-[var(--md-on-surface-variant)]">{t("analytics.trials.empty")}</p>
      ) : (
        <>
          <div className="grid grid-cols-2 gap-3">
            <div className="metric">
              <div className="metric-label">{t("analytics.trials.started")}</div>
              <div className="metric-value num">{num(s.trial_started)}</div>
            </div>
            <div className="metric">
              <div className="metric-label">{t("analytics.trials.converted")}</div>
              <div className="metric-value num">{num(s.trial_converted)}</div>
            </div>
          </div>
          <div className="mt-4 flex items-center justify-between gap-3 text-[13px]">
            <span className="text-[var(--md-on-surface-variant)]">{t("analytics.trials.conversion")}</span>
            <b className="num">{pct(s.trial_conversion)}</b>
          </div>
          <Bar pct={s.trial_conversion} className="mt-2" label={t("analytics.trials.conversion")} />
        </>
      )}
    </section>
  );
}

function ChurnCard({ s }: { s: Summary }) {
  return (
    <section className="card glass reveal" aria-labelledby="analytics-churn-title">
      <CardHead title={t("analytics.churn.title")} sub={t("analytics.churn.sub")} id="analytics-churn-title" />
      {s.expired === 0 ? (
        <p className="py-6 text-center text-[13px] text-[var(--md-on-surface-variant)]">{t("analytics.churn.empty")}</p>
      ) : (
        <>
          <div className="grid grid-cols-2 gap-3">
            <div className="metric">
              <div className="metric-label">{t("analytics.churn.expired")}</div>
              <div className="metric-value num">{num(s.expired)}</div>
            </div>
            <div className="metric">
              <div className="metric-label">{t("analytics.churn.churned")}</div>
              <div className="metric-value num">{num(s.churned)}</div>
            </div>
          </div>
          <div className="mt-4 flex items-center justify-between gap-3 text-[13px]">
            <span className="text-[var(--md-on-surface-variant)]">{t("analytics.churn.rate")}</span>
            <b className="num">{pct(s.churn)}</b>
          </div>
          <Bar pct={s.churn} className="mt-2 [&>i]:!bg-[var(--md-error)]" label={t("analytics.churn.rate")} />
        </>
      )}
    </section>
  );
}

function TariffCard({ rows }: { rows: View["tariffs"] }) {
  return (
    <section className="card glass reveal" aria-labelledby="analytics-tariffs-title">
      <CardHead title={t("analytics.tariffs.title")} sub={t("analytics.tariffs.sub")} id="analytics-tariffs-title" />
      {rows.length === 0 ? (
        <p className="py-6 text-center text-[13px] text-[var(--md-on-surface-variant)]">{t("analytics.tariffs.empty")}</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="utable">
            <thead>
              <tr>
                <th>{t("analytics.tariffs.name")}</th>
                <th className="text-right">{t("analytics.tariffs.payments")}</th>
                <th className="text-right">{t("analytics.tariffs.rub")}</th>
                <th className="text-right">{t("analytics.tariffs.stars")}</th>
                <th className="text-right">{t("analytics.tariffs.share")}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={`${r.id ?? "deleted"}-${r.name}`} style={{ cursor: "default" }}>
                  <td className="font-medium">{r.name}</td>
                  <td className="num text-right">{num(r.payments)}</td>
                  <td className="num text-right">{money(r.rub, "RUB")}</td>
                  <td className="num text-right">{r.stars ? money(r.stars, "XTR") : "—"}</td>
                  <td className="num text-right">{pct(r.share)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function ProviderCard({ rows }: { rows: View["providers"] }) {
  return (
    <section className="card glass reveal" aria-labelledby="analytics-providers-title">
      <CardHead title={t("analytics.providers.title")} sub={t("analytics.providers.sub")} id="analytics-providers-title" />
      {rows.length === 0 ? (
        <p className="py-6 text-center text-[13px] text-[var(--md-on-surface-variant)]">{t("analytics.providers.empty")}</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="utable">
            <thead>
              <tr>
                <th>{t("analytics.providers.name")}</th>
                <th className="text-right">{t("analytics.providers.payments")}</th>
                <th className="text-right">{t("analytics.providers.rub")}</th>
                <th className="text-right">{t("analytics.providers.stars")}</th>
                <th className="text-right">{t("analytics.providers.share")}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.provider} style={{ cursor: "default" }}>
                  <td className="font-medium">{providerName(r.provider)}</td>
                  <td className="num text-right">{num(r.payments)}</td>
                  <td className="num text-right">{money(r.rub, "RUB")}</td>
                  <td className="num text-right">{r.stars ? money(r.stars, "XTR") : "—"}</td>
                  <td className="num text-right">{pct(r.share)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

/** A provider as the payments page names it; an adapter keeps its own name. */
function providerName(p: string): string {
  if (p.startsWith("addon:")) return p.slice("addon:".length);
  if (p === "stars") return t("payments.providers.stars");
  if (p === "balance") return t("payments.providers.balance");
  return p;
}

function CohortCard({ rows }: { rows: View["cohorts"] }) {
  return (
    <section className="card glass reveal" aria-labelledby="analytics-cohorts-title">
      <CardHead title={t("analytics.cohorts.title")} sub={t("analytics.cohorts.sub")} id="analytics-cohorts-title" />
      {rows.length === 0 ? (
        <p className="py-6 text-center text-[13px] text-[var(--md-on-surface-variant)]">{t("analytics.cohorts.empty")}</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="utable">
            <thead>
              <tr>
                <th>{t("analytics.cohorts.month")}</th>
                <th className="text-right">{t("analytics.cohorts.users")}</th>
                {[0, 1, 2, 3, 4, 5].map((n) => (
                  <th key={n} className="text-right num">
                    {n}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((c) => (
                <tr key={c.month} style={{ cursor: "default" }}>
                  <td className="num font-medium">{c.month}</td>
                  <td className="num text-right">{num(c.users)}</td>
                  {c.rates.map((rate, i) => (
                    <td
                      key={i}
                      className="num text-right"
                      title={t("analytics.cohorts.cell", { paying: num(c.paying[i] ?? 0), users: num(c.users), rate: pct(rate) })}
                      style={{ background: rate > 0 ? `color-mix(in srgb, var(--leaf-500) ${Math.min(rate, 100) * 0.18}%, transparent)` : undefined }}
                    >
                      {rate > 0 ? pct(rate) : "—"}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function CampaignCard({ rows }: { rows: View["campaigns"] }) {
  return (
    <section className="card glass reveal" aria-labelledby="analytics-campaigns-title">
      <CardHead title={t("analytics.campaigns.title")} sub={t("analytics.campaigns.sub")} id="analytics-campaigns-title" />
      {rows.length === 0 ? (
        <EmptyState icon={Megaphone} title={t("analytics.campaigns.empty")} text={t("analytics.campaigns.sub")} />
      ) : (
        <div className="overflow-x-auto">
          <table className="utable">
            <thead>
              <tr>
                <th>{t("analytics.campaigns.name")}</th>
                <th className="text-right">{t("analytics.campaigns.bought")}</th>
                <th className="text-right">{t("analytics.campaigns.renewed")}</th>
                <th className="text-right">{t("analytics.campaigns.rub")}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((c) => (
                <tr key={c.id} style={{ cursor: "default" }}>
                  <td>
                    <div className="font-medium">{c.name}</div>
                    <div className="text-xs text-[var(--md-on-surface-variant)]">{c.tag}</div>
                  </td>
                  <td className="num text-right">{num(c.bought)}</td>
                  <td className="num text-right">{num(c.renewed)}</td>
                  <td className="num text-right">{money(c.rub, "RUB")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
