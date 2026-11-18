import { describe, it, expect } from "vitest";
import type { BilledReport, CostCell, CostCells, CredentialPlan, MeterCoverage, MeterStep } from "../api";
import {
  billedFetchBucket,
  billedFigure,
  billedPerPeriod,
  billedStatus,
  bucketStart,
  credentialFigures,
  meterCrossings,
  meterRange,
  meterWeeks,
  multiple,
  multipleOf,
  pricePerPoint,
  realCost,
  realFromWeeks,
} from "./costFigures";

// The figures the Costs page shows are argued with, so every rule that turns
// tokens, meter points and plan prices into a number is pinned here: the
// range the whole percents leave, when a multiple may be shown at all, that
// nothing crosses from one credential to another, and that each part of a
// window is priced at the plan valid then.

const HOUR = 3600_000;
const PP = 200 * 12 / 52 / 100; // $200 a month: $0.4615 per point

const SEAT = "claude_code_oauth_token";
const KEY = "anthropic_api_key";

const plan = (over: Partial<CredentialPlan> = {}): CredentialPlan => ({
  runtime_id: null, ord: null, secret_key: SEAT, secret_slot: 0, plan_name: "Max", amount: 200, currency: "USD",
  period: "month", tax_basis: "net", fx_to_usd: 1, fx_date: null, valid_from: "2026-11-01T00:00:00Z", valid_to: null,
  weekly_usd: 200 * 12 / 52, usd_per_point: PP, ...over,
});

const cell = (period: string, measured: Record<string, number>, over: Partial<CostCell> = {}): CostCell => ({
  period, agent_id: "a1", secret_key: SEAT, secret_slot: 0, credential_kind: "subscription",
  reported_usd: 99, measured, engine_usd: Object.values(measured).reduce((a, b) => a + b, 0), ...over,
});

const coverage = (over: Partial<MeterCoverage> = {}): MeterCoverage => ({
  secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd", window_kind: "seven_day",
  baseline_at: "2026-11-17T18:00:00Z", baseline_percent: 9, baseline_before_window: false,
  last_at: "2026-11-17T21:00:00Z", last_percent: 13, readings: 8, points: 4, points_low: 3, points_high: 5,
  longest_gap_secs: 1800, segments: null, measured_usd: 30, ...over,
});

const step = (period: string, points: number, over: Partial<MeterStep> = {}): MeterStep => ({
  period, secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd",
  // The server prices each reading at the plan valid when it was read.
  window_kind: "seven_day", points, readings: 1, resets: 0, longest_gap_secs: 600, usd: points * PP, unpriced_points: 0, ...over,
});

const cells = (over: Partial<CostCells> = {}): CostCells => ({
  periods: [], agents: [{ id: "a1", label: "Coordinator", reported_usd: 0 }, { id: "a2", label: "QA", reported_usd: 0 }],
  types: ["cache_read", "cache_write", "input", "output", "web_search", "unpriced", "unattributed"],
  cells: [], subscription: [], meter: [], plans: [plan()], bookings: [], ...over,
});

