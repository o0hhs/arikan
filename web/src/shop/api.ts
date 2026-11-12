import { request } from "../sub/net";

/** The shop lives at /<sub path>/shop, beside a subscription token and the Mini App. */
export const shopMode = /\/shop$/.test(location.pathname.replace(/\/$/, ""));
const here = location.origin + location.pathname.replace(/\/$/, "");
export const shopRoot = shopMode ? here : here.replace(/\/[^/]+$/, "") + "/shop";

type Posted<T> = { ok: boolean; status: number; data: Partial<T> & { code?: string } };

async function call<T>(path: string, method?: string, body?: string): Promise<Posted<T>> {
  const r = await request(shopRoot + path, {
    method,
    body,
    credentials: "same-origin",
    headers: { "X-Mikan-Shop": "1", ...(body ? { "Content-Type": "application/json" } : {}) },
  });
  const data = (await r.json().catch(() => ({}))) as Posted<T>["data"];
  return { ok: r.ok, status: r.status, data };
}

export function shopGet<T>(path: string): Promise<Posted<T>> {
  return call<T>(path);
}

export function shopPost<T>(path: string, body?: unknown): Promise<Posted<T>> {
  return call<T>(path, "POST", body === undefined ? undefined : JSON.stringify(body));
}

export type ShopConfig = { brand: string; smtp: boolean; telegram_bot: string; shop_url: string };
export type ShopTerm = { days: number; rub: number; label: string };
export type ShopOffer = { id: number; name: string; note?: string; description: string; rub: number; terms: ShopTerm[] };
export type ShopPack = { id: number; title: string; slots: number; rub: number };
export type ShopSub = { token: string; name: string; tariff_id?: number; device_pack?: number };
export type ShopProvider = { id: string; name: string };
export type Offers = {
  allow_new: boolean;
  providers: ShopProvider[];
  offers: ShopOffer[];
  packs: ShopPack[];
  subs: ShopSub[];
  renew?: ShopOffer;
};
export type Me = { auth: boolean; email?: string; telegram_id?: number; subs?: ShopSub[]; balance?: number; balance_currency?: string; token?: string };
export type PayResult = { url?: string; status?: string; payload?: string; token?: string; name?: string; code?: string };
export type Order = { status: string; name?: string; token?: string; sub_name?: string; code?: string };
export type TgUser = { id: number; first_name: string; last_name?: string; username?: string; photo_url?: string; auth_date: number; hash: string };
