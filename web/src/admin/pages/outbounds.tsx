// Outbounds added by a share link: a server of the admin's own (vless, vmess, trojan, ss
// and the rest mihomo reads) that an inbound of any node can leave through, the way it can
// leave through another node. The link is kept as it was pasted and read on the node.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Link2, RefreshCw, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { qk } from "../../api/hooks";
import { Confirm, Drawer, DrawerFooter } from "../../components/overlay";
import { Switch } from "../../components/switch";
import { useToast } from "../../components/toast";
import { Button, EmptyState, ErrorState, Field, PageHeader, Pill, Skeleton } from "../../components/ui";
import { t, tMaybe, type Key } from "../../i18n";
import { ago } from "../../lib/format";
import { ServersTabs } from "../section-tabs";

type Outbound = Schemas["OutboundView"];
type Check = Schemas["OutboundCheck"];

export function OutboundsPage() {
  const qc = useQueryClient();
  const toast = useToast();
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<Outbound | null>(null);
  const [deleting, setDeleting] = useState<Outbound | null>(null);
  const outbounds = useQuery({
    queryKey: qk.outbounds,
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/outbounds", { signal })),
  });

  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/outbounds/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.outbounds });
      setDeleting(null);
      toast.ok(t("outbounds.deleted"));
    },
    onError: (e) => toast.error(errorText(e)),
  });

  const list = outbounds.data?.outbounds ?? [];
  return (
    <>
      <PageHeader
        title={t("outbounds.title")}
        sub={t("outbounds.sub")}
        actions={
          <div className="flex gap-2">
            <Button size="sm" variant="ghost" loading={outbounds.isFetching} onClick={() => void outbounds.refetch()}>
              <RefreshCw size={16} aria-hidden /> {t("outbounds.refresh")}
            </Button>
            <Button size="sm" variant="primary" onClick={() => setAdding(true)}>
              {t("outbounds.add")}
            </Button>
          </div>
        }
      />
      <ServersTabs />
      <p className="mb-4 max-w-[70ch] text-[13px] text-[var(--ink-600)]">{t("outbounds.lead")}</p>

      {outbounds.isError && !list.length ? (
        <ErrorState text={errorText(outbounds.error)} onRetry={() => void outbounds.refetch()} />
      ) : outbounds.isPending ? (
        <Skeleton style={{ height: 160, borderRadius: 16 }} />
      ) : list.length === 0 ? (
        <EmptyState icon={Link2} title={t("outbounds.none")} text={t("outbounds.noneText")}>
          <Button variant="primary" onClick={() => setAdding(true)}>
            {t("outbounds.add")}
          </Button>
        </EmptyState>
      ) : (
        <ul className="row-list card glass reveal">
          {list.map((o) => (
            <li key={o.id} className="flex flex-wrap items-start justify-between gap-3 py-4">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2 text-[13px]">
                  <b>{o.name}</b>
                  <Pill tone="off">{o.kind}</Pill>
                  {o.enabled ? null : <Pill tone="warn">{t("outbounds.isOff")}</Pill>}
                </div>
                <div className="mt-1 break-all font-mono text-xs text-[var(--ink-500)]">{linkTail(o.link)}</div>
                <div className="mt-1 text-xs text-[var(--ink-500)]">
                  {o.uses.length
                    ? t("outbounds.uses", { list: o.uses.map((u) => `${u.inbound} (${u.node || t("outbounds.panelNode")})`).join(", ") })
                    : t("outbounds.usesNone")}
                </div>
                <CheckLine outbound={o} />
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <Switch checked={o.enabled} onChange={() => setEditing(o)} label={t("outbounds.enabled")} />
                <Button size="sm" variant="ghost" onClick={() => setEditing(o)}>
                  {t("outbounds.edit")}
                </Button>
                <Button size="sm" variant="ghost" onClick={() => setDeleting(o)}>
                  <Trash2 size={14} aria-hidden /> {t("common.delete")}
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}

      <p className="mt-4 max-w-[70ch] text-xs text-[var(--ink-500)]">{t("outbounds.hint")}</p>

      {adding ? <EditDrawer outbound={null} onClose={() => setAdding(false)} /> : null}
      {editing ? <EditDrawer outbound={editing} onClose={() => setEditing(null)} /> : null}
      <Confirm
        open={!!deleting}
        onOpenChange={(v) => !v && setDeleting(null)}
        title={t("outbounds.deleteTitle", { name: deleting?.name ?? "" })}
        text={t("outbounds.deleteText")}
        confirm={t("common.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting.id)}
      />
    </>
  );
}

// linkTail is the link without its credentials: what a glance at the list should show.
function linkTail(link: string) {
  const body = link.replace(/^[a-z0-9]+:\/\//i, "");
  const host = body.slice(Math.max(body.lastIndexOf("@"), body.lastIndexOf("://")) + 1).split(/[?#]/)[0];
  return host || link.slice(0, 40);
}

// CheckLine: what a node sees through the outbound. The check runs on a node that carries
// it, so it also says whether the node has received the outbound at all.
function CheckLine({ outbound }: { outbound: Outbound }) {
  const [check, setCheck] = useState<Check | null>(null);
  const run = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/outbounds/{id}/check", { params: { path: { id: outbound.id } } })),
    onSuccess: (v) => setCheck(v),
  });
  const geo = check?.ok ? [check.flag, check.country].filter(Boolean).join(" ") + (check.asn ? ` · AS${check.asn}${check.org ? ` ${check.org}` : ""}` : "") : "";
  return (
    <div className="mt-2 flex flex-wrap items-center gap-2">
      <Button size="sm" variant="ghost" loading={run.isPending} onClick={() => run.mutate()}>
        {t("outbounds.check")}
      </Button>
      {check ? (
        check.ok ? (
          <>
            <Pill tone="ok">{t("outbounds.checkOk", { ip: check.ip ?? "" })}</Pill>
            {geo ? <span className="text-xs text-[var(--ink-500)]">{geo}</span> : null}
            {check.node_id ? <span className="text-xs text-[var(--ink-500)]">· {t("outbounds.checkOn", { node: check.node || `#${check.node_id}` })}</span> : null}
          </>
        ) : (
          <>
            <Pill tone="warn">{tMaybe(`outbounds.checkCode.${check.code}` as Key) ?? t("outbounds.checkCode.error")}</Pill>
            {check.error ? <span className="text-xs text-[var(--ink-500)]">{check.error}</span> : null}
          </>
        )
      ) : null}
    </div>
  );
}

// EditDrawer adds an outbound or changes one: its link, its name, and whether it is on.
function EditDrawer({ outbound, onClose }: { outbound: Outbound | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [link, setLink] = useState(outbound?.link ?? "");
  const [name, setName] = useState(outbound?.name ?? "");
  const [enabled, setEnabled] = useState(outbound?.enabled ?? true);
  const save = useMutation({
    mutationFn: () =>
      outbound
        ? unwrap(api.PATCH("/api/v1/outbounds/{id}", { params: { path: { id: outbound.id } }, body: { link, name, enabled } }))
        : unwrap(api.POST("/api/v1/outbounds", { body: { link, name } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.outbounds });
      toast.ok(t("outbounds.saved"));
      onClose();
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (link.trim()) save.mutate();
  };
  return (
    <Drawer
      open
      onOpenChange={(v) => !v && onClose()}
      title={outbound ? t("outbounds.editTitle", { name: outbound.name }) : t("outbounds.addTitle")}
      meta={outbound ? outbound.kind : undefined}
      lead={t("outbounds.lead")}
      footer={
        <DrawerFooter form="outbound-form" onCancel={onClose} loading={save.isPending} disabled={!link.trim()}>
          {t("common.save")}
        </DrawerFooter>
      }
    >
      <form id="outbound-form" className="pt-5" onSubmit={submit}>
        {save.isError ? <div className="banner err mb-4">{errorText(save.error)}</div> : null}
        <Field label={t("outbounds.link")} htmlFor="outbound-link" hint={t("outbounds.linkHint")}>
          <textarea
            id="outbound-link"
            className="input min-h-[72px] font-mono text-xs"
            value={link}
            onChange={(e) => setLink(e.target.value)}
            placeholder="vless://…"
            spellCheck={false}
            required
          />
        </Field>
        <Field label={t("outbounds.name")} htmlFor="outbound-name" hint={t("outbounds.nameHint")}>
          <input id="outbound-name" className="input" value={name} onChange={(e) => setName(e.target.value)} maxLength={64} placeholder={t("outbounds.namePlaceholder")} />
        </Field>
        {outbound ? (
          <Field label={t("outbounds.enabled")} hint={t("outbounds.enabledHint")}>
            <Switch checked={enabled} onChange={setEnabled} label={t("outbounds.enabled")} />
          </Field>
        ) : null}
        {outbound ? (
          <div className="mt-2 text-xs text-[var(--ink-500)]">
            <ArrowRight size={12} aria-hidden className="mr-1 inline" />
            {t("outbounds.created", { at: ago(outbound.created_at) })}
          </div>
        ) : null}
      </form>
    </Drawer>
  );
}
