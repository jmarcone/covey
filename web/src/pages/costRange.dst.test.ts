import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { dayStart, dayStartValue, joinEnd, presetRange, splitEnd, validRange } from "./costRange";

/* The clock changes, read in zones that have them. Every other test runs in Asia/Tokyo (vite.config.ts), which has
 * none; here each describe sets the reader's zone itself (Node applies an assignment to process.env.TZ at once) and
 * puts Tokyo back afterwards. Each one first checks the zone took, so a runtime that ignored it fails here instead of
 * passing on Tokyo's clock. */

function inZone(tz: string) {
  let before: string | undefined;
  beforeAll(() => {
    before = process.env.TZ;
    process.env.TZ = tz;
  });
  afterAll(() => {
    process.env.TZ = before;
  });
}

const H = 3_600_000, D = 24 * H;

describe("Europe/Berlin: a month across the autumn clock change", () => {
  inZone("Europe/Berlin");

  it("is in Berlin", () => {
    expect(new Date(2026, 9, 1).getTimezoneOffset()).toBe(-120); // CEST
    expect(new Date(2026, 10, 1).getTimezoneOffset()).toBe(-60); // CET
  });

  it("takes all of October as last month, all November: 31 days and one hour", () => {
    const now = Date.UTC(2026, 10, 22, 11, 0); // 22 Nov 2026, 12:00 in Berlin
    const [from, to] = presetRange("lastMonth", now, "local");
    expect([from, to]).toEqual([Date.UTC(2026, 8, 30, 22), Date.UTC(2026, 9, 31, 23)]); // 1 Oct 00:00 CEST, 1 Nov 00:00 CET
    expect(to - from).toBe(31 * D + H);
    expect(validRange(from, to, now)).toBeNull();
  });

  it("takes a 31-day calendar pick across 25 October", () => {
    const now = Date.UTC(2026, 10, 22, 11, 0);
    // 10 Oct to 9 Nov, whole days: [start of 10 Oct, start of 10 Nov).
    const from = dayStart("2026-10-10", "local"), to = dayStart("2026-11-10", "local");
    expect(to - from).toBe(31 * D + H);
    expect(validRange(from, to, now)).toBeNull();
    // A day more is refused.
    expect(validRange(dayStart("2026-10-09", "local"), to, now)).toBe("range_span");
  });
});

describe("America/Santiago: the clock change skips midnight", () => {
  inZone("America/Santiago");

  it("is in Santiago", () => {
    // 6 Sep 2026 starts at 01:00: 00:00 does not exist.
    expect(new Date(2026, 8, 6).getHours()).toBe(1);
  });

  it("starts a day at its first minute that exists, and reads an end there as the day before", () => {
    expect(dayStartValue("2026-09-06", "local")).toBe("2026-09-06T01:00");
    expect(dayStartValue("2026-09-07", "local")).toBe("2026-09-07T00:00");
    // The last day 5 Sep ends where 6 Sep starts.
    expect(joinEnd("2026-09-05", "", "local")).toBe("2026-09-06T01:00");
    expect(splitEnd("2026-09-06T01:00", "2026-09-01", "local")).toEqual({ day: "2026-09-05", time: "" });
    // 01:00 is the start of no other day.
    expect(splitEnd("2026-09-07T01:00", "2026-09-01", "local")).toEqual({ day: "2026-09-07", time: "01:00" });
    // In UTC there is no clock change.
    expect(joinEnd("2026-09-05", "", "utc")).toBe("2026-09-06T00:00");
  });
});
