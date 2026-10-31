import { M3eTab, M3eTabs } from "@m3e/react/tabs";
import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

type Tab<T extends string> = { id: T; label: string; icon?: LucideIcon };

/**
 * A page's few sections (up to four) as M3E tabs. The caller keeps the value, usually in
 * the URL so a link opens the section; the panel's content goes in children.
 */
export function Tabs<T extends string>({ id, tabs, value, onChange, label, children }: { id: string; tabs: Tab<T>[]; value: T; onChange: (v: T) => void; label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-4">
      <M3eTabs aria-label={label}>
        {tabs.map(({ id: tab, label: text, icon: Icon }) => (
          <M3eTab key={tab} selected={value === tab} id={`${id}-tab-${tab}`} aria-controls={`${id}-panel`} onClick={() => onChange(tab)}>
            {Icon ? <Icon slot="icon" size={16} aria-hidden /> : null}
            {text}
          </M3eTab>
        ))}
      </M3eTabs>
      <div id={`${id}-panel`} role="tabpanel" aria-labelledby={`${id}-tab-${value}`} className="flex min-w-0 flex-col gap-4">
        {children}
      </div>
    </div>
  );
}
