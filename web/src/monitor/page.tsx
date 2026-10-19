import { LockKeyhole, LogOut, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import type { TrafficPoint } from "../api/client";
import { TrafficChart, buckets, type Range } from "../components/chart";
import { LangSwitch } from "../components/lang";
import { Button, Segmented, Skeleton } from "../components/ui";
import { t } from "../i18n";
import { ago, bytes, dashTime, money, num } from "../lib/format";
import { useTick } from "../lib/clock";

/** What the page's own API answers (internal/panel/monitor/state.go). */
type State = {
  generated_at: string;
  brand: string;
  blocks: string[];
  range: Range;
  detail_on: boolean;
  summary: {
    online: number;
    subscriptions: number;
    active: number;
    traffic_today: number;
    traffic_month: number;
    traffic_total: number;
    nodes_up: number;
    nodes_total: number;
  };
  traffic: TrafficPoint[];
  nodes: { name: string; local: boolean; ok: boolean; conns: number; up: number; down: number; last_seen?: string }[];
  sites: { site: string; up: number; down: number }[];
  payments: { currency: string; count: number; amount: number }[];
};

const RANGES = [
  { id: "24h", label: "monitor.range.24h" },
  { id: "7d", label: "monitor.range.7d" },
  { id: "30d", label: "monitor.range.30d" },
] as const;

/** Every 30 s: the numbers move slowly, and the page is left open for days. */
const REFRESH_MS = 30_000;

export function MonitorPage() {
  // The page's own address, as the server's <base> gives it.
  const root = useMemo(() => new URL(".", document.baseURI).toString(), []);
  const [session, setSession] = useState<"loading" | "login" | "ready" | "gone">("loading");
  const [range, setRange] = useState<Range>("24h");
  const [state, setState] = useState<State | null>(null);
  const [failed, setFailed] = useState(false);

  const load = useCallback(
    async (r: Range) => {
      try {
        const res = await fetch(`${root}api/state?range=${r}`, { credentials: "same-origin", headers: { Accept: "application/json" } });
        if (res.status === 401) {
          setSession("login");
          return;
        }
        if (!res.ok) throw new Error(String(res.status));
        setState((await res.json()) as State);
        setSession("ready");
        setFailed(false);
      } catch {
        setFailed(true);
        setSession((cur) => (cur === "ready" ? "ready" : "gone"));
      }
    },
    [root],
  );

  useEffect(() => {
    if (session === "login" || session === "gone") return;
    void load(range);
    const id = setInterval(() => void load(range), REFRESH_MS);
    return () => clearInterval(id);
  }, [load, range, session]);

  if (session === "login" || session === "gone") {
    return <Login root={root} closed={session === "gone" && !failed} onOpen={() => void load(range)} />;
  }
  if (!state) {
    return (
      <Frame brand="">
        <Skeleton className="mx-auto mt-10 max-w-3xl" style={{ height: 320, borderRadius: 20 }} />
      </Frame>
    );
  }
  return (
    <Frame brand={state.brand}>
      <Content
        state={state}
        range={range}
        onRange={setRange}
        failed={failed}
        onRefresh={() => void load(range)}
        onLogout={async () => {
          await fetch(`${root}api/logout`, { method: "POST", credentials: "same-origin" }).catch(() => undefined);
          setState(null);
          setSession("login");
        }}
      />
    </Frame>
  );
}

function Frame({ brand, children }: { brand: string; children: React.ReactNode }) {
  return (
    <div className="min-h-dvh bg-[var(--md-surface)] text-[var(--md-on-surface)]">
      <div className="mx-auto flex min-h-dvh max-w-[1100px] flex-col gap-4 px-4 py-6">
        <header className="flex flex-wrap items-center gap-3">
          <div className="min-w-0">
            <h1 className="font-display text-[19px] font-medium tracking-tight">{brand || t("monitor.title")}</h1>
            <p className="text-[13px] text-[var(--md-on-surface-variant)]">{t("monitor.subtitle")}</p>
          </div>
          <LangSwitch className="ml-auto" />
        </header>
        {children}
        <footer className="mt-auto pt-4 text-center text-xs text-[var(--md-on-surface-variant)]">{t("monitor.auto")}</footer>
      </div>
    </div>
  );
}

function Login({ root, closed, onOpen }: { root: string; closed: boolean; onOpen: () => void }) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<"" | "bad" | "limited" | "failed">("");
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const res = await fetch(`${root}api/login`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ password }),
      });
      if (res.ok) {
        setPassword("");
        onOpen();
        return;
      }
      setError(res.status === 429 ? "limited" : res.status === 401 ? "bad" : "failed");
    } catch {
      setError("failed");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Frame brand="">
      <form onSubmit={submit} className="card glass mx-auto mt-10 w-full max-w-[420px]">
        <div className="card-head">
          <div>
            <h2 className="card-title">{t("monitor.login.title")}</h2>
            <div className="card-sub">{closed ? t("monitor.login.closed") : t("monitor.login.sub")}</div>
          </div>
          <LockKeyhole size={20} aria-hidden className="text-[var(--md-on-surface-variant)]" />
        </div>
        <label className="text-[13px] font-medium" htmlFor="monitor-password">
          {t("monitor.login.password")}
        </label>
        <input
          id="monitor-password"
          className="input mt-1 w-full"
          type="password"
          autoComplete="current-password"
          autoFocus
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        {error ? (
          <p className="banner err mt-3" role="alert">
            {t(error === "bad" ? "monitor.login.bad" : error === "limited" ? "monitor.login.limited" : "monitor.login.failed")}
          </p>
        ) : null}
        <Button type="submit" variant="primary" className="mt-4 w-full justify-center" disabled={busy || password === ""}>
          {t("monitor.login.submit")}
        </Button>
      </form>
    </Frame>
  );
}

