import { useCallback, useEffect, useState } from "react";
import { LangSwitch } from "../components/lang";
import { Button } from "../components/ui";
import { t } from "../i18n";
import { rubles } from "../lib/format";
import { shopGet, shopPost, type Me, type Offers, type Order, type PayResult, type ShopConfig, type ShopOffer, type ShopSub, type TgUser } from "./api";

const FAIL: Record<string, "sub.web.fail" | "sub.web.limited" | "sub.web.badCode" | "sub.web.smtp" | "sub.web.none"> = {
  rate_limited: "sub.web.limited",
  promo_try_later: "sub.web.limited",
  code_invalid: "sub.web.badCode",
  smtp_off: "sub.web.smtp",
  not_for_sale: "sub.web.none",
};

function say(code?: string) {
  return t(FAIL[code ?? ""] ?? "sub.web.fail");
}

/** The browser shop: plans, a term, a device pack, a promo code, and a provider that is not Stars. */
export function ShopPage() {
  const [cfg, setCfg] = useState<ShopConfig | null>(null);
  const [me, setMe] = useState<Me | null>(null);
  const [offers, setOffers] = useState<Offers | null>(null);
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [sent, setSent] = useState(false);
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [order, setOrder] = useState<Order | null>(null);

  const load = useCallback(async () => {
    const [c, m] = await Promise.all([shopGet<ShopConfig>("/api/config"), shopGet<Me>("/api/me")]);
    if (c.ok && c.data.brand) setCfg(c.data as ShopConfig);
    if (m.ok) setMe(m.data as Me);
    if (m.data.auth) {
      const o = await shopGet<Offers>("/api/offers");
      if (o.ok) setOffers(o.data as Offers);
    } else {
      const o = await shopGet<Offers>("/api/offers");
      if (o.ok) setOffers(o.data as Offers);
    }
  }, []);

  useEffect(() => {
    void load();
    const q = new URLSearchParams(location.search).get("order");
    const hash = new URLSearchParams(location.hash.replace(/^#/, ""));
    const hashEmail = hash.get("email");
    const hashCode = hash.get("code");
    if (hashEmail && hashCode) {
      setEmail(hashEmail);
      setCode(hashCode);
      history.replaceState(null, "", location.pathname + location.search);
      void shopPost<Me>("/api/login", { email: hashEmail, code: hashCode }).then((r) => {
        if (!r.ok) setError(say(r.data.code));
        else void load();
      });
    }
    if (!q) return;
    let stop = false;
    const tick = async () => {
      const r = await shopGet<Order>("/api/order?order=" + encodeURIComponent(q));
      if (stop || !r.ok) return;
      setOrder(r.data as Order);
      if (r.data.status === "pending") window.setTimeout(() => void tick(), 3000);
    };
    void tick();
    return () => {
      stop = true;
    };
  }, [load]);

  const onTelegram = useCallback((u: TgUser) => {
    void shopPost("/api/telegram", u).then((r) => {
      if (!r.ok) setError(say(r.data.code));
      else void load();
    });
  }, [load]);
  const send = async () => {
    setBusy(true);
    setError("");
    const r = await shopPost("/api/code", { email });
    setBusy(false);
    if (!r.ok) setError(say(r.data.code));
    else setSent(true);
  };
  const enter = async () => {
    setBusy(true);
    setError("");
    const r = await shopPost<Me>("/api/login", { email, code });
    setBusy(false);
    if (!r.ok) setError(say(r.data.code));
    else void load();
  };
  const bind = async () => {
    setBusy(true);
    setError("");
    const raw = token.trim();
    const part = raw.split("/").filter(Boolean).pop() ?? raw;
    const r = await shopPost("/api/token", { token: part });
    setBusy(false);
    if (!r.ok) setError(say(r.data.code));
    else void load();
  };
  const logout = async () => {
    await shopPost("/api/logout");
    setMe({ auth: false });
    setOffers(null);
    void load();
  };

  return (
    <main className="mx-auto flex w-full max-w-[440px] flex-col gap-3 px-4 pt-8 pb-16 md:max-w-[920px]">
      <div className="flex items-center gap-2 px-1">
        <span className="font-display text-[15px] font-semibold tracking-tight">{cfg?.brand || t("sub.web.title")}</span>
        <LangSwitch className="ml-auto" />
      </div>
      <header className="glass sub-card p-4">
        <h1 className="font-display text-xl font-medium tracking-tight">{t("sub.web.title")}</h1>
        <p className="mt-1 text-[13px] text-[var(--md-on-surface-variant)]">{t("sub.web.sub")}</p>
      </header>
      {order ? <OrderCard order={order} /> : null}
      {me?.auth ? (
        <Account me={me} onOut={() => void logout()} />
      ) : (
        <SignIn
          cfg={cfg}
          email={email}
          setEmail={setEmail}
          code={code}
          setCode={setCode}
          sent={sent}
          token={token}
          setToken={setToken}
          busy={busy}
          error={error}
          onSend={() => void send()}
          onEnter={() => void enter()}
          onToken={() => void bind()}
          onTelegram={onTelegram}
        />
      )}
      {offers ? <Plans offers={offers} me={me} onPaid={() => void load()} /> : null}
      {error && me?.auth ? <p className="text-sm text-[var(--md-error)]">{error}</p> : null}
    </main>
  );
}

function SignIn(p: {
  cfg: ShopConfig | null;
  email: string;
  setEmail: (v: string) => void;
  code: string;
  setCode: (v: string) => void;
  sent: boolean;
  token: string;
  setToken: (v: string) => void;
  busy: boolean;
  error: string;
  onSend: () => void;
  onEnter: () => void;
  onToken: () => void;
  onTelegram: (u: TgUser) => void;
}) {
  return (
    <section className="glass sub-card grid gap-4 p-4 md:grid-cols-2">
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (p.sent) p.onEnter();
          else p.onSend();
        }}
      >
        <h2 className="text-[15px] font-semibold">{t("sub.web.email")}</h2>
        {p.cfg && !p.cfg.smtp ? <p className="text-[13px] text-[var(--md-on-surface-variant)]">{t("sub.web.smtp")}</p> : null}
        <label className="text-xs text-[var(--md-on-surface-variant)]" htmlFor="shop-email">
          {t("sub.web.email")}
          <input id="shop-email" className="input mt-1" type="email" autoComplete="email" value={p.email} onChange={(e) => p.setEmail(e.target.value)} required />
        </label>
        {p.sent ? (
          <label className="text-xs text-[var(--md-on-surface-variant)]" htmlFor="shop-code">
            {t("sub.web.code")}
            <input id="shop-code" className="input mt-1" inputMode="numeric" autoComplete="one-time-code" value={p.code} onChange={(e) => p.setCode(e.target.value)} required />
          </label>
        ) : null}
        {p.sent ? <p className="text-[13px] text-[var(--md-on-surface-variant)]">{t("sub.web.sent")}</p> : null}
        <Button variant="primary" block loading={p.busy} type="submit">
          {p.sent ? t("sub.web.enter") : t("sub.web.send")}
        </Button>
        {p.cfg?.telegram_bot ? <TelegramLogin bot={p.cfg.telegram_bot} onUser={p.onTelegram} /> : null}
      </form>
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          p.onToken();
        }}
      >
        <h2 className="text-[15px] font-semibold">{t("sub.web.token")}</h2>
        <p className="text-[13px] text-[var(--md-on-surface-variant)]">{t("sub.web.tokenHint")}</p>
        <input className="input" value={p.token} onChange={(e) => p.setToken(e.target.value)} autoComplete="off" spellCheck={false} aria-label={t("sub.web.token")} />
        <Button variant="glass" block loading={p.busy} type="submit">
          {t("sub.web.tokenGo")}
        </Button>
      </form>
      {p.error ? <p className="text-sm text-[var(--md-error)] md:col-span-2">{p.error}</p> : null}
    </section>
  );
}

