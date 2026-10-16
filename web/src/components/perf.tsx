import clsx from "clsx";
import { Gauge } from "lucide-react";
import { t } from "../i18n";
import { SwitchRow } from "./switch";
import { usePerf } from "../lib/perf";

/**
 * The performance mode's switch in the sidebar: one tap for the device the panel is
 * opened on, the state of this browser alone.
 */
export function PerfSwitch({ className }: { className?: string }) {
  const [on, set] = usePerf();
  return (
    <button
      type="button"
      className={clsx("icon-btn perf-btn", className)}
      aria-pressed={on}
      title={on ? t("perf.onTitle") : t("perf.offTitle")}
      aria-label={t("perf.title")}
      onClick={() => set(!on)}
    >
      <Gauge size={18} aria-hidden />
    </button>
  );
}

/** The same switch where the look of the panel is set — beside the themes. */
export function PerfCard() {
  const [on, set] = usePerf();
  return (
    <section className="card glass reveal" style={{ "--i": 5 } as React.CSSProperties}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("perf.title")}</h2>
          <div className="card-sub">{t("perf.sub")}</div>
        </div>
      </div>
      <ul className="row-list">
        <li>
          <SwitchRow label={t("perf.switch")} sub={t("perf.hint")} checked={on} onChange={set} />
        </li>
      </ul>
      <p className="mt-3 text-xs text-[var(--md-on-surface-variant)]">{t("perf.note")}</p>
    </section>
  );
}
