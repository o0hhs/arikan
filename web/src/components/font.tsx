import { t } from "../i18n";
import { Segmented } from "./ui";
import { useFont, type Font } from "../lib/font";

/**
 * The panel's font, beside the themes: the palette's own (each theme names its stacks; the
 * Material ones are Roboto), Roboto Flex — the face Material 3 on Android is drawn in — or
 * the device's. The choice is this browser's, like the theme (lib/font.ts).
 */
export function FontCard() {
  const [font, set] = useFont();
  const options: { value: Font; label: string }[] = [
    { value: "theme", label: t("settings.fontTheme") },
    { value: "roboto", label: t("settings.fontRoboto") },
    { value: "system", label: t("settings.fontSystem") },
  ];
  return (
    <section className="card glass reveal" style={{ "--i": 4 } as React.CSSProperties}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.font")}</h2>
          <div className="card-sub">{t("settings.fontSub")}</div>
        </div>
      </div>
      <Segmented value={font} options={options} onChange={set} label={t("settings.font")} block />
    </section>
  );
}
