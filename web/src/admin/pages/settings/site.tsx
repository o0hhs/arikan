import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Check, ExternalLink, FileCode2, Gamepad2, Wrench, XCircle } from "lucide-react";
import { api, errorText, unwrap, type Schemas } from "../../../api/client";
import { qk, useNodes } from "../../../api/hooks";
import { useToast } from "../../../components/toast";
import { Segmented } from "../../../components/ui";
import { t } from "../../../i18n";
import { CardHead } from "../../../components/layout";
import { commonPage } from "../../../lib/front-page";
import { nodeLabel } from "../../../lib/node-label";

type Site = Schemas["SettingsView"]["site_template"];
type Node = Schemas["NodeInfo"];

const OPTIONS: { id: Site; icon: typeof Wrench }[] = [
  { id: "", icon: XCircle },
  { id: "maintenance", icon: Wrench },
  { id: "cats", icon: Gamepad2 },
];

/**
 * What a stranger sees instead of a bare 404: at the panel's address, where the admin's own
 * files in data/panel/www win over any page, and on each node (its protocols on XHTTP,
 * WebSocket or gRPC, and its own domain behind the shared 443). The tiles choose one page for
 * the panel and every node at once, the rows one for each of them.
 */
export function SiteCard({ s }: { s: Schemas["SettingsView"] }) {
  const qc = useQueryClient();
  const toast = useToast();
  const nodes = useNodes();
  const list = nodes.data ?? [];
  // The targets that have another page get it, all at once: the panel (null) and the nodes.
  // Those that fail are named with the first reason; the others stay saved.
  const save = useMutation({
    mutationFn: async ({ targets, page }: { targets: (Node | null)[]; page: Site }) => {
      const done = await Promise.allSettled(
        targets.map((n) =>
          n
            ? unwrap(api.PATCH("/api/v1/nodes/{id}", { params: { path: { id: n.id } }, body: { front_page: page } }))
            : unwrap(api.PATCH("/api/v1/settings", { body: { site_template: page } })).then((v) => qc.setQueryData(qk.settings, v)),
        ),
      );
      const failed = targets.filter((_, i) => done[i]?.status === "rejected");
      const first = done.find((d): d is PromiseRejectedResult => d.status === "rejected");
      if (first) {
        throw new Error(t("settings.site.failed", { names: failed.map((n) => (n ? nodeLabel(n) : t("settings.site.panel"))).join(", "), error: errorText(first.reason) }));
      }
    },
    onSuccess: () => toast.ok(t("settings.saved")),
    onError: (e) => toast.error(e.message),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: qk.nodes });
      void qc.invalidateQueries({ queryKey: qk.settings });
    },
  });
  const current = nodes.isSuccess ? commonPage([s.site_template, ...list.map((n) => n.front_page)]) : null;
  const hint = !nodes.isSuccess ? null : current == null ? t("settings.site.mixed") : t(`settings.site.hint.${current || "none"}`);
  const toAll = (page: Site) => {
    const targets: (Node | null)[] = [...(s.site_template !== page ? [null] : []), ...list.filter((n) => n.front_page !== page)];
    if (targets.length > 0) save.mutate({ targets, page });
  };
  // The page sits at the root of the panel's address, where the admin path is not.
  const root = s.admin_url ? new URL("/", s.admin_url).toString() : "";
  return (
    <section className="card glass reveal" style={{ "--i": 2 } as React.CSSProperties} aria-busy={save.isPending}>
      <CardHead title={t("settings.site.title")} sub={t("settings.site.sub")} />
      {s.site_own ? (
        <div className="banner info mb-3" role="status">
          <FileCode2 size={18} className="shrink-0" aria-hidden />
          <span>{t("settings.site.own")}</span>
        </div>
      ) : null}
      <p id="site-all" className="mb-2 text-[13px] font-semibold text-[var(--ink-700)]">
        {t("settings.site.forAll")}
      </p>
      <div role="radiogroup" aria-labelledby="site-all" className="theme-tiles">
        {OPTIONS.map((o) => {
          const checked = current === o.id;
          const Icon = o.icon;
          return (
            <button
              key={o.id || "none"}
              type="button"
              role="radio"
              aria-checked={checked}
              className="theme-tile"
              disabled={save.isPending || !nodes.isSuccess}
              onClick={() => !checked && toAll(o.id)}
            >
              <span className="grid h-16 place-items-center rounded-xl bg-[var(--surface)] text-[var(--ink-500)]" aria-hidden>
                <Icon size={24} />
              </span>
              <span className="theme-tile-name">
                <span>{t(`settings.site.option.${o.id || "none"}`)}</span>
                {checked ? <Check size={16} aria-hidden /> : null}
              </span>
            </button>
          );
        })}
      </div>
      {hint ? <p className="mt-3 text-xs text-[var(--ink-500)]">{hint}</p> : null}
      <h3 className="mt-6 text-[13px] font-semibold text-[var(--ink-700)]">{t("settings.site.panelOwn")}</h3>
      <ul className="row-list">
        <SiteRow name={t("settings.site.panel")} value={s.site_template} disabled={save.isPending} onChange={(page) => save.mutate({ targets: [null], page })} />
      </ul>
      <p className="mt-3 text-xs text-[var(--ink-500)]">{t("settings.site.nodeEditHint")}</p>
      <p className="mt-3 text-xs text-[var(--ink-500)]">{t("settings.site.nodeHint")}</p>
      {root ? (
        <a className="mt-3 inline-flex items-center gap-2 text-[13px] font-medium text-[var(--mikan-700)] hover:underline" href={root} target="_blank" rel="noreferrer">
          <ExternalLink size={16} aria-hidden /> {t("settings.site.open")}
        </a>
      ) : null}
    </section>
  );
}

/** The page of one target, saved as soon as it is pressed. */
function SiteRow({ name, value, disabled, onChange }: { name: string; value: Site; disabled: boolean; onChange: (page: Site) => void }) {
  const shown = value || "none";
  const options = OPTIONS.map((o) => ({ value: o.id || "none", label: t(`settings.site.short.${o.id || "none"}`) }));
  return (
    <li className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 py-3">
      <div className="min-w-[min(100%,160px)] flex-1">
        <div className="text-[13px] font-semibold">{name}</div>
      </div>
      <Segmented
        label={t("settings.site.pageOf", { name })}
        value={shown}
        options={options}
        disabled={disabled}
        onChange={(v) => v !== shown && onChange(v === "none" ? "" : (v as Site))}
      />
    </li>
  );
}
