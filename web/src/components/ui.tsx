import { M3eAvatar } from "@m3e/react/avatar";
import { M3eButton, type M3eButtonElement } from "@m3e/react/button";
import { M3eChip } from "@m3e/react/chips";
import { M3eFormField } from "@m3e/react/form-field";
import { M3eHeading } from "@m3e/react/heading";
import { M3eLinearProgressIndicator } from "@m3e/react/progress-indicator";
import { M3eButtonSegment, M3eSegmentedButton } from "@m3e/react/segmented-button";
import type { M3eSegmentedButtonElement } from "@m3e/react/segmented-button";
import { M3eSkeleton } from "@m3e/react/skeleton";
import clsx from "clsx";
import { LoaderCircle, SearchX, TriangleAlert, UserRound, type LucideIcon } from "lucide-react";
import { cloneElement, isValidElement, lazy, Suspense, useEffect, useId, useState, type ButtonHTMLAttributes, type CSSProperties, type ReactElement, type ReactNode } from "react";
import type { UserState } from "../api/client";
import { t } from "../i18n";

/**
 * The M3 button set (styles/app.css). The names already in use keep their meaning — `primary`
 * is the filled button, `glass` the tonal one, `ghost` a text button — and `outlined` joins
 * them: the quiet action that wants an edge, for toolbars and rows of peers.
 */
type Variant = "primary" | "glass" | "ghost" | "danger" | "danger-solid" | "outlined";

export function Button({
  variant = "glass",
  size,
  loading,
  block,
  className,
  children,
  disabled,
  type = "button",
  onClick,
  id,
  name,
  title,
  form,
  value,
  autoFocus,
  tabIndex,
  "aria-label": ariaLabel,
  "aria-describedby": ariaDescribedBy,
  "aria-pressed": ariaPressed,
  "aria-expanded": ariaExpanded,
  "aria-controls": ariaControls,
  "aria-current": ariaCurrent,
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: "sm"; loading?: boolean; block?: boolean }) {
  const filled = variant === "primary" || variant === "danger-solid";
  const text = variant === "ghost" || variant === "danger";
  const v = filled ? "filled" : variant === "outlined" ? "outlined" : text ? "text" : "tonal";
  const danger = variant === "danger" || variant === "danger-solid";
  return (
    <M3eButton
      ref={(el: M3eButtonElement | null) => {
        if (!el) return;
        if (form) el.setAttribute("form", form);
        else el.removeAttribute("form");
      }}
      type={type === "submit" || type === "reset" ? type : "button"}
      variant={v}
      size={size === "sm" ? "small" : "medium"}
      className={clsx(block && "btn-block", danger && "is-danger", className)}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      aria-label={ariaLabel}
      aria-describedby={ariaDescribedBy}
      aria-pressed={ariaPressed}
      aria-expanded={ariaExpanded}
      aria-controls={ariaControls}
      aria-current={ariaCurrent}
      onClick={onClick ? (e) => onClick(e as unknown as Parameters<typeof onClick>[0]) : undefined}
      id={id}
      name={name}
      title={title}
      value={typeof value === "string" ? value : undefined}
      autoFocus={autoFocus}
      tabIndex={tabIndex}
    >
      {loading ? <LoaderCircle slot="icon" size={16} className="spin" aria-hidden /> : null}
      {children}
    </M3eButton>
  );
}

const stateTone: Record<UserState, "ok" | "warn" | "bad" | "off"> = {
  active: "ok",
  expiring: "warn",
  limited: "bad",
  expired: "bad",
  disabled: "off",
};

const pillTone: Record<"ok" | "warn" | "bad" | "off", CSSProperties> = {
  ok: { "--m3e-elevated-chip-container-color": "var(--leaf-50)", "--m3e-chip-label-text-color": "var(--leaf-700)" } as CSSProperties,
  warn: { "--m3e-elevated-chip-container-color": "var(--honey-50)", "--m3e-chip-label-text-color": "var(--honey-600)" } as CSSProperties,
  bad: { "--m3e-elevated-chip-container-color": "var(--berry-50)", "--m3e-chip-label-text-color": "var(--berry-600)" } as CSSProperties,
  off: { "--m3e-elevated-chip-container-color": "var(--md-surface-container-high)", "--m3e-chip-label-text-color": "var(--md-on-surface-variant)" } as CSSProperties,
};

