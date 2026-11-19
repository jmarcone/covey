import { describe, it, expect, beforeEach } from "vitest";
import { fireEvent, screen, within } from "@testing-library/react";
import Costs from "./Costs";
import type { CostCells, ExternalMeterAgent, ExternalMeterStep, ExternalMeterSums, ExternalMeterTotal, ExternalUsage } from "../api";
import i18n from "../i18n";
import { fmtNumber, fmtUSD } from "../format";
import { mockFetch, renderWithProviders } from "../test/render";
import { credentialFigures, headline, perCredentialTotals, seatLists, splitPerPeriod, subscriptionDent } from "./costFigures";

// #120, the headline: once Claude Code sessions outside covey report on a
// subscription meter, the page shows the whole dent in the meters, split
// between covey agents (internal) and those sessions, with list $ per real $ for
// each and for both together. The server splits the meter per chunk of at
// least 5 points of readings by the chunk's list-price shares (F3c); a
// reading the outside sessions had not reported is split to nobody — never
// booked to covey whole — and says so. The page only reads the server's
// figures: the fixtures below give them as the server would, whatever
// split produced them.
// The headline, the chart's real bars and the agents' real share all read
// the server's steps, so they add up to the same figures.

const SEAT = "claude_code_oauth_token";
const PP = 200 * 12 / 52 / 100; // $200 a month: $0.4615 per point
const H18 = "2026-11-17T18:00:00Z", H19 = "2026-11-17T19:00:00Z", H20 = "2026-11-17T20:00:00Z";
const times = (v: number) => `${fmtNumber(v, 1)}×`;

/** One reading step as the server returns it: split by list-price share
 *  when reported and some list is there (after `unattributed` of its points,
 *  its chunk's part), else unsplit — idle when reported with neither list
 *  nor movement, unattributed whole when reported with movement and no list
 *  (a chunk with no list at all). */
function step(at: string, from: string, points: number, reported: boolean, outList: number, covList: number, key = SEAT,
  unattributed = 0): ExternalMeterStep {
  const real = points * PP;
  const split = reported && outList + covList > 0;
  const noCall = reported && !split && points > 0;
  const un = noCall ? real : unattributed * PP;
  const out = split ? (real - un) * outList / (outList + covList) : null;
  return {
    at, from, secret_key: key, secret_slot: 0, points, reset: false, real_usd: real,
    external_list_usd: outList, covey_list_usd: covList, reported, split,
    reason: split ? undefined : noCall ? "moved_without_reported_call" : reported && points === 0 ? "idle" : "not_reported",
    external_real_usd: out, covey_real_usd: out == null ? null : real - un - out,
    unattributed_real_usd: un, unattributed_points: noCall ? points : unattributed,
    unsplit_real_usd: split || noCall ? 0 : real,
  };
}

const zero: ExternalMeterSums = {
  real_usd: 0, external_real_usd: 0, covey_real_usd: 0, unsplit_real_usd: 0, share: null,
  unattributed_real_usd: 0, unattributed_points: 0, unattributed_steps: 0,
  unattributed_no_call_real_usd: 0, unattributed_no_call_points: 0, incomplete_chunks: 0,
  split_external_list_usd: 0, split_covey_list_usd: 0, points: 0, split_points: 0, split_external_points: 0,
  split_covey_points: 0, split_runs: 0, split_slack_points: 0, split_slack_usd: 0, split_external_slack_points: 0,
  split_external_slack_usd: 0, split_covey_slack_points: 0, split_covey_slack_usd: 0, point_rate: 0, point_rate_mixed: false,
  step_count: 0, split_steps: 0, unsplit_steps: 0, outside_unpriced_steps: 0, outside_unpriced_real_usd: 0,
  unpriced_steps: 0, unpriced_points: 0, unpriced_calls: 0, unpriced_tokens: 0, unmetered_list_usd: 0,
  usage_steps: 0, usage_points: 0,
};

/** A reported step whose outside calls all had no price: unsplit
 *  (outside_unpriced), its share unknown — never covey's whole. */
function unpricedStep(at: string, from: string, points: number, covList: number): ExternalMeterStep {
  return { ...step(at, from, points, false, 0, covList), reported: true, reason: "outside_unpriced" };
}

/** A seat's totals summed from its steps, with the ranges and the agents as
 *  the server computes them (given here: TestSplitSeatWebFixture pins them
 *  in Go). */
function seat(steps: ExternalMeterStep[], extra: Partial<ExternalMeterTotal> = {}): ExternalMeterTotal {
  const sum = (f: (s: ExternalMeterStep) => number, only = (_: ExternalMeterStep) => true) => steps.filter(only).reduce((a, s) => a + f(s), 0);
  const split = (s: ExternalMeterStep) => s.split;
  const out = sum((s) => s.external_real_usd ?? 0, split), cov = sum((s) => s.covey_real_usd ?? 0, split);
  const share = (s: ExternalMeterStep) => (s.external_list_usd / (s.external_list_usd + s.covey_list_usd));
  return {
    ...zero,
    secret_key: steps[0]?.secret_key ?? SEAT, secret_slot: 0, meter_source: "usage_cmd", sources: ["operator: home"],
    hours_with_reading: steps.length, agents: [], points_per_list_usd: null, plan_rates: [],
    real_usd: sum((s) => s.real_usd ?? 0), external_real_usd: out, covey_real_usd: cov,
    unsplit_real_usd: sum((s) => s.unsplit_real_usd), share: out + cov > 0 ? out / (out + cov) : null,
    unattributed_real_usd: sum((s) => s.unattributed_real_usd), unattributed_points: sum((s) => s.unattributed_points),
    unattributed_steps: steps.filter((s) => s.unattributed_points > 0).length,
    unattributed_no_call_real_usd: sum((s) => s.unattributed_real_usd, (s) => !s.split && s.reason === "moved_without_reported_call"),
    unattributed_no_call_points: sum((s) => s.unattributed_points, (s) => !s.split && s.reason === "moved_without_reported_call"),
    split_external_list_usd: sum((s) => s.external_list_usd, split), split_covey_list_usd: sum((s) => s.covey_list_usd, split),
    points: sum((s) => s.points), split_points: sum((s) => s.points, split),
    split_external_points: sum((s) => (s.points - s.unattributed_points) * share(s), split),
    split_covey_points: sum((s) => (s.points - s.unattributed_points) * (1 - share(s)), split),
    split_runs: 1, split_slack_points: 1, split_slack_usd: PP, point_rate: PP,
    step_count: steps.length, split_steps: steps.filter(split).length,
    unsplit_steps: steps.filter((s) => !s.split && s.points > 0 && s.reason !== "moved_without_reported_call").length,
    outside_unpriced_steps: steps.filter((s) => s.reason === "outside_unpriced" && s.points > 0).length,
    outside_unpriced_real_usd: sum((s) => s.unsplit_real_usd, (s) => s.reason === "outside_unpriced"),
    ...extra,
  };
}

