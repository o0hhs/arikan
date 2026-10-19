import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Copy, KeyRound, Link2Off, ShieldCheck } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, errorText, type Schemas, unwrap } from "../../../api/client";
import { qk, useMonitor } from "../../../api/hooks";
import { useCopy } from "../../../lib/copy";
import { Confirm } from "../../../components/overlay";
import { SwitchRow } from "../../../components/switch";
import { useToast } from "../../../components/toast";
import { Button, Field, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";

type View = Schemas["MonitorView"];

/** The blocks the page may show, as the page itself names them. */
const BLOCKS = ["summary", "traffic", "nodes", "sites", "payments"] as const;

/**
 * The public monitoring page: its own address on this panel's host, opened by a password of
 * its own. The panel's password and an admin session mean nothing there, and nothing on the
 * page names a subscriber: it is the service's own numbers, shown to whoever holds the link
 * and the password.
 */
export function MonitorCard() {
  const q = useMonitor();
  if (!q.data) {
    return q.isError ? (
      <section className="card glass">
        <div className="banner err">{errorText(q.error)}</div>
      </section>
    ) : (
      <Skeleton style={{ height: 360, borderRadius: 20 }} />
    );
  }
  return <MonitorForm v={q.data} />;
}

function MonitorForm({ v }: { v: View }) {
  const qc = useQueryClient();
  const toast = useToast();
  const copyText = useCopy();
  const [confirm, setConfirm] = useState<"clear" | "rotate" | null>(null);
  const [password, setPassword] = useState("");
  const keep = (nv: View) => qc.setQueryData(qk.monitor, nv);
  const save = useMutation({
    mutationFn: (body: { enabled?: boolean; blocks?: string[] }) => unwrap(api.PUT("/api/v1/monitor", { body: { enabled: v.enabled, blocks: v.blocks, ...body } })),
    onSuccess: keep,
    onError: (e) => toast.error(errorText(e)),
  });
  const putPassword = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/monitor/password", { body: { password } })),
    onSuccess: (nv) => {
      keep(nv);
      setPassword("");
      toast.ok(t("settings.monitor.passwordSaved"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const clearPassword = useMutation({
    mutationFn: () => unwrap(api.DELETE("/api/v1/monitor/password")),
    onSuccess: (nv) => {
      keep(nv);
      setConfirm(null);
      toast.ok(t("settings.monitor.passwordCleared"));
    },
    onError: (e) => {
      setConfirm(null);
      toast.error(errorText(e));
    },
  });
  const rotate = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/monitor/path", { body: { path: randomPath() } })),
    onSuccess: (nv) => {
      keep(nv);
      setConfirm(null);
      toast.ok(t("settings.monitor.linkRotated"));
    },
    onError: (e) => {
      setConfirm(null);
      toast.error(errorText(e));
    },
  });
  const link = v.path && v.has_password ? `${window.location.origin}/${v.path}/` : "";
  const short = password.length > 0 && password.length < v.min_password;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!short) putPassword.mutate();
  };
  return (
    <section className="card glass reveal">
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.monitor.title")}</h2>
          <div className="card-sub">{t("settings.monitor.sub")}</div>
        </div>
        <ShieldCheck size={20} aria-hidden className="text-[var(--md-on-surface-variant)]" />
      </div>
      <ul className="row-list">
        <li>
          <SwitchRow label={t("settings.monitor.on")} sub={t("settings.monitor.onSub")} checked={v.enabled} disabled={save.isPending} onChange={(on) => save.mutate({ enabled: on })} />
        </li>
      </ul>

      <Field label={t("settings.monitor.link")} hint={v.enabled ? t("settings.monitor.linkHint") : undefined}>
        {link ? (
          <>
            <div className="link-field">
              <span className="mono">{link}</span>
              <button type="button" className="icon-btn" onClick={() => copyText(link, t("common.linkCopied"))} aria-label={t("common.copyLink")}>
                <Copy size={18} />
              </button>
            </div>
            <div className="mt-2 flex flex-wrap items-center gap-3">
              <Button type="button" variant="ghost" size="sm" onClick={() => setConfirm("rotate")}>
                <Link2Off size={16} aria-hidden />
                {t("settings.monitor.newLink")}
              </Button>
            </div>
          </>
        ) : (
          <p className="text-[13px] text-[var(--md-on-surface-variant)]">{t("settings.monitor.noLink")}</p>
        )}
      </Field>

      <form onSubmit={submit} noValidate className="mt-4 border-t border-[var(--md-outline-variant)] pt-4">
        <Field
          label={t("settings.monitor.password")}
          htmlFor="monitor-password"
          hint={t("settings.monitor.passwordHint", { n: v.min_password })}
          error={short ? t("settings.monitor.passwordHint", { n: v.min_password }) : undefined}
        >
          <p className={v.has_password ? "text-[13px] text-[var(--leaf-700)]" : "text-[13px] text-[var(--honey-600)]"}>{v.has_password ? t("settings.monitor.passwordSet") : t("settings.monitor.passwordNone")}</p>
          <div className="mt-2 flex flex-wrap items-center gap-3">
            <input
              id="monitor-password"
              className="input max-w-[260px]"
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              aria-invalid={short}
            />
            <Button type="submit" variant="primary" size="sm" disabled={putPassword.isPending || password === "" || short}>
              {v.has_password ? t("settings.monitor.passwordChange") : t("settings.monitor.passwordSet2")}
            </Button>
            {v.has_password ? (
              <Button type="button" variant="danger" size="sm" onClick={() => setConfirm("clear")}>
                <KeyRound size={16} aria-hidden />
                {t("settings.monitor.passwordClear")}
              </Button>
            ) : null}
          </div>
        </Field>
      </form>

      <Field label={t("settings.monitor.blocks")} hint={t("settings.monitor.blocksHint")}>
        <ul className="row-list">
          {BLOCKS.map((b) => (
            <li key={b}>
              <SwitchRow
                label={t(`settings.monitor.block.${b}`)}
                sub={t(`settings.monitor.block.${b}Sub`)}
                checked={v.blocks.includes(b)}
                disabled={save.isPending}
                onChange={(on) => save.mutate({ blocks: on ? [...v.blocks, b] : v.blocks.filter((x) => x !== b) })}
              />
            </li>
          ))}
        </ul>
      </Field>

      <Confirm
        open={confirm === "clear"}
        onOpenChange={(open) => !open && setConfirm(null)}
        danger
        title={t("settings.monitor.passwordClearTitle")}
        text={t("settings.monitor.passwordClearText")}
        confirm={t("settings.monitor.passwordClearConfirm")}
        loading={clearPassword.isPending}
        onConfirm={() => clearPassword.mutate()}
      />
      <Confirm
        open={confirm === "rotate"}
        onOpenChange={(open) => !open && setConfirm(null)}
        danger
        title={t("settings.monitor.newLinkTitle")}
        text={t("settings.monitor.newLinkText")}
        confirm={t("settings.monitor.newLinkConfirm")}
        loading={rotate.isPending}
        onConfirm={() => rotate.mutate()}
      />
    </section>
  );
}

/** A new address for the page: the same shape the panel's own secrets have (secure.Token). */
function randomPath(): string {
  const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789";
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => alphabet[b % alphabet.length]).join("");
}
