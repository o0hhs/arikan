// Checks that every panel theme keeps its text readable: WCAG AA, 4.5:1 for text, 3:1 for
// the faint labels and the accent's marks. Runs before the build. The tokens resolve the way
// the page does: the light base, then the dark base for a dark theme, then the theme's own
// block, then the M3 colour roles on top of all three. The roles are derivations (var(),
// color-mix(), a translucent layer over a surface) or the generated themes' own — so the
// resolving below is the browser's own arithmetic, narrowed to what the stylesheets write:
// hex, rgb()/rgba(), var() and a two-colour color-mix() in srgb. Anything else is an error
// rather than a silent pass, so a token that changes shape cannot slip through.
//
// The hand-written themes (styles/themes.css) and the generated Material ones
// (styles/themes-m3.css) are one set: the two files are read as one, which is also how the
// panel loads them.
import { readFileSync } from "node:fs";

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
const css = read("../src/styles/themes.css") + "\n" + read("../src/styles/themes-m3.css");

function block(selector) {
  const at = css.indexOf(selector + " {");
  if (at < 0) return {};
  const body = css.slice(css.indexOf("{", at) + 1, css.indexOf("}", at));
  const out = {};
  for (const m of body.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) out[m[1]] = m[2].trim();
  return out;
}

const light = block(":root, [data-tone=\"light\"]");
const dark = block("[data-tone=\"dark\"]");
const m3Light = block(":root, [data-tone=\"light\"], [data-theme], [data-scheme=\"light\"]");
const m3Dark = block("[data-tone=\"dark\"], [data-scheme=\"dark\"]");
const themes = [...css.matchAll(/^\[data-theme="([\w-]+)"\] \{/gm)].map((m) => m[1]);
// Which themes are dark comes from the list the generator rewrites (src/lib/themes.ts): a new
// theme cannot be checked against the wrong base, and the two files cannot drift apart.
const themesSource = read("../src/lib/themes.ts");
const darkThemes = new Set([.../export const DARK_THEMES = \[([^\]]*)\]/.exec(themesSource)[1].matchAll(/"([\w-]+)"/g)].map((m) => m[1]));

/** Splits color-mix arguments at the commas that belong to it, not to a nested function. */
function args(text) {
  const out = [];
  let depth = 0;
  let start = 0;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (c === "(") depth++;
    else if (c === ")") depth--;
    else if (c === "," && depth === 0) {
      out.push(text.slice(start, i));
      start = i + 1;
    }
  }
  out.push(text.slice(start));
  return out.map((s) => s.trim());
}

const TRANSPARENT = { r: 0, g: 0, b: 0, a: 0 };

/** A CSS colour as {r, g, b, a}, the way the browser would resolve it in this theme. */
function rgba(tokens, value, seen = new Set()) {
  const v = value.trim();
  const ref = /^var\(\s*(--[\w-]+)\s*(?:,(.*))?\)$/s.exec(v);
  if (ref) {
    const next = tokens[ref[1]] ?? ref[2]?.trim();
    if (next === undefined) throw new Error(`${ref[1]} is not set`);
    if (seen.has(ref[1])) throw new Error(`${ref[1]} refers to itself`);
    seen.add(ref[1]);
    return rgba(tokens, next, seen);
  }
  if (v === "transparent") return TRANSPARENT;
  const mix = /^color-mix\(\s*in\s+srgb\s*,(.*)\)$/s.exec(v);
  if (mix) {
    const [a, b] = args(mix[1]).map((part) => {
      const pct = /^([\s\S]*?)\s+([\d.]+)%$/.exec(part);
      return { value: (pct ? pct[1] : part).trim(), pct: pct ? Number(pct[2]) : null };
    });
    if (!a || !b) throw new Error(`${v} is not a two-colour mix`);
    const first = a.pct ?? (b.pct === null ? 50 : 100 - b.pct);
    const one = rgba(tokens, a.value, seen);
    const two = rgba(tokens, b.value, seen);
    const part = first / 100;
    return {
      r: one.r * part + two.r * (1 - part),
      g: one.g * part + two.g * (1 - part),
      b: one.b * part + two.b * (1 - part),
      a: one.a * part + two.a * (1 - part),
    };
  }
  const rgb = /^rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)\s*(?:,\s*([\d.]+)\s*)?\)$/.exec(v);
  if (rgb) return { r: Number(rgb[1]), g: Number(rgb[2]), b: Number(rgb[3]), a: rgb[4] === undefined ? 1 : Number(rgb[4]) };
  const hex = v.startsWith("#") ? v.slice(1) : "";
  if (/^[0-9a-f]{3}$/i.test(hex) || /^[0-9a-f]{6}$/i.test(hex)) {
    const full = hex.length === 3 ? [...hex].map((c) => c + c).join("") : hex;
    return { r: parseInt(full.slice(0, 2), 16), g: parseInt(full.slice(2, 4), 16), b: parseInt(full.slice(4, 6), 16), a: 1 };
  }
  throw new Error(`${v} is not a colour this script can resolve`);
}

