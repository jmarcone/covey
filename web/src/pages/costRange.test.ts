import { describe, it, expect } from "vitest";
import type { UsageReading } from "../api";
import {
  addDays,
  betweenReadings,
  canFreeze,
  cutToNow,
  dayKeyOf,
  dayStart,
  freezeRange,
  joinEnd,
  presetRange,
  splitEnd,
  splitStart,
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
    // the default range (30 days) and the default zone leave the address bare; 24 hours is no default any more
    expect(writeRange(quick, { days: 30 }, "local").toString()).toBe("prices=recomputed");
    expect(writeRange(quick, { days: 1 }, "local").get("days")).toBe("1");
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

  it("takes only what the server reads the same way: to the minute, a real day", () => {
    const q = (from: string, to = "2026-11-20T16:45:00Z") => readRange(new URLSearchParams({ from, to }), NOW).error;
    expect(q("2026-11-20T16:00Z")).toBe("range_time"); // Go's RFC 3339 wants seconds
    expect(q("2026-11-20T16:00:30Z")).toBe("range_time"); // the page would floor it to 16:00
    expect(q("2026-11-20T16:00:00.5Z")).toBe("range_time");
    expect(q("2026-02-30T16:00:00Z")).toBe("range_time"); // JS and Go disagree on a day that does not exist
    expect(q("2026-11-20T24:00:00Z")).toBe("range_time");
    expect(q("2026-11-20T16:00:00Z")).toBeNull();
  });
});

describe("validRange", () => {
  const H = 3600_000, D = 24 * H;
  it("is the API's rule: to after from, at most 31 days, from before now", () => {
    expect(validRange(NOW - H, NOW, NOW)).toBeNull();
    expect(validRange(NOW, NOW, NOW)).toBe("range_order");
    expect(validRange(NOW - 31 * D, NOW, NOW)).toBeNull();
    // One hour of slack: a whole local month across the autumn clock change is 31 days and one hour.
    expect(validRange(NOW - 31 * D - H, NOW, NOW)).toBeNull();
    expect(validRange(NOW - 31 * D - H - 60_000, NOW, NOW)).toBe("range_span");
    // October 2026 in Berlin, the instants written out: 1 Oct 00:00 CEST to 1 Nov 00:00 CET.
    expect(validRange(Date.UTC(2026, 8, 30, 22), Date.UTC(2026, 9, 31, 23), NOW)).toBeNull();
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

// The calendar picker (cost range calendar): days, presets and the freeze, in both zones. NOW is Friday 20 Nov 17:30
// UTC, which is already Saturday 21 Nov 02:30 on the test's clock (Tokyo): a UTC day that followed the machine's
// zone would start at 15:00 UTC and land on the wrong date.
describe("the calendar's days", () => {
  const H = 3_600_000;
  it("starts a day at the zone's midnight, UTC's whatever the machine's zone", () => {
    expect(dayStart("2026-11-20", "utc")).toBe(Date.UTC(2026, 10, 20));
    expect(dayStart("2026-11-20", "local")).toBe(Date.UTC(2026, 10, 19, 15)); // 00:00 in Tokyo
    expect(dayStart("2026-02-30", "utc")).toBeNaN();
    expect(dayKeyOf(NOW, "utc")).toBe("2026-11-20");
    expect(dayKeyOf(NOW, "local")).toBe("2026-11-21");
    expect(addDays("2026-11-30", 1)).toBe("2026-12-01");
    expect(addDays("2026-03-01", -1)).toBe("2026-02-28");
  });

  it("reads an end at midnight as the whole day before, and writes a day without a time as the next midnight", () => {
    expect(splitStart("2026-11-17T08:15")).toEqual({ day: "2026-11-17", time: "08:15" });
    expect(splitStart("nonsense")).toEqual({ day: "", time: "" });
    expect(splitEnd("2026-11-20T00:00", "2026-11-17", "local")).toEqual({ day: "2026-11-19", time: "" });
    expect(splitEnd("2026-11-19T12:30", "2026-11-17", "local")).toEqual({ day: "2026-11-19", time: "12:30" });
    // An end at the start day's own midnight is not a day (and validRange refuses it).
    expect(splitEnd("2026-11-17T00:00", "2026-11-17", "utc")).toEqual({ day: "2026-11-17", time: "00:00" });
    expect(joinEnd("2026-11-19", "", "utc")).toBe("2026-11-20T00:00");
    expect(joinEnd("2026-11-19", "12:30", "local")).toBe("2026-11-19T12:30");
  });

  it("cuts the presets at the zone's days, weeks from Monday, ending now where they hold today", () => {
    const end = NOW; // NOW is on a whole minute
    const utc = (d: number, m = 10) => Date.UTC(2026, m, d);
    expect(presetRange("today", NOW, "utc")).toEqual([utc(20), end]);
    expect(presetRange("yesterday", NOW, "utc")).toEqual([utc(19), utc(20)]);
    expect(presetRange("last7", NOW, "utc")).toEqual([utc(14), end]);
    expect(presetRange("thisWeek", NOW, "utc")).toEqual([utc(16), end]); // Monday 16 Nov
    expect(presetRange("lastWeek", NOW, "utc")).toEqual([utc(9), utc(16)]);
    expect(presetRange("thisMonth", NOW, "utc")).toEqual([utc(1), end]);
    expect(presetRange("lastMonth", NOW, "utc")).toEqual([utc(1, 9), utc(1)]);
    // In Tokyo it is Saturday the 21st: the same presets, nine hours earlier and a day later.
    const jst = (d: number, m = 10) => Date.UTC(2026, m, d) - 9 * H;
    expect(presetRange("today", NOW, "local")).toEqual([jst(21), end]);
    expect(presetRange("yesterday", NOW, "local")).toEqual([jst(20), jst(21)]);
    expect(presetRange("last7", NOW, "local")).toEqual([jst(15), end]);
    expect(presetRange("thisWeek", NOW, "local")).toEqual([jst(16), end]);
    expect(presetRange("lastWeek", NOW, "local")).toEqual([jst(9), jst(16)]);
    expect(presetRange("lastMonth", NOW, "local")).toEqual([jst(1, 9), jst(1)]);
  });

  it("freezes a quick range at now, up to the longest range", () => {
    expect(freezeRange(30, NOW + 20_000)).toEqual([NOW + 60_000 - 30 * 24 * H, NOW + 60_000]);
    expect(canFreeze(30)).toBe(true);
    expect(canFreeze(90)).toBe(false);
  });

  it("cuts an end in the future to the end of this minute, and leaves a start in the future to validRange", () => {
    expect(cutToNow(NOW - H, NOW + 5 * H, NOW + 1000)).toBe(NOW + 60_000);
    expect(cutToNow(NOW - H, NOW - 1000, NOW)).toBe(NOW - 1000);
    expect(cutToNow(NOW + H, NOW + 2 * H, NOW)).toBe(NOW + 2 * H);
  });
});
