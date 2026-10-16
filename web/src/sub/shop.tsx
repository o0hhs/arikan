// The Mini App's shop: the plans on sale and a way to pay for them. Stars open Telegram's
// own payment sheet; the marketplace's adapters (YooKassa, CryptoBot…) open their pages. The subscription itself is
// issued or renewed by the panel once the provider confirms the payment.
import { Check, RefreshCw } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "../components/ui";
import { t, type Key } from "../i18n";
import { bytes, days, money, num, rubles } from "../lib/format";
import { okData, postJSON } from "./net";

/** A term of a plan sold for several: its own price; label is "30 days", "3 months". */
type Term = { days: number; label: string; description: string; stars?: number; rub?: number };
type Offer = { id: number; name: string; description: string; note?: string; stars?: number; rub?: number; terms?: Term[] };
/** One dimension of the constructor: what the base plan has (0: endless) and what may be asked. */
type BuiltLimit = { value: number; min: number; max: number };
/** The tariff constructor the panel offers: the base plan, its terms and what may be built. */
type Built = {
  id: number;
  name: string;
  description: string;
  limits: string;
  reset: string;
  days: BuiltLimit;
  traffic: BuiltLimit;
  devices: BuiltLimit;
  speed: BuiltLimit;
  rates: { traffic: number; devices: number; speed: number; duration: number };
  terms: { days: number; stars?: number; rub?: number }[];
  stars?: number;
  rub?: number;
};
/** An access code the account redeemed: the plan it opened. */
type AccessItem = { tariff_id: number; name: string; code: string; mode: string };
export type ShopData = {
  allow_new: boolean;
  providers: { stars: boolean };
  /** Marketplace adapters that take rubles, by the name the buyer sees. */
  addons?: { provider: `addon:${string}`; name: string }[];
  offers: Offer[];
  packages?: Offer[];
  /** The priced trial the account may buy now: once, before any plan. */
  trial?: Offer;
  /** The free plan the account may take now; channel is the one it asks the account to join. */
  free?: { name: string; description: string; channel?: string };
  /** The tariff constructor, when the panel offers one. */
  built?: Built;
  /** The access codes this account redeemed (their plans are among the offers). */
  access?: AccessItem[];
};
type Provider = "stars" | `addon:${string}`;

// YooKassa and CryptoBot keep the words buyers knew them by, and come first.
const KNOWN: Record<string, Key> = { "addon:yookassa": "sub.shopCard", "addon:cryptobot": "sub.shopCrypto" };
const rank = (p: string) => (p === "addon:yookassa" ? 0 : p === "addon:cryptobot" ? 1 : 2);

const FAIL: Record<string, Key> = {
  not_for_sale: "sub.shopNotForSale",
  too_many_invoices: "sub.shopTooMany",
  too_many_subs: "sub.shopTooManySubs",
  promo_unavailable: "sub.promoUnavailable",
  promo_try_later: "sub.promoTryLater",
  trial_used: "sub.trialUsed",
  trial_off: "sub.trialOff",
  bad_options: "sub.builtBad",
  constructor_off: "sub.builtOff",
  provider_off: "sub.shopFail",
};

/** What the account can buy; with token, the traffic packages of that subscription too. */
export function loadShop(subRoot: string, initData: string, token = ""): Promise<ShopData> {
  return postJSON<ShopData>(subRoot + "/tg/shop", { init_data: initData, token }).then(okData);
}

/**
 * Offers and pay buttons: plans (field tariff_id), traffic packages (package_id) or the
 * priced trial (trial). token is the subscription to renew or add traffic to; "" buys a
 * new one. openInvoice/openLink go through the Mini App bridge.
 */
