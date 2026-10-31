import "@m3e/react/snackbar";
import { Check, TriangleAlert } from "lucide-react";
import { createContext, createElement, useCallback, useContext, useEffect, useMemo, useRef, useState, type HTMLAttributes, type ReactNode } from "react";

type Toast = { id: number; text: string; error?: boolean };
type Api = {
  ok: (text: string) => void;
  error: (text: string) => void;
};

const Ctx = createContext<Api | null>(null);

/** How long a toast stays (an error longer), and what is left of it once the pointer or focus leaves. */
const LIFETIME = { ok: 4_200, error: 6_000, resume: 2_500 };

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Toast[]>([]);
  const seq = useRef(0);
  const timers = useRef(new Map<number, number>());

  const dismiss = useCallback((id: number) => {
    window.clearTimeout(timers.current.get(id));
    timers.current.delete(id);
    setItems((xs) => xs.filter((x) => x.id !== id));
  }, []);
  const arm = useCallback(
    (id: number, ms: number) => {
      window.clearTimeout(timers.current.get(id));
      timers.current.set(id, window.setTimeout(() => dismiss(id), ms));
    },
    [dismiss],
  );
  // A toast under the pointer or with focus inside stays until the reader is done with it
  // (WCAG 2.2.1: a message must not vanish while it is being read).
  const hold = useCallback((id: number) => {
    window.clearTimeout(timers.current.get(id));
    timers.current.delete(id);
  }, []);
  const push = useCallback(
    (t: Omit<Toast, "id">) => {
      const id = ++seq.current;
      // Three at most; an older one pushed out has a timer left that finds nothing to remove.
      setItems((xs) => [...xs.slice(-2), { ...t, id }]);
      arm(id, t.error ? LIFETIME.error : LIFETIME.ok);
    },
    [arm],
  );
  const api = useMemo<Api>(() => ({ ok: (text) => push({ text }), error: (text) => push({ text, error: true }) }), [push]);

  return (
    <Ctx.Provider value={api}>
      {children}
      {/* One snackbar at a time: m3e keeps a single current bar. The timer pauses while it is held. */}
      {items.slice(-1).map((item) => (
        <Snack key={item.id} item={item} onDone={() => dismiss(item.id)} onHold={() => hold(item.id)} onResume={() => arm(item.id, LIFETIME.resume)} />
      ))}
    </Ctx.Provider>
  );
}

function Snack({ item, onDone, onHold, onResume }: { item: Toast; onDone: () => void; onHold: () => void; onResume: () => void }) {
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const onToggle = (e: Event) => {
      if ((e as ToggleEvent).newState === "closed") onDone();
    };
    el.addEventListener("toggle", onToggle);
    el.addEventListener("pointerenter", onHold);
    el.addEventListener("pointerleave", onResume);
    el.addEventListener("focusin", onHold);
    el.addEventListener("focusout", onResume);
    return () => {
      el.removeEventListener("toggle", onToggle);
      el.removeEventListener("pointerenter", onHold);
      el.removeEventListener("pointerleave", onResume);
      el.removeEventListener("focusin", onHold);
      el.removeEventListener("focusout", onResume);
    };
  }, [onDone, onHold, onResume]);
  return createElement(
    "m3e-snackbar",
    {
      ref,
      className: item.error ? "snack-err" : undefined,
      open: true,
      duration: 60_000,
      dismissible: true,
      closeLabel: "Close",
      role: item.error ? "alert" : "status",
    } as HTMLAttributes<HTMLElement>,
    item.error ? <TriangleAlert size={16} aria-hidden /> : <Check size={16} aria-hidden />,
    item.text,
  );
}

export function useToast(): Api {
  const v = useContext(Ctx);
  if (!v) throw new Error("useToast outside ToastProvider");
  return v;
}
