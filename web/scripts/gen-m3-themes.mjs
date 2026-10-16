/**
 * Generates the Material themes — web/src/styles/themes-m3.css and the three theme lists —
 * from Google's own colour algorithm: the Material colour utilities (the HCT colour space,
 * its tonal palettes and the dynamic scheme that maps them to the M3 roles). Each theme is
 * one seed colour and one of the algorithm's variants, chosen here.
 *
 *   node scripts/gen-m3-themes.mjs          # from web/, after `pnpm install`
 *
 * The output is committed, so a checkout needs nothing generated; run this only when a seed,
 * a variant or a role changes. The script refuses to write anything if the algorithm stops
 * matching the values Material publishes for the baseline purple — a version bump that
 * redefines the scheme then fails here instead of silently repainting the panel.
 *
 * The palettes the panel's own tokens need (the ink ramp, the accent ramp, the status ramps)
 * are read from the same HCT tonal palettes: tone 95 is the palest tint, tone 30 the deepest
 * ink. The M3 roles are the algorithm's, not a derivation — those themes are the real thing.
 */
import { register } from "node:module";
import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

register(new URL("./mcu-resolve.mjs", import.meta.url));
const mcu = await import("@material/material-color-utilities");

const here = (path) => fileURLToPath(new URL(path, import.meta.url));

/** The seeds: the Material baseline purple, four of Google's own palette seeds, and the
 *  expressive variant of the baseline. `id` is the theme name, `title` its key in the
 *  dictionaries (settings.theme<Name>). */
const SEEDS = [
  { id: "material", seed: "#6750A4", variant: "tonal", title: "Material" },
  { id: "material-blue", seed: "#0B57D0", variant: "tonal", title: "MaterialBlue" },
  { id: "material-teal", seed: "#006A6A", variant: "tonal", title: "MaterialTeal" },
  { id: "material-green", seed: "#386A20", variant: "tonal", title: "MaterialGreen" },
  { id: "material-rose", seed: "#9C4146", variant: "tonal", title: "MaterialRose" },
  { id: "material-expressive", seed: "#6750A4", variant: "expressive", title: "MaterialExpressive" },
];

/** The status ramps have no M3 role of their own: each is a plain hue at a fixed chroma. */
const STATUS = {
  leaf: { hue: 142, chroma: 48 },   // good
  honey: { hue: 85, chroma: 60 },   // warning
  berry: { hue: 25, chroma: 60 },   // the error hue, for the ramp the panel already had
  lagoon: { hue: 258, chroma: 48 }, // info — the blue side
};

const hex = (argb) => mcu.hexFromArgb(argb);
const rgb = (argb) => {
  const v = argb >>> 0;
  return { r: (v >>> 16) & 255, g: (v >>> 8) & 255, b: v & 255 };
};
const alpha = (argb, a) => {
  const { r, g, b } = rgb(argb);
  return `rgba(${r}, ${g}, ${b}, ${a})`;
};
const mix = (argb, pct) => `color-mix(in srgb, ${hex(argb)} ${pct}%, transparent)`;

/* ---------- the check: the algorithm still says what Material says ---------- */

function check() {
  const t = mcu.themeFromSourceColor(mcu.argbFromHex("#6750A4"));
  const near = (name, got, want, tol = 3) => {
    const a = rgb(mcu.argbFromHex(got));
    const b = rgb(mcu.argbFromHex(want));
    const off = Math.max(Math.abs(a.r - b.r), Math.abs(a.g - b.g), Math.abs(a.b - b.b));
    if (off > tol) throw new Error(`the colour algorithm moved: ${name} is ${got}, Material publishes ${want}`);
  };
  // The tonal spot keeps the source at tone 40, and the roles are the published baseline.
  near("primary tone 40", hex(t.palettes.primary.tone(40)), "#6750A4");
  near("neutral-variant tone 50", hex(t.palettes.neutralVariant.tone(50)), "#7A757F");
  near("neutral tone 95", hex(t.palettes.neutral.tone(95)), "#F4EFF4");
  near("error tone 40", hex(t.palettes.error.tone(40)), "#BA1A1A");
  const light = new mcu.SchemeTonalSpot(mcu.Hct.fromInt(mcu.argbFromHex("#6750A4")), false, 0);
  near("on-primary", hex(mcu.MaterialDynamicColors.onPrimary.getArgb(light)), "#FFFFFF", 0);
  near("surface container highest", hex(mcu.MaterialDynamicColors.surfaceContainerHighest.getArgb(light)), "#E6E0E9");
  console.log("the algorithm matches Material's published baseline");
}

/* ---------- the scheme of one seed ---------- */

function palette(hue, chroma) {
  return mcu.TonalPalette.fromHueAndChroma(hue, chroma);
}

