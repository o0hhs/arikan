// The advertising agents' screen: a login of its own and three pages — the money's
// summary, the links and the clients they brought. The numbers are the panel's; the
// agent may look but not touch, so every call is a plain GET.
import { useMutation, useQuery, type UseQueryResult } from "@tanstack/react-query";
import { Copy, LogOut } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { ApiError, errorText, setCsrf } from "../api/client";
import { qk, rawJson } from "../api/hooks";
import { LangSwitch } from "../components/lang";
import { Tabs } from "../components/tabs";
import { useToast } from "../components/toast";
import { Button, EmptyState, ErrorState, Field, Pill, Skeleton } from "../components/ui";
import { Logo } from "../components/atmosphere";
import { t } from "../i18n";
import { useCopy } from "../lib/copy";
import { dateShort, money, num } from "../lib/format";

type Self = { id: number; login: string; created_at: string; csrf_token: string };
type Overview = {
  visits: number;
  bought: number;
  renewed: number;
  revenue: number;
  earned: number;
  paid: number;
  owed: number;
  currency: string;
};
type LinkRow = {
  id: number;
  tag: string;
  name?: string;
  enabled: boolean;
  link?: string;
  percent: number;
  visits: number;
  bought: number;
  renewed: number;
  revenue: number;
  earned: number;
  created_at: string;
};
type ClientRow = { chat_id: number; name?: string; tag: string; first_seen: string; bought: number; renewed: number; spent: number };
type PayoutRow = { id: number; amount: number; note?: string; created_at: string };
type AgentTab = "overview" | "links" | "clients";

export function App() {
  const [state, setState] = useState<"loading" | "anon" | "ready">("loading");
  const [me, setMe] = useState<Self>();

  useEffect(() => {
    const out = () => {
      setCsrf("");
      setMe(undefined);
      setState("anon");
    };
    window.addEventListener("mikan:unauthorized", out);
    return () => window.removeEventListener("mikan:unauthorized", out);
  }, []);

  useEffect(() => {
    rawJson<Self>("/api/agent/me")
      .then((m) => {
        setCsrf(m.csrf_token);
        setMe(m);
        setState("ready");
      })
      .catch(() => setState("anon"));
  }, []);

  if (state === "loading") {
    return (
      <div className="grid min-h-screen place-items-center px-4">
        <div className="flex flex-col items-center gap-4">
          <Logo size={48} />
          <Skeleton style={{ height: 16, width: 180 }} />
        </div>
      </div>
    );
  }
  if (state === "anon" || !me) {
    return (
      <LoginPage
        onDone={(m) => {
          setMe(m);
          setState("ready");
        }}
      />
    );
  }
  return <PanelPage me={me} onOut={() => setMe(undefined)} />;
}