describe("the meter and the multiple", () => {
  it("gives the live-shaped case ≈28.6× with the range 22.9–38.1×", () => {
    const m = multiple(52.77, 4, 0.4615);
    expect(m?.kind).toBe("estimate");
    if (m?.kind !== "estimate") throw new Error("no estimate");
    expect(m.value.toFixed(1)).toBe("28.6");
    expect(m.low.toFixed(1)).toBe("22.9");
    expect(m.high.toFixed(1)).toBe("38.1");
  });

  it("gives only a range for 2 points and no figure for none, where the real cost is below one point", () => {
    const two = multiple(52.77, 2, 0.4615);
    expect(two).toEqual({ kind: "range", low: 52.77 / (3 * 0.4615), high: 52.77 / 0.4615 });
    expect(multiple(52.77, 1, 0.4615)).toMatchObject({ kind: "range", high: Infinity });
    expect(multiple(52.77, 0, 0.4615)).toBeNull();
    expect(meterRange(0)).toEqual({ low: 0, high: 1 });
    const none = realCost(0, 0.4615);
    expect(none.below).toBe(true);
    expect(none.high).toBeCloseTo(0.4615, 10); // "< $0.46"
  });

  it("sums a week reset across two weeks, each leaving ±1 point", () => {
    const cov = coverage({
      points: 7,
      segments: [
        { start_at: "2026-11-17T10:00:00Z", end_at: "2026-11-17T16:00:00Z", from_percent: 95, to_percent: 98, reset: false, points: 3 },
        { start_at: "2026-11-17T17:00:00Z", end_at: "2026-11-17T21:00:00Z", from_percent: 0, to_percent: 4, reset: true, points: 4 },
      ],
    });
    const steps = [step("2026-11-17T12:00:00Z", 3), step("2026-11-17T17:00:00Z", 4, { resets: 1 })];
    const real = realFromWeeks(meterWeeks(cov, steps, [plan()], HOUR));
    expect(real.points).toBe(7);
    expect(real.weeks).toBe(2);
    expect(real.value).toBeCloseTo(7 * PP, 10);
    expect(real.low).toBeCloseTo(5 * PP, 10);
    expect(real.high).toBeCloseTo(9 * PP, 10);
    // and the crossings restart at the reset
    const x = meterCrossings(coverage({ baseline_percent: 95 }), steps);
    expect(x.get("2026-11-17T12:00:00Z")).toEqual({ from: 95, to: 98, reset: false });
    expect(x.get("2026-11-17T17:00:00Z")).toEqual({ from: 0, to: 4, reset: true });
  });

  it("prices each part of a window at the plan valid then", () => {
    const change = "2026-11-17T20:00:00Z";
    const plans = [plan({ amount: 100, valid_to: change }), plan({ amount: 200, valid_from: change })];
    const pp100 = pricePerPoint(plans[0])!, pp200 = pricePerPoint(plans[1])!;
    const cov = coverage({ points: 5 });
    const steps = [step("2026-11-17T18:00:00Z", 2, { usd: 2 * pp100 }), step("2026-11-17T20:00:00Z", 3, { usd: 3 * pp200 })];
    const real = realFromWeeks(meterWeeks(cov, steps, plans, HOUR));
    expect(real.priced).toBe(true);
    expect(real.value).toBeCloseTo(2 * pp100 + 3 * pp200, 10);
    // the ±1 point of the week at its dearest rate
    expect(real.high).toBeCloseTo(2 * pp100 + 3 * pp200 + pp200, 10);
  });

  // Review C2: the client re-priced a step at its bucket's START. A day
  // bucket that begins under the old plan but whose readings came after the
  // change was priced at the old rate; the server prices each reading when
  // it was read, and that figure is the one to show.
  it("takes each step's dollars as the server priced them, not at the bucket's start", () => {
    const change = "2026-11-17T20:00:00Z";
    const plans = [plan({ amount: 100, valid_to: change }), plan({ amount: 200, valid_from: change })];
    const pp200 = pricePerPoint(plans[1])!;
    const cov = coverage({ points: 4 });
    // one day bucket from midnight (old plan), the 4 points read after 20:00 (new plan)
    const steps = [step("2026-11-17T00:00:00Z", 4, { usd: 4 * pp200 })];
    const real = realFromWeeks(meterWeeks(cov, steps, plans, 24 * HOUR));
    expect(real.value).toBeCloseTo(4 * pp200, 10);
  });

  it("shows no real figure and no multiple while any point has no plan price", () => {
    const cov = coverage({ points: 4 });
    const steps = [step("2026-11-17T18:00:00Z", 2), step("2026-11-17T19:00:00Z", 2, { usd: null, unpriced_points: 2 })];
    const real = realFromWeeks(meterWeeks(cov, steps, [plan()], HOUR));
    expect(real.priced).toBe(false);
    expect(real.unpricedPoints).toBe(2);
    expect(real.points).toBe(4);
    const [seat] = credentialFigures({
      cells: cells({ cells: [cell("2026-11-17T18:00:00Z", { output: 30 })], meter: [cov], subscription: steps }),
      bucketMs: HOUR, windowEnd: new Date("2026-11-17T23:00:00Z"),
    });
    expect(seat.multiple).toBeNull();
    expect(seat.noMultiple).toBe("unpriced");
    expect(seat.agents[0].realShare).toBeNull();
    // a credential without any plan, and a meter that did not move: no price either
    const none = realFromWeeks(meterWeeks(coverage({ points: 0 }), [], [], HOUR));
    expect(none.priced).toBe(false);
  });

  it("converts a yearly plan with the stored fx rate", () => {
    expect(pricePerPoint({ amount: 2000, period: "year", fx_to_usd: 1.08, currency: "EUR" })).toBeCloseTo((2000 * 1.08) / 52 / 100, 12);
    expect(pricePerPoint({ amount: 200, period: "month", fx_to_usd: 1, currency: "USD" })).toBeCloseTo(PP, 12);
    // a foreign plan without a rate is not priced as if it were dollars
    expect(pricePerPoint({ amount: 200, period: "month", fx_to_usd: 0, currency: "EUR" })).toBeNull();
  });
});