function schemeFor(seed, variant, dark) {
  const hct = mcu.Hct.fromInt(mcu.argbFromHex(seed));
  if (variant === "expressive") return new mcu.SchemeExpressive(hct, dark, 0);
  return new mcu.SchemeTonalSpot(hct, dark, 0);
}

/** Every role the panel's tokens name, in the order the block writes them. */
const ROLE_TOKENS = [
  ["primary", "--md-primary"], ["onPrimary", "--md-on-primary"],
  ["primaryContainer", "--md-primary-container"], ["onPrimaryContainer", "--md-on-primary-container"],
  ["secondary", "--md-secondary"], ["onSecondary", "--md-on-secondary"],
  ["secondaryContainer", "--md-secondary-container"], ["onSecondaryContainer", "--md-on-secondary-container"],
  ["tertiary", "--md-tertiary"], ["onTertiary", "--md-on-tertiary"],
  ["tertiaryContainer", "--md-tertiary-container"], ["onTertiaryContainer", "--md-on-tertiary-container"],
  ["error", "--md-error"], ["onError", "--md-on-error"],
  ["errorContainer", "--md-error-container"], ["onErrorContainer", "--md-on-error-container"],
  ["surface", "--md-surface"], ["onSurface", "--md-on-surface"],
  ["surfaceVariant", "--md-surface-variant"], ["onSurfaceVariant", "--md-on-surface-variant"],
  ["surfaceContainerLowest", "--md-surface-container-lowest"], ["surfaceContainerLow", "--md-surface-container-low"],
  ["surfaceContainer", "--md-surface-container"], ["surfaceContainerHigh", "--md-surface-container-high"],
  ["surfaceContainerHighest", "--md-surface-container-highest"],
  ["outline", "--md-outline"], ["outlineVariant", "--md-outline-variant"],
  ["inverseSurface", "--md-inverse-surface"], ["inverseOnSurface", "--md-inverse-on-surface"],
  ["inversePrimary", "--md-inverse-primary"], ["scrim", "--md-scrim"],
];

