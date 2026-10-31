import { M3eTheme, type M3eThemeElement } from "@m3e/react/theme";
import { useEffect, useRef, type ReactNode } from "react";
import { usePerf } from "../lib/perf";

/**
 * The panel's pages are a Vite React app embedded in the Go binary. M3E is bundled with that
 * app (not loaded from a CDN — the panel's CSP only allows scripts and fonts from itself).
 * One theme element wraps each document: dynamic color from the palette the admin already
 * picked, the expressive color variant, and expressive motion unless this browser asked for
 * the performance mode or less motion.
 *
 * `sync` copies the generated roles onto the tokens the pages already read, so every screen
 * follows the theme. The public subscription page leaves `sync` off: its look is the one the
 * admin designed, and only the controls take the expressive components.
 */

const ROLES: [string, string][] = [
  ["--md-primary", "--md-sys-color-primary"],
  ["--md-on-primary", "--md-sys-color-on-primary"],
  ["--md-primary-container", "--md-sys-color-primary-container"],
  ["--md-on-primary-container", "--md-sys-color-on-primary-container"],
  ["--md-secondary", "--md-sys-color-secondary"],
  ["--md-on-secondary", "--md-sys-color-on-secondary"],
  ["--md-secondary-container", "--md-sys-color-secondary-container"],
  ["--md-on-secondary-container", "--md-sys-color-on-secondary-container"],
  ["--md-tertiary", "--md-sys-color-tertiary"],
  ["--md-on-tertiary", "--md-sys-color-on-tertiary"],
  ["--md-tertiary-container", "--md-sys-color-tertiary-container"],
  ["--md-on-tertiary-container", "--md-sys-color-on-tertiary-container"],
  ["--md-error", "--md-sys-color-error"],
  ["--md-on-error", "--md-sys-color-on-error"],
  ["--md-error-container", "--md-sys-color-error-container"],
  ["--md-on-error-container", "--md-sys-color-on-error-container"],
  ["--md-surface", "--md-sys-color-surface"],
  ["--md-on-surface", "--md-sys-color-on-surface"],
  ["--md-surface-variant", "--md-sys-color-surface-variant"],
  ["--md-on-surface-variant", "--md-sys-color-on-surface-variant"],
  ["--md-surface-container-lowest", "--md-sys-color-surface-container-lowest"],
  ["--md-surface-container-low", "--md-sys-color-surface-container-low"],
  ["--md-surface-container", "--md-sys-color-surface-container"],
  ["--md-surface-container-high", "--md-sys-color-surface-container-high"],
  ["--md-surface-container-highest", "--md-sys-color-surface-container-highest"],
  ["--md-outline", "--md-sys-color-outline"],
  ["--md-outline-variant", "--md-sys-color-outline-variant"],
  ["--md-inverse-surface", "--md-sys-color-inverse-surface"],
  ["--md-inverse-on-surface", "--md-sys-color-inverse-on-surface"],
  ["--md-inverse-primary", "--md-sys-color-inverse-primary"],
];

function token(el: HTMLElement, name: string): string {
  return getComputedStyle(el).getPropertyValue(name).trim();
}

