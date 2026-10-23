import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Dev: run the panel with MIKAN_DEV=1 on 127.0.0.1:2053 and admin path "dev-admin-path-0000".
const devAdminPath = process.env.MIKAN_DEV_ADMIN_PATH ?? "dev-admin-path-0000";

// The sections of the dictionaries each page outside the admin panel reads. They are a few
// percent of the text, and those pages should not download the rest: add a section here
// when such a page (or what it imports) starts using one (`pnpm dev` warns about a missing
// key).
const DICTS: Record<string, string[]> = {
  sub: ["common", "promocodes", "states", "sub", "tariffs", "time", "units"],
  monitor: ["common", "monitor", "time", "units"],
};

/** virtual:i18n-<page>-<lang>: the dictionary cut down to that page's sections. */
function cutDictionaries(): Plugin {
  const prefix = "virtual:i18n-";
  return {
    name: "mikan-i18n-cut",
    resolveId: (id) => {
      if (!id.startsWith(prefix)) return undefined;
      const [name, lang] = id.slice(prefix.length).split("-");
      return DICTS[name!] && (lang === "ru" || lang === "en") ? `\0${id}` : undefined;
    },
    load(id) {
      if (!id.startsWith(`\0${prefix}`)) return undefined;
      const [name, lang] = id.slice(1 + prefix.length).split("-");
      const file = fileURLToPath(new URL(`./src/i18n/${lang}.json`, import.meta.url));
      this.addWatchFile(file);
      const all = JSON.parse(readFileSync(file, "utf8")) as Record<string, unknown>;
      const part = Object.fromEntries(DICTS[name!]!.map((s) => [s, all[s]]));
      return `export default ${JSON.stringify(part)};`;
    },
  };
}

/**
 * Preloads the text font's faces: the browser finds them only after the CSS is parsed, and
 * until then (a phone on a slow network) the page is shown in the system font and then
 * jumps. Vite does not rewrite the hashed names in a hand-written <link>, so they are
 * taken from the finished bundle.
 */
function preloadFonts(files: string[]): Plugin {
  return {
    name: "mikan-preload-fonts",
    transformIndexHtml: {
      order: "post",
      handler: (_html, ctx) =>
        Object.keys(ctx.bundle ?? {})
          .filter((f) => files.some((n) => f.includes(n)))
          .map((f) => ({ tag: "link", attrs: { rel: "preload", as: "font", type: "font/woff2", crossorigin: "", href: `./${f}` }, injectTo: "head" as const })),
    },
  };
}

export default defineConfig({
  // Relative asset URLs: the Go server injects <base href="/<secret>/"> at runtime.
  base: "./",
  plugins: [react(), tailwindcss(), cutDictionaries(), preloadFonts(["onest-latin-wght", "onest-cyrillic-wght"])],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // Never inline assets as data: URIs — the CSP only allows fonts and scripts from 'self'.
    assetsInlineLimit: 0,
    rollupOptions: {
      input: { index: "index.html", sub: "sub.html", monitor: "monitor.html", agent: "agent.html" },
    },
  },
  server: {
    proxy: {
      "/api": {
        target: "http://127.0.0.1:2053",
        rewrite: (p) => `/${devAdminPath}${p}`,
      },
    },
  },
});
