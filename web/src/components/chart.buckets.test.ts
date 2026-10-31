import { describe, expect, it } from "vitest";
import { buckets } from "./chart";

describe("buckets", () => {
  it("drops the open hour when it has no traffic yet", () => {
    const now = Date.parse("2026-10-31T18:40:00Z");
    const step = 3_600_000;
    const open = Math.floor(now / step) * step;
    const points = [1, 2, 3].map((h) => ({
      t: new Date(open - h * step).toISOString(),
      up: h * 1000,
      down: h * 2000,
    }));
    const out = buckets(points, "24h", now);
    expect(out.at(-1)?.up).toBeGreaterThan(0);
    expect(new Date(out.at(-1)!.t).getTime()).toBe(open - step);
  });

  it("keeps a finished hour that really was zero", () => {
    const now = Date.parse("2026-10-31T18:40:00Z");
    const step = 3_600_000;
    const open = Math.floor(now / step) * step;
    const out = buckets(
      [
        { t: new Date(open - 2 * step).toISOString(), up: 10, down: 10 },
        { t: new Date(open - step).toISOString(), up: 0, down: 0 },
      ],
      "24h",
      now,
    );
    expect(out.at(-1)?.up).toBe(0);
    expect(new Date(out.at(-1)!.t).getTime()).toBe(open - step);
  });
});