function TelegramLogin({ bot, onUser }: { bot: string; onUser: (u: TgUser) => void }) {
  const [node, setNode] = useState<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!node) return;
    const w = window as Window & { onTelegramAuth?: (u: TgUser) => void };
    w.onTelegramAuth = onUser;
    const s = document.createElement("script");
    s.async = true;
    s.src = "https://telegram.org/js/telegram-widget.js?22";
    s.setAttribute("data-telegram-login", bot);
    s.setAttribute("data-size", "large");
    s.setAttribute("data-onauth", "onTelegramAuth(user)");
    s.setAttribute("data-request-access", "write");
    node.replaceChildren(s);
    return () => {
      delete w.onTelegramAuth;
    };
  }, [bot, node, onUser]);
  return (
    <div>
      <div className="mb-1 text-xs text-[var(--md-on-surface-variant)]">{t("sub.web.telegram")}</div>
      <div ref={setNode} />
    </div>
  );
}

function Account({ me, onOut }: { me: Me; onOut: () => void }) {
  const who = me.email || (me.telegram_id ? String(me.telegram_id) : "");
  return (
    <section className="glass sub-card flex items-center gap-3 p-4">
      <div className="min-w-0">
        <div className="truncate text-[15px] font-semibold">{who}</div>
        {me.balance != null ? (
          <div className="text-[13px] text-[var(--md-on-surface-variant)]">
            {t("sub.web.balance")}: {me.balance} {me.balance_currency}
          </div>
        ) : null}
      </div>
      <Button className="ml-auto" onClick={onOut}>
        {t("sub.web.out")}
      </Button>
    </section>
  );
}

