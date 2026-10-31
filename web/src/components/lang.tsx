import { M3eButton } from "@m3e/react/button";
import clsx from "clsx";
import { LOCALES, setLocale, t, useLocale } from "../i18n";

/** Compact RU/EN switch; the choice is remembered in this browser only. */
export function LangSwitch({ className }: { className?: string }) {
  const current = useLocale();
  return (
    <div className={clsx("lang-switch", className)} role="group" aria-label={t("common.language")}>
      {LOCALES.map((l) => (
        <M3eButton key={l.id} type="button" size="extra-small" variant={l.id === current ? "filled" : "text"} lang={l.id} title={l.label} aria-pressed={l.id === current} onClick={() => void setLocale(l.id)}>
          {l.id.toUpperCase()}
        </M3eButton>
      ))}
    </div>
  );
}
