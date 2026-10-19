import { useEffect, useState } from "react";

/**
 * The current moment, re-read every step milliseconds: the dashboard's clock and its
 * preview in the settings. A second while the time is shown, a slow tick otherwise, so the
 * page crosses midnight on its own without keeping anyone's laptop awake.
 */
export function useTick(step: number): Date {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), step);
    return () => clearInterval(id);
  }, [step]);
  return now;
}

/** How often the clock ticks for a style: the time needs a second, a date alone does not. */
export const tickStep = (style: "compact" | "full" | "large", seconds: boolean): number => (style !== "compact" && seconds ? 1000 : 30_000);
