// The bot's own buttons beyond the main menu: the trial, connect, renew and pay ones. An
// emoji changes only the sign at the start of the label, a text replaces the whole button.
import { RotateCcw } from "lucide-react";
import { CardHead, Disclosure } from "../../../components/layout";
import { Button } from "../../../components/ui";
import { t } from "../../../i18n";
import { rise, type Config } from "./shared";

// The ids the panel's tgbot package knows (internal/panel/tgbot/labels.go), grouped as the
// admin meets them. A key here that the panel does not know would be refused on save.
const GROUPS = [
  {
    title: "telegram.labels.groupScreens",
    keys: [
      "sub", "devices", "connect", "renew", "buy", "buy_another", "buy_traffic", "buy_device",
      "subscriptions", "trial", "free", "balance", "topup", "promo", "promo_clear", "new_link",
      "remove_sub", "referral", "ref_share", "ref_history",
    ],
  },
  {
    title: "telegram.labels.groupConnect",
    keys: ["platform_ios", "platform_android", "platform_desktop", "one_tap", "download", "install", "tg_proxy"],
  },
  { title: "telegram.labels.groupCommon", keys: ["back", "support", "open_page", "channel_join", "channel_check", "cancel"] },
] as const;

type Label = { emoji?: string; text?: string; emoji_id?: string };

/** The button's badge: its emoji, its whole label and a premium emoji's id. */
export function LabelsCard({ draft, setDraft }: { draft: Config; setDraft: (c: Config) => void }) {
  const labels: Record<string, Label> = (draft.labels ?? {}) as Record<string, Label>;
  const set = (key: string, patch: Label) => {
    const next: Record<string, Label> = { ...labels, [key]: { ...labels[key], ...patch } };
    const l = next[key]!;
    if (!l.emoji?.trim() && !l.text?.trim() && !l.emoji_id?.trim()) delete next[key];
    setDraft({ ...draft, labels: Object.keys(next).length ? next : undefined });
  };
  const changed = Object.keys(labels).length;
  return (
    <section {...rise(3)}>
      <CardHead title={t("telegram.labels.title")} sub={t("telegram.labels.sub")} />
      <p className="mb-4 text-xs text-[var(--md-on-surface-variant)]">{t("telegram.labels.hint")}</p>
      <div className="flex flex-col gap-3">
        {GROUPS.map((g) => (
          <Disclosure key={g.title} title={t(g.title)} sub={t("telegram.labels.changed", { n: g.keys.filter((k) => labels[k]).length })}>
            <div className="flex flex-col gap-3 pt-3">
              <div className="hidden gap-2 text-[11px] uppercase tracking-wide text-[var(--ink-500)] sm:grid sm:grid-cols-[minmax(0,1fr)_72px_minmax(0,1.4fr)_120px_auto]">
                <span>{t("telegram.labels.button")}</span>
                <span>{t("telegram.labels.emoji")}</span>
                <span>{t("telegram.labels.text")}</span>
                <span>{t("telegram.labels.premium")}</span>
                <span />
              </div>
              {g.keys.map((key) => {
                const l = labels[key] ?? {};
                return (
                  <div key={key} className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_72px_minmax(0,1.4fr)_120px_auto] sm:items-end">
                    <div className="text-[13px] font-medium">{t(`telegram.labels.key.${key}`)}</div>
                    <input
                      className="input num"
                      value={l.emoji ?? ""}
                      onChange={(e) => set(key, { emoji: e.target.value })}
                      maxLength={8}
                      aria-label={t("telegram.labels.emoji")}
                      placeholder="🎁"
                    />
                    <input
                      className="input"
                      value={l.text ?? ""}
                      onChange={(e) => set(key, { text: e.target.value })}
                      maxLength={40}
                      aria-label={t("telegram.labels.text")}
                      placeholder={t(`telegram.labels.key.${key}`)}
                    />
                    <input
                      className="input mono"
                      value={l.emoji_id ?? ""}
                      onChange={(e) => set(key, { emoji_id: e.target.value.replace(/[^0-9]/g, "") })}
                      maxLength={20}
                      aria-label={t("telegram.labels.premium")}
                      placeholder={t("telegram.labels.premium")}
                      inputMode="numeric"
                    />
                    <Button
                      type="button"
                      size="sm"
                      variant="ghost"
                      disabled={!labels[key]}
                      onClick={() => set(key, { emoji: "", text: "", emoji_id: "" })}
                      aria-label={t("telegram.labels.reset", { name: t(`telegram.labels.key.${key}`) })}
                    >
                      <RotateCcw size={14} aria-hidden />
                    </Button>
                  </div>
                );
              })}
            </div>
          </Disclosure>
        ))}
      </div>
      {changed ? (
        <p className="mt-3 text-xs text-[var(--ink-500)]" role="status">
          {t("telegram.labels.changed", { n: changed })}
        </p>
      ) : null}
    </section>
  );
}
