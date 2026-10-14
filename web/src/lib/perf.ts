import { useSyncExternalStore } from "react";

/*
 * Performance mode: fewer animations, no glass blur and quieter polling, for the device
 * the admin panel runs on (an old laptop, a phone). Like the theme (components/theme.tsx),
 * it is this browser's own choice kept in localStorage and applied to <html> — before the
 * first render here, and from public/theme-boot.js before the first paint.
 */

const KEY = "mikan-perf";
const ON = "1";

/** poll() stretches a background poll while the mode is on: whose frequency costs frames. */
const QUIET = 3;

export function getPerf(): boolean {
  try {
    return window.localStorage.getItem(KEY) === ON;
  } catch {
    return false;
  }
}

// The listeners of usePerf; a change here is rare, so the notify set stays plain.
const listeners = new Set<() => void>();

export function setPerf(on: boolean) {
  try {
    if (on) {
      window.localStorage.setItem(KEY, ON);
    } else {
      window.localStorage.removeItem(KEY);
    }
  } catch {
    // Storage blocked: only this visit sees the toggle.
  }
  applyPerf(on);
  for (const f of listeners) f();
}

/** Puts the state on <html>, where the CSS drops the animations and the glass effects. */
export function applyPerf(on: boolean) {
  if (on) {
    document.documentElement.setAttribute("data-perf", ON);
  } else {
    document.documentElement.removeAttribute("data-perf");
  }
}

// Applied as this module loads: everything mounted after (main.tsx renders at once) grows
// without the animations, and a saved mode flickers nothing.
applyPerf(getPerf());

function subscribe(f: () => void) {
  listeners.add(f);
  return () => {
    listeners.delete(f);
  };
}

/** The mode of this browser, and its switch; the toggle re-renders whoever reads it. */
export function usePerf(): [boolean, (on: boolean) => void] {
  const on = useSyncExternalStore(subscribe, getPerf);
  return [on, setPerf];
}

/**
 * A background poll's interval in the mode: the panel's data still comes, just less often
 * (the numbers on open pages do not need the second-guessing of a redraw anyway). Active
 * operations poll conditioned on their progress and never go through here.
 */
export function poll(ms: number): number {
  return getPerf() ? ms * QUIET : ms;
}