function Content({ state, range, onRange, failed, onRefresh, onLogout }: {
  state: State;
  range: Range;
  onRange: (r: Range) => void;
  failed: boolean;
  onRefresh: () => void;
  onLogout: () => void;
}) {
  const now = useTick(1000);
  const s = state.summary;
  const points = useMemo(() => buckets(state.traffic, range), [state.traffic, range]);
  const totals = useMemo(() => state.traffic.reduce((a, p) => ({ up: a.up + p.up, down: a.down + p.down }), { up: 0, down: 0 }), [state.traffic]);
  const show = (block: string) => state.blocks.includes(block);
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <span className="num text-[13px] text-[var(--md-on-surface-variant)]">{t("monitor.updated", { time: dashTime(now, true) })}</span>
        {failed ? (
          <span className="banner err" role="status">
            {t("monitor.stale")}
          </span>
        ) : null}
        <div className="ml-auto flex items-center gap-2">
          {show("traffic") ? <Segmented label={t("monitor.period")} value={range} onChange={(v) => onRange(v as Range)} options={RANGES.map((r) => ({ value: r.id, label: t(r.label) }))} /> : null}
          <Button variant="ghost" size="sm" onClick={onRefresh} aria-label={t("monitor.refresh")} title={t("monitor.refresh")}>
            <RefreshCw size={16} aria-hidden />
          </Button>
          <Button variant="ghost" size="sm" onClick={onLogout}>
            <LogOut size={16} aria-hidden />
            {t("monitor.logout")}
          </Button>
        </div>
      </div>

      {show("summary") ? (
        <section className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
          <Stat label={t("monitor.kpi.online")} value={num(s.online)} />
          <Stat label={t("monitor.kpi.active")} value={num(s.active)} of={t("monitor.kpi.ofSubs", { n: s.subscriptions })} />
          <Stat label={t("monitor.kpi.today")} value={bytes(s.traffic_today)} />
          <Stat label={t("monitor.kpi.month")} value={bytes(s.traffic_month)} />
          <Stat label={t("monitor.kpi.total")} value={bytes(s.traffic_total)} />
          <Stat label={t("monitor.kpi.nodes")} value={`${s.nodes_up}/${s.nodes_total}`} tone={s.nodes_up < s.nodes_total ? "warn" : ""} />
        </section>
      ) : null}

      {show("traffic") ? (
        <section className="card glass">
          <div className="card-head">
            <div>
              <h2 className="card-title">{t("monitor.traffic.title")}</h2>
              <div className="card-sub">
                {t("monitor.traffic.totals", { down: bytes(totals.down), up: bytes(totals.up), range: t(`monitor.range.${range}`) })}
              </div>
            </div>
          </div>
          <TrafficChart points={points} range={range} />
        </section>
      ) : null}

      {show("nodes") ? (
        <section className="card glass">
          <div className="card-head">
            <div>
              <h2 className="card-title">{t("monitor.nodes.title")}</h2>
              <div className="card-sub">{t("monitor.nodes.sub")}</div>
            </div>
          </div>
          {state.nodes.length === 0 ? (
            <p className="text-[13px] text-[var(--md-on-surface-variant)]">{t("monitor.nodes.none")}</p>
          ) : (
            <ul className="flex flex-col divide-y divide-[var(--md-outline-variant)]">
              {state.nodes.map((n) => (
                <li key={n.name} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2.5">
                  <span className={n.ok ? "size-2 shrink-0 rounded-full bg-[var(--leaf-500)]" : "size-2 shrink-0 rounded-full bg-[var(--berry-600)]"} aria-hidden />
                  <span className="mono min-w-0 flex-1 truncate text-[14px]">{n.name}</span>
                  {n.local ? <span className="text-xs text-[var(--md-on-surface-variant)]">{t("monitor.nodes.panel")}</span> : null}
                  <span className="num text-[13px] text-[var(--md-on-surface-variant)]">{t("monitor.nodes.conns", { n: n.conns })}</span>
                  <span className="num w-[92px] text-right text-[13px]">{bytes(n.down + n.up)}</span>
                  <span className="w-[128px] text-right text-xs text-[var(--md-on-surface-variant)]">{n.ok ? t("monitor.nodes.up") : n.last_seen ? t("monitor.nodes.seen", { ago: ago(n.last_seen) }) : t("monitor.nodes.never")}</span>
                </li>
              ))}
            </ul>
          )}
        </section>
      ) : null}

      {show("sites") ? (
        <section className="card glass">
          <div className="card-head">
            <div>
              <h2 className="card-title">{t("monitor.sites.title")}</h2>
              <div className="card-sub">{t("monitor.sites.sub")}</div>
            </div>
          </div>
          {!state.detail_on ? (
            <p className="text-[13px] text-[var(--md-on-surface-variant)]">{t("monitor.sites.off")}</p>
          ) : state.sites.length === 0 ? (
            <p className="text-[13px] text-[var(--md-on-surface-variant)]">{t("monitor.sites.none")}</p>
          ) : (
            <ul className="flex flex-col gap-1.5">
              {state.sites.map((site) => (
                <li key={site.site} className="flex items-baseline justify-between gap-3 text-[13px]">
                  <span className="mono truncate">{site.site === "*" ? t("monitor.sites.other") : site.site}</span>
                  <span className="num shrink-0 text-[var(--md-on-surface-variant)]">{bytes(site.up + site.down)}</span>
                </li>
              ))}
            </ul>
          )}
        </section>
      ) : null}

      {show("payments") ? (
        <section className="card glass">
          <div className="card-head">
            <div>
              <h2 className="card-title">{t("monitor.payments.title")}</h2>
              <div className="card-sub">{t("monitor.payments.sub")}</div>
            </div>
          </div>
          {state.payments.length === 0 ? (
            <p className="text-[13px] text-[var(--md-on-surface-variant)]">{t("monitor.payments.none")}</p>
          ) : (
            <ul className="flex flex-col gap-1.5">
              {state.payments.map((p) => (
                <li key={p.currency} className="flex items-baseline justify-between gap-3 text-[13px]">
                  <span>{t("monitor.payments.count", { n: p.count })}</span>
                  <span className="num shrink-0">{money(p.amount, p.currency)}</span>
                </li>
              ))}
            </ul>
          )}
        </section>
      ) : null}
    </>
  );
}

function Stat({ label, value, of, tone }: { label: string; value: string; of?: string; tone?: "" | "warn" }) {
  return (
    <div className="card glass">
      <div className="text-[12px] tracking-wide text-[var(--md-on-surface-variant)] uppercase">{label}</div>
      <div className={tone === "warn" ? "num mt-1 text-[19px] font-semibold text-[var(--berry-600)]" : "num mt-1 text-[19px] font-semibold"}>{value}</div>
      {of ? <div className="num text-xs text-[var(--md-on-surface-variant)]">{of}</div> : null}
    </div>
  );
}