export function Shop({
  data,
  offers,
  field = "tariff_id",
  pick = t("sub.shopPick"),
  subRoot,
  initData,
  token,
  title,
  openInvoice,
  openLink,
  onRefresh,
  promoCode = "",
}: {
  data: ShopData;
  offers: Offer[];
  field?: "tariff_id" | "package_id" | "trial";
  pick?: string;
  subRoot: string;
  initData: string;
  token: string;
  title: string;
  openInvoice: (slug: string) => void;
  openLink: (url: string) => void;
  onRefresh: () => void;
  promoCode?: string;
}) {
  const [picked, setPicked] = useState<number | null>(offers.length === 1 ? offers[0]!.id : null);
  // The chosen term of a plan sold for several; the first until another is chosen.
  const [termDays, setTermDays] = useState<number | null>(null);
  const [busy, setBusy] = useState<Provider | null>(null);
  const [error, setError] = useState("");
  const [promoPreview, setPromoPreview] = useState<{ discount: number; final_amount: number; currency: string } | null>(null);
  const [opened, setOpened] = useState(false);
  // The payment page of a ruble provider, ready to open. Telegram opens a link only for a tap
  // it sees, and the requests that made this link took that tap, so the buyer taps again.
  const [ready, setReady] = useState<{ url: string; provider: Provider } | null>(null);
  useEffect(() => { setError(""); setPromoPreview(null); setReady(null); }, [picked, termDays, promoCode]);
  useEffect(() => setTermDays(null), [picked]);
  const offer = offers.find((o) => o.id === picked);
  const terms = offer?.terms && offer.terms.length > 1 ? offer.terms : null;
  const term = terms ? (terms.find((x) => x.days === termDays) ?? terms[0]!) : null;
  // What is paid for: the chosen term's prices, or the offer's own.
  const price = term ?? offer;
  // A single term the providers take now is named too: it may not be the plan's first,
  // which a request without days is for.
  const sold = term ?? offer?.terms?.[0];
  const order = field === "trial" ? { trial: true } : { [field]: offer?.id, ...(sold ? { term_days: sold.days } : {}) };
  const failText = (code: string) => (field === "package_id" && code === "not_for_sale" ? t("sub.packageNotForSale") : t(FAIL[code] ?? "sub.shopFail"));

  const pay = async (provider: Provider, custom?: BuiltPlan) => {
    if (!offer) return;
    setBusy(provider);
    setError("");
    setReady(null);
    try {
      if (promoCode) {
        const preview = await postJSON<{ code: string; discount: number; final_amount: number; currency: string }>(subRoot + "/tg/promo", {
          init_data: initData,
          token,
          code: promoCode,
          ...order,
          provider,
          validate_only: true,
        });
        const result = preview.data;
        if (!preview.ok) {
          setError(failText(result.code ?? "promo_unavailable"));
          return;
        }
        if (result.discount != null && result.final_amount != null && result.currency) {
          setPromoPreview({ discount: result.discount, final_amount: result.final_amount, currency: result.currency });
        }
      }
      const r = await postJSON<{ url: string; code: string }>(subRoot + "/tg/pay", {
        init_data: initData,
        ...order,
        ...(custom ? { custom } : {}),
        provider,
        token,
        promo_code: promoCode,
      });
      const body = r.data;
      if (!r.ok || !body.url) {
        setError(failText(body.code ?? ""));
        return;
      }
      // A Stars link is https://t.me/$<slug>: Telegram opens its payment sheet for it.
      const slug = /^https:\/\/t\.me\/\$(.+)$/.exec(body.url)?.[1];
      if (provider === "stars" && slug) {
        openInvoice(slug);
        setOpened(true);
      } else setReady({ url: body.url, provider });
    } catch {
      setError(t("sub.shopFail"));
    } finally {
      setBusy(null);
    }
  };

  const methods: { id: Provider; label: string; price?: string }[] = [];
  if (price?.stars && data.providers.stars) methods.push({ id: "stars", label: t("sub.shopStars"), price: `⭐ ${price.stars}` });
  if (price?.rub) {
    const adapters = [...(data.addons ?? [])].sort((a, b) => rank(a.provider) - rank(b.provider) || a.provider.localeCompare(b.provider));
    for (const a of adapters) methods.push({ id: a.provider, label: KNOWN[a.provider] ? t(KNOWN[a.provider]!) : a.name, price: rubles(price.rub) });
  }

  const goName = ready ? data.addons?.find((a) => a.provider === ready.provider)?.name : undefined;

  return (
    <section className="glass sub-card p-4" aria-label={title}>
      <h2 className="mb-1 text-[15px] font-semibold">{title}</h2>
      <p className="mb-3 text-xs text-[var(--md-on-surface-variant)]">{pick}{promoCode ? ` · ${t("sub.promoSelected", { code: promoCode })}` : ""}</p>
      {promoPreview ? <p className="mb-3 text-xs text-[var(--leaf-600)]">{t("sub.promoCheckoutDiscount", { discount: money(promoPreview.discount, promoPreview.currency), total: money(promoPreview.final_amount, promoPreview.currency) })}</p> : null}
      <div className="flex flex-col gap-2" role="radiogroup" aria-label={pick}>
        {offers.map((o) => (
          <button key={o.id} type="button" role="radio" aria-checked={picked === o.id} className="opt" onClick={() => setPicked(o.id)}>
            <span className="flex items-center justify-between gap-2">
              <span className="font-semibold">{o.name}</span>
              <span className="num text-[13px] text-[var(--md-on-surface-variant)]">{listPrice(o)}</span>
            </span>
            <span className="text-xs text-[var(--md-on-surface-variant)]">{o.description}</span>
            {o.note ? <span className="whitespace-pre-line text-xs text-[var(--md-on-surface-variant)]">{o.note}</span> : null}
          </button>
        ))}
      </div>
      {terms ? (
        <div className="mt-3 flex flex-wrap gap-2" role="radiogroup" aria-label={t("sub.shopPickTerm")}>
          {terms.map((x) => (
            <button key={x.days} type="button" role="radio" aria-checked={term?.days === x.days} className="opt flex-1" style={{ minWidth: 96 }} onClick={() => setTermDays(x.days)}>
              <span className="font-semibold">{x.label}</span>
              <span className="num text-xs text-[var(--md-on-surface-variant)]">{x.rub ? rubles(x.rub) : `⭐ ${x.stars}`}</span>
            </button>
          ))}
        </div>
      ) : null}
      {offer ? (
        <div className="mt-3 grid gap-2" style={{ gridTemplateColumns: `repeat(${Math.min(3, Math.max(1, methods.length))}, minmax(0, 1fr))` }}>
          {methods.map((m, i) => (
            <Button key={m.id} variant={i === 0 ? "primary" : "glass"} className="flex-col gap-0 py-2" style={{ height: "auto" }} loading={busy === m.id} disabled={!!busy} onClick={() => void pay(m.id)}>
              <span className="text-[13px] font-semibold">{m.price}</span>
              <span className="text-[11px] opacity-80">{m.label}</span>
            </Button>
          ))}
        </div>
      ) : null}
      {ready && !busy && !error ? (
        <div className="mt-3 flex flex-col gap-2">
          <Button
            variant="primary"
            block
            onClick={() => {
              // Straight from the tap: no await between it and the bridge call.
              openLink(ready.url);
              setOpened(true);
            }}
          >
            {goName ? t("sub.shopGoVia", { name: goName }) : t("sub.shopGo")}
          </Button>
          {opened ? null : <p className="text-xs text-[var(--md-on-surface-variant)]">{t("sub.shopGoHint")}</p>}
        </div>
      ) : null}
      {error ? (
        <p className="mt-3 text-xs text-[var(--md-error)]" role="alert">
          {error}
        </p>
      ) : null}
      {opened && !error ? (
        <div className="mt-3 flex items-center justify-between gap-2 text-xs text-[var(--md-on-surface-variant)]" role="status">
          <span className="flex items-center gap-2">
            <Check size={14} className="shrink-0 text-[var(--leaf-500)]" aria-hidden /> {t("sub.shopDone")}
          </span>
          <Button size="sm" variant="ghost" onClick={onRefresh}>
            <RefreshCw size={14} aria-hidden /> {t("sub.shopRefresh")}
          </Button>
        </div>
      ) : null}
    </section>
  );
}

