import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, errorText, unwrap } from "../../api/client";
import { CardHead, FormActions } from "../../components/layout";
import { Confirm } from "../../components/overlay";
import { SwitchRow } from "../../components/switch";
import { useToast } from "../../components/toast";
import { Button, Field, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { useDraft } from "../../lib/draft";

const key = ["traffic-detail"] as const;

/** Where the clients go: the nodes sum each subscription's traffic by site, kept a few days. */
export function TrafficDetailCard() {
  const q = useQuery({ queryKey: key, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/traffic-detail", { signal })) });
  if (!q.data) {
    return q.isError ? (
      <section className="card glass">
        <div className="banner err" role="alert">{errorText(q.error)}</div>
      </section>
    ) : (
      <Skeleton style={{ height: 160, borderRadius: 20 }} />
    );
  }
  return <DetailForm v={q.data} />;
}

function DetailForm({ v }: { v: { enabled: boolean; days: number } }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [confirm, setConfirm] = useState(false);
  const { draft, setDraft, dirty, reset } = useDraft({ enabled: v.enabled, days: String(v.days) });
  const days = Number(draft.days);
  const bad = !Number.isInteger(days) || days < 1 || days > 90;
  const save = useMutation({
    mutationFn: () => unwrap(api.PUT("/api/v1/traffic-detail", { body: { enabled: draft.enabled, days } })),
    onSuccess: (nv) => {
      qc.setQueryData(key, nv);
      setDraft({ enabled: nv.enabled, days: String(nv.days) });
      toast.ok(t("filters.saved"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  // Clearing everything the nodes summed so far: no confirm dialog asks why, and the admin
  // has no way back, so the button says what it deletes and the dialog says it again.
  const clear = useMutation({
    mutationFn: () => unwrap(api.DELETE("/api/v1/traffic-detail/sites")),
    onSuccess: (r) => {
      setConfirm(false);
      void qc.invalidateQueries({ queryKey: ["user-sites"] });
      toast.ok(t("settings.detail.clearDone", { n: r.rows }));
    },
    onError: (e) => {
      setConfirm(false);
      toast.error(errorText(e));
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!bad) save.mutate();
  };
  return (
    <section className="card glass reveal">
      <form onSubmit={submit} noValidate>
        <CardHead title={t("settings.detail.title")} sub={t("settings.detail.sub")} />
        <SwitchRow label={t("settings.detail.on")} sub={t("settings.detail.onSub")} checked={draft.enabled} onChange={(on) => setDraft((d) => ({ ...d, enabled: on }))} />
        <Field label={t("settings.detail.keep")} htmlFor="detail-days" hint={t("settings.detail.keepHint")} error={bad ? t("settings.detail.keepHint") : undefined}>
          <input id="detail-days" className="input max-w-[120px]" inputMode="numeric" value={draft.days} onChange={(e) => setDraft((d) => ({ ...d, days: e.target.value }))} aria-invalid={bad} />
        </Field>
        <FormActions dirty={dirty} saving={save.isPending} onReset={reset} />
      </form>
      <div className="mt-4 flex flex-wrap items-center justify-between gap-3 border-t border-[var(--md-outline-variant)] pt-4">
        <div className="min-w-0">
          <div className="text-[15px] font-medium text-[var(--md-on-surface)]">{t("settings.detail.clear")}</div>
          <div className="text-[13px] text-[var(--md-on-surface-variant)]">{t("settings.detail.clearSub")}</div>
        </div>
        <Button type="button" variant="danger" disabled={clear.isPending} onClick={() => setConfirm(true)}>
          <Trash2 size={16} aria-hidden />
          {t("settings.detail.clearButton")}
        </Button>
      </div>
      <Confirm open={confirm} onOpenChange={setConfirm} danger title={t("settings.detail.clearConfirmTitle")} text={t("settings.detail.clearConfirmText")} confirm={t("settings.detail.clearConfirm")} loading={clear.isPending} onConfirm={() => clear.mutate()} />
    </section>
  );
}
