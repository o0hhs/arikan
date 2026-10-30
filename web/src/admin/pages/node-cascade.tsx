// Cascades: client → this node → another node → internet. Inbounds pick their exit in
// their own settings; here a node shows where it sends traffic, who comes through it, and
// where the traffic other nodes relay through it goes next.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, RefreshCw } from "lucide-react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { qk, useNodes } from "../../api/hooks";
import { Drawer } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { useToast } from "../../components/toast";
import { Button, Field, Pill, Segmented, Skeleton } from "../../components/ui";
import { t, tMaybe } from "../../i18n";
import { useDraft } from "../../lib/draft";
import { fieldErrors } from "../../lib/fields";
import { ago } from "../../lib/format";
import { nodeLabel } from "../../lib/node-label";

type Cascade = Schemas["CascadeView"];
type Route = "direct" | "warp" | "node";

export function CascadeDrawer({ node, onClose, onWarp }: { node: { id: number; name: string } | null; onClose: () => void; onWarp?: () => void }) {
  const cascade = useQuery({
    queryKey: qk.cascade(node?.id ?? 0),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/nodes/{id}/cascade", { params: { path: { id: node!.id } }, signal })),
    enabled: !!node,
  });
  return (
    <Drawer open={!!node} onOpenChange={(v) => !v && onClose()} title={t("cascade.title")} meta={node?.name}>
      <div className="pt-5">
        <p className="mb-4 text-[13px] text-[var(--md-on-surface-variant)]">{t("cascade.intro")}</p>
        <QueryBoundary query={cascade} pending={<Skeleton style={{ height: 240, borderRadius: 16 }} />}>
          {(c) => <Body key={node!.id} nodeId={node!.id} c={c} refetch={() => void cascade.refetch()} checking={cascade.isFetching} onWarp={onWarp} />}
        </QueryBoundary>
      </div>
    </Drawer>
  );
}