export function Pill({ tone, children }: { tone: "ok" | "warn" | "bad" | "off"; children: ReactNode }) {
  return (
    <M3eChip variant="elevated" className={clsx("pill", tone)} style={pillTone[tone]}>
      {children}
    </M3eChip>
  );
}

export function StatePill({ state }: { state: UserState }) {
  return <Pill tone={stateTone[state]}>{t(`states.${state}`)}</Pill>;
}

const AVATAR_COLORS = ["#FBE3D2", "#DCEBFA", "#DDF1E6", "#F4E6C9", "#EADFF3", "#F6DDE0", "#DDEFF1"];

export function Avatar({ name, seed, size }: { name: string; seed: number; size?: "sm" | "lg" }) {
  const initials = name
    .replace(/[«»"@()]/g, "")
    .trim()
    .split(/\s+/)
    .map((w) => Array.from(w)[0] ?? "")
    .slice(0, 2)
    .join("")
    .toUpperCase();
  const color = AVATAR_COLORS[seed % AVATAR_COLORS.length] ?? "#E7E0EB";
  const dim = size === "lg" ? "56px" : size === "sm" ? "32px" : "40px";
  const style = { "--av": color, "--m3e-avatar-color": color, "--m3e-avatar-label-color": "#1d1b20", "--m3e-avatar-size": dim } as CSSProperties;
  return (
    <M3eAvatar className={clsx("avatar", size)} style={style} aria-hidden>
      {initials || "?"}
    </M3eAvatar>
  );
}

export function Segmented<T extends string>({ value, options, onChange, label, block, disabled }: { value: T; options: { value: T; label: string }[]; onChange: (v: T) => void; label: string; block?: boolean; disabled?: boolean }) {
  return (
    <M3eSegmentedButton
      className={block ? "seg seg-block" : "seg"}
      aria-label={label}
      disabled={disabled}
      onChange={(e) => {
        const next = (e.currentTarget as M3eSegmentedButtonElement).value;
        if (typeof next === "string" && next !== value) onChange(next as T);
      }}
    >
      {options.map((o) => (
        <M3eButtonSegment key={o.value} value={o.value} checked={o.value === value} disabled={disabled}>
          {o.label}
        </M3eButtonSegment>
      ))}
    </M3eSegmentedButton>
  );
}

/**
 * A bar. Beside its number it is decoration; give it a `label` and it is a progressbar
 * of its own, with the value for a screen reader.
 */
export function Bar({ pct, className, label }: { pct: number; className?: string; label?: string }) {
  const v = Math.max(0, Math.min(100, pct));
  return (
    <M3eLinearProgressIndicator
      className={clsx("bar", className)}
      value={v}
      max={100}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    />
  );
}

export function Ring({ pct, label, sub, size = 112, unlimited }: { pct: number; label: ReactNode; sub: ReactNode; size?: number; unlimited?: boolean }) {
  const r = size / 2 - 8;
  const c = 2 * Math.PI * r;
  // With no limit there is nothing to run out of: the ring is full in the accent colour, never the red of a spent one.
  const fill = unlimited ? 100 : Math.min(Math.max(pct, 0), 100);
  const tone = unlimited ? "" : pct >= 100 ? "bad" : pct >= 85 ? "warn" : "";
  return (
    <div className={clsx("gauge", tone)} style={{ width: size, height: size }}>
      <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} aria-hidden>
        <circle className="track" cx={size / 2} cy={size / 2} r={r} fill="none" strokeWidth={10} />
        <circle
          className="val"
          cx={size / 2}
          cy={size / 2}
          r={r}
          fill="none"
          strokeWidth={10}
          strokeLinecap="round"
          strokeDasharray={c}
          strokeDashoffset={c * (1 - fill / 100)}
        />
      </svg>
      <div className="gauge-label">
        <div>
          <b className="num">{label}</b>
          <span>{sub}</span>
        </div>
      </div>
    </div>
  );
}

const QRCode = lazy(() => import("./qr"));

/** A QR code of `value`; the encoder is a chunk of its own, fetched when a code first shows. */
export function QR({ value, size = 136, label }: { value: string; size?: number; label?: string }) {
  return (
    <Suspense fallback={<Skeleton style={{ width: size, height: size, borderRadius: 16 }} />}>
      <QRCode value={value} size={size} label={label} />
    </Suspense>
  );
}

export function Skeleton({ className, style }: { className?: string; style?: CSSProperties }) {
  return <M3eSkeleton className={clsx("sk", className)} shape="rounded" animation="wave" style={{ display: "block", height: 12, ...style }} aria-hidden />;
}

export function Spinner({ size = 16 }: { size?: number }) {
  return <LoaderCircle size={size} className="spin" role="img" aria-label={t("common.loading")} />;
}

export function EmptyState({ title, text, children, search, icon: Icon = UserRound }: { title: string; text: ReactNode; children?: ReactNode; search?: boolean; icon?: LucideIcon }) {
  return (
    <div className="state-box">
      <div className="state-mark">{search ? <SearchX size={22} /> : <Icon size={22} />}</div>
      <h2>{title}</h2>
      <p>{text}</p>
      {children ? <div className="mt-2 flex flex-wrap justify-center gap-2">{children}</div> : null}
    </div>
  );
}

export function ErrorState({ title, text, onRetry }: { title?: string; text: string; onRetry?: () => void }) {
  return (
    <div className="state-box" role="alert">
      <div className="state-mark err">
        <TriangleAlert size={22} />
      </div>
      <h2>{title ?? t("common.loadFailed")}</h2>
      <p>{text}</p>
      {onRetry ? (
        <Button variant="primary" onClick={onRetry}>
          {t("common.retry")}
        </Button>
      ) : null}
    </div>
  );
}

/**
 * A labelled field with its hint or error. With `htmlFor` the label belongs to that input
 * and the hint/error is tied to it (aria-describedby, put on the input when it is the
 * direct child); without it the label names the group of controls inside (a segmented
 * switch, a select next to a switch).
 */
const fieldControl = (node: ReactNode) => isValidElement(node) && (node.type === "input" || node.type === "select" || node.type === "textarea");

export function Field({ label, htmlFor, hint, error, children }: { label: string; htmlFor?: string; hint?: ReactNode; error?: string; children: ReactNode }) {
  const uid = useId();
  // A single native control wears the outlined M3 field. A group (a switch beside a
  // select, a segmented control) keeps the label above it.
  if (htmlFor && fieldControl(children)) {
    return (
      <M3eFormField variant="outlined" floatLabel="always" hideRequiredMarker error={!!error} hideSubscript={error || hint ? "never" : "always"}>
        <label slot="label" htmlFor={htmlFor}>
          {label}
        </label>
        {children}
        {error ? <span slot="error">{error}</span> : hint ? <span slot="hint">{hint}</span> : null}
      </M3eFormField>
    );
  }
  const noteId = error || hint ? `${uid}-note` : undefined;
  const control = htmlFor && noteId && isValidElement<{ id?: string; "aria-describedby"?: string }>(children) && children.props.id === htmlFor ? children : null;
  return (
    <div className="field" {...(htmlFor ? {} : { role: "group", "aria-labelledby": `${uid}-label` })}>
      {htmlFor ? <label htmlFor={htmlFor}>{label}</label> : <span className="lbl" id={`${uid}-label`}>{label}</span>}
      {control ? cloneElement(control as ReactElement<{ "aria-describedby"?: string }>, { "aria-describedby": [control.props["aria-describedby"], noteId].filter(Boolean).join(" ") }) : children}
      {error ? (
        <span className="err" role="alert" id={noteId}>
          {error}
        </span>
      ) : hint ? (
        <span className="hint" id={noteId}>
          {hint}
        </span>
      ) : null}
    </div>
  );
}

export function PageHeader({ title, sub, actions }: { title: string; sub?: ReactNode; actions?: ReactNode }) {
  // The header lies flat on the page and turns into a floating bar once the page scrolls
  // under it.
  const [stuck, setStuck] = useState(false);
  useEffect(() => {
    const onScroll = () => setStuck(window.scrollY > 8);
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);
  return (
    <header className="topbar" data-stuck={stuck || undefined}>
      <div className="min-w-0">
        <M3eHeading className="page-title" variant="headline" size="medium" level={1}>
          {title}
        </M3eHeading>
        {sub ? <p className="page-sub">{sub}</p> : null}
      </div>
      {actions ? <div className="top-actions">{actions}</div> : null}
    </header>
  );
}