describe("window alignment", () => {
  const hourly = ["17", "18", "19", "20", "21", "22"].map((h) => cell(`2026-11-17T${h}:00:00Z`, { output: 10 }));

  it("feeds the multiple with the server's spend between baseline and last reading, not the whole window", () => {
    const data = cells({
      cells: hourly,
      meter: [coverage()],
      subscription: [step("2026-11-17T18:00:00Z", 1), step("2026-11-17T19:00:00Z", 2), step("2026-11-17T20:00:00Z", 1)],
    });
    const [seat] = credentialFigures({ cells: data, bucketMs: HOUR, windowEnd: new Date("2026-11-17T23:00:00Z") });
    expect(seat.recomputed).toBeCloseTo(60, 10);
    expect(seat.aligned).toBeCloseTo(30, 10);
    const want = multipleOf(30, realCost(4, PP));
    if (seat.multiple?.kind !== "estimate" || want?.kind !== "estimate") throw new Error("no estimate");
    expect(seat.multiple.value).toBeCloseTo(want.value, 9);
    expect(seat.multiple.low).toBeCloseTo(want.low, 9);
    expect(seat.multiple.high).toBeCloseTo(want.high, 9);
  });

  // The live case of the review: one day at day buckets, read at 01:50 UTC.
  // The window starts mid-day (Nov 17, 01:50), the meter's baseline is 18:06
  // and its last reading 01:41; the Nov 17 cell holds $131, the Nov 18 cell
  // $159, about $270 of it between the two readings. Prorating the cells by
  // their share of a whole day gave ≈ $43 and a multiple of ≈ 4.5×; the
  // figure must be the same as at hour buckets, ≈ 28×.
  it("gives the same multiple at day buckets when the window starts mid-day", () => {
    const DAY = 24 * HOUR;
    const cov = coverage({ baseline_at: "2026-11-17T18:06:00Z", baseline_percent: 9, baseline_before_window: false,
      last_at: "2026-11-18T01:41:00Z", last_percent: 30, points: 21, points_low: 20, points_high: 22, measured_usd: 270 });
    const daily = cells({
      cells: [cell("2026-11-17T00:00:00Z", { output: 131 }), cell("2026-11-18T00:00:00Z", { output: 159 })],
      meter: [cov],
      subscription: [step("2026-11-17T00:00:00Z", 13), step("2026-11-18T00:00:00Z", 8)],
    });
    const hourlyCells = cells({
      cells: [cell("2026-11-17T18:00:00Z", { output: 131 }), cell("2026-11-18T01:00:00Z", { output: 159 })],
      meter: [cov],
      subscription: [step("2026-11-17T20:00:00Z", 13), step("2026-11-18T01:00:00Z", 8)],
    });
    const end = new Date("2026-11-18T01:50:00Z");
    const [byDay] = credentialFigures({ cells: daily, bucketMs: DAY, windowEnd: end });
    const [byHour] = credentialFigures({ cells: hourlyCells, bucketMs: HOUR, windowEnd: end });
    if (byDay.multiple?.kind !== "estimate" || byHour.multiple?.kind !== "estimate") throw new Error("no estimate");
    expect(byDay.aligned).toBe(270);
    expect(byDay.multiple.value).toBeCloseTo(270 / (21 * PP), 9);
    expect(byDay.multiple.value.toFixed(1)).toBe("27.9");
    expect(byDay.multiple.value).toBeCloseTo(byHour.multiple.value, 9);
  });

  it("shows no multiple without the server's spend figure", () => {
    const [seat] = credentialFigures({
      cells: cells({ cells: hourly, meter: [coverage({ measured_usd: undefined })], subscription: [step("2026-11-17T18:00:00Z", 4)] }),
      bucketMs: HOUR, windowEnd: new Date("2026-11-17T23:00:00Z"),
    });
    expect(seat.multiple).toBeNull();
    expect(seat.noMultiple).toBe("meter");
  });
});

