// The support's page: one subscriber's card and the three things the role may do — the
// bonus days, the sessions (a new link, every device unbound) and the block. The search
// takes an id, an @username or a payment's number.
import { useMutation } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { useToast } from "../../components/toast";
import { Button, Field, PageHeader, Pill } from "../../components/ui";
import { t, type Key } from "../../i18n";
import { bytes } from "../../lib/format";

type Card = Schemas["SupportCard"];

export function ClientPage() {
  const toast = useToast();
  const [query, setQuery] = useState("");
  const [card, setCard] = useState<Card | null>(null);
  const find = useMutation({
    mutationFn: (q: string) => unwrap(api.GET("/api/v1/support/card", { params: { query: { q } } })),
    onSuccess: setCard,
    onError: (e) => {
      setCard(null);
      toast.error(errorText(e));
    },
  });
  const act = useMutation({
    mutationFn: async (kind: "days" | "sessions" | "blocked") => {
      if (!card) throw new Error("no card");
      const path = { id: card.id };
      if (kind === "days") return unwrap(api.POST("/api/v1/support/card/{id}/days", { params: { path }, body: { days: 3 } }));
      if (kind === "sessions") return unwrap(api.POST("/api/v1/support/card/{id}/sessions", { params: { path } }));
      return unwrap(api.POST("/api/v1/support/card/{id}/blocked", { params: { path }, body: { blocked: !card.blocked } }));
    },
    onSuccess: (c) => {
      setCard(c);
      toast.ok(t("client.done"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (query.trim()) find.mutate(query.trim());
  };
  return (
    <>
      <PageHeader title={t("client.title")} sub={t("client.sub")} />
      <section className="card glass reveal">
        <form onSubmit={submit} className="mb-4 flex flex-wrap items-end gap-2">
          <div className="min-w-0 flex-1">
            <Field label={t("client.search")} htmlFor="sc-q" hint={t("client.searchHint")}>
              <input id="sc-q" className="input" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="12345 · @username · 678" />
            </Field>
          </div>
          <Button type="submit" variant="primary" loading={find.isPending}>
            <Search size={18} aria-hidden /> {t("client.find")}
          </Button>
        </form>
        {card ? <SupportCardView c={card} busy={act.isPending} onAct={(k) => act.mutate(k)} /> : null}
      </section>
    </>
  );
}

function stateLabel(c: Card): string {
  return t(`client.state.${c.state}` as Key);
}

function SupportCardView({ c, busy, onAct }: { c: Card; busy: boolean; onAct: (k: "days" | "sessions" | "blocked") => void }) {
  return (
    <div className="rounded-2xl border border-[var(--hairline)] p-4">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <div className="text-[15px] font-semibold">
            {c.name} · <span className="mono">#{c.id}</span>
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-[var(--md-on-surface-variant)]">
            <Pill tone={c.blocked ? "off" : c.state === "active" ? "ok" : "off"}>{stateLabel(c)}</Pill>
            {c.tariff ? <span>{c.tariff}</span> : null}
            {c.tg_username ? <span className="mono">@{c.tg_username}</span> : null}
          </div>
        </div>
        {c.blocked ? <Pill tone="off">{t("client.blocked")}</Pill> : null}
      </div>
      <dl className="mt-4 grid grid-cols-2 gap-3 text-xs text-[var(--md-on-surface-variant)] sm:grid-cols-4">
        <Item label={t("client.until")} value={c.expires_at ? new Date(c.expires_at).toLocaleDateString() : t("client.forever")} />
        <Item label={t("client.traffic")} value={c.traffic_limit != null ? `${bytes(c.traffic_used)} / ${bytes(c.traffic_limit)}` : `${bytes(c.traffic_used)}`} />
        <Item label={t("client.devices")} value={`${c.devices}${c.device_limit != null ? " / " + c.device_limit : ""}`} />
        {c.contact ? <Item label={t("client.contact")} value={c.contact} /> : null}
      </dl>
      <div className="mt-4 flex flex-wrap gap-2">
        <Button size="sm" loading={busy} onClick={() => onAct("days")}>➕ {t("client.addDays")}</Button>
        <Button size="sm" variant="glass" loading={busy} onClick={() => onAct("sessions")}>🔄 {t("client.sessions")}</Button>
        <Button size="sm" variant={c.blocked ? "glass" : "danger"} loading={busy} onClick={() => onAct("blocked")}>
          {c.blocked ? "✅ " + t("client.unblock") : "🚫 " + t("client.block")}
        </Button>
      </div>
      {c.traffic_limit != null && c.traffic_used > 0 ? (
        <p className="mt-3 text-xs text-[var(--md-on-surface-variant)]">{t("client.trafficNote", { used: bytes(c.traffic_used), left: bytes(Math.max(0, c.traffic_limit - c.traffic_used)) })}</p>
      ) : null}
    </div>
  );
}

function Item({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd className="mt-0.5 text-sm font-medium text-[var(--md-on-surface)]">{value}</dd>
    </div>
  );
}
