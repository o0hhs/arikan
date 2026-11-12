import { useState, type FormEvent } from "react";
import { useSaveSettings } from "./shared";
import type { Schemas } from "../../../api/client";
import { FormActions } from "../../../components/layout";
import { Field } from "../../../components/ui";
import { SwitchRow } from "../../../components/switch";
import { t } from "../../../i18n";
import { fieldErrors } from "../../../lib/fields";

/** The browser shop: off until the admin turns it on. Stars stay in Telegram. */
export function WebShopCard({ s }: { s: Schemas["SettingsView"] }) {
  const shop = s.web_shop;
  const save = useSaveSettings();
  const [on, setOn] = useState(shop.enabled);
  const [ret, setRet] = useState(shop.return_url);
  const [origins, setOrigins] = useState(shop.origins.join("\n"));
  const [host, setHost] = useState(shop.smtp_host);
  const [port, setPort] = useState(shop.smtp_port ? String(shop.smtp_port) : "");
  const [user, setUser] = useState(shop.smtp_user);
  const [pass, setPass] = useState("");
  const [from, setFrom] = useState(shop.smtp_from);
  const [tls, setTls] = useState(shop.smtp_tls || "starttls");
  const errors = fieldErrors(save.error);
  const url = s.sub_base_url ? s.sub_base_url.replace(/\/?$/, "/") + "shop" : "";
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({
      web_shop: {
        enabled: on,
        return_url: ret.trim(),
        origins: origins.split("\n").map((line) => line.trim()).filter(Boolean),
        smtp_host: host.trim(),
        smtp_port: Number(port) || 0,
        smtp_user: user.trim(),
        smtp_from: from.trim(),
        smtp_tls: tls,
        ...(pass.trim() ? { smtp_password: pass.trim() } : {}),
      },
    });
  };
  return (
    <section className="card glass reveal" style={{ "--i": 1 } as React.CSSProperties}>
      <form onSubmit={submit}>
        <div className="card-head">
          <div>
            <h2 className="card-title">{t("settings.webShop.title")}</h2>
            <div className="card-sub">{t("settings.webShop.sub")}</div>
          </div>
        </div>
        <SwitchRow label={t("settings.webShop.on")} sub={t("settings.webShop.onSub")} checked={on} onChange={setOn} />
        {url ? (
          <div className="panel-soft mb-4 p-3">
            <div className="text-xs text-[var(--md-on-surface-variant)]">{t("settings.webShop.url")}</div>
            <div className="mono truncate text-[13px]">{url}</div>
          </div>
        ) : null}
        <Field label={t("settings.webShop.return")} htmlFor="web-shop-return" hint={t("settings.webShop.returnHint")} error={errors["web_shop.return_url"]}>
          <input id="web-shop-return" className="input" value={ret} onChange={(e) => setRet(e.target.value)} placeholder="https://" autoComplete="off" spellCheck={false} />
        </Field>
        <Field label={t("settings.webShop.origins")} htmlFor="web-shop-origins" hint={t("settings.webShop.originsHint")} error={errors["web_shop.origins"]}>
          <textarea id="web-shop-origins" className="input min-h-20 font-mono text-[13px]" value={origins} onChange={(e) => setOrigins(e.target.value)} placeholder="https://example.com" spellCheck={false} />
        </Field>
        <h3 className="mt-4 mb-2 text-[13px] font-semibold">{t("settings.webShop.smtp")}</h3>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t("settings.webShop.host")} htmlFor="web-shop-host" error={errors["web_shop.smtp_host"]}>
            <input id="web-shop-host" className="input" value={host} onChange={(e) => setHost(e.target.value)} autoComplete="off" spellCheck={false} />
          </Field>
          <Field label={t("settings.webShop.port")} htmlFor="web-shop-port">
            <input id="web-shop-port" className="input" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, ""))} placeholder="587" />
          </Field>
          <Field label={t("settings.webShop.user")} htmlFor="web-shop-user">
            <input id="web-shop-user" className="input" value={user} onChange={(e) => setUser(e.target.value)} autoComplete="off" />
          </Field>
          <Field label={t("settings.webShop.pass")} htmlFor="web-shop-pass" hint={shop.smtp_password_set ? t("settings.webShop.passSet") : undefined}>
            <input id="web-shop-pass" className="input" type="password" value={pass} onChange={(e) => setPass(e.target.value)} autoComplete="new-password" />
          </Field>
          <Field label={t("settings.webShop.from")} htmlFor="web-shop-from" error={errors["web_shop.smtp_from"]}>
            <input id="web-shop-from" className="input" type="email" value={from} onChange={(e) => setFrom(e.target.value)} autoComplete="off" />
          </Field>
          <Field label={t("settings.webShop.tls")} htmlFor="web-shop-tls">
            <select id="web-shop-tls" className="input" value={tls} onChange={(e) => setTls(e.target.value)}>
              <option value="starttls">{t("settings.webShop.tlsStart")}</option>
              <option value="tls">{t("settings.webShop.tlsOn")}</option>
              <option value="none">{t("settings.webShop.tlsOff")}</option>
            </select>
          </Field>
        </div>
        <p className="mt-3 text-xs text-[var(--md-on-surface-variant)]">{t("settings.webShop.note")}</p>
        <p className="mt-1 text-xs text-[var(--md-on-surface-variant)]">{t("settings.webShop.needSale")}</p>
        <FormActions saving={save.isPending} />
      </form>
    </section>
  );
}