describe("per credential", () => {
  const mixed = cells({
    cells: [
      cell("2026-11-17T18:00:00Z", { output: 30, cache_read: 10, unattributed: 7 }),
      cell("2026-11-17T18:00:00Z", { output: 5 }, { agent_id: "a2" }),
      cell("2026-11-17T18:00:00Z", { output: 100 }, { secret_key: KEY, secret_slot: 1, credential_kind: "api_key" }),
      cell("2026-11-17T18:00:00Z", { output: 1 }, { secret_key: null, secret_slot: null, credential_kind: "" }),
    ],
    meter: [coverage()],
    subscription: [step("2026-11-17T18:00:00Z", 4)],
    bookings: [
      { secret_key: SEAT, secret_slot: 0, basis: "invocation", entries: 3, usd: 40 },
      { secret_key: KEY, secret_slot: 1, basis: "legacy", entries: 1, usd: 200 },
    ],
  });

  it("never mixes two credentials", () => {
    const figs = credentialFigures({ cells: mixed, bucketMs: HOUR, windowEnd: new Date("2026-11-17T23:00:00Z") });
    expect(figs.map((f) => f.id)).toEqual([`${KEY}#1`, `${SEAT}#0`, ""]);
    const [key, seat, none] = figs;
    expect(seat.recomputed).toBe(45); // the booking without lines (7) is not measured
    expect(seat.unattributed).toBe(7);
    expect(seat.real?.points).toBe(4);
    expect(seat.multiple?.kind).toBe("estimate");
    expect(key.recomputed).toBe(100);
    expect(key.real).toBeNull();
    expect(key.multiple).toBeNull();
    expect(none.recomputed).toBe(1);
    expect(none.real).toBeNull();
    // the booking-check names only the key's legacy booking
    expect(seat.bookingCheck).toEqual([]);
    expect(key.bookingCheck.map((b) => b.basis)).toEqual(["legacy"]);
    // and no figure is the booked one (99 per cell)
    for (const f of figs) expect([f.reported, f.recomputed]).not.toContain(99);
  });

  it("shares a credential's list $ among its agents, the real share an estimate of the same", () => {
    const [, seat] = credentialFigures({ cells: mixed, bucketMs: HOUR, windowEnd: new Date("2026-11-17T23:00:00Z") });
    expect(seat.agents.map((a) => a.id)).toEqual(["a1", "a2"]);
    expect(seat.agents[0].share).toBeCloseTo(40 / 45, 10);
    expect(seat.agents[1].realShare).toBeCloseTo((5 / 45) * 4 * PP, 10);
  });

  it("shows no multiple under an agent filter or in the per-agent layout", () => {
    const filtered = credentialFigures({ cells: mixed, bucketMs: HOUR, windowEnd: new Date("2026-11-17T23:00:00Z"), agents: new Set(["a1"]) });
    const seat = filtered.find((f) => f.id === `${SEAT}#0`)!;
    expect(seat.multiple).toBeNull();
    expect(seat.noMultiple).toBe("agents");
    expect(seat.recomputed).toBe(40);
    const perAgent = credentialFigures({ cells: mixed, bucketMs: HOUR, windowEnd: new Date("2026-11-17T23:00:00Z"), layout: "agents" });
    expect(perAgent.find((f) => f.id === `${SEAT}#0`)!.multiple).toBeNull();
  });

  const sync = (over: Partial<BilledReport["sync"]> = {}): BilledReport["sync"] => ({
    configured: true, last_attempt_at: "2026-11-17T22:00:00Z", last_success_at: "2026-11-17T22:00:00Z", last_daily_at: null,
    data_refreshed_at: "2026-11-17T20:00:00Z", last_status: "ok", last_error: "", ...over,
  });
  const report = (over: Partial<BilledReport> = {}): BilledReport => ({
    bucket: "hour",
    // The chart's points reach back to the midnight before the page's start:
    // they must not be what the tile adds up.
    points: [
      { bucket: "2026-11-17T00:00:00Z", api_key_id: "apikey_01", workspace_id: "", model: "claude-opus-5-5",
        secret_key: KEY, secret_slot: 1, billed_usd: "500.00", billed_list_usd: "500.00", complete: true },
      { bucket: "2026-11-17T18:00:00Z", api_key_id: "apikey_01", workspace_id: "", model: "claude-opus-5-5",
        secret_key: KEY, secret_slot: 1, billed_usd: "80.50", billed_list_usd: "100.00", complete: true },
    ],
    window: { start: "2026-11-17T18:00:00Z", end: "2026-11-17T23:00:00Z", width: "1h",
      totals: [{ secret_key: KEY, secret_slot: 1, has_billed: true, billed_usd: "80.50", billed_list_usd: "100.00",
        reported_usd: 95, list_usd: 99 }] },
    sync: sync(),
    ...over,
  });
  const keyOf = (billed: BilledReport | null) =>
    credentialFigures({ cells: mixed, billed, bucketMs: HOUR, windowEnd: new Date("2026-11-17T23:00:00Z") }).find((f) => f.id === `${KEY}#1`)!;

  it("sums an API key's bill over the window the server names, beside the same window's lines, pending after data_refreshed_at", () => {
    const key = keyOf(report());
    expect(key.billed).toMatchObject({ state: "measured", usd: 80.5, listUsd: 100, ratio: 0.805, reported: 95, recomputed: 99 });
    expect(key.billed?.window?.start.toISOString()).toBe("2026-11-17T18:00:00.000Z");
    expect(key.billed?.window?.width).toBe("1h");
    expect(key.billed?.status.state).toBe("pending");
    expect(key.billed?.status.completeTo?.toISOString()).toBe("2026-11-17T20:00:00.000Z");
    expect(billedStatus(new Date("2026-11-17T19:00:00Z"), "2026-11-17T20:00:00Z").state).toBe("complete");
    expect(billedStatus(new Date("2026-11-17T19:00:00Z"), null)).toEqual({ state: "pending", completeTo: null });
  });

  // Review C3: the bars took every point of the bill, back to the midnight
  // before the page's start, and at month asked for weeks the chart has no
  // period for; the table's billed total was not the tile's.
  it("puts only the tile's window of the bill into the chart's buckets, summing to the tile", () => {
    const hours = billedPerPeriod(report(), KEY, 1, "hour");
    expect([...hours.entries()]).toEqual([[Date.parse("2026-11-17T18:00:00Z"), 80.5]]);
    const tile = keyOf(report()).billed!;
    expect([...hours.values()].reduce((a, b) => a + b, 0)).toBeCloseTo(tile.usd, 10);
    // day points summed per month: two months, nothing before the window
    const days = (d: string, usd: string) => ({ bucket: d, api_key_id: "apikey_01", workspace_id: "", model: "claude-opus-5-5",
      secret_key: KEY, secret_slot: 1, billed_usd: usd, billed_list_usd: usd, complete: true });
    const monthly = billedPerPeriod(report({ bucket: "day",
      points: [days("2026-09-30T00:00:00Z", "7"), days("2026-10-30T00:00:00Z", "1"), days("2026-10-31T00:00:00Z", "2"), days("2026-11-01T00:00:00Z", "4")],
      window: { start: "2026-10-01T00:00:00Z", end: "2026-11-17T23:00:00Z", width: "1d", totals: [] } }), KEY, 1, "month");
    expect([...monthly.entries()]).toEqual([[Date.parse("2026-10-01T00:00:00Z"), 3], [Date.parse("2026-11-01T00:00:00Z"), 4]]);
    // a week from Monday, as the server cuts it; the bill is never asked for per week
    expect(new Date(bucketStart(Date.parse("2026-11-01T10:00:00Z"), "week")).toISOString()).toBe("2026-10-26T00:00:00.000Z");
    expect(billedFetchBucket(90, "week")).toBe("day");
    expect(billedFetchBucket(30, "month")).toBe("day");
    expect(billedFetchBucket(1, "day")).toBe("hour");
  });

  it("gives no dollar figure where no bill could be read, and says why", () => {
    const quiet = { secret_key: KEY, secret_slot: 1, has_billed: false, billed_usd: "0.00000000", billed_list_usd: "0.00000000",
      reported_usd: 95, list_usd: 99 };
    const win = (totals: typeof quiet[]) => ({ start: "2026-11-17T18:00:00Z", end: "2026-11-17T23:00:00Z", width: "1h", totals });
    expect(keyOf(null).billed?.state).toBe("unavailable");
    expect(keyOf(report({ points: [], window: win([]), sync: sync({ configured: false, data_refreshed_at: null }) })).billed?.state).toBe("unconfigured");
    expect(keyOf(report({ points: [], window: win([]) })).billed?.state).toBe("unmapped");
    expect(keyOf(report({ points: [], window: win([quiet]) })).billed?.state).toBe("not_landed");
    const failed = keyOf(report({ points: [], window: win([quiet]),
      sync: sync({ last_status: "failed", last_error: "HTTP 401", last_success_at: null, data_refreshed_at: null }) })).billed!;
    expect(failed.state).toBe("failed");
    expect(failed.error).toBe("HTTP 401");
    // $0.00 only when configured, mapped and the window is complete
    const zero = billedFigure(report({ points: [], window: win([quiet]), sync: sync({ data_refreshed_at: "2026-11-18T00:00:00Z" }) }),
      KEY, 1, new Date("2026-11-17T23:00:00Z"));
    expect(zero).toMatchObject({ state: "measured", usd: 0 });
    // a failed sync after data had landed keeps the figure and names the failure
    const stale = keyOf(report({ sync: sync({ last_status: "failed", last_error: "HTTP 500" }) })).billed!;
    expect(stale).toMatchObject({ state: "measured", usd: 80.5, error: "HTTP 500" });
  });

  it("says why there is no multiple below 3 points", () => {
    const figs = credentialFigures({
      cells: cells({ cells: mixed.cells, meter: [coverage({ points: 2 })], subscription: [step("2026-11-17T18:00:00Z", 2)] }),
      bucketMs: HOUR, windowEnd: new Date("2026-11-17T23:00:00Z"),
    });
    const seat = figs.find((f) => f.id === `${SEAT}#0`)!;
    expect(seat.multiple).toBeNull();
    expect(seat.noMultiple).toBe("movement");
  });
});
