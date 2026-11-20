import { describe, it, expect } from "vitest";
import type { UsageReading } from "../api";
import {
  betweenReadings,
  bucketForSpan,
  customWindow,
  fromInputValue,
  isoMinute,
  lastMinutesRange,
  lastResetAt,
  readRange,
  readingChoices,
  todayRange,
  toInputValue,
  validRange,
  windowQuery,
  writeRange,
} from "./costRange";

// The tests run in Asia/Tokyo (vite.config.ts), so the reader's clock is
// UTC+9 all year, with no clock change: local and UTC are never the same here.
const NOW = Date.UTC(2026, 10, 20, 17, 30); // 2026-11-20 17:30 UTC

describe("the range in the address", () => {
  it("reads and writes a custom range, and reads back what it wrote", () => {
    const w = customWindow(Date.UTC(2026, 10, 20, 16, 0), Date.UTC(2026, 10, 20, 16, 45));
    expect(w).toEqual({ days: 1, from: "2026-11-20T16:00:00Z", to: "2026-11-20T16:45:00Z" });
    const p = writeRange(new URLSearchParams("only=covey"), w, "utc");
    expect(p.toString()).toBe("only=covey&from=2026-11-20T16%3A00%3A00Z&to=2026-11-20T16%3A45%3A00Z&tz=utc");
    const r = readRange(p, NOW);
    expect(r).toEqual({ tz: "utc", from: Date.UTC(2026, 10, 20, 16, 0), to: Date.UTC(2026, 10, 20, 16, 45), error: null });
  });

  it("keeps the zone of a link and the other parameters, and drops the range for a quick one", () => {
    const custom = writeRange(new URLSearchParams("prices=recomputed"), customWindow(NOW - 3600_000, NOW), "local");
    expect(custom.get("tz")).toBe("local");
    const quick = writeRange(custom, { days: 7 }, "local");
    expect(quick.get("days")).toBe("7");
    expect(quick.get("from")).toBeNull();
    expect(quick.get("to")).toBeNull();
    expect(quick.get("prices")).toBe("recomputed");
    // the default day and the default zone leave the address bare
    expect(writeRange(quick, { days: 1 }, "local").toString()).toBe("prices=recomputed");
  });

  it("reads the reader's clock unless the address says utc", () => {
    expect(readRange(new URLSearchParams(""), NOW).tz).toBe("local");
    expect(readRange(new URLSearchParams("tz=utc"), NOW).tz).toBe("utc");
    expect(readRange(new URLSearchParams("tz=mars"), NOW).tz).toBe("local");
  });

  it("names the reason a range in the address cannot be used", () => {
    const q = (s: string) => readRange(new URLSearchParams(s), NOW).error;
    expect(q("from=2026-11-20T16:00:00Z")).toBe("range_incomplete");
    expect(q("from=yesterday&to=2026-11-20T16:00:00Z")).toBe("range_time");
    expect(q("from=2026-11-20T16:00:00&to=2026-11-20T17:00:00")).toBe("range_time"); // no zone
    expect(q("from=2026-11-20T17:00:00Z&to=2026-11-20T16:00:00Z")).toBe("range_order");
    expect(q("from=2026-09-01T00:00:00Z&to=2026-11-01T00:00:00Z")).toBe("range_span");
    expect(q("from=2026-11-20T18:00:00Z&to=2026-11-20T19:00:00Z")).toBe("range_future");
    expect(q("from=2026-11-20T16:00:00Z&to=2026-11-20T16:45:00Z")).toBeNull();
    // an offset is an instant too
    expect(q("from=2026-11-20T17:00:00%2B01:00&to=2026-11-20T17:45:00%2B01:00")).toBeNull();
  });
});

describe("validRange", () => {
  const H = 3600_000, D = 24 * H;
  it("is the API's rule: to after from, at most 31 days, from before now", () => {
    expect(validRange(NOW - H, NOW, NOW)).toBeNull();
    expect(validRange(NOW, NOW, NOW)).toBe("range_order");
    expect(validRange(NOW - 31 * D, NOW, NOW)).toBeNull();
    expect(validRange(NOW - 31 * D - 60_000, NOW, NOW)).toBe("range_span");
    expect(validRange(NOW, NOW + H, NOW)).toBe("range_future");
    expect(validRange(NaN, NOW, NOW)).toBe("range_time");
    // a to after now is allowed: the API cuts it
    expect(validRange(NOW - H, NOW + H, NOW)).toBeNull();
  });
});

