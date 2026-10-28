import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ClipboardCheck, Play, X } from "lucide-react";
import { useState } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { meQuery, useApprovals, type ApprovalStatus } from "../../api/hooks";
import { CardHead } from "../../components/layout";
import { QueryBoundary } from "../../components/query";
import { useToast } from "../../components/toast";
import { Button, EmptyState, PageHeader, Pill, Segmented, Skeleton } from "../../components/ui";
import { t, type Key } from "../../i18n";
import { ago, dateTime } from "../../lib/format";

type Approval = Schemas["ApprovalView"];
type Status = Approval["status"];

const FILTERS: { value: ApprovalStatus; label: Key }[] = [
  { value: "", label: "approvals.filters.all" },
  { value: "pending", label: "approvals.filters.pending" },
  { value: "approved", label: "approvals.filters.approved" },
  { value: "executing", label: "approvals.filters.executing" },
  { value: "rejected", label: "approvals.filters.rejected" },
  { value: "used", label: "approvals.filters.used" },
  { value: "expired", label: "approvals.filters.expired" },
];

const ACTION_LABELS: Record<string, Key> = {
  "user.delete": "approvals.actions.userDelete",
  "user.reissue": "approvals.actions.userReissue",
  "payment.refund": "approvals.actions.paymentRefund",
  "payment.refunded_by_hand": "approvals.actions.paymentRefundedByHand",
  "s3.restore": "approvals.actions.s3Restore",
};

const STATUS_TONE: Record<Status, "ok" | "warn" | "bad" | "off"> = {
  pending: "warn",
  approved: "ok",
  executing: "warn",
  rejected: "bad",
  expired: "off",
  used: "off",
};

export function ApprovalsPage() {
  const [status, setStatus] = useState<ApprovalStatus>("");
  const list = useApprovals(status);
  const me = useQuery({ ...meQuery });
  const qc = useQueryClient();
  const toast = useToast();
  const act = useMutation<unknown, Error, { id: number; kind: "approve" | "reject" | "execute" }>({
    mutationFn: async ({ id, kind }) => {
      if (kind === "approve") return unwrap(api.POST("/api/v1/approvals/{id}/approve", { params: { path: { id } } }));
      if (kind === "reject") return unwrap(api.POST("/api/v1/approvals/{id}/reject", { params: { path: { id } } }));
      return unwrap(api.POST("/api/v1/approvals/{id}/execute", { params: { path: { id } } }));
    },
    onSuccess: (_data, vars) => {
      void qc.invalidateQueries({ queryKey: ["approvals"] });
      toast.ok(vars.kind === "approve" ? t("approvals.approved") : vars.kind === "reject" ? t("approvals.rejected") : t("approvals.executed"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const admin = me.data?.admin;
  const canDecide = admin?.role === "superadmin" || !!admin?.permissions.includes("approvals.decide");
  return (
    <>
      <PageHeader
        title={t("approvals.title")}
        sub={t("approvals.subtitle")}
        actions={<Segmented label={t("approvals.filter")} value={status} onChange={setStatus} options={FILTERS.map((f) => ({ value: f.value, label: t(f.label) }))} />}
      />
      <QueryBoundary
        query={list}
        pending={<Skeleton style={{ height: 320, borderRadius: 20 }} />}
        wrap={(state) => <section className="card glass">{state}</section>}
      >
        {(rows) => (
          <section className="card glass reveal">
            <CardHead title={t("approvals.listTitle")} sub={t("approvals.listSub")} />
            {rows.length === 0 ? (
              <EmptyState icon={ClipboardCheck} title={t("approvals.emptyTitle")} text={t("approvals.emptyText")} />
            ) : (
              <ul className="row-list">
                {rows.map((a) => {
                  const own = admin?.id === a.requested_by;
                  const actionLabel = ACTION_LABELS[a.action];
                  return (
                    <li key={a.id} className="py-3">
                      <div className="flex flex-wrap items-start justify-between gap-3">
                        <div className="min-w-0 flex-1">
                          <div className="flex flex-wrap items-center gap-2">
                            <span className="text-[13px] font-semibold">{actionLabel ? t(actionLabel) : a.action}</span>
                            <Pill tone={STATUS_TONE[a.status]}>{t(`approvals.status.${a.status}` as Key)}</Pill>
                            {own ? <Pill tone="off">{t("approvals.own")}</Pill> : null}
                          </div>
                          <div className="mt-1 text-xs text-[var(--md-on-surface-variant)]">
                            {a.target_type} · <span className="mono">{a.target_id}</span> · {t("approvals.requestedBy", { id: a.requested_by, ago: ago(a.requested_at) })}
                          </div>
                          {a.summary ? <p className="mt-1 text-[13px]">{a.summary}</p> : null}
                          <p className="mt-1 text-xs text-[var(--md-on-surface-variant)]">
                            {t("approvals.expires", { at: dateTime(a.expires_at) })}
                            {a.decided_by ? ` · ${t("approvals.decidedBy", { id: a.decided_by })}` : ""}
                            {a.used_at ? ` · ${t("approvals.usedAt", { at: dateTime(a.used_at) })}` : ""}
                          </p>
                          <details className="mt-2 text-xs">
                            <summary className="cursor-pointer text-[var(--md-on-surface-variant)]">{t("approvals.payload")}</summary>
                            <pre className="mono mt-1 overflow-x-auto rounded-xl bg-[var(--md-surface-container)] p-2 text-[11px]">{JSON.stringify(a.payload, null, 2)}</pre>
                          </details>
                        </div>
                        <div className="flex flex-wrap gap-2">
                          {a.status === "pending" && canDecide && !own ? (
                            <>
                              <Button size="sm" variant="primary" loading={act.isPending && act.variables?.id === a.id && act.variables.kind === "approve"} onClick={() => act.mutate({ id: a.id, kind: "approve" })}>
                                <Check size={16} aria-hidden /> {t("approvals.approve")}
                              </Button>
                              <Button size="sm" variant="danger" loading={act.isPending && act.variables?.id === a.id && act.variables.kind === "reject"} onClick={() => act.mutate({ id: a.id, kind: "reject" })}>
                                <X size={16} aria-hidden /> {t("approvals.reject")}
                              </Button>
                            </>
                          ) : null}
                          {a.status === "approved" && (own || canDecide) ? (
                            <Button size="sm" variant="primary" loading={act.isPending && act.variables?.id === a.id && act.variables.kind === "execute"} onClick={() => act.mutate({ id: a.id, kind: "execute" })}>
                              <Play size={16} aria-hidden /> {t("approvals.execute")}
                            </Button>
                          ) : null}
                        </div>
                      </div>
                    </li>
                  );
                })}
              </ul>
            )}
          </section>
        )}
      </QueryBoundary>
      <p className="mt-3 text-xs text-[var(--md-on-surface-variant)]">{t("approvals.note")}</p>
    </>
  );
}
