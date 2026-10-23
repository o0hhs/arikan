export type Platform = "ios" | "android" | "windows" | "macos" | "linux";

// The apps the page offers, one line each: where it comes from (site: the store or the
// project's own page — the download the buyer needs first) and how a subscription goes into
// it (link: the one-tap import).
type App = {
  name: string;
  note: "easiest" | "free" | "stable" | "openSource" | "modern" | "bestWindows" | "tun" | "oneButton" | "allProtocols" | "official";
  site: string;
  link: (url: string, brand: string, happ?: string) => string;
};
export const enc = encodeURIComponent;
const clash = (url: string, brand: string) => `clash://install-config?url=${enc(url)}&name=${enc(brand)}`;

// Happ opens the crypt link when the panel gives one: the address stays hidden.
const happ = (u: string, _brand: string, crypt?: string) => crypt || `happ://add/${u}`;

// Where each app is downloaded: the only three the page names now.
export const SITES = {
  happ: "https://happproxy.com/",
  happAppStore: "https://apps.apple.com/app/happ-proxy-utility/id6504287215",
  happGooglePlay: "https://play.google.com/store/apps/details?id=com.happproxy",
  incy: "https://incy.app/",
  kabosu: "https://github.com/getmikan/kabosu",
};

export const APPS: Record<Platform, App[]> = {
  ios: [
    { name: "Happ", note: "easiest", site: SITES.happAppStore, link: happ },
    { name: "Incy", note: "modern", site: SITES.incy, link: (u) => `incy://add/${u}` },
  ],
  android: [
    { name: "Happ", note: "easiest", site: SITES.happGooglePlay, link: happ },
    { name: "Incy", note: "modern", site: SITES.incy, link: (u) => `incy://add/${u}` },
  ],
  windows: [{ name: "Kabosu", note: "official", site: SITES.kabosu, link: clash }],
  macos: [{ name: "Kabosu", note: "official", site: SITES.kabosu, link: clash }],
  linux: [{ name: "Kabosu", note: "official", site: SITES.kabosu, link: clash }],
};

export function detect(): Platform {
  const ua = navigator.userAgent;
  if (/iPhone|iPad|iPod/.test(ua)) return "ios";
  if (/Android/.test(ua)) return "android";
  if (/Mac OS X/.test(ua)) return "macos";
  if (/Windows/.test(ua)) return "windows";
  if (/Linux|X11|CrOS/.test(ua)) return "linux"; // after Android: its browsers say Linux too
  return "android";
}
