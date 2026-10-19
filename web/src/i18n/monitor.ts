import type { Dict, Loaders } from "./index";

// The monitoring page reads a few sections of the dictionaries (see DICTS in
// vite.config.ts, which builds these two modules): the rest would only be download.
export const monitorDicts: Loaders<Partial<Dict>> = {
  ru: () => import("virtual:i18n-monitor-ru"),
  en: () => import("virtual:i18n-monitor-en"),
};