function block({ seed, variant }, dark) {
  const s = schemeFor(seed, variant, dark);
  const role = (name) => mcu.MaterialDynamicColors[name].getArgb(s);
  const nv = (tone) => s.neutralVariantPalette.tone(tone);
  const n = (tone) => s.neutralPalette.tone(tone);
  const p = (tone) => s.primaryPalette.tone(tone);
  const sec = (tone) => s.secondaryPalette.tone(tone);
  const ter = (tone) => s.tertiaryPalette.tone(tone);
  const status = Object.fromEntries(
    Object.entries(STATUS).map(([name, { hue, chroma }]) => [name, palette(hue, chroma)]),
  );

  // The ink ramp is the palette's own neutral-variant tones — the same ladder Material walks
  // for on-surface, on-surface-variant and the outlines; on a dark theme it reads upside down.
  const ink = dark
    ? [nv(95), nv(90), nv(80), nv(70), nv(60), nv(50), nv(30), nv(25), nv(20), nv(15)]
    : [nv(10), nv(20), nv(25), nv(30), nv(40), nv(50), nv(70), nv(80), nv(90), nv(95)];
  const accent = dark
    ? [p(30), p(35), p(45), p(70), p(80), p(85), p(90)]
    : [p(95), p(90), p(80), p(60), p(50), p(40), p(30)];

  // The status ramps, light and dark: the container tones the panel's own themes use.
  const st = dark
    ? { leaf: [status.leaf.tone(30), status.leaf.tone(80), status.leaf.tone(90)],
        honey: [status.honey.tone(30), status.honey.tone(80), status.honey.tone(90)],
        berry: [status.berry.tone(30), status.berry.tone(80)],
        lagoon: [status.lagoon.tone(30), status.lagoon.tone(70), status.lagoon.tone(80), status.lagoon.tone(90)] }
    : { leaf: [status.leaf.tone(90), status.leaf.tone(40), status.leaf.tone(30)],
        honey: [status.honey.tone(90), status.honey.tone(40), status.honey.tone(30)],
        berry: [status.berry.tone(90), status.berry.tone(40)],
        lagoon: [status.lagoon.tone(90), status.lagoon.tone(60), status.lagoon.tone(50), status.lagoon.tone(40)] };

  const tokens = [];
  const push = (name, value) => tokens.push([name, value]);

  // Type: the Material themes are typed in the face Material 3 is drawn in.
  push("--font-display-stack", `"Roboto Flex Variable", Roboto, system-ui, sans-serif`);
  push("--font-ui-stack", `"Roboto Flex Variable", Roboto, system-ui, sans-serif`);

  push("--bg", hex(role("surface")));
  ["--ink-900", "--ink-800", "--ink-700", "--ink-600", "--ink-500", "--ink-400", "--ink-300", "--ink-200", "--ink-100", "--ink-50"].forEach((name, i) => push(name, hex(ink[i])));
  push("--fill", hex(role("primary")));
  push("--on-fill", hex(role("onPrimary")));
  push("--on-danger", dark ? hex(n(10)) : "#ffffff");
  push("--code-bg", hex(role("inverseSurface")));
  push("--code-fg", hex(role("inverseOnSurface")));

  ["--mikan-50", "--mikan-100", "--mikan-200", "--mikan-400", "--mikan-500", "--mikan-600", "--mikan-700"].forEach((name, i) => push(name, hex(accent[i])));
  push("--leaf-50", hex(st.leaf[0])); push("--leaf-500", hex(st.leaf[1])); push("--leaf-700", hex(st.leaf[2]));
  push("--honey-50", hex(st.honey[0])); push("--honey-500", hex(st.honey[1])); push("--honey-600", hex(st.honey[2]));
  push("--berry-50", hex(st.berry[0])); push("--berry-600", hex(st.berry[1]));
  push("--lagoon-50", hex(st.lagoon[0])); push("--lagoon-400", hex(st.lagoon[1])); push("--lagoon-500", hex(st.lagoon[2])); push("--lagoon-600", hex(st.lagoon[3]));
  push("--fc-blue", hex(status.lagoon.tone(dark ? 80 : 40)));
  push("--fc-purple", hex(sec(dark ? 80 : 40)));
  push("--fc-pink", hex(ter(dark ? 80 : 40)));

  // The surfaces the components used to take from the glass tokens: the M3 containers, the
  // translucent ones mixed from them, and no painted highlight — a Material card is flat.
  push("--surface", mix(role("surfaceContainer"), 86));
  push("--surface-soft", mix(role("surfaceContainerLow"), 70));
  push("--surface-hover", mix(role("surfaceContainerHigh"), 94));
  push("--surface-solid", hex(role("surfaceContainer")));
  push("--raised", hex(role("surfaceContainerHigh")));
  push("--glass", mix(role("surfaceContainer"), dark ? 72 : 60));
  push("--glass-strong", mix(role("surfaceContainerHigh"), dark ? 92 : 86));
  push("--glass-highlight", "transparent");
  push("--glass-edge", hex(role("outlineVariant")));
  push("--hairline", hex(role("outlineVariant")));
  push("--hover", alpha(role("onSurface"), 0.05));
  push("--ring", alpha(role("primary"), 0.2));
  push("--scrim", `rgba(0, 0, 0, ${dark ? 0.5 : 0.32})`);
  push("--shadow-sm", `0 1px 2px rgba(0, 0, 0, ${dark ? 0.3 : 0.07})`);
  push("--shadow-md", `0 1px 2px rgba(0, 0, 0, ${dark ? 0.24 : 0.05}), 0 12px 32px -14px rgba(0, 0, 0, ${dark ? 0.5 : 0.18})`);
  push("--shadow-lg", `0 2px 8px rgba(0, 0, 0, ${dark ? 0.28 : 0.07}), 0 32px 64px -24px rgba(0, 0, 0, ${dark ? 0.6 : 0.3})`);

  // The backdrop stays, in the seed's own hues: the same five glows the other themes have,
  // washed from the tonal palettes rather than painted by hand.
  push("--glow-a", alpha(dark ? p(40) : p(60), dark ? 0.44 : 0.5));
  push("--glow-b", alpha(dark ? sec(30) : sec(70), dark ? 0.3 : 0.42));
  push("--glow-c", alpha(dark ? ter(40) : ter(70), dark ? 0.38 : 0.46));
  push("--glow-d", alpha(dark ? ter(20) : ter(50), dark ? 0.34 : 0.36));
  push("--glow-e", alpha(dark ? sec(20) : status.lagoon.tone(50), dark ? 0.3 : 0.4));
  push("--glow-opacity", dark ? "0.45" : "0.7");
  push("--grain-blend", dark ? "overlay" : "multiply");
  push("--grain-opacity", dark ? "0.04" : "0.045");
  push("--pattern", "none");
  push("--pattern-size", "auto");
  push("--pattern-opacity", "0");
  push("--pattern-blur", "0px");

  for (const [name, token] of ROLE_TOKENS) push(token, hex(role(name)));
  push("--wash", hex(role("surfaceContainerHigh")));

  const pad = Math.max(...tokens.map(([name]) => name.length)) + 1;
  return tokens.map(([name, value]) => `  ${(name + ":").padEnd(pad)} ${value};`).join("\n");
}

/* ---------- the file ---------- */

