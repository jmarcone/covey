import { describe, expect, it } from "vitest";
import { clampInt, STICKY_CAP_MAX, STICKY_WINDOW_MAX } from "./sticky";

// The field keeps what it sends inside the bounds the server checks, so a
// refusal is never the first thing a person sees.
describe("clampInt", () => {
  it("keeps a value inside the bounds", () => {
    expect(clampInt("30", 1, STICKY_WINDOW_MAX, 55)).toBe(30);
    expect(clampInt("0", 1, STICKY_WINDOW_MAX, 55)).toBe(1);
    expect(clampInt("5000", 1, STICKY_WINDOW_MAX, 55)).toBe(1440);
    expect(clampInt("2000000", 1, STICKY_CAP_MAX, 200000)).toBe(1000000);
    expect(clampInt("12.7", 1, STICKY_WINDOW_MAX, 55)).toBe(12);
  });
  it("falls back for an empty or unreadable entry", () => {
    expect(clampInt("", 1, STICKY_WINDOW_MAX, 55)).toBe(55);
    expect(clampInt("  ", 1, STICKY_WINDOW_MAX, 55)).toBe(55);
    expect(clampInt("abc", 1, STICKY_WINDOW_MAX, 55)).toBe(55);
  });
});
