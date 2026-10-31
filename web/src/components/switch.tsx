import { M3eSwitch, type M3eSwitchElement } from "@m3e/react/switch";
import clsx from "clsx";
import { useId, type ReactNode } from "react";

/** An on/off switch. Kept out of ui.tsx: the subscription page has none and should not carry it. */
export function Switch({ checked, onChange, label, disabled, describedBy }: { checked: boolean; onChange: (v: boolean) => void; label: string; disabled?: boolean; describedBy?: string }) {
  return (
    <M3eSwitch
      checked={checked}
      disabled={disabled}
      aria-label={label}
      aria-describedby={describedBy}
      icons="none"
      onChange={(e) => onChange((e.target as M3eSwitchElement).checked)}
    />
  );
}

/** A row with a switch: what it is (and what it does) on the left, the switch on the right.
 * title is the row's head when it is not the switch's own name (a section: "Sale" over "On sale"). */
export function SwitchRow({
  label,
  title,
  sub,
  warn,
  checked,
  onChange,
  disabled,
  className,
}: {
  label: string;
  title?: string;
  sub?: ReactNode;
  warn?: boolean;
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
  className?: string;
}) {
  const id = useId();
  return (
    <div className={clsx("switch-row", className)}>
      <div className="min-w-0">
        <div className="switch-row-title">{title ?? label}</div>
        {sub ? (
          <div id={id} className={clsx("switch-row-sub", warn && "warn")}>
            {sub}
          </div>
        ) : null}
      </div>
      <Switch checked={checked} label={label} onChange={onChange} disabled={disabled} describedBy={sub ? id : undefined} />
    </div>
  );
}
