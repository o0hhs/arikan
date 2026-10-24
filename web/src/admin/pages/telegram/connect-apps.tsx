// The screen that hands the subscription to an app («Подключить»): which clients are
// offered on which platforms, where they are downloaded from, and what the screen says. It
// is saved with the bot's config, so the admin decides what the buyer sees.
import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import type { Schemas } from "../../../api/client";
import { CardHead } from "../../../components/layout";
import { Switch, SwitchRow } from "../../../components/switch";
import { Button, Field } from "../../../components/ui";
import { t } from "../../../i18n";
import { rise, slide, type Config } from "./shared";

type App = Schemas["ConnectApp"];

const CLIENTS = ["happ", "incy", "kabosu", "clash", "mihomo"] as const;
const PLATFORMS = ["ios", "android", "desktop"] as const;
const MAX_APPS = 8;

export function ConnectAppsCard({ draft, setDraft }: { draft: Config; setDraft: (c: Config) => void }) {
  const connect = draft.connect;
  const apps = connect.apps ?? [];
  const reduce = useReducedMotion();
  const set = (patch: Partial<Schemas["ConnectConfig"]>) => setDraft({ ...draft, connect: { ...connect, apps, ...patch } });
  const setApp = (i: number, patch: Partial<App>) => set({ apps: apps.map((a, j) => (j === i ? { ...a, ...patch } : a)) });
  const move = (i: number, d: -1 | 1) => {
    const list = [...apps];
    [list[i], list[i + d]] = [list[i + d]!, list[i]!];
    set({ apps: list });
  };
  const remove = (i: number) => set({ apps: apps.filter((_, j) => j !== i) });
  const add = () => {
    const id = `app${Date.now().toString(36)}`;
    set({ apps: [...apps, { id, name: t("telegram.connectAppNew"), client: "happ", platforms: ["ios", "android"], download: "", primary: false, on: true }] });
  };
  const togglePlatform = (i: number, p: string) => {
    const a = apps[i]!;
    const platforms = a.platforms.includes(p) ? a.platforms.filter((x) => x !== p) : [...a.platforms, p];
    setApp(i, { platforms });
  };
  return (
    <section {...rise(5)}>
      <CardHead title={t("telegram.connectScreen")} sub={t("telegram.connectScreenSub")} />
      <SwitchRow label={t("telegram.connectOneScreen")} sub={t("telegram.connectOneScreenSub")} checked={connect.one_screen} onChange={(one_screen) => set({ one_screen })} />
      <Field label={t("telegram.connectIntro")} htmlFor="tg-connect-intro" hint={t("telegram.connectIntroHint")}>
        <textarea id="tg-connect-intro" className="input min-h-[64px]" rows={2} maxLength={400} value={connect.intro ?? ""} onChange={(e) => set({ intro: e.target.value })} />
      </Field>
      <ul className="mt-4 flex flex-col gap-2">
        <AnimatePresence initial={false}>
          {apps.map((a, i) => (
            <motion.li key={a.id} className="panel-soft p-3" layout={!reduce} initial={reduce ? false : { opacity: 0, scale: 0.96 }} animate={{ opacity: 1, scale: 1 }} exit={reduce ? { opacity: 0 } : { opacity: 0, scale: 0.96 }} transition={slide}>
              <div className="flex items-center gap-2">
                <Switch checked={a.on} label={t("telegram.connectAppShow", { name: a.name })} onChange={(on) => setApp(i, { on })} />
                <input className="input h-10 min-w-0 flex-1" value={a.name} maxLength={24} onChange={(e) => setApp(i, { name: e.target.value })} aria-label={t("telegram.connectAppName")} />
                <select className="input h-10 w-auto" value={a.client} onChange={(e) => setApp(i, { client: e.target.value as App["client"] })} aria-label={t("telegram.connectAppClient")}>
                  {CLIENTS.map((c) => (
                    <option key={c} value={c}>
                      {c}
                    </option>
                  ))}
                </select>
                <button type="button" className="icon-btn" disabled={i === 0} onClick={() => move(i, -1)} aria-label={t("telegram.moveUp")}>
                  <ArrowUp size={16} />
                </button>
                <button type="button" className="icon-btn" disabled={i === apps.length - 1} onClick={() => move(i, 1)} aria-label={t("telegram.moveDown")}>
                  <ArrowDown size={16} />
                </button>
                <button type="button" className="icon-btn" disabled={apps.length <= 1} onClick={() => remove(i)} aria-label={t("telegram.connectDeleteApp", { name: a.name })}>
                  <Trash2 size={16} />
                </button>
              </div>
              <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-[var(--ink-500)]">
                <span className="flex items-center gap-2" role="group" aria-label={t("telegram.connectAppPlatforms")}>
                  {PLATFORMS.map((p) => (
                    <label key={p} className="flex items-center gap-1.5">
                      <input type="checkbox" className="check" checked={a.platforms.includes(p)} onChange={() => togglePlatform(i, p)} /> {t(`telegram.platform.${p}`)}
                    </label>
                  ))}
                </span>
                <label className="flex items-center gap-1.5">
                  <input type="checkbox" className="check" checked={a.primary} onChange={(e) => setApp(i, { primary: e.target.checked })} /> {t("telegram.connectAppPrimary")}
                </label>
              </div>
              <input className="input mono mt-2 w-full" value={a.download ?? ""} maxLength={300} placeholder="https://" onChange={(e) => setApp(i, { download: e.target.value })} aria-label={t("telegram.connectAppDownload")} />
            </motion.li>
          ))}
        </AnimatePresence>
      </ul>
      <div className="mt-3">
        <Button size="sm" onClick={add} disabled={apps.length >= MAX_APPS}>
          <Plus size={16} aria-hidden /> {t("telegram.connectAddApp")}
        </Button>
      </div>
    </section>
  );
}
