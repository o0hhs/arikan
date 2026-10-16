import { useSyncExternalStore } from "react";

/*
 * The panel's font, kept in this browser like the theme (components/theme.tsx) and the
 * performance mode (lib/perf.ts). "theme" leaves the choice to the palette — every theme
 * names its own stacks, and the Material ones are typed in Roboto; "roboto" is that face
 * everywhere, the one Material 3 on Android is drawn with; "system" is the device's own.
 *
 * The chosen value goes on <html> as data-font, where app.css swaps the two stacks, and
 * public/theme-boot.js applies it before the first paint so no text reflows on load.
 */

const KEY = "mikan-font";

export const FONTS = ["theme", "roboto", "system"] as const;
export type Font = (typeof FONTS)[number];

export function getFont(): Font {
  try {
    const saved = window.localStorage.getItem(KEY);
    return (FONTS as readonly string[]).includes(saved ?? "") ? (saved as Font) : "theme";
  } catch {
    return "theme";
  }
}

/** Puts the choice on <html>; "theme" takes the attribute off and lets the palette decide. */
export function applyFont(font: Font) {
  const root = document.documentElement;
  if (font === "theme") root.removeAttribute("data-font");
  else root.setAttribute("data-font", font);
}

const listeners = new Set<() => void>();

export function setFont(font: Font) {
  try {
    if (font === "theme") window.localStorage.removeItem(KEY);
    else window.localStorage.setItem(KEY, font);
  } catch {
    // Storage blocked: only this visit sees the choice.
  }
  applyFont(font);
  for (const f of listeners) f();
}

// Applied as this module loads, so a saved choice is in place before the panel renders.
applyFont(getFont());

function subscribe(f: () => void) {
  listeners.add(f);
  return () => {
    listeners.delete(f);
  };
}

/** The font of this browser, and its switch; a change re-renders whoever reads it. */
export function useFont(): [Font, (font: Font) => void] {
  const font = useSyncExternalStore(subscribe, getFont);
  return [font, setFont];
}
