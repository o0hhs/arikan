// The admin's own site: sold and renewed subscriptions go there as signed webhooks, and
// the test button shows what that site answers.
import { useMutation } from "@tanstack/react-query";
import { Send } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../../api/client";
import { CardHead, Disclosure } from "../../../components/layout";
import { useToast } from "../../../components/toast";
import { Button, Field } from "../../../components/ui";
import { t } from "../../../i18n";
import { useSaveSettings } from "./shared";

type TestResult = { status: number; ms: number; error?: string; payload: string };

export function WebhooksCard({ s }: { s: Schemas["SettingsView"] }) {
  const save = useSaveSettings();
  const toast = useToast();
  const [url, setUrl] = useState(s.webhook_url);
  const [secret, setSecret] = useState("");
  const [result, setResult] = useState<TestResult | null>(null);
  const test = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/settings/webhook/test")),
    onSuccess: (r) => {
      setResult(r as TestResult);
      toast.ok(t("settings.webhooks.testSent"));
    },
    onError: (e) => {
      setResult(null);
      toast.error(errorText(e));
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    // An empty secret field keeps the saved one: it is never shown back to be edited.
    const body: Schemas["PatchSettingsInputBody"] = { webhook_url: url.trim() };
    if (secret.trim()) body.webhook_secret = secret.trim();
    save.mutate(body, { onSuccess: () => setSecret("") });
  };
  const clearSecret = () => {
    setSecret("");
    save.mutate({ webhook_secret: "" });
  };
  const ok = result != null && !result.error;
  return (
    <section className="card glass reveal" style={{ "--i": 6 } as React.CSSProperties}>
      <CardHead title={t("settings.webhooks.title")} sub={t("settings.webhooks.sub")} />
      <form onSubmit={submit} className="flex flex-col gap-3">
        <Field label={t("settings.webhooks.url")} htmlFor="wh-url" hint={t("settings.webhooks.urlHint")}>
          <input id="wh-url" className="input mono" type="url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://example.com/arikan/webhook" maxLength={500} autoComplete="off" spellCheck={false} />
        </Field>
        <Field label={t("settings.webhooks.secret")} htmlFor="wh-secret" hint={s.webhook_secret_set ? t("settings.webhooks.secretSaved") : t("settings.webhooks.secretHint")}>
          <input id="wh-secret" className="input mono" type="password" value={secret} onChange={(e) => setSecret(e.target.value)} placeholder={s.webhook_secret_set ? "••••••••" : t("settings.webhooks.secretPlaceholder")} maxLength={200} autoComplete="new-password" spellCheck={false} />
        </Field>
        <div className="flex flex-wrap items-center gap-2">
          <Button type="submit" variant="primary" loading={save.isPending}>
            {t("common.save")}
          </Button>
          <Button type="button" loading={test.isPending} disabled={!s.webhook_url} onClick={() => test.mutate()}>
            <Send size={16} aria-hidden /> {t("settings.webhooks.test")}
          </Button>
          {s.webhook_secret_set ? (
            <Button type="button" variant="ghost" onClick={clearSecret} disabled={save.isPending}>
              {t("settings.webhooks.clearSecret")}
            </Button>
          ) : null}
        </div>
      </form>
      {result ? (
        <div className={ok ? "banner info mt-3" : "banner err mt-3"} role="status">
          <span>{ok ? t("settings.webhooks.testOk", { status: result.status, ms: result.ms }) : t("settings.webhooks.testFail", { error: result.error ?? "", status: result.status })}</span>
        </div>
      ) : null}
      <Disclosure title={t("settings.webhooks.how")} sub={t("settings.webhooks.howSub")}>
        <div className="flex flex-col gap-2 text-[13px] text-[var(--md-on-surface-variant)]">
          <p>{t("settings.webhooks.howBody")}</p>
          <pre className="mono overflow-x-auto rounded-xl bg-[var(--md-surface-container)] p-3 text-[12px]">{`POST https://example.com/arikan/webhook
Content-Type: application/json
X-Arikan-Event: subscription.paid
X-Arikan-Delivery: 42
X-Arikan-Timestamp: 1790000000
X-Arikan-Signature: sha256=<HMAC-SHA256(secret, "1790000000." + body)>

{"event":"subscription.paid","at":"2026-10-25T10:00:00Z",
 "subscription":{"id":101,"uuid":"…","expires_at":"…","tariff":"…","term_days":30,…},
 "payment":{"id":7,"provider":"stars","amount":150,"currency":"XTR","kind":"new"},
 "telegram_id":555}`}</pre>
          <p>{t("settings.webhooks.howCheck")}</p>
        </div>
      </Disclosure>
    </section>
  );
}