function external(seats: { steps: ExternalMeterStep[]; total: ExternalMeterTotal }[]): ExternalUsage {
  const m: ExternalMeterSums = { ...zero };
  for (const { total } of seats) {
    for (const k of Object.keys(zero) as (keyof ExternalMeterSums)[]) {
      if (typeof zero[k] === "number") (m as Record<string, unknown>)[k] = (m[k] as number) + (total[k] as number);
    }
  }
  m.point_rate = Math.max(...seats.map((s) => s.total.point_rate));
  m.point_rate_mixed = new Set(seats.map((s) => s.total.point_rate).filter((r) => r > 0)).size > 1;
  m.share = m.external_real_usd + m.covey_real_usd > 0 ? m.external_real_usd / (m.external_real_usd + m.covey_real_usd) : null;
  return {
    sources: [{ source: "operator: home", meter_credential: { secret_key: SEAT, secret_slot: 0 }, calls: 30,
      list_usd: m.split_external_list_usd, reported_usd: null, unpriced_tokens: 0, unpriced_calls: 0, unpriced_models: [],
      first_at: H18, last_at: H19, hosts: [{ host: "laptop", reported_through: "2026-11-17T20:00:00Z", pushed_at: "2026-11-17T20:01:00Z" }] }],
    cells: [],
    meter: { ...m, steps: seats.flatMap((s) => s.steps), hours: [], credentials: seats.map((s) => s.total) },
  };
}

/** The PR's fixture (cost-controller's (C)): the outside sessions reported
 *  the readings at 18:40 and 19:40, not 20:40. covey's list is $303 and the
 *  outside sessions' $455 over the split readings, the proportions of the
 *  day #120 measured. a1 ran in the first split reading and in the unsplit
 *  one, a2 in both split readings. */
const STEPS = [
  step("2026-11-17T18:40:00Z", "2026-11-17T17:40:00Z", 4, true, 300, 151),
  step("2026-11-17T19:40:00Z", "2026-11-17T18:40:00Z", 2, true, 155, 152),
  step("2026-11-17T20:40:00Z", "2026-11-17T19:40:00Z", 3, false, 0, 40),
];
const AGENTS: ExternalMeterAgent[] = [
  { agent_id: "a2", split_list_usd: 202, real_usd: 0.661702, unsplit_list_usd: 0 },
  { agent_id: "a1", split_list_usd: 101, real_usd: 0.413440, unsplit_list_usd: 40 },
];
// k as the server finds it: the one run (6 points over $758), at its upper
// end, so its own unattributed part is 0.
const SEAT_A = () => seat(STEPS, { agents: AGENTS, split_external_slack_usd: 0.307010, split_covey_slack_usd: 0.228514,
  points_per_list_usd: 7 / 758 });
const EXT = () => external([{ steps: STEPS, total: SEAT_A() }]);

/** A seat with an unattributed part, placed in the three hours, as the
 *  server returns it: a clean stretch (5 and 4 points over $50 of list each,
 *  30 / 20) that sets k = 10 / 100; a reading the outside sessions had not
 *  reported (1 point); 30 points over $100 (20 over 50 / 30, 10 over
 *  0 / 20), 19 of them beyond the rate (12⅔ and 6⅓); then 2 points with no
 *  call at all, a chunk of their own (unattributed whole, ≈). */
const U_STEPS = [
  step("2026-11-17T18:20:00Z", "2026-11-17T18:00:00Z", 5, true, 30, 20),
  step("2026-11-17T18:40:00Z", "2026-11-17T18:20:00Z", 4, true, 30, 20),
  step("2026-11-17T19:10:00Z", "2026-11-17T18:40:00Z", 1, false, 0, 5),
  step("2026-11-17T19:40:00Z", "2026-11-17T19:10:00Z", 20, true, 50, 30, SEAT, 38 / 3),
  step("2026-11-17T20:20:00Z", "2026-11-17T19:40:00Z", 10, true, 0, 20, SEAT, 19 / 3),
  step("2026-11-17T20:40:00Z", "2026-11-17T20:20:00Z", 2, true, 0, 0),
];
const U_SEAT = (extra: Partial<ExternalMeterTotal> = {}) => seat(U_STEPS, { points_per_list_usd: 0.1, split_runs: 2,
  split_slack_points: 2, split_slack_usd: 2 * PP, split_external_slack_usd: PP, split_covey_slack_usd: PP, ...extra });
const escapeRe = (v: string) => v.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

const U_EXT = (extra: Partial<ExternalMeterTotal> = {}) => external([{ steps: U_STEPS, total: U_SEAT(extra) }]);

/** STEPS on a seat no source names: reported with L_out = 0, all covey's
 *  but for 1 point of the 19:40 reading beyond the rate (unattributed). */
const UNLINKED_STEPS = () => STEPS.map((s, i) => step(s.at, s.from, s.points, true, 0, s.covey_list_usd, SEAT, i === 1 ? 1 : 0));
const UNLINKED_SEAT = (b: ExternalMeterStep[]) => seat(b, { sources: [], agents: [
  { agent_id: "a1", split_list_usd: 141, real_usd: 8 * PP * 141 / 343, unsplit_list_usd: 0 },
  { agent_id: "a2", split_list_usd: 202, real_usd: 8 * PP * 202 / 343, unsplit_list_usd: 0 },
] });

