// The panel's theme before the first paint, so a dark theme never flashes light. A plain
// file, not inline: the panel's CSP allows scripts from itself only. The lists are the
// ones in src/components/theme.tsx, which takes over once the app runs.
(function () {
    /* gen:begin — rewritten by scripts/gen-m3-themes.mjs */
  var dark = ["midnight", "graphite", "abyss", "ember", "plum", "moss", "terminal", "nord", "mocha", "tokyo", "dracula", "aurora", "cosmos", "material-dark", "material-blue-dark", "material-teal-dark", "material-green-dark", "material-rose-dark", "material-expressive-dark"];
  var all = ["mikan", "ocean", "sakura", "forest", "latte", "snow", "dawn", "dune", "material", "material-blue", "material-teal", "material-green", "material-rose", "material-expressive"].concat(dark);
  /* gen:end */
  var theme = "mikan";
  try {
    var saved = localStorage.getItem("mikan-theme");
    if (saved === "system") {
      theme = window.matchMedia && matchMedia("(prefers-color-scheme: dark)").matches ? "midnight" : "mikan";
    } else if (all.indexOf(saved) >= 0) {
      theme = saved;
    }
  } catch (e) {
    // Storage blocked: the default theme.
  }
  var root = document.documentElement;
  root.setAttribute("data-theme", theme);
  root.setAttribute("data-tone", dark.indexOf(theme) >= 0 ? "dark" : "light");
  // The performance mode (src/lib/perf.ts) before the first paint, like the theme: no
  // animation ever draws when the device asked to skip them.
  try {
    if (localStorage.getItem("mikan-perf") === "1") root.setAttribute("data-perf", "1");
  } catch (e) {}
  // The panel's font (src/lib/font.ts): the attribute swaps the stacks in app.css; a saved
  // "roboto" or "system" reaches the first paint, so no text reflows once the app runs.
  try {
    var font = localStorage.getItem("mikan-font");
    if (font === "roboto" || font === "system") root.setAttribute("data-font", font);
  } catch (e) {}
})();
