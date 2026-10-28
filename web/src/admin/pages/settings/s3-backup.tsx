import { useMutation, useQueryClient } from "@tanstack/react-query";
import { CloudUpload, HardDriveDownload, RotateCcw } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, apiCodeText, errorText, unwrap, type Schemas } from "../../../api/client";
import { qk, useS3Backup, useS3BackupObjects } from "../../../api/hooks";
import { CardHead } from "../../../components/layout";
import { Confirm } from "../../../components/overlay";
import { QueryBoundary } from "../../../components/query";
import { SwitchRow } from "../../../components/switch";
import { useToast } from "../../../components/toast";
import { Button, Field, Pill, Skeleton } from "../../../components/ui";
import { t, type Key } from "../../../i18n";
import { useDraft } from "../../../lib/draft";
import { fieldErrors } from "../../../lib/fields";
import { ago, bytes, utcHourLabel } from "../../../lib/format";

type View = Schemas["S3BackupView"];
type ObjectRow = Schemas["S3BackupObjectView"];

const formOf = (v: View) => ({
  enabled: v.enabled,
  endpoint: v.endpoint,
  region: v.region,
  bucket: v.bucket,
  prefix: v.prefix,
  access_key: "",
  secret_key: "",
  session_token: "",
  path_style: v.path_style,
  hour: v.hour,
  retention_days: v.retention_days,
  password: "",
});

export function S3BackupCard() {
  const backup = useS3Backup();
  return (
    <QueryBoundary
      query={backup}
      pending={<Skeleton style={{ height: 420, borderRadius: 20 }} />}
      wrap={(state) => <section className="card glass">{state}</section>}
    >
      {(v) => <S3BackupBody v={v} />}
    </QueryBoundary>
  );
}