/** The cost cells of one seat over three hours, with the meter's steps. */
function cells(ext: ExternalUsage | undefined, points = [4, 2, 3]): CostCells {
  const periods = [H18, H19, H20];
  const meterStep = (period: string, p: number) => ({ period, secret_key: SEAT, secret_slot: 0, credential_kind: "subscription",
    source: "usage_cmd", window_kind: "seven_day", points: p, readings: 2, resets: 0, longest_gap_secs: 900, usd: p * PP, unpriced_points: 0 });
  const cell = (period: string, agent: string, output: number) => ({ period, agent_id: agent, secret_key: SEAT, secret_slot: 0,
    credential_kind: "subscription", reported_usd: output, measured: { output }, engine_usd: output, tokens: { output: output * 50_000 } });
  const total = points.reduce((a, p) => a + p, 0);
  return {
    periods,
    agents: [{ id: "a1", label: "Coordinator", reported_usd: 0, measured_usd: 0 }, { id: "a2", label: "Developer", reported_usd: 0, measured_usd: 0 }],
    types: ["cache_read", "cache_write", "input", "output", "web_search", "unpriced", "unattributed"],
    cells: [cell(H18, "a1", 101), cell(H18, "a2", 50), cell(H19, "a2", 152), cell(H20, "a1", 40)],
    subscription: periods.map((p, i) => meterStep(p, points[i])),
    meter: [{
      secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd", window_kind: "seven_day",
      baseline_at: H18, baseline_percent: 10, baseline_before_window: true, last_at: "2026-11-17T20:40:00Z",
      last_percent: 10 + total, readings: 6, points: total, points_low: total - 1, points_high: total + 1, longest_gap_secs: 900,
      segments: null, measured_usd: 343,
    }],
    plans: [{
      runtime_id: null, ord: 0, secret_key: SEAT, secret_slot: 0, plan_name: "Max", amount: 200, currency: "USD", period: "month",
      tax_basis: "net", fx_to_usd: 1, fx_date: null, valid_from: "2026-11-01T00:00:00Z", valid_to: null,
      weekly_usd: 200 * 12 / 52, usd_per_point: PP,
    }],
    bookings: [],
    external: ext,
  };
}

function serve(data: CostCells) {
  mockFetch({
    "/api/v1/cost/cells": data,
    "/api/v1/cost/billed": { bucket: "hour", points: [], sync: { configured: false, last_attempt_at: null, last_success_at: null,
      last_daily_at: null, data_refreshed_at: null, last_status: "", last_error: "" } },
    "/api/v1/cost/runs": [],
    "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
    "/api/v1/cost/usage-readings": [],
    "/api/v1/cost/indicators": null,
  });
  return renderWithProviders(<Costs />);
}

describe("subscriptionDent", () => {
  it("splits the dent into covey, the outside sessions and the unreported readings, and they add up", () => {
    const d = subscriptionDent(EXT())!;
    expect(d.real).toBeCloseTo(9 * PP, 9);
    expect(d.unsplit.real).toBeCloseTo(3 * PP, 9);
    expect(d.unsplit.steps).toBe(1);
    expect(d.covey.real + d.outside.real + d.unsplit.real).toBeCloseTo(d.real!, 9);
    expect(d.outside.real).toBeCloseTo(1.694089, 6);
    expect(d.covey.real).toBeCloseTo(1.075142, 6);
    expect(d.covey.list).toBe(303);
    expect(d.outside.list).toBe(455);
    expect(d.points).toBe(9);
    expect(d.covey.points + d.outside.points).toBeCloseTo(6, 9);
  });

  it("gives each multiple a lower bound at the low end of its own range, as the server computes it (finding 19, F3c)", () => {
    // The meter is shared with whatever does not report: each multiple is
    // list / (real + slack), at least — never an estimate with an upper end.
    const d = subscriptionDent(EXT())!;
    const split = 6 * PP;
    expect(d.together.multiple).toEqual({ kind: "floor", low: 758 / (split + PP) });
    expect(d.covey.multiple!.kind).toBe("floor");
    expect(d.covey.multiple!.low).toBeCloseTo(303 / (d.covey.real + 0.228514), 9);
    expect(d.outside.multiple!.low).toBeCloseTo(455 / (d.outside.real + 0.307010), 9);
  });

  it("books the movement neither covey nor the reported sessions explain to nobody, and the parts add up (F3c)", () => {
    const d = subscriptionDent(U_EXT())!;
    expect(d.real).toBeCloseTo(42 * PP, 9);
    expect(d.unattributed.real).toBeCloseTo(21 * PP, 9);
    expect(d.unattributed.points).toBeCloseTo(21, 9);
    expect(d.unattributed.steps).toBe(3);
    expect(d.unattributed.share).toBeCloseTo(0.5, 9);
    // Of it, 19 points beyond the rate (a lower bound) and 2 with no call (≈).
    expect(d.unattributed.beyond).toBeCloseTo(19 * PP, 9);
    expect(d.unattributed.noCall).toBeCloseTo(2 * PP, 9);
    expect(d.unsplit.real).toBeCloseTo(PP, 9);
    expect(d.unsplit.steps).toBe(1);
    expect(d.covey.real).toBeCloseTo((2 + 1.6 + 2.75 + 11 / 3) * PP, 9);
    expect(d.outside.real).toBeCloseTo((3 + 2.4 + 55 / 12) * PP, 9);
    expect(d.covey.real + d.outside.real + d.unattributed.real + d.unsplit.real).toBeCloseTo(d.real!, 9);
    // The points too: the attributed points of the split readings.
    expect(d.covey.points + d.outside.points + 19).toBeCloseTo(39, 9);
    expect(d.together.points).toBeCloseTo(20, 9);
    // The multiples over the attributed real: lower bounds.
    expect(d.together.multiple).toEqual({ kind: "floor", low: 200 / (d.covey.real + d.outside.real + 2 * PP) });
    expect(d.covey.multiple!.low).toBeCloseTo(90 / (d.covey.real + PP), 9);
    expect(d.seats[0].pointsPerList).toBe(0.1);
    expect(d.seats[0].unattributed).toBeCloseTo(21 * PP, 9);
    expect(d.incompleteChunks).toBe(0);
    expect(subscriptionDent(U_EXT({ incomplete_chunks: 2 }))!.incompleteChunks).toBe(2);
  });

  it("gives no multiple below 3 points of split movement, nor without a split reading", () => {
    const small = [step(H18, H18, 2, true, 10, 10), step(H19, H18, 5, false, 0, 4)];
    expect(subscriptionDent(external([{ steps: small, total: seat(small) }]))!.noMultiple).toBe("movement");
    const none = [step(H18, H18, 5, false, 3, 10)];
    const d = subscriptionDent(external([{ steps: none, total: seat(none) }]))!;
    expect(d.noMultiple).toBe("split");
    expect(d.covey.real).toBe(0);
    expect(d.unsplit.real).toBeCloseTo(5 * PP, 9);
  });

  it("has no figure without a priced reading, and counts the points no plan prices (finding 23)", () => {
    const unpriced = seat([], { secret_key: "seat_c", sources: [], step_count: 2, unpriced_steps: 2, unpriced_points: 30, point_rate: 0 });
    const d = subscriptionDent(external([{ steps: [], total: unpriced }]))!;
    expect(d.real).toBeNull();
    expect(d.unpricedPoints).toBe(30);
    const both = subscriptionDent(external([{ steps: STEPS, total: SEAT_A() }, { steps: [], total: unpriced }]))!;
    expect(both.real).toBeCloseTo(9 * PP, 9);
    expect(both.unpricedPoints).toBe(30);
    expect(both.seats.find((s) => s.key === "seat_c")!.real).toBeNull();
  });

  it("keeps the points read under the usage basis out of the dent, and does not call them unpriced (0151)", () => {
    const onUsage = seat([], { secret_key: "seat_u", sources: [], step_count: 3, usage_steps: 3, usage_points: 7, point_rate: 0 });
    const d = subscriptionDent(external([{ steps: [], total: onUsage }]))!;
    expect(d.real).toBeNull();
    expect(d.usagePoints).toBe(7);
    expect(d.unpricedPoints).toBe(0);
    const both = subscriptionDent(external([{ steps: STEPS, total: SEAT_A() }, { steps: [], total: onUsage }]))!;
    expect(both.real).toBeCloseTo(9 * PP, 9);
    expect(both.seats.find((s) => s.key === "seat_u")!).toMatchObject({ real: null, usagePoints: 7, unpricedPoints: 0 });
  });

  it("tells readings whose outside calls had no price from unreported ones; neither is covey's (#126 fix 2)", () => {
    const steps = [...STEPS, unpricedStep("2026-11-17T21:40:00Z", "2026-11-17T20:40:00Z", 5, 0.1)];
    const d = subscriptionDent(external([{ steps, total: seat(steps, { agents: AGENTS }) }]))!;
    expect(d.unsplit.steps).toBe(2);
    expect(d.unsplit.unpricedSteps).toBe(1);
    expect(d.unsplit.real).toBeCloseTo(8 * PP, 9);
    expect(d.unsplit.unpricedReal).toBeCloseTo(5 * PP, 9);
    expect(d.covey.real).toBeCloseTo(1.075142, 6);
    const hourly = splitPerPeriod(external([{ steps, total: seat(steps) }]), SEAT, 0, "hour");
    expect(hourly.get(Date.parse("2026-11-17T21:00:00Z"))).toEqual({ covey: 0, outside: 0, unattributed: 0, unsplit: 5 * PP, unsplitSteps: 1, unpricedSteps: 1 });
  });

  it("is absent until a source names a meter", () => {
    expect(subscriptionDent(undefined)).toBeNull();
    const e = EXT();
    expect(subscriptionDent({ ...e, meter: { ...e.meter, steps: [], credentials: [] } })).toBeNull();
  });
});