function css() {
  const entries = [];
  for (const s of SEEDS) {
    entries.push({ id: `${s.id}`, dark: false, seed: s });
    entries.push({ id: `${s.id}-dark`, dark: true, seed: s });
  }
  const header = `/*
 * The Material themes, generated by scripts/gen-m3-themes.mjs — do not edit by hand.
 *
 * Every block is one seed colour walked through Google's own algorithm (the Material colour
 * utilities: HCT, the tonal palettes and the dynamic scheme), so the roles here are the ones
 * Material Theme Builder would give for that seed, not a derivation — and the palettes, the
 * surfaces and the atmosphere are built from the same tones. The panel's own themes are the
 * other way round (styles/themes.css), which is why this layer lives in its own file: the
 * panel imports it, the subscription page does not need it.
 *
 * Variants: ${SEEDS.map((s) => `${s.id}=${s.variant}(${s.seed})`).join(", ")}.
 * A theme is named <id>, its dark half <id>-dark; both are in the panel's theme lists
 * (src/lib/themes.ts, public/theme-boot.js), which this script rewrites as well.
 */

`;
  const body = entries
    .map(({ id, dark, seed }) => {
      const title = dark ? `${seed.title}Dark` : seed.title;
      return `/* ${title} — seed ${seed.seed}, ${seed.variant}${dark ? ", dark" : ""} */\n[data-theme="${id}"] {\n  color-scheme: ${dark ? "dark" : "light"};\n${block(seed, dark)}\n}`;
    })
    .join("\n\n");
  return header + body + "\n";
}

/* ---------- the lists, kept in one place ---------- */

const LISTS = {
  "src/lib/themes.ts": (light, dark, m3) => [
    "/* gen:begin — rewritten by scripts/gen-m3-themes.mjs (the Material themes it generates) */",
    `export const LIGHT_THEMES = [${light.map((t) => `"${t}"`).join(", ")}] as const;`,
    `export const DARK_THEMES = [${dark.map((t) => `"${t}"`).join(", ")}] as const;`,
    "/**",
    " * The generated Material themes, light and dark side by side (the picker groups them",
    " * together, and the subscription page leaves them out: its palette is a Go enum).",
    " */",
    `export const M3_THEMES = [${m3.map((t) => `"${t}"`).join(", ")}] as const;`,
    "/* gen:end */",
  ].join("\n"),
  "public/theme-boot.js": (light, dark) => [
    "  /* gen:begin — rewritten by scripts/gen-m3-themes.mjs */",
    `  var dark = [${dark.map((t) => `"${t}"`).join(", ")}];`,
    `  var all = [${light.map((t) => `"${t}"`).join(", ")}].concat(dark);`,
    "  /* gen:end */",
  ].join("\n"),
};

function patch(path, generated) {
  const file = here(`../${path}`);
  const source = readFileSync(file, "utf8");
  const begin = source.indexOf("/* gen:begin");
  // The end marker may sit at any indentation inside its file (theme-boot.js indents it).
  const endMatch = /\n[ \t]*\/\* gen:end \*\//.exec(source.slice(Math.max(begin, 0)));
  if (begin < 0 || !endMatch) throw new Error(`${path}: the gen:begin/gen:end markers are gone`);
  const end = Math.max(begin, 0) + endMatch.index;
  const next = source.slice(0, begin) + generated + source.slice(end + endMatch[0].length);
  if (next === source) return false;
  writeFileSync(file, next);
  return true;
}

/* ---------- run ---------- */

check();

const handLight = ["mikan", "ocean", "sakura", "forest", "latte", "snow", "dawn", "dune"];
const handDark = ["midnight", "graphite", "abyss", "ember", "plum", "moss", "terminal", "nord", "mocha", "tokyo", "dracula", "aurora", "cosmos"];
const m3Light = SEEDS.map((s) => s.id);
const m3Dark = SEEDS.map((s) => `${s.id}-dark`);

writeFileSync(here("../src/styles/themes-m3.css"), css());
const changedThemes = patch("src/lib/themes.ts", LISTS["src/lib/themes.ts"](
  [...handLight, ...m3Light],
  [...handDark, ...m3Dark],
  [...m3Light, ...m3Dark],
));
const changedBoot = patch("public/theme-boot.js", LISTS["public/theme-boot.js"](
  [...handLight, ...m3Light],
  [...handDark, ...m3Dark],
));
console.log(`themes: ${SEEDS.length * 2} generated (${SEEDS.map((s) => s.id).join(", ")})`);
console.log(`lists: src/lib/themes.ts ${changedThemes ? "rewritten" : "already in place"}, public/theme-boot.js ${changedBoot ? "rewritten" : "already in place"}`);
console.log("next: check-contrast.mjs reads the new file; the picker needs settings.theme<Name> keys, and themeNames() in components/theme.tsx lists them.");
