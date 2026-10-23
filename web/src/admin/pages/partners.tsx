// Partners: the franchise's books. A partner's bot has a code its subscribers come to the
// panel's bot with (/start partner_<code>); every paid order of such an account credits the
// partner the percentage kept here. Balances are in kopecks, the ledger is the bot's own.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { QueryBoundary } from "../../components/query";
import { useToast } from "../../components/toast";
import { Button, Field, PageHeader, Pill, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { rubles } from "../../lib/format";

type Partner = Schemas["PartnerView"];
type PartnerBot = Schemas["PartnerBotView"];

const qk = { partners: ["partners"] as const };

function usePartners() {
  return useQuery({ queryKey: qk.partners, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/partners", { signal })) });
}

export function PartnersPage() {
  const partners = usePartners();
  const qc = useQueryClient();
  const toast = useToast();
  const [name, setName] = useState("");
  const [note, setNote] = useState("");
  const create = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/partners", { body: { name: name.trim(), note: note.trim() } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.partners });
      setName("");
      setNote("");
      toast.ok(t("partners.created"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (name.trim()) create.mutate();
  };
  return (
    <>
      <PageHeader title={t("nav.partners")} sub={t("partners.sub")} />
      <section className="card glass reveal">
        <form onSubmit={submit} className="mb-4 grid gap-3 sm:grid-cols-[1fr_2fr_auto] sm:items-end">
          <Field label={t("partners.name")} htmlFor="p-name">
            <input id="p-name" className="input" value={name} onChange={(e) => setName(e.target.value)} maxLength={120} placeholder={t("partners.namePlaceholder")} />
          </Field>
          <Field label={t("partners.note")} htmlFor="p-note">
            <input id="p-note" className="input" value={note} onChange={(e) => setNote(e.target.value)} maxLength={200} />
          </Field>
          <Button type="submit" variant="primary" loading={create.isPending} disabled={!name.trim()}>
            <Plus size={18} aria-hidden /> {t("partners.add")}
          </Button>
        </form>
        <QueryBoundary query={partners} pending={<Skeleton style={{ height: 64 }} />}>
          {(list) =>
            list.length === 0 ? (
              <p className="text-xs text-[var(--md-on-surface-variant)]">{t("partners.none")}</p>
            ) : (
              <ul className="row-list">
                {list.map((p) => (
                  <PartnerCard key={p.id} p={p} onChanged={() => void qc.invalidateQueries({ queryKey: qk.partners })} />
                ))}
              </ul>
            )
          }
        </QueryBoundary>
      </section>
    </>
  );
}

function PartnerCard({ p, onChanged }: { p: Partner; onChanged: () => void }) {
  const toast = useToast();
  const [botName, setBotName] = useState("");
  const [token, setToken] = useState("");
  const [percent, setPercent] = useState("10");
  const addBot = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/partners/{id}/bots", { params: { path: { id: p.id } }, body: { bot_username: botName.trim(), bot_token: token.trim(), commission_percent: Number(percent), enabled: true } })),
    onSuccess: () => {
      setBotName("");
      setToken("");
      onChanged();
      toast.ok(t("partners.botAdded"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const saveBot = useMutation({
    mutationFn: (b: PartnerBot) => unwrap(api.PUT("/api/v1/partners/bots/{bot}", { params: { path: { bot: b.id } }, body: { bot_username: b.bot_username, bot_token: "", commission_percent: b.commission_percent, enabled: !b.enabled } })),
    onSuccess: onChanged,
    onError: (e) => toast.error(errorText(e)),
  });
  const removeBot = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/partners/bots/{bot}", { params: { path: { bot: id } } })),
    onSuccess: onChanged,
    onError: (e) => toast.error(errorText(e)),
  });
  const remove = useMutation({
    mutationFn: () => unwrap(api.DELETE("/api/v1/partners/{id}", { params: { path: { id: p.id } } })),
    onSuccess: onChanged,
    onError: (e) => toast.error(errorText(e)),
  });
  return (
    <li className="py-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="text-[13px] font-semibold">{p.name}</div>
          <div className="text-xs text-[var(--md-on-surface-variant)]">
            {p.note ? p.note + " · " : ""}
            {t("partners.balance")}: <span className="num">{rubles(p.balance_kop)}</span>
          </div>
        </div>
        <Button size="sm" variant="ghost" onClick={() => remove.mutate()} loading={remove.isPending}>
          {t("partners.remove")}
        </Button>
      </div>
      <ul className="mt-2 flex flex-col gap-2">
        {p.bots.map((b) => (
          <li key={b.id} className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-[var(--hairline)] p-3 text-xs">
            <div>
              <div className="text-[13px] font-medium">
                {b.bot_username ? "@" + b.bot_username : t("partners.botNoName")} · <span className="mono">/start partner_{b.code}</span>
              </div>
              <div className="text-[var(--md-on-surface-variant)]">
                {b.commission_percent}% · {t("partners.turnover")}: <span className="num">{rubles(b.turnover_kop)}</span>
              </div>
            </div>
            <div className="flex items-center gap-2">
              {b.enabled ? <Pill tone="ok">{t("partners.on")}</Pill> : <Pill tone="off">{t("partners.off")}</Pill>}
              <Button size="sm" variant="ghost" onClick={() => saveBot.mutate(b)}>{b.enabled ? t("partners.disable") : t("partners.enable")}</Button>
              <Button size="sm" variant="ghost" onClick={() => removeBot.mutate(b.id)}>{t("partners.remove")}</Button>
            </div>
          </li>
        ))}
      </ul>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          addBot.mutate();
        }}
        className="mt-2 grid gap-2 sm:grid-cols-[1fr_2fr_90px_auto] sm:items-end"
      >
        <input className="input" value={botName} onChange={(e) => setBotName(e.target.value)} placeholder={t("partners.botUsername")} maxLength={64} />
        <input className="input" value={token} onChange={(e) => setToken(e.target.value)} placeholder={t("partners.botToken")} maxLength={200} />
        <input className="input" value={percent} onChange={(e) => setPercent(e.target.value)} placeholder="10" inputMode="numeric" maxLength={3} />
        <Button type="submit" size="sm" loading={addBot.isPending}>{t("partners.addBot")}</Button>
      </form>
    </li>
  );
}