/** `fg` as it lands on `bg`: a translucent colour is drawn over whatever is under it. */
function over(fg, bg) {
  return {
    r: fg.r * fg.a + bg.r * (1 - fg.a),
    g: fg.g * fg.a + bg.g * (1 - fg.a),
    b: fg.b * fg.a + bg.b * (1 - fg.a),
    a: 1,
  };
}

function luminance(c) {
  const [r, g, b] = [c.r, c.g, c.b].map((v) => {
    const s = v / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function ratio(a, b) {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}

// [text, background, minimum]. The first block is what the panel has always checked; the
// second is the Material 3 layer — every on-role against the surface it is painted on.
const pairs = [
  ["--ink-900", "--bg", 7],
  ["--ink-900", "--surface-solid", 7],
  ["--ink-700", "--surface-solid", 4.5],
  ["--ink-500", "--surface-solid", 4.5],
  ["--ink-500", "--bg", 4.5],
  ["--ink-400", "--surface-solid", 3],
  ["--on-fill", "--fill", 4.5],
  ["--mikan-700", "--surface-solid", 4.5],
  // The focus ring and the active icons; --mikan-500 fills bars that always have a number beside them.
  ["--mikan-600", "--surface-solid", 3],
  ["--leaf-700", "--leaf-50", 4.5],
  ["--honey-600", "--honey-50", 4.5],
  ["--berry-600", "--berry-50", 4.5],
  ["--lagoon-600", "--lagoon-50", 4.5],
  ["--berry-600", "--surface-solid", 4.5],
  ["--code-fg", "--code-bg", 7],

  ["--md-on-primary", "--md-primary", 4.5],
  ["--md-on-primary-container", "--md-primary-container", 4.5],
  ["--md-on-secondary", "--md-secondary", 4.5],
  ["--md-on-secondary-container", "--md-secondary-container", 4.5],
  ["--md-on-tertiary", "--md-tertiary", 4.5],
  ["--md-on-tertiary-container", "--md-tertiary-container", 4.5],
  ["--md-on-error", "--md-error", 4.5],
  ["--md-on-error-container", "--md-error-container", 4.5],
  ["--md-on-surface", "--md-surface-container", 7],
  ["--md-on-surface", "--md-surface-container-highest", 7],
  ["--md-on-surface-variant", "--md-surface-container", 4.5],
  ["--md-on-surface-variant", "--md-surface-container-highest", 4.5],
  // A control's outline is a non-text edge: 3:1 against the card it is drawn on. Material
  // specifies the outline against a surface, not against the highest container (which is a
  // field's fill, not its edge), and the generated dark palettes rely on exactly that.
  ["--md-outline", "--md-surface-container", 3],
  ["--md-inverse-on-surface", "--md-inverse-surface", 4.5],
  ["--md-inverse-primary", "--md-inverse-surface", 4.5],
];

let failed = 0;
const rows = [];
for (const name of ["mikan", ...themes]) {
  const tokens = { ...light, ...(darkThemes.has(name) ? dark : {}), ...block(`[data-theme="${name}"]`), ...(darkThemes.has(name) ? m3Dark : m3Light) };
  let worst = Infinity;
  for (const [fg, bg, min] of pairs) {
    const back = rgba(tokens, `var(${bg})`);
    if (back.a < 1) throw new Error(`${bg} is translucent: a text pair needs an opaque surface under it`);
    const r = ratio(over(rgba(tokens, `var(${fg})`), back), back);
    worst = Math.min(worst, r / min);
    if (r < min) {
      failed++;
      console.error(`contrast: ${name}: ${fg} on ${bg} is ${r.toFixed(2)}:1, needs ${min}:1`);
    }
  }
  rows.push(`${name} ${(worst * 100).toFixed(0)}%`);
}
if (failed) {
  console.error(`contrast: ${failed} pair(s) below WCAG AA`);
  process.exit(1);
}
console.log(`contrast: AA in every theme (closest to the limit: ${rows.join(", ")})`);