function Plans({ offers, me, onPaid }: { offers: Offers; me: Me | null; onPaid: () => void }) {
  const list = offers.offers ?? [];
  const renew = offers.renew && !list.some((o) => o.id === offers.renew!.id) ? [offers.renew, ...list] : list;
  const [sub, setSub] = useState<string>(offers.subs?.[0]?.token ?? "");
  const [picked, setPicked] = useState<number | null>(renew[0]?.id ?? null);
  const [term, setTerm] = useState<number | null>(null);
  const [pack, setPack] = useState(0);
  const [promo, setPromo] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState("");
  const offer = renew.find((o) => o.id === picked);
  const terms = offer?.terms ?? [];
  const chosen = terms.find((x) => x.days === term) ?? terms[0];
  const subs = offers.subs ?? [];
  const owned = subs.find((s) => s.token === sub);
  useEffect(() => {
    setPack(owned?.device_pack ?? 0);
  }, [owned?.device_pack]);

  const pay = async (provider: string) => {
    if (!offer || !chosen) return;
    setBusy(provider);
    setNote("");
    const body = {
      tariff_id: offer.id,
      term_days: chosen.days,
      device_pack_id: pack,
      provider,
      promo_code: promo.trim(),
      token: sub,
    };
    if (promo.trim()) {
      const preview = await shopPost<{ discount: number; final_amount: number; currency: string }>("/api/promo", body);
      if (!preview.ok) {
        setNote(say(preview.data.code));
        setBusy("");
        return;
      }
    }
    const r = await shopPost<PayResult>("/api/pay", body);
    setBusy("");
    if (!r.ok || (!r.data.url && r.data.status !== "applied" && r.data.status !== "paid")) {
      setNote(say(r.data.code));
      return;
    }
    if (r.data.url) {
      location.href = r.data.url;
      return;
    }
    onPaid();
    if (r.data.token) setNote(t("sub.web.paid"));
  };

  if (!renew.length) return <p className="px-1 text-sm text-[var(--md-on-surface-variant)]">{t("sub.web.none")}</p>;
  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2 px-1">
        <h2 className="text-[15px] font-semibold">{t("sub.web.plans")}</h2>
        {me?.auth && offers.allow_new ? (
          <button type="button" className="opt" aria-checked={sub === ""} onClick={() => setSub("")}>
            {t("sub.web.new")}
          </button>
        ) : null}
        {subs.map((s) => (
          <button type="button" key={s.token} className="opt" aria-checked={sub === s.token} onClick={() => setSub(s.token)}>
            {s.name}
          </button>
        ))}
      </div>
      <div className="grid gap-3 md:grid-cols-2">
        {renew.map((o) => (
          <button type="button" key={o.id} className="opt" aria-checked={o.id === picked} onClick={() => { setPicked(o.id); setTerm(null); }}>
            <div className="flex items-baseline justify-between gap-2">
              <span className="font-display text-lg font-medium">{o.name}</span>
              <span className="text-sm">{rubles(o.rub)}</span>
            </div>
            <p className="mt-1 text-[13px] text-[var(--md-on-surface-variant)]">{o.description}</p>
            {o.note ? <p className="mt-2 text-[13px] whitespace-pre-line">{o.note}</p> : null}
          </button>
        ))}
      </div>
      {offer && terms.length > 1 ? (
        <div className="flex flex-wrap gap-2 px-1">
          <span className="text-xs text-[var(--md-on-surface-variant)]">{t("sub.web.term")}</span>
          {terms.map((x) => (
            <button type="button" key={x.days} className="opt" aria-checked={chosen?.days === x.days} onClick={() => setTerm(x.days)}>
              {x.label} · {rubles(x.rub)}
            </button>
          ))}
        </div>
      ) : null}
      {(offers.packs ?? []).length > 0 ? (
        <label className="px-1 text-xs text-[var(--md-on-surface-variant)]">
          {t("sub.web.pack")}
          <select className="input mt-1" value={pack} onChange={(e) => setPack(Number(e.target.value))}>
            <option value={0}>{t("sub.web.packNone")}</option>
            {offers.packs.map((pk) => (
              <option key={pk.id} value={pk.id}>
                {pk.title} · {pk.slots} · {rubles(pk.rub)}
              </option>
            ))}
          </select>
        </label>
      ) : null}
      <div className="flex gap-2 px-1">
        <input className="input" value={promo} onChange={(e) => setPromo(e.target.value)} placeholder={t("sub.web.promo")} aria-label={t("sub.web.promo")} />
      </div>
      <div className="grid gap-2 md:grid-cols-2">
        {(offers.providers ?? []).map((p) => (
          <Button key={p.id} variant="primary" block loading={busy === p.id} disabled={!me?.auth || !chosen} onClick={() => void pay(p.id)}>
            {t("sub.web.pay", { price: chosen ? rubles(chosen.rub) : "" })} · {p.id === "balance" ? t("sub.web.balance") : p.name}
          </Button>
        ))}
      </div>
      {note ? <p className="px-1 text-sm text-[var(--md-error)]">{note}</p> : null}
    </section>
  );
}

function OrderCard({ order }: { order: Order }) {
  const [copied, setCopied] = useState(false);
  const link = order.token ? location.origin + location.pathname.replace(/\/shop$/, "") + "/" + order.token : "";
  return (
    <section className="glass sub-card p-4">
      <h2 className="text-[15px] font-semibold">{order.status === "applied" || order.status === "paid" ? t("sub.web.paid") : t("sub.web.waiting")}</h2>
      {order.name ? <p className="mt-1 text-[13px] text-[var(--md-on-surface-variant)]">{order.sub_name || order.name}</p> : null}
      {link ? (
        <div className="mt-3 flex flex-col gap-2">
          <code className="mono text-[13px] break-all">{link}</code>
          <Button
            onClick={() => {
              void navigator.clipboard.writeText(link).then(() => {
                setCopied(true);
                window.setTimeout(() => setCopied(false), 2000);
              });
            }}
          >
            {copied ? t("sub.web.copied") : t("sub.web.copy")}
          </Button>
        </div>
      ) : null}
    </section>
  );
}

// Offer is named so a plan picked from the list and the renewal share one shape.
export type { ShopOffer, ShopSub };