function toHex(value: string): string | null {
  const v = value.trim();
  if (/^#[0-9a-fA-F]{6}$/.test(v)) return v;
  if (/^#[0-9a-fA-F]{3}$/.test(v)) return `#${v[1]}${v[1]}${v[2]}${v[2]}${v[3]}${v[3]}`;
  const m = v.match(/^rgba?\(\s*(\d+)[,\s]+(\d+)[,\s]+(\d+)/i);
  if (!m) return null;
  const h = (n: string) => Number(n).toString(16).padStart(2, "0");
  return `#${h(m[1]!)}${h(m[2]!)}${h(m[3]!)}`;
}

/** The active palette's primary, read off a probe so a later inline role does not feed itself back in. */
function seedColor(): string {
  const root = document.documentElement;
  const probe = document.createElement("div");
  probe.setAttribute("data-theme", root.dataset.theme || "mikan");
  probe.setAttribute("data-tone", root.dataset.tone || "light");
  if (root.dataset.scheme) probe.setAttribute("data-scheme", root.dataset.scheme);
  probe.style.cssText = "position:absolute;left:-9999px;visibility:hidden";
  document.body.appendChild(probe);
  const raw = getComputedStyle(probe).getPropertyValue("--md-primary") || getComputedStyle(probe).getPropertyValue("--mikan-600");
  probe.remove();
  return toHex(raw) ?? toHex(getComputedStyle(root).getPropertyValue("--md-primary")) ?? "#6750A4";
}

function schemeOf(): "light" | "dark" {
  const root = document.documentElement;
  if (root.dataset.tone === "dark" || root.dataset.scheme === "dark") return "dark";
  return "light";
}

function syncTokens(theme: HTMLElement) {
  const root = document.documentElement;
  const set = (name: string, value: string) => {
    if (value) root.style.setProperty(name, value);
  };
  for (const [to, from] of ROLES) set(to, token(theme, from));
  const background = token(theme, "--md-sys-color-background");
  const surface = token(theme, "--md-sys-color-surface");
  const onSurface = token(theme, "--md-sys-color-on-surface");
  const onVariant = token(theme, "--md-sys-color-on-surface-variant");
  const outline = token(theme, "--md-sys-color-outline");
  const outlineVar = token(theme, "--md-sys-color-outline-variant");
  const low = token(theme, "--md-sys-color-surface-container-low");
  const mid = token(theme, "--md-sys-color-surface-container");
  const high = token(theme, "--md-sys-color-surface-container-high");
  const highest = token(theme, "--md-sys-color-surface-container-highest");
  const primary = token(theme, "--md-sys-color-primary");
  const onPrimary = token(theme, "--md-sys-color-on-primary");
  const container = token(theme, "--md-sys-color-primary-container");
  const onError = token(theme, "--md-sys-color-on-error");
  set("--bg", background || surface);
  set("--ink-900", onSurface);
  set("--ink-800", onSurface);
  set("--ink-700", onVariant);
  set("--ink-600", onVariant);
  set("--ink-500", outline);
  set("--ink-400", outline);
  set("--ink-300", outlineVar);
  set("--ink-200", highest);
  set("--ink-100", high);
  set("--ink-50", low);
  set("--fill", primary);
  set("--fill-hover", primary);
  set("--on-fill", onPrimary);
  set("--on-danger", onError);
  set("--surface", mid);
  set("--surface-soft", low);
  set("--surface-solid", mid);
  set("--surface-hover", high);
  set("--raised", high);
  set("--hairline", outlineVar);
  set("--glass-edge", outlineVar);
  set("--mikan-50", container);
  set("--mikan-100", container);
  set("--mikan-200", container);
  set("--mikan-400", primary);
  set("--mikan-500", primary);
  set("--mikan-600", primary);
  set("--mikan-700", primary);
  const bg = background || surface;
  if (bg) document.querySelector('meta[name="theme-color"]')?.setAttribute("content", bg);
}

export function M3eRoot({ sync = false, children }: { sync?: boolean; children: ReactNode }) {
  const ref = useRef<M3eThemeElement>(null);
  const [perf] = usePerf();
  useEffect(() => {
    const theme = ref.current;
    if (!theme) return;
    const root = document.documentElement;
    if (sync) root.dataset.m3e = "sync";
    else delete root.dataset.m3e;
    const apply = () => {
      theme.variant = "expressive";
      theme.strongFocus = true;
      theme.density = 0;
      const reduce = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
      theme.motion = perf || reduce ? "standard" : "expressive";
      theme.scheme = schemeOf();
      theme.color = seedColor();
    };
    apply();
    const syncNow = () => {
      if (sync) syncTokens(theme);
    };
    theme.addEventListener("change", syncNow);
    const timers = [0, 40, 200].map((ms) => window.setTimeout(syncNow, ms));
    const mo = new MutationObserver(apply);
    mo.observe(root, { attributes: true, attributeFilter: ["data-theme", "data-tone", "data-scheme"] });
    return () => {
      theme.removeEventListener("change", syncNow);
      for (const id of timers) window.clearTimeout(id);
      mo.disconnect();
    };
  }, [perf, sync]);
  return (
    <M3eTheme ref={ref} variant="expressive" motion="expressive" scheme="light" color="#6750A4" strongFocus>
      {children}
    </M3eTheme>
  );
}