describe("date and time to the minute, in a zone", () => {
  it("shows an instant in UTC or on the reader's clock, and reads it back", () => {
    const at = Date.UTC(2026, 10, 20, 16, 45);
    expect(toInputValue(at, "utc")).toBe("2026-11-20T16:45");
    expect(toInputValue(at, "local")).toBe("2026-11-21T01:45"); // UTC+9
    expect(fromInputValue("2026-11-20T16:45", "utc")).toBe(at);
    expect(fromInputValue("2026-11-21T01:45", "local")).toBe(at);
  });

  it("refuses what is not a date and a time to the minute", () => {
    expect(fromInputValue("", "utc")).toBeNaN();
    expect(fromInputValue("2026-11-20", "utc")).toBeNaN();
    expect(fromInputValue("2026-02-30T10:00", "utc")).toBeNaN();
    expect(fromInputValue("2026-11-20T25:00", "utc")).toBeNaN();
  });

  it("floors an instant to its minute in RFC 3339", () => {
    expect(isoMinute(Date.UTC(2026, 10, 20, 16, 45, 59, 999))).toBe("2026-11-20T16:45:00Z");
  });

  it("asks the API for days, or for from and to", () => {
    expect(windowQuery({ days: 7 })).toBe("days=7");
    expect(windowQuery(customWindow(NOW - 3600_000, NOW))).toBe("from=2026-11-20T16%3A30%3A00Z&to=2026-11-20T17%3A30%3A00Z");
  });
});

describe("the bucket of a custom range", () => {
  it("is one bucket for a span shorter than it, and finer for shorter spans", () => {
    expect(bucketForSpan(5 * 60_000)).toBe("10min"); // shorter than a bucket: the one that holds it
    expect(bucketForSpan(3 * 3600_000)).toBe("10min");
    expect(bucketForSpan(3 * 3600_000 + 60_000)).toBe("hour");
    expect(bucketForSpan(2 * 24 * 3600_000)).toBe("hour");
    expect(bucketForSpan(2 * 24 * 3600_000 + 60_000)).toBe("day");
  });
});

describe("the helpers", () => {
  it("today runs from midnight in the zone to the end of this minute", () => {
    expect(todayRange(NOW, "utc")).toEqual([Date.UTC(2026, 10, 20), NOW]);
    // 17:30 UTC is 02:30 on the 21st at UTC+9: today there began 15:00 UTC the day before
    expect(todayRange(NOW, "local")).toEqual([Date.UTC(2026, 10, 20, 15), NOW]);
  });

  it("the last N minutes end with this minute", () => {
    expect(lastMinutesRange(NOW + 20_000, 45)).toEqual([NOW + 60_000 - 45 * 60_000, NOW + 60_000]);
  });

  const reading = (over: Partial<UsageReading>): UsageReading => ({
    runtime_id: "r1", credential_ord: 0, secret_key: "k", secret_slot: 0, credential_kind: "subscription", source: "usage_cmd",
    window_kind: "seven_day", supported: true, window_percent: null, week_percent: 10, resets_at: null, status: "ok", reason: "",
    stale: false, fetched_at: "2026-11-20T10:00:00Z", received_at: "2026-11-20T10:00:00Z", cost_usd: null, ...over,
  });

  it("the last reset is the latest reset time that has passed", () => {
    const rs = [
      reading({ resets_at: "2026-11-16T09:00:00Z" }),
      reading({ resets_at: "2026-11-19T09:00:00Z" }),
      reading({ resets_at: "2026-11-26T09:00:00Z" }), // ahead: not a reset yet
      reading({ window_kind: "five_hour", resets_at: "2026-11-20T15:00:00Z" }), // another window
      reading({ supported: false, resets_at: "2026-11-20T16:00:00Z" }),
    ];
    expect(lastResetAt(rs, NOW)).toBe(Date.UTC(2026, 10, 19, 9));
    expect(lastResetAt([reading({ resets_at: null })], NOW)).toBeNull();
  });

  it("offers the weekly readings newest first, once per credential and instant", () => {
    const rs = [
      reading({ fetched_at: "2026-11-20T10:00:00Z", week_percent: 12 }),
      reading({ fetched_at: "2026-11-20T10:00:00Z", week_percent: 12, source: "inband" }), // the same reading, seen twice
      reading({ fetched_at: "2026-11-20T11:00:00Z", week_percent: 14 }),
      reading({ fetched_at: "2026-11-20T12:00:00Z", week_percent: null }), // no figure
      reading({ fetched_at: "2026-11-20T12:00:00Z", window_kind: "five_hour" }),
    ];
    const c = readingChoices(rs);
    expect(c.map((x) => x.percent)).toEqual([14, 12]);
    expect(readingChoices(rs, 1)).toHaveLength(1);
  });

  it("between two readings, the first is the baseline and the second is in the window", () => {
    const a = Date.UTC(2026, 10, 20, 10, 0, 30), b = Date.UTC(2026, 10, 20, 11, 0, 10);
    const [from, to] = betweenReadings(b, a); // either order
    expect(from).toBe(Date.UTC(2026, 10, 20, 10, 1)); // just after the first: it lies before from
    expect(to).toBe(Date.UTC(2026, 10, 20, 11, 1)); // just after the second: it lies before to
    expect(from > a && to > b).toBe(true);
  });
});
