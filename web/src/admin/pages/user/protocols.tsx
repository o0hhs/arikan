// Which node with which protocol the subscription actually used: each node remembers the
// inbound that last let each device in, and the card shows that per node, beside the
// traffic the node carried.
import { errorText, type User } from "../../../api/client";
import { useUserProtocols } from "../../../api/hooks";
import { ErrorState, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { ago, bytes } from "../../../lib/format";
import { Section } from "./section";

export function ProtocolsUsageSection({ u }: { u: User }) {
  const usage = useUserProtocols(u.id);
  const nodes = usage.data?.nodes ?? [];
  return (
    <Section title={t("userDrawer.usage.title")} aside={t("userDrawer.usage.window")}>
      {usage.isPending ? (
        <Skeleton style={{ height: 72, borderRadius: 16 }} />
      ) : usage.data === undefined ? (
        <ErrorState text={errorText(usage.error)} onRetry={() => void usage.refetch()} />
      ) : nodes.length === 0 ? (
        <p className="text-[13px] text-[var(--md-on-surface-variant)]">{t("userDrawer.usage.empty")}</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {nodes.map((n) => (
            <li key={n.node_id} className="panel-soft p-3">
              <div className="flex items-baseline justify-between gap-2">
                <span className="truncate text-[13px] font-medium">{n.public_name || n.name || t("nodes.kindLocal")}</span>
                <span className="num shrink-0 text-[11px] text-[var(--md-on-surface-variant)]">{bytes(n.bytes)}</span>
              </div>
              {n.last_seen ? (
                <div className="mt-0.5 text-[11px] text-[var(--md-on-surface-variant)]">
                  {t("userDrawer.nodeSeen", { ago: ago(n.last_seen) })}
                </div>
              ) : null}
              {n.protocols.length === 0 ? (
                <p className="mt-1 text-xs text-[var(--md-on-surface-variant)]">{t("userDrawer.usage.nodeIdle")}</p>
              ) : (
                <ul className="mt-2 flex flex-col gap-1.5">
                  {n.protocols.map((p) => (
                    <li key={p.preset} className="flex items-baseline justify-between gap-3 text-[13px]">
                      <span className="min-w-0 truncate">{p.title || p.preset}</span>
                      <span className="shrink-0 text-right text-[11px] text-[var(--md-on-surface-variant)]">
                        {t("userDrawer.usage.devices", { n: p.devices })}
                        {p.last_seen ? ` · ${t("userDrawer.nodeSeen", { ago: ago(p.last_seen) })}` : ""}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </li>
          ))}
        </ul>
      )}
      <p className="mt-3 text-xs text-[var(--md-on-surface-variant)]">{t("userDrawer.usage.note")}</p>
    </Section>
  );
}
