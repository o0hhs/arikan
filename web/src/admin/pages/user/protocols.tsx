// The protocols a subscription actually uses: each node remembers which inbound last let
// each device in, and the panel sums that per preset. Not traffic — usage.
import { errorText, type User } from "../../../api/client";
import { useUserProtocols } from "../../../api/hooks";
import { ErrorState, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { ago } from "../../../lib/format";
import { Section } from "./section";

export function ProtocolsUsageSection({ u }: { u: User }) {
  const usage = useUserProtocols(u.id);
  const items = usage.data?.items ?? [];
  return (
    <Section title={t("userDrawer.usage.title")} aside={t("userDrawer.usage.window")}>
      {usage.isPending ? (
        <Skeleton style={{ height: 56, borderRadius: 16 }} />
      ) : usage.data === undefined ? (
        <ErrorState text={errorText(usage.error)} onRetry={() => void usage.refetch()} />
      ) : items.length === 0 ? (
        <p className="text-[13px] text-[var(--md-on-surface-variant)]">{t("userDrawer.usage.empty")}</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {items.map((p) => (
            <li key={p.preset} className="panel-soft grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 p-3">
              <div className="min-w-0">
                <div className="truncate text-[13px] font-medium">{p.title || p.preset}</div>
                <div className="mt-0.5 text-xs text-[var(--md-on-surface-variant)]">
                  {p.last_seen ? t("userDrawer.nodeSeen", { ago: ago(p.last_seen) }) : t("userDrawer.nodeNever")}
                </div>
              </div>
              <div className="text-right text-[11px] text-[var(--ink-400)]">
                <div>{t("userDrawer.usage.devices", { n: p.devices })}</div>
                {p.nodes > 1 ? <div>{t("userDrawer.usage.nodes", { n: p.nodes })}</div> : null}
              </div>
            </li>
          ))}
        </ul>
      )}
      <p className="mt-3 text-xs text-[var(--md-on-surface-variant)]">{t("userDrawer.usage.note")}</p>
    </Section>
  );
}