describe("one rule for the headline, the chart and the agents", () => {
  it("buckets the steps where their readings fall, at any bucket down to 10 minutes (finding 15)", () => {
    const ext = EXT();
    const d = subscriptionDent(ext)!;
    const hourly = splitPerPeriod(ext, SEAT, 0, "hour");
    expect(hourly.get(Date.parse(H20))).toEqual({ covey: 0, outside: 0, unattributed: 0, unsplit: 3 * PP, unsplitSteps: 1, unpricedSteps: 0 });
    // F3c: a chunk's unattributed part and a reading with no call land where
    // their readings fall, apart from the unsplit one.
    const u = splitPerPeriod(U_EXT(), SEAT, 0, "hour");
    expect(u.get(Date.parse(H19))!.unattributed).toBeCloseTo(38 / 3 * PP, 9);
    expect(u.get(Date.parse(H19))!.unsplit).toBeCloseTo(PP, 9);
    expect(u.get(Date.parse(H20))!.unattributed).toBeCloseTo((19 / 3 + 2) * PP, 9);
    expect(u.get(Date.parse(H20))!.unsplitSteps).toBe(0);
    // At 10 minutes the 18:40 reading's split lands at 18:40, not at 18:00.
    const tens = splitPerPeriod(ext, SEAT, 0, "10min");
    expect(tens.has(Date.parse("2026-11-17T18:00:00Z"))).toBe(false);
    expect(tens.get(Date.parse("2026-11-17T18:40:00Z"))!.outside).toBeCloseTo(4 * PP * 300 / 451, 9);
    const daily = [...splitPerPeriod(ext, SEAT, 0, "day").values()];
    expect(daily).toHaveLength(1);
    expect(daily[0].covey).toBeCloseTo(d.covey.real, 9);
    expect(daily[0].outside).toBeCloseTo(d.outside.real, 9);
    expect(daily[0].covey + daily[0].outside + daily[0].unsplit).toBeCloseTo(9 * PP, 9);
  });

  it("gives each agent the server's share of covey's part, per split reading — asserted each, not the sum (findings 10, 22)", () => {
    const data = cells(EXT());
    const [s] = credentialFigures({ cells: data, bucketMs: 3600_000, windowEnd: new Date("2026-11-17T21:00:00Z") });
    expect(s.split).not.toBeNull();
    expect(s.multiple).toBeNull();
    expect(s.noMultiple).toBe("outside");
    const by = new Map(s.agents.map((a) => [a.id, a]));
    expect(by.get("a1")!.realShare).toBeCloseTo(0.413440, 6);
    expect(by.get("a2")!.realShare).toBeCloseTo(0.661702, 6);
    // covey's part is the server's per-chunk estimate (F3c), so each
    // agent's share is one too: "≈", not a bound.
    for (const x of s.agents) expect(x.realBound).toBe("estimate");
    // An agent that ran only in the unsplit reading has no real share: "–", never $0.00.
    const only = cells(external([{ steps: STEPS, total: SEAT_A() }].map((x) => ({ ...x, total: { ...x.total,
      agents: [{ agent_id: "a2", split_list_usd: 303, real_usd: 1.075142, unsplit_list_usd: 0 }, { agent_id: "a1", split_list_usd: 0, real_usd: 0, unsplit_list_usd: 141 }] } }))));
    const [o] = credentialFigures({ cells: only, bucketMs: 3600_000, windowEnd: new Date("2026-11-17T21:00:00Z") });
    const a1 = o.agents.find((a) => a.id === "a1")!;
    expect(a1.realShare).toBeNull();
    expect(a1.realWhy).toBe("unsplit");
    // Without outside sessions the page is as it was.
    const [before] = credentialFigures({ cells: cells(undefined), bucketMs: 3600_000, windowEnd: new Date("2026-11-17T21:00:00Z") });
    expect(before.split).toBeNull();
    expect(before.outsideUnreported).toBe(false);
    expect(before.multiple).not.toBeNull();
    expect(before.agents.reduce((a, x) => a + x.realShare!, 0)).toBeCloseTo(before.real!.value, 9);
    expect(perCredentialTotals([s]).real!.recomputed).toBeNull();
    expect(perCredentialTotals([before]).real!.recomputed).toBe(343);
  });

  it("splits a seat no source names as the server does: covey agents (internal) and the unattributed part, no outside part (#120 review)", () => {
    // No source names this seat, so the server returns its steps reported
    // with L_out = 0 (#126 fix 1): covey's after the chunk's unattributed
    // part — here 1 point of the 19:40 reading. The seat block uses that
    // split, as the headline does, never "≤ share × R" of the whole.
    const b = UNLINKED_STEPS();
    const ext = external([{ steps: b, total: UNLINKED_SEAT(b) }]);
    const [s] = credentialFigures({ cells: cells(ext), bucketMs: 3600_000, windowEnd: new Date("2026-11-17T21:00:00Z") });
    expect(s.split).not.toBeNull();
    expect(s.unlinked).toBe(true);
    expect(s.outsideUnreported).toBe(false);
    expect(s.noMultiple).not.toBe("outside");
    // covey's list over the whole movement is still a lower bound there.
    expect(s.multiple?.kind).toBe("floor");
    for (const a of s.agents) {
      expect(a.realBound).toBe("estimate");
      expect(a.realShare).toBeGreaterThan(0);
    }
    // The agents add up to the server's covey part: the whole less the unattributed point.
    expect(s.split!.covey_real_usd).toBeCloseTo(8 * PP, 9);
    expect(s.agents.reduce((a, x) => a + x.realShare!, 0)).toBeCloseTo(s.split!.covey_real_usd, 9);
    // The chart's real bar: covey and unattributed, nothing outside.
    const by = splitPerPeriod(ext, SEAT, 0, "hour");
    expect(by.get(Date.parse(H19))!.unattributed).toBeCloseTo(PP, 9);
    expect([...by.values()].every((p) => p.outside === 0 && p.unsplit === 0)).toBe(true);
  });

  it("gives an agent whose lines lie only before the range a row of its own, so the agents add up to covey's part (#120 review)", () => {
    const ext = EXT();
    ext.meter.credentials[0] = { ...ext.meter.credentials[0], agents: [...AGENTS,
      { agent_id: "a9", split_list_usd: 12, real_usd: 0.05, unsplit_list_usd: 0 }] };
    const [s] = credentialFigures({ cells: cells(ext), bucketMs: 3600_000, windowEnd: new Date("2026-11-17T21:00:00Z") });
    const a9 = s.agents.find((a) => a.id === "a9")!;
    expect(a9.beforeWindow).toBe(true);
    expect(a9.realShare).toBe(0.05);
    expect(a9.recomputed).toBe(0);
    expect(s.agents.reduce((a, x) => a + (x.realShare ?? 0), 0)).toBeCloseTo(0.413440 + 0.661702 + 0.05, 9);
  });

  it("keeps covey-only bounds on a linked seat whose only reported reading is idle, never ≈ $0.00 (finding 25)", () => {
    // Reported but unmoved with no call on either side, then a reading the
    // outside sessions had not reported: the server counts no split step.
    const b = [
      step("2026-11-17T18:40:00Z", "2026-11-17T17:40:00Z", 0, true, 0, 0),
      step("2026-11-17T19:40:00Z", "2026-11-17T18:40:00Z", 3, false, 0, 2),
    ];
    expect(b[0].split).toBe(false);
    expect(b[0].reason).toBe("idle");
    const total = seat(b, { split_runs: 0, split_slack_points: 0, split_slack_usd: 0 });
    expect(total.split_steps).toBe(0);
    const [s] = credentialFigures({ cells: cells(external([{ steps: b, total }]), [0, 3, 0]), bucketMs: 3600_000,
      windowEnd: new Date("2026-11-17T21:00:00Z") });
    expect(s.split).toBeNull();
    expect(s.outsideUnreported).toBe(true);
    expect(s.noMultiple).not.toBe("outside");
    for (const a of s.agents) expect(a.realBound).toBe("upTo");
  });
});