function S3BackupBody({ v }: { v: View }) {
  const qc = useQueryClient();
  const toast = useToast();
  const objects = useS3BackupObjects();
  const { draft: form, setDraft: setForm, dirty, reset } = useDraft(formOf(v));
  const [restoreKey, setRestoreKey] = useState<string | null>(null);
  const refresh = () => Promise.all([qc.invalidateQueries({ queryKey: qk.s3Backup }), qc.invalidateQueries({ queryKey: qk.s3BackupObjects })]);
  const save = useMutation({
    mutationFn: (body: Schemas["PatchS3BackupInputBody"]) => unwrap(api.PATCH("/api/v1/s3-backup", { body })),
    onSuccess: () => {
      setForm((f) => ({ ...f, access_key: "", secret_key: "", session_token: "", password: "" }));
      void refresh();
      toast.ok(t("settings.s3.saved"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const run = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/s3-backup/run")),
    onSuccess: () => {
      void refresh();
      toast.ok(t("settings.s3.started"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const restore = useMutation({
    mutationFn: (key: string) => unwrap(api.POST("/api/v1/s3-backup/restore", { body: { key } })),
    onSuccess: () => {
      setRestoreKey(null);
      void refresh();
      toast.ok(t("settings.s3.restoreStarted"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const errors = fieldErrors(save.error);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const body: Schemas["PatchS3BackupInputBody"] = {
      enabled: form.enabled,
      endpoint: form.endpoint.trim(),
      region: form.region.trim(),
      bucket: form.bucket.trim(),
      prefix: form.prefix.trim(),
      path_style: form.path_style,
      hour: form.hour,
      retention_days: form.retention_days,
    };
    if (form.access_key.trim()) body.access_key = form.access_key.trim();
    if (form.secret_key.trim()) body.secret_key = form.secret_key.trim();
    if (form.session_token.trim()) body.session_token = form.session_token.trim();
    if (form.password.trim()) body.password = form.password.trim();
    save.mutate(body);
  };
  const set = <K extends keyof typeof form,>(k: K, value: (typeof form)[K]) => setForm((f) => ({ ...f, [k]: value }));
  return (
    <section className="card glass reveal">
      <CardHead title={t("settings.s3.title")} sub={t("settings.s3.sub")} />
      {v.restore_pending ? (
        <div className="banner warn mb-3" role="status">
          <HardDriveDownload size={18} className="shrink-0" aria-hidden />
          <span>{t("settings.s3.pending")}</span>
        </div>
      ) : null}
      {v.last_error ? (
        <div className="banner err mb-3" role="alert">
          {t("settings.s3.lastError", { error: apiCodeText(v.last_error) })}
        </div>
      ) : null}
      {v.restore_last_error ? (
        <div className="banner err mb-3" role="alert">
          {t("settings.s3.restoreError", { error: apiCodeText(v.restore_last_error) })}
        </div>
      ) : null}
      <form onSubmit={submit} className="flex flex-col gap-3">
        <SwitchRow label={t("settings.s3.enabled")} sub={t("settings.s3.enabledSub")} checked={form.enabled} onChange={(x) => set("enabled", x)} />
        <div className="grid gap-x-3 sm:grid-cols-2">
          <Field label={t("settings.s3.endpoint")} htmlFor="s3-endpoint" hint={t("settings.s3.endpointHint")} error={errors.endpoint}>
            <input id="s3-endpoint" className="input mono" value={form.endpoint} onChange={(e) => set("endpoint", e.target.value)} placeholder="https://s3.example.com" autoComplete="off" spellCheck={false} />
          </Field>
          <Field label={t("settings.s3.region")} htmlFor="s3-region" hint={t("settings.s3.regionHint")} error={errors.region}>
            <input id="s3-region" className="input mono" value={form.region} onChange={(e) => set("region", e.target.value)} placeholder="us-east-1" autoComplete="off" spellCheck={false} />
          </Field>
          <Field label={t("settings.s3.bucket")} htmlFor="s3-bucket" error={errors.bucket}>
            <input id="s3-bucket" className="input mono" value={form.bucket} onChange={(e) => set("bucket", e.target.value)} autoComplete="off" spellCheck={false} />
          </Field>
          <Field label={t("settings.s3.prefix")} htmlFor="s3-prefix" hint={t("settings.s3.prefixHint")} error={errors.prefix}>
            <input id="s3-prefix" className="input mono" value={form.prefix} onChange={(e) => set("prefix", e.target.value)} placeholder="arikan" autoComplete="off" spellCheck={false} />
          </Field>
          <Field label={t("settings.s3.accessKey")} htmlFor="s3-access" hint={v.access_key_set ? t("settings.s3.keepSecret") : undefined} error={errors.access_key}>
            <input id="s3-access" className="input mono" value={form.access_key} onChange={(e) => set("access_key", e.target.value)} placeholder={v.access_key_set ? "••••••••" : ""} autoComplete="off" spellCheck={false} />
          </Field>
          <Field label={t("settings.s3.secretKey")} htmlFor="s3-secret" hint={v.secret_key_set ? t("settings.s3.keepSecret") : undefined} error={errors.secret_key}>
            <input id="s3-secret" className="input mono" type="password" value={form.secret_key} onChange={(e) => set("secret_key", e.target.value)} placeholder={v.secret_key_set ? "••••••••" : ""} autoComplete="new-password" spellCheck={false} />
          </Field>
          <Field label={t("settings.s3.sessionToken")} htmlFor="s3-token" hint={v.session_token_set ? t("settings.s3.keepSecret") : t("settings.s3.sessionTokenHint")} error={errors.session_token}>
            <input id="s3-token" className="input mono" type="password" value={form.session_token} onChange={(e) => set("session_token", e.target.value)} placeholder={v.session_token_set ? "••••••••" : ""} autoComplete="new-password" spellCheck={false} />
          </Field>
          <Field label={t("settings.s3.password")} htmlFor="s3-password" hint={v.password_set ? t("settings.s3.passwordSaved") : t("settings.s3.passwordHint")} error={errors.password}>
            <input id="s3-password" className="input mono" type="password" value={form.password} onChange={(e) => set("password", e.target.value)} placeholder={v.password_set ? "••••••••" : ""} autoComplete="new-password" spellCheck={false} />
          </Field>
        </div>
        <div className="grid gap-x-3 sm:grid-cols-2">
          <Field label={t("settings.s3.hour")} htmlFor="s3-hour" hint={t("settings.s3.hourHint")}>
            <select id="s3-hour" className="input max-w-[220px]" value={form.hour} onChange={(e) => set("hour", Number(e.target.value))}>
              {Array.from({ length: 24 }, (_, h) => <option key={h} value={h}>{utcHourLabel(h)}</option>)}
            </select>
          </Field>
          <Field label={t("settings.s3.retention")} htmlFor="s3-retention" hint={t("settings.s3.retentionHint")}>
            <input id="s3-retention" className="input max-w-[220px]" type="number" min={0} max={3650} value={form.retention_days} onChange={(e) => set("retention_days", Number(e.target.value) || 0)} />
          </Field>
        </div>
        <SwitchRow label={t("settings.s3.pathStyle")} sub={t("settings.s3.pathStyleSub")} checked={form.path_style} onChange={(x) => set("path_style", x)} />
        <div className="flex flex-wrap items-center gap-2">
          <Button type="submit" variant="primary" loading={save.isPending} disabled={!dirty}>
            {t("common.save")}
          </Button>
          {dirty ? <Button type="button" variant="ghost" onClick={reset}>{t("common.discard")}</Button> : null}
          <Button type="button" loading={run.isPending} disabled={!v.configured || v.sending || v.restore_pending} onClick={() => run.mutate()}>
            <CloudUpload size={16} aria-hidden /> {t("settings.s3.run")}
          </Button>
        </div>
      </form>
      <div className="mt-4 grid gap-2 text-xs text-[var(--md-on-surface-variant)] sm:grid-cols-2">
        <span>{t("settings.s3.status", { state: v.configured ? t("settings.s3.configured") : t("settings.s3.notConfigured") })}</span>
        {v.last_ok ? <span>{t("settings.s3.lastOk", { ago: ago(v.last_ok), size: bytes(v.last_size ?? 0) })}</span> : null}
        {v.last_key ? <span className="break-all sm:col-span-2">{t("settings.s3.lastKey", { key: v.last_key })}</span> : null}
        {v.restore_last_ok ? <span>{t("settings.s3.restoreLastOk", { ago: ago(v.restore_last_ok) })}</span> : null}
        {v.restore_last_key ? <span className="break-all sm:col-span-2">{t("settings.s3.restoreLastKey", { key: v.restore_last_key })}</span> : null}
      </div>
      <div className="mt-5 border-t border-[var(--hairline)] pt-4">
        <h3 className="text-[14px] font-semibold">{t("settings.s3.history")}</h3>
        <QueryBoundary query={objects} pending={<Skeleton style={{ height: 120, marginTop: 12 }} />}>
          {(rows) => <History rows={rows} onRestore={setRestoreKey} disabled={v.restore_pending || restore.isPending} />}
        </QueryBoundary>
      </div>
      <Confirm
        open={!!restoreKey}
        onOpenChange={(x) => !x && setRestoreKey(null)}
        title={t("settings.s3.restoreTitle")}
        text={t("settings.s3.restoreText", { key: restoreKey ?? "" })}
        confirm={t("settings.s3.restore")}
        danger
        loading={restore.isPending}
        onConfirm={() => restoreKey && restore.mutate(restoreKey)}
      />
    </section>
  );
}

function History({ rows, onRestore, disabled }: { rows: ObjectRow[]; onRestore: (key: string) => void; disabled: boolean }) {
  if (rows.length === 0) return <p className="mt-3 text-[13px] text-[var(--md-on-surface-variant)]">{t("settings.s3.noHistory")}</p>;
  return (
    <div className="mt-3 overflow-x-auto">
      <table className="utable">
        <thead>
          <tr>
            <th>{t("settings.s3.object")}</th>
            <th>{t("settings.s3.kind")}</th>
            <th className="text-right">{t("settings.s3.size")}</th>
            <th>{t("settings.s3.when")}</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.id} style={{ cursor: "default" }}>
              <td className="max-w-[280px] break-all font-mono text-xs">{r.key}</td>
              <td><Pill tone={r.status === "ok" ? "ok" : "bad"}>{t(`settings.s3.kinds.${r.kind}` as Key)}</Pill>{r.error ? <span className="ml-1 text-xs">{apiCodeText(r.error)}</span> : null}</td>
              <td className="num text-right">{bytes(r.size)}</td>
              <td className="whitespace-nowrap text-xs">{ago(r.created_at)}</td>
              <td className="text-right">
                <Button size="sm" disabled={disabled || r.status !== "ok"} onClick={() => onRestore(r.key)} title={t("settings.s3.restore")}>
                  <RotateCcw size={15} aria-hidden /> {t("settings.s3.restore")}
                </Button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