function Body({ nodeId, c, refetch, checking, onWarp }: { nodeId: number; c: Cascade; refetch: () => void; checking: boolean; onWarp?: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const nodes = useNodes();
  const name = (id: number) => {
    const n = nodes.data?.find((x) => x.id === id);
    return n ? nodeLabel(n) : `#${id}`;
  };
  // "Check" refetches `c`: the chosen route survives it.
  const {
    draft: { route, exit },
    setDraft,
  } = useDraft<{ route: Route; exit: number }>({ route: (c.relay?.outbound as Route) ?? "direct", exit: c.relay?.exit_node_id ?? 0 });
  const setRoute = (v: Route) => setDraft((d) => ({ ...d, route: v }));
  const setExit = (v: number) => setDraft((d) => ({ ...d, exit: v }));
  const save = useMutation({
    mutationFn: () =>
      unwrap(api.PATCH("/api/v1/nodes/{id}/cascade", { params: { path: { id: nodeId } }, body: { outbound: route, ...(route === "node" ? { exit_node_id: exit } : {}) } })),
    onSuccess: (v) => {
      qc.setQueryData(qk.cascade(nodeId), v);
      toast.ok(t("cascade.saved"));
    },
  });
  const fields = fieldErrors(save.error);
  const changed = route !== ((c.relay?.outbound as Route) ?? "direct") || (route === "node" && exit !== (c.relay?.exit_node_id ?? 0));

  return (
    <>
      {c.tunnel ? <TunnelSection tunnel={c.tunnel} /> : null}
      <section className="mb-5">
        <div className="mb-2 flex items-center justify-between gap-2">
          <h3 className="text-[13px] font-semibold">{t("cascade.exits")}</h3>
          {c.exits.length ? (
            <Button size="sm" variant="ghost" loading={checking} onClick={refetch}>
              <RefreshCw size={14} aria-hidden /> {t("cascade.check")}
            </Button>
          ) : null}
        </div>
        {c.exits.length === 0 ? (
          <p className="text-xs text-[var(--md-on-surface-variant)]">{t("cascade.exitsNone")}</p>
        ) : (
          <ul className="row-list">
            {c.exits.map((e) => (
              <li key={`${e.node_id}-${e.via}`} className="py-3">
                <div className="flex flex-wrap items-center gap-2 text-[13px]">
                  <ArrowRight size={14} aria-hidden className="text-[var(--ink-400)]" />
                  <b>{e.name || name(e.node_id)}</b>
                  {e.via === "warp" ? <Pill tone="warn">{t("cascade.viaWarp")}</Pill> : null}
                  {e.tunnel?.reserve ? <Pill tone="off">{t("cascade.tunnel.reserve")}</Pill> : null}
                  {e.tunnel && !e.tunnel.reserve ? (
                    <Pill tone={e.tunnel.connected ? "ok" : "bad"}>{t(e.tunnel.connected ? "cascade.tunnel.inTunnel" : "cascade.tunnel.notInTunnel")}</Pill>
                  ) : null}
                  {e.probe ? (
                    e.probe.ok ? (
                      <Pill tone="ok">{t("cascade.exitIP", { ip: e.probe.ip ?? "" })}</Pill>
                    ) : (
                      <Pill tone="bad">{t("cascade.down")}</Pill>
                    )
                  ) : (
                    <Pill tone="off">{t("cascade.unknown")}</Pill>
                  )}
                </div>
                <div className="mt-1 text-xs text-[var(--md-on-surface-variant)]">
                  {[...e.inbounds, ...(e.relay ? [t("cascade.relayed")] : [])].join(", ")}
                  {e.probe ? ` · ${ago(e.probe.checked_at)}` : ""}
                </div>
                {/* The chain the rows give, not the setting: what the traffic really does. */}
                <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs">
                  <span className="text-[var(--md-on-surface-variant)]">{t("cascade.then")}</span>
                  <Then chain={e.chain} end={e.end} warp={e.warp} name={name} />
                  {e.end === "warp" && e.warp?.status ? <span className="text-[var(--md-on-surface-variant)]">· {ago(e.warp.status.checked_at)}</span> : null}
                </div>
                {e.end === "warp" && e.warp?.status && !e.warp.status.ok && e.warp.status.error !== "node_unreachable" ? (
                  <p className="mono mt-1 break-words text-xs text-[var(--md-on-surface-variant)]">
                    {tMaybe(`warp.why.${e.warp.status.error ?? "failed"}`, { endpoint: "" }) ?? e.warp.status.detail ?? ""}
                  </p>
                ) : null}
              </li>
            ))}
          </ul>
        )}
        <p className="mt-2 text-xs text-[var(--md-on-surface-variant)]">{t("cascade.exitsHint")}</p>
      </section>

      <section className="border-t border-[var(--hairline)] pt-4">
        <h3 className="mb-1 text-[13px] font-semibold">{t("cascade.relay")}</h3>
        <p className="mb-3 text-xs text-[var(--md-on-surface-variant)]">
          {c.relay && c.relay.sources.length
            ? t("cascade.sources", { list: c.relay.sources.map((s) => s.name || name(s.node_id)).join(", "), port: c.relay.port })
            : t("cascade.sourcesNone")}
        </p>
        {c.relay ? <RelayListener relay={c.relay} /> : null}
        {c.relay && c.relay.warp_users > 0 ? (
          <p className="mb-3 text-xs text-[var(--md-on-surface-variant)]">{t("cascade.warpUsers", { n: c.relay.warp_users })}</p>
        ) : null}
        {save.error && !Object.keys(fields).length ? <div className="banner err mb-4" role="alert">{errorText(save.error)}</div> : null}
        <Field label={t("cascade.route")} hint={t(`cascade.routeHint.${route}`)} error={fields.outbound ?? fields.exit_node_id}>
          <Segmented
            label={t("cascade.route")}
            value={route}
            onChange={setRoute}
            options={[
              { value: "direct", label: t("inbounds.outboundDirect") },
              { value: "warp", label: "WARP" },
              { value: "node", label: t("inbounds.outboundNode") },
            ]}
          />
          {route === "node" ? (
            <select className="input mt-2 max-w-[320px]" value={exit} onChange={(e) => setExit(Number(e.target.value))} aria-label={t("inbounds.exitNode")}>
              <option value={0} disabled>
                {t("inbounds.exitPick")}
              </option>
              {(nodes.data ?? [])
                .filter((n) => n.id !== nodeId)
                .map((n) => (
                  <option key={n.id} value={n.id} disabled={!n.enabled}>
                    {nodeLabel(n)}
                  </option>
                ))}
            </select>
          ) : null}
        </Field>
        {/* WARP is the way out, not a second setting: without it the route is refused, and
            the button opens the node's own WARP page instead of saving a route to nowhere. */}
        {route === "warp" && (!c.warp.configured || !c.warp.enabled) ? (
          <div className="banner warn mb-4" role="status">
            <div>
              <div>{t(!c.warp.configured ? "cascade.warp.setupText" : "cascade.warp.offText")}</div>
              {onWarp ? (
                <Button size="sm" className="mt-2" onClick={onWarp}>
                  {t("cascade.warp.open")}
                </Button>
              ) : null}
            </div>
          </div>
        ) : null}
        {route === "warp" && c.relay?.warp?.enabled && c.relay.warp.status && !c.relay.warp.status.ok && c.relay.warp.status.error !== "node_unreachable" ? (
          <div className="banner warn mb-4" role="status">
            <div>
              <div>{t("cascade.warp.downText")}</div>
              <p className="mono mt-1 break-words text-xs opacity-80">
                {tMaybe(`warp.why.${c.relay.warp.status.error ?? "failed"}`, { endpoint: "" }) ?? c.relay.warp.status.detail ?? ""}
              </p>
            </div>
          </div>
        ) : null}
        {/* The saved chain, as the rows give it: this node and where its traffic leaves. */}
        {c.relay ? (
          <div className="mb-4 flex flex-wrap items-center gap-1.5 text-xs">
            <span className="text-[var(--md-on-surface-variant)]">{t("cascade.chainThis")}</span>
            <b className="font-semibold">{name(nodeId)}</b>
            <Then chain={c.relay.chain} end={c.relay.end} warp={c.relay.warp} name={name} />
            {c.relay.end === "warp" && c.relay.warp?.status ? <span className="text-[var(--md-on-surface-variant)]">· {ago(c.relay.warp.status.checked_at)}</span> : null}
          </div>
        ) : null}
        <div className="form-actions">
          <Button variant="primary" loading={save.isPending} disabled={!changed || (route === "node" && !exit)} onClick={() => save.mutate()}>
            {t("common.save")}
          </Button>
        </div>
      </section>
    </>
  );
}

// Then is what an exit does with the traffic after itself: the nodes it passes it on to,
// and where it leaves the internet. WARP is shown with its own state, because a route to
// a WARP that is missing or down carries nothing.
function Then({ chain, end, warp, name }: { chain: { node_id: number; name: string }[]; end: string; warp?: Schemas["CascadeWarp"]; name: (id: number) => string }) {
  return (
    <>
      {chain.map((h) => (
        <span key={h.node_id} className="inline-flex items-center gap-1.5">
          <ArrowRight size={11} aria-hidden className="text-[var(--ink-400)]" />
          <b className="font-semibold">{h.name || name(h.node_id)}</b>
        </span>
      ))}
      <ArrowRight size={11} aria-hidden className="text-[var(--ink-400)]" />
      {end === "warp" ? <WarpPill w={warp} /> : <span className="text-[var(--md-on-surface-variant)]">{t("cascade.endDirect")}</span>}
    </>
  );
}

// WarpPill is the WARP a chain ends in: the address sites see, or the reason it carries
// nothing. A node without WARP is named so — the panel refuses to save that route.
function WarpPill({ w }: { w?: Schemas["CascadeWarp"] }) {
  if (!w || !w.configured) return <Pill tone="bad">{t("cascade.warp.missing")}</Pill>;
  if (!w.enabled) return <Pill tone="bad">{t("cascade.warp.off")}</Pill>;
  const s = w.status;
  if (!s || s.error === "node_unreachable") return <Pill tone="off">{t("cascade.warp.unknown")}</Pill>;
  if (!s.ok) return <Pill tone="bad">{t("cascade.warp.down")}</Pill>;
  return (
    <Pill tone="ok">
      {s.warp === "plus" ? "WARP+" : "WARP"} · {s.ip}
    </Pill>
  );
}

// TunnelSection is the cascade over the panel's tunnel as this node stands in it: the
// bridge the panel holds to an entry, and the stream an exit keeps to the panel. Without
// either, the inbounds behind the cascade fail — nothing is dialled directly.
function TunnelSection({ tunnel }: { tunnel: NonNullable<Cascade["tunnel"]> }) {
  return (
    <section className="mb-5">
      <h3 className="mb-2 text-[13px] font-semibold">{t("cascade.tunnel.title")}</h3>
      <div className="flex flex-col gap-2">
        {tunnel.entry ? (
          <div className="flex flex-wrap items-center gap-2 text-[13px]">
            <Pill tone={tunnel.bridge ? "ok" : "bad"}>{t(tunnel.bridge ? "cascade.tunnel.bridgeOn" : "cascade.tunnel.bridgeOff")}</Pill>
            <span className="text-xs text-[var(--md-on-surface-variant)]">{t("cascade.tunnel.entry")}</span>
          </div>
        ) : null}
        {tunnel.exit ? (
          <div className="flex flex-wrap items-center gap-2 text-[13px]">
            <Pill tone={tunnel.online ? "ok" : "bad"}>{t(tunnel.online ? "cascade.tunnel.exitOnline" : "cascade.tunnel.exitOffline")}</Pill>
            <span className="text-xs text-[var(--md-on-surface-variant)]">{t("cascade.tunnel.exit")}</span>
          </div>
        ) : null}
      </div>
      <p className="mt-2 text-xs text-[var(--md-on-surface-variant)]">{t("cascade.tunnel.hint")}</p>
    </section>
  );
}

// The relay's listener on the node. A port another program holds keeps it from starting,
// and the nodes that leave through it then reach that program instead.
function RelayListener({ relay }: { relay: NonNullable<Cascade["relay"]> }) {
  const state = relay.listener;
  if (state === "unknown") return null;
  return (
    <div className="mb-3">
      <Pill tone={state === "ok" ? "ok" : "bad"}>{t(`cascade.listener.${state}`, { port: relay.port })}</Pill>
      {state === "ok" ? null : (
        <div className="banner err mt-2" role="alert">
          <div>
            {t(state === "busy" ? "cascade.listener.busyText" : "cascade.listener.failedText", { port: relay.port })}
            {relay.error ? <div className="mt-1 break-all font-mono text-xs opacity-80">{relay.error}</div> : null}
          </div>
        </div>
      )}
    </div>
  );
}