const FREE_FAIL: Record<string, Key> = {
  free_active: "sub.freeActive",
  free_off: "sub.freeOff",
  too_many_subs: "sub.shopTooManySubs",
};

/**
 * The free plan: one tap gives the subscription. A plan for the channel's subscribers
 * shows the channel next to it, and says so when the account is not in it yet.
 */
export function FreePlan({
  offer,
  subRoot,
  initData,
  openLink,
  onTaken,
}: {
  offer: NonNullable<ShopData["free"]>;
  subRoot: string;
  initData: string;
  openLink: (url: string) => void;
  onTaken: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const take = async () => {
    setBusy(true);
    setError("");
    try {
      const r = await postJSON<{ token: string; code: string }>(subRoot + "/tg/free", { init_data: initData });
      if (r.ok && r.data.token) {
        onTaken();
        return;
      }
      const code = r.data.code ?? "";
      setError(code === "free_channel" ? t("sub.freeChannelNeeded") : t(FREE_FAIL[code] ?? "sub.freeFail"));
    } catch {
      setError(t("sub.freeFail"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="glass sub-card p-4" aria-label={t("sub.freeTitle")}>
      <h2 className="mb-1 text-[15px] font-semibold">{t("sub.freeTitle")}</h2>
      <p className="mb-3 text-xs text-[var(--md-on-surface-variant)]">{t("sub.freeAgain")}</p>
      <div className="mb-3">
        <div className="font-semibold">{offer.name}</div>
        <div className="text-xs text-[var(--md-on-surface-variant)]">{offer.description}</div>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button variant="primary" loading={busy} disabled={busy} onClick={() => void take()}>
          {t("sub.freeTake")}
        </Button>
        {offer.channel ? (
          <Button variant="glass" onClick={() => openLink(offer.channel!)}>
            {t("sub.freeJoin")}
          </Button>
        ) : null}
      </div>
      {error ? (
        <p className="mt-3 text-xs text-[var(--md-error)]" role="alert">
          {error}
        </p>
      ) : null}
    </section>
  );
}

/** The price a list shows: rubles when sold for them, else Stars; "from" the cheapest term
 * of a plan sold for several. */
function listPrice(o: Offer): string {
  const terms = o.terms && o.terms.length > 1 ? o.terms : [o];
  const rub = Math.min(...terms.map((x) => x.rub || Infinity));
  const price = Number.isFinite(rub) ? rubles(rub) : `⭐ ${Math.min(...terms.map((x) => x.stars || Infinity))}`;
  return terms.length > 1 ? t("sub.shopFrom", { price }) : price;
}

// The constructor: the buyer puts the plan's numbers together and the panel prices the order
// again on its side (billing.CustomPrice) when the invoice is opened. This is the same
// arithmetic, so the price on the button is the price charged: each dimension scales by how
// far the choice is from the plan's own number, times the multiplier the admin set, and the
// result is rounded up to a whole step — five Stars, a ruble.

/** What the buyer built: only the dimensions they touched; the rest is the plan's own. */
type BuiltPlan = { days?: number; traffic?: number; devices?: number; speed?: number };

/** Rounds up to a whole step: 1333 Stars is 1335, 132.67 ₽ is 133 ₽. */
function stepPrice(base: number, factor: number, step: number): number {
  const v = Math.max(1, Math.ceil(base * factor));
  return Math.ceil(v / step) * step;
}

/** The prices of a built plan, or null when nothing may be paid for it. */
function builtPrice(b: Built, plan: BuiltPlan): { stars?: number; rub?: number } | null {
  const term = b.terms.find((x) => x.days === plan.days) ?? b.terms[0];
  if (!term) return null;
  const days = plan.days ?? b.days.value;
  const ratio = (want: number | undefined, base: number, mult: number) => {
    if (want == null || base <= 0) return 1;
    return Math.max(want / base, 0.1) * mult;
  };
  const factor =
    ratio(plan.traffic, b.traffic.value, b.rates.traffic) *
    ratio(plan.devices, b.devices.value, b.rates.devices) *
    ratio(plan.speed, b.speed.value, b.rates.speed) *
    Math.max(days / term.days, 0.1) * b.rates.duration;
  const out: { stars?: number; rub?: number } = {};
  if (term.stars) out.stars = stepPrice(term.stars, factor, 5);
  if (term.rub) out.rub = stepPrice(term.rub, factor, 100);
  return out.stars || out.rub ? out : null;
}

/** A dimension of the builder: a switch that offers to change it and, once on, a number. */
function BuiltField({
  label,
  limit,
  bytes: asBytes,
  onChange,
}: {
  label: string;
  limit: BuiltLimit;
  /** The value is in bytes (traffic): the field is typed and shown in whole gigabytes. */
  bytes?: boolean;
  onChange: (v: number | undefined) => void;
}) {
  const [on, setOn] = useState(false);
  const step = asBytes ? 1024 ** 3 : limit.value >= 1000 ? 10 : 1;
  const value = asBytes ? Math.round(limit.value / step) : limit.value;
  const input = (
    <input
      type="number"
      className="input num w-24"
      defaultValue={value}
      min={asBytes ? Math.max(1, Math.ceil(limit.min / step)) : limit.min}
      max={asBytes ? Math.floor(limit.max / step) : limit.max}
      onChange={(e) => {
        const n = Number(e.target.value);
        if (Number.isFinite(n) && n > 0) onChange(asBytes ? n * step : n);
      }}
    />
  );
  return (
    <div className="flex items-center justify-between gap-2">
      <span className="text-[13px]">{label}</span>
      <div className="flex items-center gap-2">
        {on ? input : <span className="num text-[13px] text-[var(--md-on-surface-variant)]">{asBytes ? bytes(limit.value) : num(limit.value)}</span>}
        <Button
          size="sm"
          variant={on ? "primary" : "glass"}
          onClick={() => {
            setOn(!on);
            onChange(on ? undefined : asBytes ? value * step : value);
          }}
        >
          {on ? t("sub.builtEndless") : t("sub.builtChange")}
        </Button>
      </div>
    </div>
  );
}

/**
 * The tariff constructor: the buyer moves the plan's numbers and pays for the result. The
 * term is one of the plan's own terms; the other dimensions may go anywhere the plan's own
 * limits reach (a dimension the plan leaves endless has no range and is not offered).
 */
export function Builder({
  built,
  data,
  subRoot,
  initData,
  token,
  openInvoice,
  openLink,
  onRefresh,
}: {
  built: Built;
  data: ShopData;
  subRoot: string;
  initData: string;
  token: string;
  openInvoice: (slug: string) => void;
  openLink: (url: string) => void;
  onRefresh: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [plan, setPlan] = useState<BuiltPlan>({});
  const [busy, setBusy] = useState<Provider | null>(null);
  const [error, setError] = useState("");
  const [ready, setReady] = useState<{ url: string; provider: Provider } | null>(null);
  const [opened, setOpened] = useState(false);
  const price = builtPrice(built, plan);
  const changed = Object.values(plan).some((v) => v != null);

  const pay = async (provider: Provider) => {
    setBusy(provider);
    setError("");
    setReady(null);
    try {
      const custom: BuiltPlan = { ...plan };
      if (custom.days == null && built.terms.length > 1) custom.days = built.terms[0]!.days;
      const r = await postJSON<{ url: string; code: string }>(subRoot + "/tg/pay", {
        init_data: initData,
        tariff_id: built.id,
        ...(custom.days != null ? { term_days: custom.days } : {}),
        custom,
        provider,
        token,
      });
      const body = r.data;
      if (!r.ok || !body.url) {
        setError(t(FAIL[body.code ?? ""] ?? "sub.shopFail"));
        return;
      }
      const slug = /^https:\/\/t\.me\/\$(.+)$/.exec(body.url)?.[1];
      if (provider === "stars" && slug) {
        openInvoice(slug);
        setOpened(true);
      } else setReady({ url: body.url, provider });
    } catch {
      setError(t("sub.shopFail"));
    } finally {
      setBusy(null);
    }
  };

  // One button per way to pay: the plan is priced in Stars and rubles alike, and every adapter
  // that takes rubles is offered the same figure — the one the panel computes on its side.
  const methods: { id: Provider; label: string; price: string }[] = [];
  if (price?.stars && data.providers.stars) methods.push({ id: "stars", label: t("sub.shopStars"), price: `⭐ ${price.stars}` });
  if (price?.rub) {
    const adapters = [...(data.addons ?? [])].sort((a, b) => rank(a.provider) - rank(b.provider) || a.provider.localeCompare(b.provider));
    for (const a of adapters) methods.push({ id: a.provider, label: KNOWN[a.provider] ? t(KNOWN[a.provider]!) : a.name, price: rubles(price.rub) });
  }

  return (
    <section className="glass sub-card p-4" aria-label={t("sub.builtTitle")}>
      <h2 className="mb-1 text-[15px] font-semibold">{t("sub.builtTitle")}</h2>
      <p className="mb-3 text-xs text-[var(--md-on-surface-variant)]">{t("sub.builtHint", { name: built.name })}</p>
      {!open ? (
        <Button variant="glass" block onClick={() => setOpen(true)}>
          {t("sub.builtOpen")}
        </Button>
      ) : (
        <div className="flex flex-col gap-3">
          <p className="text-xs text-[var(--md-on-surface-variant)]">{built.limits}</p>
          {built.terms.length > 1 ? (
            <div className="flex flex-wrap gap-2" role="radiogroup" aria-label={t("sub.builtDays")}>
              {built.terms.map((x) => (
                <button key={x.days} type="button" role="radio" aria-checked={(plan.days ?? built.terms[0]!.days) === x.days} className="opt flex-1" style={{ minWidth: 96 }} onClick={() => setPlan({ ...plan, days: x.days })}>
                  <span className="font-semibold">{days(x.days)}</span>
                  <span className="num text-xs text-[var(--md-on-surface-variant)]">{x.rub ? rubles(x.rub) : `⭐ ${x.stars}`}</span>
                </button>
              ))}
            </div>
          ) : null}
          {built.traffic.value > 0 ? <BuiltField label={t("sub.builtTraffic")} limit={built.traffic} bytes onChange={(v) => setPlan((p) => ({ ...p, traffic: v }))} /> : null}
          {built.devices.value > 0 ? <BuiltField label={t("sub.builtDevices")} limit={built.devices} onChange={(v) => setPlan((p) => ({ ...p, devices: v }))} /> : null}
          {built.speed.value > 0 ? <BuiltField label={t("sub.builtSpeed")} limit={built.speed} onChange={(v) => setPlan((p) => ({ ...p, speed: v }))} /> : null}
          {built.reset ? <p className="text-xs text-[var(--md-on-surface-variant)]">{t("sub.builtResets", { when: t(`tariffs.resetLabel.${built.reset}` as Key) })}</p> : null}
          <div className="flex flex-col gap-2">
            {methods.map((m, i) => (
              <Button key={m.id} variant={i === 0 ? "primary" : "glass"} block loading={busy === m.id} disabled={!!busy || !changed || !price} onClick={() => void pay(m.id)}>
                {price && methods.length ? t("sub.builtPay", { price: m.price }) : t("sub.builtBad")}
              </Button>
            ))}
          </div>
          <Button size="sm" variant="ghost" onClick={() => { setOpen(false); setPlan({}); setReady(null); setError(""); }}>
            {t("sub.builtClose")}
          </Button>
        </div>
      )}
      {ready && !busy && !error ? (
        <Button
          variant="primary"
          block
          className="mt-3"
          onClick={() => {
            openLink(ready.url);
            setOpened(true);
          }}
        >
          {t("sub.shopGo")}
        </Button>
      ) : null}
      {error ? (
        <p className="mt-3 text-xs text-[var(--md-error)]" role="alert">
          {error}
        </p>
      ) : null}
      {opened && !error ? (
        <div className="mt-3 flex items-center justify-between gap-2 text-xs text-[var(--md-on-surface-variant)]" role="status">
          <span className="flex items-center gap-2">
            <Check size={14} className="shrink-0 text-[var(--leaf-500)]" aria-hidden /> {t("sub.shopDone")}
          </span>
          <Button size="sm" variant="ghost" onClick={onRefresh}>
            <RefreshCw size={14} aria-hidden /> {t("sub.shopRefresh")}
          </Button>
        </div>
      ) : null}
    </section>
  );
}

const ACCESS_FAIL: Record<string, Key> = {
  promo_unavailable: "sub.promoUnavailable",
  promo_try_later: "sub.promoTryLater",
  too_many_subs: "sub.shopTooManySubs",
  not_yours: "sub.shopFail",
  internal: "sub.shopFail",
};

/**
 * An access code: it opens a plan the shop keeps hidden. A paid code shows the plan among the
 * offers (the page reloads and offers it); a free one hands a subscription over right away.
 */
export function AccessCode({
  subRoot,
  initData,
  onTaken,
}: {
  subRoot: string;
  initData: string;
  onTaken: () => void;
}) {
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState("");
  const apply = async () => {
    const value = code.trim();
    if (!value) return;
    setBusy(true);
    setError("");
    setDone("");
    try {
      const r = await postJSON<{ ok?: boolean; type?: string; name?: string; token?: string; code?: string }>(subRoot + "/tg/access", {
        init_data: initData,
        code: value,
      });
      const body = r.data;
      if (!r.ok || !body.ok) {
        setError(t(ACCESS_FAIL[body.code ?? ""] ?? "sub.promoUnavailable"));
        return;
      }
      setCode("");
      if (body.type === "access_free") {
        setDone(t("sub.accessGiven", { name: body.name ?? "" }));
        onTaken();
        return;
      }
      setDone(t("sub.accessOpened", { name: body.name ?? "" }));
      onTaken();
    } catch {
      setError(t("sub.shopFail"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="glass sub-card p-4" aria-label={t("sub.accessTitle")}>
      <h2 className="mb-1 text-[15px] font-semibold">{t("sub.accessTitle")}</h2>
      <p className="mb-3 text-xs text-[var(--md-on-surface-variant)]">{t("sub.accessHint")}</p>
      <div className="flex gap-2">
        <input
          className="input num flex-1 uppercase"
          value={code}
          placeholder="XXXX-XXXX"
          autoCapitalize="characters"
          onChange={(e) => setCode(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") void apply();
          }}
        />
        <Button variant="primary" loading={busy} disabled={busy || !code.trim()} onClick={() => void apply()}>
          {t("sub.accessApply")}
        </Button>
      </div>
      {done ? <p className="mt-3 text-xs text-[var(--leaf-600)]" role="status">{done}</p> : null}
      {error ? (
        <p className="mt-3 text-xs text-[var(--md-error)]" role="alert">
          {error}
        </p>
      ) : null}
    </section>
  );
}