describe("Costs — the headline", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("shows the dent, its points, its split, the plain sentence and the three multiples, as the API's figures give them", async () => {
    const data = cells(EXT());
    const d = subscriptionDent(data.external)!;
    serve(data);
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    const group = (name: string) => within(row).getByRole("group", { name });
    const value = (name: string) => within(group(name)).getByText(/^≈ /).textContent;
    // covey's and the outside part are estimates (F3c): "≈", never "≤"
    // without the slack.
    expect(value("Real cost of the meters")).toBe(`≈ ${fmtUSD(d.real!)}`);
    expect(group("Real cost of the meters").textContent).toContain(`${fmtNumber(9, 1)} points`);
    expect(value("covey agents (internal)")).toBe(`≈ ${fmtUSD(d.covey.real)}`);
    expect(value("Sessions outside covey")).toBe(`≈ ${fmtUSD(d.outside.real)}`);
    expect(row.textContent).not.toMatch(/≤/);
    expect(group("Sessions outside covey").textContent).toContain(`≈ ${fmtNumber(d.outside.points, 1)} points`);
    expect(value("Not split")).toBe(`≈ ${fmtUSD(d.unsplit.real)}`);
    expect(group("Not split").textContent).toMatch(/readings the outside sessions had not reported: 1/);

    // The sentence names the dent, then what the split readings paid (finding 11).
    expect(row.querySelector("[data-dent-sentence]")!.textContent).toBe(
      `Your subscription paid ≈ ${fmtUSD(d.real!)} real. Of it, the readings the outside sessions reported paid ≈ ${fmtUSD(d.together.real)} ` +
      `for ${fmtUSD(758)} of list-price work: ${fmtUSD(303)} by covey agents (internal), ${fmtUSD(455)} by the sessions outside covey that drive them.`);
    // The multiples are lower bounds: the meter is shared with whatever does
    // not report, whose use would only raise them.
    const m = (x: typeof d.covey) => times(x.multiple!.low);
    expect(row.querySelector("[data-dent-multiples]")!.textContent).toBe(
      `List price per real dollar, at least: ≥ ${m(d.covey)} for covey agents (internal), ≥ ${m(d.outside)} for the sessions outside covey ` +
      `and ≥ ${m(d.together)} for both together. Unattributed use beyond the estimate would only raise them.`);
    expect(row.querySelector("[data-dent-unsplit]")!.textContent).toMatch(/not booked to covey agents \(internal\)/);
    // No movement beyond the rate here: the unattributed part is shown, at
    // $0.00, and no sentence names it.
    expect(value("Unattributed")).toBe(`≈ ${fmtUSD(0)}`);
    expect(row.querySelector("[data-dent-unattributed]")).toBeNull();
  });

  it("names the unattributed part, the rate it rests on and the multiples as lower bounds (F3c)", async () => {
    const data = cells(U_EXT(), [9, 21, 12]);
    const d = subscriptionDent(data.external)!;
    serve(data);
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    const group = (name: string) => within(row).getByRole("group", { name });
    expect(within(group("Unattributed")).getByText(/^≈ /).textContent).toBe(`≈ ${fmtUSD(21 * PP)}`);
    // Beyond the rate a lower bound, the chunk with no call an estimate.
    expect(group("Unattributed").textContent).toContain(`50 % of the dent · ≈ ${fmtNumber(21, 0)} points · ` +
      `beyond the rate: at least ${fmtUSD(19 * PP)} · moved with no call (meter lag or another machine): ≈ ${fmtUSD(2 * PP)} · booked to nobody`);
    expect(within(group("covey agents (internal)")).getByText(/^≈ /).textContent).toBe(`≈ ${fmtUSD(d.covey.real)}`);
    expect(group("covey agents (internal)").textContent).toContain(`≥ ${times(d.covey.multiple!.low)}`);
    expect(row.querySelector("[data-dent-sentence]")!.textContent).toBe(
      `Your subscription paid ≈ ${fmtUSD(42 * PP)} real. Of it, ≈ ${fmtUSD(d.together.real)} went to ${fmtUSD(200)} of list-price work: ` +
      `${fmtUSD(90)} by covey agents (internal), ${fmtUSD(110)} by the sessions outside covey that drive them.`);
    expect(row.querySelector("[data-dent-unattributed]")!.textContent).toBe(
      `≈ ${fmtUSD(21 * PP)} of the meter's movement is not explained by covey agents (internal) or the reported sessions — ` +
      "another machine on the same account, for example; it is booked to nobody.");
    expect(row.querySelector("[data-dent-rate]")!.textContent).toBe(
      `1 point ≈ ${fmtUSD(10)} of list price, the most list price per point seen since the start of the meter week the range begins in; assumes the meter charges in proportion to list price. ` +
      "Movement beyond that rate counts as unattributed, and that part is a lower bound. The parts of covey agents (internal) " +
      "and the outside sessions are estimates and may still hold another machine's use.");
    expect(row.querySelector("[data-dent-incomplete]")).toBeNull();
    expect(row.querySelector("[data-dent-multiples]")!.textContent).toMatch(
      /^List price per real dollar, at least: ≥ .* Unattributed use beyond the estimate would only raise them\.$/);
    // The chart: the 19:00 real bar carries an unattributed segment beside
    // covey's and the outside sessions' parts, and the key names it.
    const bar = document.querySelector(`[data-period="${H19}"]`)!;
    fireEvent.focus(bar);
    const tip = await screen.findByRole("tooltip");
    expect(tip.textContent).toContain(`Unattributed${fmtUSD(38 / 3 * PP)}`);
    expect(tip.textContent).toMatch(/covey agents \(internal\)/);
    expect(document.querySelector('[data-split-key="unattributed"]')!.textContent).toMatch(/Real cost unattributed/);
    expect(document.querySelector("[data-split-note]")!.textContent).toContain(
      `≈ ${fmtUSD(21 * PP)} is unattributed: not explained by covey agents (internal) or the reported sessions, booked to nobody.`);
  });

  it("says when there is no rate yet, and still books a chunk that moved with no call to nobody (F3c)", async () => {
    serve(cells(U_EXT({ points_per_list_usd: null }), [9, 21, 12]));
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(row.querySelector("[data-dent-rate]")!.textContent).toMatch(
      /^No stretch of adjacent chunks of split readings with every call priced moved the meter 10 points or more, so there is no rate yet to measure unattributed use by/);
    expect(row.querySelector("[data-dent-rate]")!.textContent).not.toMatch(/lower bound/);
    // No seat has a rate: the tile says so, never "beyond the rate: at least $0.00".
    const tile = within(row).getByRole("group", { name: "Unattributed" }).textContent;
    expect(tile).toContain("no rate yet to measure use beyond it");
    expect(tile).not.toContain("beyond the rate");
  });

  it("names the earlier plan price a seat's rate was measured at (F3c)", async () => {
    serve(cells(U_EXT({ points_per_list_older_plan_usd: 0.23 }), [9, 21, 12]));
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(row.querySelector("[data-dent-rate]")!.textContent).toMatch(new RegExp("^" + escapeRe(
      `1 point ≈ ${fmtUSD(10)} of list price at the earlier plan price of ${fmtUSD(0.23)} a point, ` +
      "the most list price per point seen since the start of the meter week the range begins in; the current plan price has no rate yet.")));
    expect(within(row).getByRole("group", { name: "Unattributed" }).textContent).toContain("beyond the rate: at least");
  });

  it("says how many chunks had calls of no list price or bookings without a line (F3c)", async () => {
    serve(cells(U_EXT({ incomplete_chunks: 2 }), [9, 21, 12]));
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(row.querySelector("[data-dent-incomplete]")!.textContent).toBe(
      "Chunks of readings with outside calls of no list price or covey bookings without a line: 2. They set no rate and nothing " +
      "in them counts as unattributed, so their parts are approximate and may hold use that is not theirs.");
  });

  it("says how many points have no plan price, and shows such a seat as – (finding 23)", async () => {
    const unpriced = seat([], { secret_key: "seat_c", sources: [], step_count: 1, unpriced_steps: 1, unpriced_points: 30, point_rate: 0 });
    serve(cells(external([{ steps: STEPS, total: SEAT_A() }, { steps: [], total: unpriced }])));
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(row.querySelector("[data-dent-unpriced]")!.textContent).toBe("30 points without a plan price are not in the dent.");
    expect(row.querySelector('[data-dent-seat="seat_c #0"]')!.textContent).toMatch(/^seat_c #0: – \(30 points without a plan price\) · no outside source/);
  });

  it("with every seat on usage alone: no dent, every part –, never ≈ $0.00 or \"had not reported\" (0151, review M1)", async () => {
    const alone = seat([], { sources: ["operator: home"], step_count: 3, usage_steps: 3, usage_points: 6, point_rate: 0 });
    const d = subscriptionDent(external([{ steps: [], total: alone }]))!;
    expect(d.real).toBeNull();
    expect(d.noMultiple).toBe("usage");
    serve(cells(external([{ steps: [], total: alone }])));
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    const total = row.querySelector("[data-dent=\"total\"]")!;
    expect(total.textContent).toContain(i18n.t("costs.dent.noPricedReadingUsage"));
    expect(total.textContent).not.toContain(i18n.t("costs.dent.noPricedReading"));
    for (const k of ["covey", "outside", "unattributed"]) {
      const p = row.querySelector(`[data-dent="${k}"]`)!;
      expect(p.textContent).toContain("–");
      expect(p.textContent).toContain(i18n.t("costs.dent.noPart"));
    }
    // The dent's figures: never ≈ $0.00 (the headline above may list a
    // source's $0.00 of list price: that is a list, not a real cost).
    expect(row.querySelector("[data-dent-detail]")!.textContent).not.toContain(fmtUSD(0));
    expect(row.textContent).not.toMatch(/had not reported/);
    expect(row.querySelector("[data-dent-no-multiple]")!.textContent).toBe(i18n.t("costs.dent.noMultipleUsage"));
    expect(row.querySelector("[data-dent-usage]")).not.toBeNull();
    // The headline: no real cost because every reading was billed by usage,
    // not because of a missing plan price; the seat named as left out.
    expect(row.querySelector("[data-head-sentence]")!.textContent).toMatch(/every reading in the range was taken while the seat was billed by usage/);
    expect(row.querySelector("[data-head-sentence]")!.textContent).not.toMatch(/no reading in the range has a plan price/);
    expect(row.querySelector("[data-head-usage]")!.textContent).toMatch(/^Left out of the real cost and of the multiple: .*\(claude_code_oauth_token #0: 6\.0 points/);
    expect(row.querySelector("[data-head-usage-charges]")!.textContent).toBe(i18n.t("costs.dent.head.usageNoCharge"));
    expect(row.querySelector("[data-head-no-multiple]")).toBeNull();
  });

  it("keeps a seat's time on the usage basis out of the real cost and of the list the multiple divides, and names it (0151)", async () => {
    // STEPS with the 20:40 reading (3 points, covey's $40) taken while the
    // seat was billed by usage: the server prices none of it and splits
    // none of it (reason usage).
    const usageStep: ExternalMeterStep = { ...STEPS[2], reported: true, split: false, reason: "usage", real_usd: null,
      external_real_usd: null, covey_real_usd: null, unattributed_real_usd: 0, unattributed_points: 0, unsplit_real_usd: 0 };
    const steps = [STEPS[0], STEPS[1], usageStep];
    const total = seat(steps, { agents: AGENTS, points: 6, real_usd: 6 * PP, unsplit_steps: 0, unsplit_real_usd: 0,
      usage_steps: 1, usage_points: 3, points_per_list_usd: 7 / 758 });
    const data = cells(external([{ steps, total }]));
    const l = seatLists(data, total);
    expect(l.covey.usage).toBeCloseTo(40, 9);
    expect(l.covey.unpriced).toBe(0);
    expect(l.covey.metered).toBeCloseTo(303, 9);
    const h = headline(data)!;
    // R and Xm: the seat's priced readings only; X: all of it, in its parts.
    expect(h.real).toBeCloseTo(6 * PP, 9);
    expect(h.usage).toBeCloseTo(40, 9);
    expect(h.unpriced).toBe(0);
    expect(h.metered).toBeCloseTo(303 + 455, 9);
    expect(h.list).toBeCloseTo(h.metered + h.unmetered + h.unpriced + h.usage, 9);
    expect(h.multiple).toBeCloseTo(758 / (6 * PP), 9);
    expect(h.why).toBeNull();
    expect(h.usageSeats).toEqual(["claude_code_oauth_token #0"]);
    expect(h.usagePoints).toBe(3);
    expect(h.usageCharges).toBeNull();
    // Without the basis the same reading is a not-reported one, in Xm.
    expect(headline(cells(EXT()))!.metered).toBeCloseTo(343 + 455, 9);
    serve(data);
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(row.querySelector("[data-head-usage]")!.textContent).toBe(i18n.t("costs.dent.head.usageOut",
      { seats: "claude_code_oauth_token #0", n: "3.0", list: fmtUSD(40) }));
    expect(row.querySelector("[data-head-unmetered]")).toBeNull();
  });

  it("names a seat on usage that never moved as billed by usage, and both counts for a mixed seat (0151, review m2)", async () => {
    const still = seat([], { secret_key: "seat_u", sources: [], step_count: 2, usage_steps: 2, usage_points: 0, point_rate: 0 });
    const mixed = seat([], { secret_key: "seat_m", sources: [], step_count: 3, usage_steps: 1, usage_points: 3,
      unpriced_steps: 2, unpriced_points: 5, point_rate: 0 });
    serve(cells(external([{ steps: STEPS, total: SEAT_A() }, { steps: [], total: still }, { steps: [], total: mixed }])));
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(row.querySelector('[data-dent-seat="seat_u #0"]')!.textContent)
      .toMatch(/^seat_u #0: – \(billed by usage: 0\.0 points, their money is the usage charges\)/);
    expect(row.querySelector('[data-dent-seat="seat_u #0"]')!.textContent).not.toMatch(/without a plan price/);
    expect(row.querySelector('[data-dent-seat="seat_m #0"]')!.textContent)
      .toMatch(/^seat_m #0: – \(5\.0 points without a plan price, 3\.0 billed by usage\)/);
  });

  it("says how many points were billed by usage, and shows a seat on usage as – with why, not as unpriced (0151)", async () => {
    const onUsage = seat([], { secret_key: "seat_u", sources: [], step_count: 2, usage_steps: 2, usage_points: 7, point_rate: 0 });
    serve(cells(external([{ steps: STEPS, total: SEAT_A() }, { steps: [], total: onUsage }])));
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(row.querySelector("[data-dent-usage]")!.textContent)
      .toBe("7.0 points read while the seat was billed by usage are not in the dent: their money is the usage charges.");
    expect(row.querySelector("[data-dent-unpriced]")).toBeNull();
    expect(row.querySelector('[data-dent-seat="seat_u #0"]')!.textContent)
      .toMatch(/^seat_u #0: – \(billed by usage: 7.0 points, their money is the usage charges\)/);
  });

  it("words a reading whose outside calls had no price apart, in the headline and the chart, with counts as numbers (#126 fix 2)", async () => {
    const steps = [STEPS[0], STEPS[1], unpricedStep(STEPS[2].at, STEPS[2].from, 3, 40)];
    serve(cells(external([{ steps, total: seat(steps, { agents: AGENTS, unpriced_calls: 1234 }) }])));
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    const unsplit = within(row).getByRole("group", { name: "Not split" }).textContent;
    expect(unsplit).toMatch(/readings whose outside calls had no price: 1/);
    expect(unsplit).not.toMatch(/had not reported/);
    expect(row.querySelector("[data-dent-unsplit]")).toBeNull();
    expect(row.querySelector("[data-dent-unsplit-unpriced]")!.textContent).toBe(
      `≈ ${fmtUSD(3 * PP)} of the movement fell in readings whose outside calls all had no price (1): their share is unknown, so it is split to nobody, not booked to covey agents (internal).`);
    expect(row.querySelector("[data-dent-unpriced-calls]")!.textContent).toMatch(new RegExp(`^${fmtNumber(1234, 0)} outside calls`));
    expect(fmtNumber(1234, 0)).not.toBe("1234");
    const note = document.querySelector("[data-split-note]")!.textContent;
    expect(note).toMatch(/in readings whose outside calls all had no price \(1\) is split to nobody/);
    expect(note).not.toMatch(/had not reported/);
    expect(document.querySelector('[data-split-key="unsplit"]')!.textContent).toMatch(/or their calls had no price/);
  });

  it("names both reasons when the unsplit readings have both", async () => {
    const steps = [...STEPS, unpricedStep("2026-11-17T21:40:00Z", "2026-11-17T20:40:00Z", 5, 0.1)];
    serve(cells(external([{ steps, total: seat(steps, { agents: AGENTS }) }])));
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(within(row).getByRole("group", { name: "Not split" }).textContent).toMatch(
      /readings the outside sessions had not reported: 1 · readings whose outside calls had no price: 1/);
    expect(row.querySelector("[data-dent-unsplit]")!.textContent!.startsWith(`≈ ${fmtUSD(3 * PP)} of the movement`)).toBe(true);
    expect(row.querySelector("[data-dent-unsplit-unpriced]")!.textContent!.startsWith(`≈ ${fmtUSD(5 * PP)} of the movement`)).toBe(true);
  });

  it("is not on the page while no outside source names a meter", async () => {
    serve(cells(undefined));
    expect(await screen.findByRole("group", { name: "Real cost (estimate)" })).toBeInTheDocument();
    expect(document.querySelector("[data-dent-row]")).toBeNull();
    expect(document.querySelector("[data-split-note]")).toBeNull();
  });

  it("stays with an agent filter, marked as organisation-wide: the meter is not the agents'", async () => {
    serve(cells(EXT()));
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(row.querySelector("[data-org-wide]")).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: /Coordinator/ }));
    expect(row.querySelector("[data-org-wide]")).not.toBeNull();
  });

  it("splits the chart's real bar, says which reading was not reported, and moves the multiple to the headline", async () => {
    serve(cells(EXT()));
    await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(document.querySelector("[data-split-note]")!.textContent).toMatch(/≈ .* in readings the outside sessions had not reported \(1\) is split to nobody/);
    // By default the seat shows the multiple of everything on its meter
    // (#120); "covey only" gives today's view: no multiple, the split ones
    // in the headline.
    expect(screen.getByRole("group", { name: "List $ per real $ — everything on this meter" })).toBeTruthy();
    expect(document.querySelector("[data-no-multiple]")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "covey only" }));
    expect(document.querySelector("[data-no-multiple]")!.textContent).toMatch(/The split multiples are in the headline/);
    const shares = [...document.querySelectorAll("[data-real-share]")].map((e) => e.textContent);
    expect(shares).toEqual(expect.arrayContaining([`≈ ${fmtUSD(0.413440)}`, `≈ ${fmtUSD(0.661702)}`]));
    // The 20:00 bar: its real cost is the unreported reading's, split to nobody.
    const bar = document.querySelector(`[data-period="${H20}"]`)!;
    fireEvent.focus(bar);
    const tip = await screen.findByRole("tooltip");
    expect(tip.textContent).not.toMatch(/covey agents \(internal\)/);
    fireEvent.blur(bar);
    const bar18 = document.querySelector(`[data-period="${H18}"]`)!;
    fireEvent.focus(bar18);
    const tip18 = await screen.findByRole("tooltip");
    expect(tip18.textContent).toMatch(/covey agents \(internal\)/);
    expect(tip18.textContent).toMatch(/Sessions outside covey/);
  });

  it("never claims outside sessions share a seat no source names (finding 9), and splits it as the headline does (#120 review)", async () => {
    const b = UNLINKED_STEPS();
    const ext = external([{ steps: b, total: UNLINKED_SEAT(b) }]);
    // The one source is billed per use: it names no meter.
    ext.sources[0] = { ...ext.sources[0], meter_credential: null };
    serve(cells(ext));
    await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(document.body.textContent).not.toMatch(/share this meter/);
    // The real bar is split as the server split it: covey agents (internal) and the
    // unattributed point, and the note says no outside source names the meter.
    expect(document.querySelector("[data-split-note]")!.textContent).toMatch(/^No outside source names this meter/);
    expect(document.querySelector("[data-outside-unreported]")!.textContent).toMatch(/^No outside source reports on this meter, but it may carry use outside covey/);
    const shares = [...document.querySelectorAll("[data-real-share]")].map((e) => e.textContent);
    expect(shares).toEqual([`≈ ${fmtUSD(8 * PP * 202 / 343)}`, `≈ ${fmtUSD(8 * PP * 141 / 343)}`]);
    expect(shares).not.toContain(`≈ ${fmtUSD(0)}`);
    const bar = document.querySelector(`[data-period="${H19}"]`)!;
    fireEvent.focus(bar);
    const tip = await screen.findByRole("tooltip");
    expect(tip.textContent).toMatch(/Unattributed/);
    expect(tip.textContent).not.toMatch(/outside covey/);
    // The per-use source stands in a block of its own, marked, with no real cost.
    expect(document.querySelector("[data-outside-block]")!.textContent).toMatch(/operator: home/);
  });
});
