// Dictionaries cut down for the pages outside the admin panel, made by the plugin in
// vite.config.ts (DICTS: which sections each one reads).
declare module "virtual:i18n-sub-ru" {
  const dict: Partial<typeof import("./i18n/ru.json")>;
  export default dict;
}
declare module "virtual:i18n-sub-en" {
  const dict: Partial<typeof import("./i18n/ru.json")>;
  export default dict;
}
declare module "virtual:i18n-monitor-ru" {
  const dict: Partial<typeof import("./i18n/ru.json")>;
  export default dict;
}
declare module "virtual:i18n-monitor-en" {
  const dict: Partial<typeof import("./i18n/ru.json")>;
  export default dict;
}
