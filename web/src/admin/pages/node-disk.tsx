// The disk of one node: what its data directory's filesystem has free, what its
// directories take, and a one-button clean of the junk (update leftovers, temporary
// files). The heavy part of what fills a node's server — old release images — is the
// updater's: it prunes them after every successful update.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Eraser, HardDrive } from "lucide-react";
import { useState } from "react";
import { errorText } from "../../api/client";
import { rawJson } from "../../api/hooks";
import { Drawer } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { useToast } from "../../components/toast";
import { Bar, Button, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { bytes } from "../../lib/format";

type Disk = {
  total: number;
  free: number;
  used: number;
  dirs: { path: string; bytes: number }[];
  junk: number;
  files: string[];
  freed: number;
};

const diskKey = (node: { id: number }) => ["node-disk", node.id] as const;

function useDisk(node: { id: number } | null) {
  return useQuery({
    queryKey: diskKey(node!),
    queryFn: ({ signal }) => rawJson<Disk>(`/api/v1/nodes/${node!.id}/disk`, { signal }),
    enabled: !!node,
  });
}

export function NodeDiskDrawer({ node, onClose }: { node: { id: number; name: string } | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [confirming, setConfirming] = useState(false);
  const disk = useDisk(node);
  const clean = useMutation({
    mutationFn: () => rawJson<Disk>(`/api/v1/nodes/${node!.id}/disk/clean`, { method: "POST" }),
    onSuccess: (st) => {
      setConfirming(false);
      toast.ok(st.freed > 0 ? t("nodes.diskFreed", { size: bytes(st.freed) }) : t("nodes.diskNothing"));
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: diskKey(node!) }),
    onError: (e) => toast.error(errorText(e)),
  });
  const pct = disk.data?.total ? Math.round(((disk.data.total - disk.data.free) / disk.data.total) * 100) : 0;
  return (
    <Drawer open={!!node} onOpenChange={(v) => !v && onClose()} title={t("nodes.diskTitle")} meta={node?.name}>
      <div className="pt-5">
        <QueryBoundary query={disk} pending={<Skeleton style={{ height: 240, borderRadius: 16 }} />}>
          {(d) => (
            <div className="flex flex-col gap-5">
              <section className="panel-soft rounded-2xl p-4">
                <div className="flex items-center gap-2">
                  <HardDrive size={16} aria-hidden />
                  <span className="text-xs text-[var(--md-on-surface-variant)]">{t("nodes.diskFs")}</span>
                </div>
                {d.total ? (
                  <>
                    <div className="mt-3 flex items-center gap-2">
                      <Bar pct={pct} className="flex-1" label={t("nodes.diskFs")} />
                      <span className="num w-10 text-right">{pct}%</span>
                    </div>
                    <div className="mt-1.5 flex justify-between text-xs text-[var(--md-on-surface-variant)]">
                      <span>{t("nodes.diskUsed", { size: bytes(d.used) })}</span>
                      <span>{t("nodes.diskFree", { size: bytes(d.free) })}</span>
                    </div>
                  </>
                ) : (
                  <p className="mt-2 text-xs text-[var(--md-on-surface-variant)]">{t("nodes.diskUnknown")}</p>
                )}
              </section>
              {d.dirs.length > 0 ? (
                <section>
                  <h3 className="mb-2 text-sm font-semibold">{t("nodes.diskDirs")}</h3>
                  <ul className="flex flex-col gap-1.5">
                    {d.dirs.map((dir) => (
                      <li key={dir.path} className="flex items-center justify-between text-[13px]">
                        <span className="mono truncate">{dir.path}</span>
                        <span className="num shrink-0 text-[var(--md-on-surface-variant)]">{bytes(dir.bytes)}</span>
                      </li>
                    ))}
                  </ul>
                </section>
              ) : null}
              <section className="panel-soft rounded-2xl p-4">
                <div className="flex items-center justify-between gap-3">
                  <div>
                    <div className="text-sm font-medium">{t("nodes.diskJunk")}</div>
                    <div className="mt-0.5 text-xs text-[var(--md-on-surface-variant)]">
                      {d.junk > 0 ? t("nodes.diskJunkSize", { size: bytes(d.junk) }) : t("nodes.diskEmpty")}
                    </div>
                  </div>
                  {d.junk > 0 ? (
                    <Button size="sm" variant="primary" loading={clean.isPending} disabled={clean.isPending} onClick={() => (confirming ? clean.mutate() : setConfirming(true))}>
                      <Eraser size={15} aria-hidden /> {confirming ? t("nodes.diskCleanSure") : t("nodes.diskClean")}
                    </Button>
                  ) : null}
                </div>
                {d.files.length > 0 ? (
                  <ul className="mt-3 flex flex-col gap-1 border-t border-[var(--hairline)] pt-3">
                    {d.files.map((f) => (
                      <li key={f} className="mono truncate text-xs text-[var(--md-on-surface-variant)]">
                        {f}
                      </li>
                    ))}
                  </ul>
                ) : null}
              </section>
              <p className="text-xs text-[var(--md-on-surface-variant)]">{t("nodes.diskNote")}</p>
            </div>
          )}
        </QueryBoundary>
      </div>
    </Drawer>
  );
}