function LoginPage({ onDone }: { onDone: (m: Self) => void }) {
  const [login, setLogin] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [wait, setWait] = useState(0);

  useEffect(() => {
    if (wait <= 0) return;
    const timer = window.setTimeout(() => setWait((w) => w - 1), 1000);
    return () => window.clearTimeout(timer);
  }, [wait]);

  const login2 = useMutation({
    mutationFn: (body: { login: string; password: string }) =>
      rawJson<Self>("/api/agent/login", { method: "POST", body: JSON.stringify(body), headers: { "Content-Type": "application/json" } }),
    onSuccess: (m) => {
      setCsrf(m.csrf_token);
      onDone(m);
    },
    onError: (e) => {
      if (e instanceof ApiError && e.status === 401) {
        setError(t("agentPanel.badCredentials"));
        return;
      }
      if (e instanceof ApiError && e.status === 429) {
        setWait(e.retryAfter || 60);
        setError("");
        return;
      }
      setError(errorText(e));
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (wait > 0 || login2.isPending || !login.trim() || !password) return;
    login2.mutate({ login: login.trim(), password });
  };

  return (
    <div className="grid min-h-screen place-items-center px-4 py-12">
      <LangSwitch className="fixed top-4 right-4" />
      <form onSubmit={submit} className="glass-strong w-full max-w-[400px] rounded-2xl p-8" noValidate>
        <div className="mb-6 flex flex-col items-center gap-3 text-center">
          <Logo size={48} />
          <div>
            <h1 className="text-lg font-semibold">{t("agentPanel.loginTitle")}</h1>
            <p className="mt-1 text-[13px] text-[var(--md-on-surface-variant)]">{t("agentPanel.loginSub")}</p>
          </div>
        </div>
        {error ? (
          <div className="banner err mb-4" role="alert">
            {error}
          </div>
        ) : null}
        <div className="flex flex-col gap-3">
          <Field label={t("agentPanel.login")} htmlFor="agent-login">
            <input id="agent-login" className="input mono" autoComplete="username" maxLength={64} value={login} onChange={(e) => setLogin(e.target.value)} />
          </Field>
          <Field label={t("agentPanel.password")} htmlFor="agent-password">
            <input id="agent-password" className="input mono" type="password" autoComplete="current-password" maxLength={256} value={password} onChange={(e) => setPassword(e.target.value)} />
          </Field>
        </div>
        <div className="mt-5 flex justify-center">
          {wait > 0 ? (
            <Pill tone="warn">{t("agentPanel.wait", { s: wait })}</Pill>
          ) : (
            <Button type="submit" variant="primary" loading={login2.isPending} disabled={!login || !password}>
              {t("agentPanel.enter")}
            </Button>
          )}
        </div>
      </form>
    </div>
  );
}

function PanelPage({ me, onOut }: { me: Self; onOut: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [tab, setTab] = useState<AgentTab>("overview");
  const out = useMutation({
    mutationFn: () => rawJson<Record<string, never>>("/api/agent/logout", { method: "POST" }),
    onSuccess: () => {
      setCsrf("");
      qc.clear();
      onOut();
    },
    onError: (e) => toast.error(errorText(e)),
  });
  return (
    <div className="mx-auto flex w-full max-w-[880px] flex-col gap-4 px-4 py-6">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <Logo size={36} />
          <div>
            <h1 className="text-base font-semibold">{t("agentPanel.loginTitle")}</h1>
            <code className="text-xs text-[var(--md-on-surface-variant)]">{me.login}</code>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <LangSwitch />
          <Button size="sm" variant="ghost" loading={out.isPending} onClick={() => out.mutate()} aria-label={t("agentPanel.out")}>
            <LogOut size={16} aria-hidden />
            {t("agentPanel.out")}
          </Button>
        </div>
      </header>
      <Tabs
        id="agent"
        label={t("agentPanel.tabs")}
        tabs={[
          { id: "overview", label: t("agentPanel.overview") },
          { id: "links", label: t("agentPanel.links") },
          { id: "clients", label: t("agentPanel.clients") },
        ]}
        value={tab}
        onChange={(next) => setTab(next)}
      >
        {tab === "overview" ? <OverviewPage /> : tab === "links" ? <LinksPage /> : <ClientsPage />}
      </Tabs>
    </div>
  );
}

function OverviewPage() {
  const st = useQuery({
    queryKey: [...qk.agents, "me", "overview"],
    queryFn: ({ signal }) => rawJson<Overview>("/api/agent/overview", { signal }),
  });
  return (
    <>
      <HeaderCard st={st} />
      <PayoutList />
    </>
  );
}

function HeaderCard({ st }: { st: UseQueryResult<Overview> }) {
  if (st.isError) return <ErrorState text={errorText(st.error)} onRetry={() => void st.refetch()} />;
  if (st.isPending) return <Skeleton style={{ height: 180, borderRadius: 20 }} />;
  const v = st.data;
  const conversion = v!.visits > 0 ? Math.round((v!.bought / v!.visits) * 100) : 0;
  const items: [string, string][] = [
    [t("agentPanel.visits"), num(v!.visits)],
    [t("agentPanel.bought"), num(v!.bought)],
    [t("agentPanel.conversion"), `${conversion}%`],
    [t("agentPanel.revenue"), money(v!.revenue, v!.currency)],
  ];
  return (
    <section className="card glass reveal">
      <div className="mb-3 flex flex-wrap items-center gap-x-6 gap-y-2">
        <BigStat label={t("agentPanel.earned")} value={money(v.earned, v.currency)} />
        <BigStat label={t("agentPanel.owed")} value={money(v.owed, v.currency)} strong />
        <BigStat label={t("agentPanel.paid")} value={money(v.paid, v.currency)} />
      </div>
      <dl className="flex flex-wrap gap-x-5 gap-y-1">
        {items.map(([label, value]) => (
          <div key={label}>
            <dt className="text-[11px] text-[var(--md-on-surface-variant)]">{label}</dt>
            <dd className="num text-[15px] font-semibold">{value}</dd>
          </div>
        ))}
      </dl>
      <p className="mt-3 text-xs text-[var(--md-on-surface-variant)]">{t("agentPanel.moneyNote")}</p>
    </section>
  );
}

function BigStat({ label, value, strong }: { label: string; value: string; strong?: boolean }) {
  return (
    <div>
      <dt className="text-[11px] text-[var(--md-on-surface-variant)]">{label}</dt>
      <dd className={`num ${strong ? "text-xl font-bold" : "text-lg font-semibold"}`}>{value}</dd>
    </div>
  );
}

function PayoutList() {
  const payouts = useQuery({
    queryKey: [...qk.agents, "me", "payouts"],
    queryFn: ({ signal }) => rawJson<{ items: PayoutRow[]; total: number }>("/api/agent/payouts?limit=50", { signal }),
  });
  return (
    <section className="card glass reveal">
      <div className="card-head">
        <h2 className="card-title">{t("agentPanel.payouts")}</h2>
      </div>
      {payouts.isError ? (
        <ErrorState text={errorText(payouts.error)} onRetry={() => void payouts.refetch()} />
      ) : payouts.isPending ? (
        <Skeleton style={{ height: 90 }} />
      ) : (payouts.data?.items ?? []).length === 0 ? (
        <EmptyState title={t("agentPanel.payoutsEmpty")} text="" />
      ) : (
        <ul className="row-list">
          {(payouts.data?.items ?? []).map((p) => (
            <li key={p.id} className="flex items-center justify-between gap-3 py-2">
              <span className="text-[13px]">
                <span className="num font-semibold">{money(p.amount, "RUB")}</span>
                {p.note ? <span className="ml-2 text-xs text-[var(--md-on-surface-variant)]">{p.note}</span> : null}
              </span>
              <span className="text-xs text-[var(--md-on-surface-variant)]">{dateShort(p.created_at)}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function LinksPage() {
  const links = useQuery({
    queryKey: [...qk.agents, "me", "links"],
    queryFn: ({ signal }) => rawJson<{ items: LinkRow[]; total: number }>("/api/agent/links?limit=100", { signal }),
  });
  if (links.isError) return <ErrorState text={errorText(links.error)} onRetry={() => void links.refetch()} />;
  if (links.isPending) return <Skeleton style={{ height: 180, borderRadius: 20 }} />;
  const items = links.data?.items ?? [];
  if (items.length === 0) {
    return (
      <section className="card glass reveal">
        <EmptyState title={t("agentPanel.linksEmpty")} text="" />
      </section>
    );
  }
  return (
    <ul className="flex flex-col gap-3">
      {items.map((l) => (
        <LinkCard key={l.id} l={l} />
      ))}
    </ul>
  );
}

function LinkCard({ l }: { l: LinkRow }) {
  const copyText = useCopy();
  const conversion = l.visits > 0 ? Math.round((l.bought / l.visits) * 100) : 0;
  const title = l.name || l.tag;
  return (
    <li className="card glass reveal">
      <div className="card-head items-start">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="card-title">{title}</h3>
            <code className="pill mono">{l.percent}%</code>
            {l.enabled ? null : <Pill tone="off">{t("agentPanel.offBadge")}</Pill>}
          </div>
          <div className="mt-1 text-xs text-[var(--md-on-surface-variant)]">{dateShort(l.created_at)}</div>
        </div>
      </div>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <span className="text-xs text-[var(--md-on-surface-variant)]">{t("agentPanel.link")}</span>
        {l.link ? (
          <div className="link-field min-w-[240px] flex-1">
            <span className="mono" title={l.link}>
              {l.link}
            </span>
            <button type="button" className="icon-btn" onClick={() => void copyText(l.link!, t("common.linkCopied"))} aria-label={t("common.copyLink")}>
              <Copy size={16} />
            </button>
          </div>
        ) : (
          <span className="text-xs text-[var(--md-on-surface-variant)]">{t("agentPanel.linkOff")}</span>
        )}
      </div>
      <dl className="flex flex-wrap gap-x-6 gap-y-2">
        <Stat label={t("agentPanel.visits")} value={num(l.visits)} />
        <Stat label={t("agentPanel.bought")} value={num(l.bought)} />
        <Stat label={t("agentPanel.renewed")} value={num(l.renewed)} />
        <Stat label={t("agentPanel.conversion")} value={`${conversion}%`} />
        <Stat label={t("agentPanel.revenue")} value={money(l.revenue, "RUB")} />
        <Stat label={t("agentPanel.earned")} value={money(l.earned, "RUB")} />
      </dl>
    </li>
  );
}

function ClientsPage() {
  const clients = useQuery({
    queryKey: [...qk.agents, "me", "clients"],
    queryFn: ({ signal }) => rawJson<{ items: ClientRow[]; total: number }>("/api/agent/referrals?limit=100", { signal }),
  });
  if (clients.isError) return <ErrorState text={errorText(clients.error)} onRetry={() => void clients.refetch()} />;
  if (clients.isPending) return <Skeleton style={{ height: 180, borderRadius: 20 }} />;
  const items = clients.data?.items ?? [];
  return (
    <section className="card glass reveal">
      <div className="card-head">
        <h2 className="card-title">{t("agentPanel.clients")}</h2>
        <span className="num text-sm text-[var(--md-on-surface-variant)]">{num(clients.data?.total ?? 0)}</span>
      </div>
      {items.length === 0 ? (
        <EmptyState title={t("agentPanel.clientsEmpty")} text="" />
      ) : (
        <ul className="row-list">
          {items.map((c) => (
            <li key={c.chat_id} className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 py-2">
              <span className="min-w-0 flex-1 truncate text-[13px]">{c.name || `tg ${c.chat_id}`}</span>
              <code className="pill mono">{c.tag}</code>
              <span className="num text-xs">{t("agentPanel.bought")}: {num(c.bought)} · {t("agentPanel.renewed")}: {num(c.renewed)}</span>
              <span className="num text-xs font-semibold">{money(c.spent, "RUB")}</span>
              <span className="text-xs text-[var(--md-on-surface-variant)]">{dateShort(c.first_seen)}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
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
