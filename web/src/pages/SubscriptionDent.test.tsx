import { describe, it, expect, beforeEach } from "vitest";
import { fireEvent, screen, within } from "@testing-library/react";
import Costs from "./Costs";
import type { CostCells, ExternalMeterAgent, ExternalMeterStep, ExternalMeterSums, ExternalMeterTotal, ExternalUsage } from "../api";
import i18n from "../i18n";
import { fmtNumber, fmtUSD } from "../format";
import { mockFetch, renderWithProviders } from "../test/render";
import { credentialFigures, perCredentialTotals, splitPerPeriod, subscriptionDent } from "./costFigures";

// #120, the headline: once Claude Code sessions outside covey report on a
// subscription meter, the page shows the whole dent in the meters, split
// between covey's agents and those sessions, with list $ per real $ for
// each and for both together. The server splits each meter reading over the
// span since the reading before it; a reading the outside sessions had not
// reported is split to nobody — never booked to covey whole — and says so.
// The headline, the chart's real bars and the agents' real share all read
// the server's steps, so they add up to the same figures.

const SEAT = "claude_code_oauth_token";
const PP = 200 * 12 / 52 / 100; // $200 a month: $0.4615 per point
const H18 = "2026-11-17T18:00:00Z", H19 = "2026-11-17T19:00:00Z", H20 = "2026-11-17T20:00:00Z";
const times = (v: number) => `${fmtNumber(v, 1)}×`;

/** One reading step as the server returns it: split by list-price share
 *  when reported and some list is there (after `unattributed` of its points,
 *  its run's part, F3b), else unsplit — idle when reported with neither list
 *  nor movement, unattributed whole when reported with movement and no list. */
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
  split_external_list_usd: 0, split_covey_list_usd: 0, points: 0, split_points: 0, split_external_points: 0,
  split_covey_points: 0, split_runs: 0, split_slack_points: 0, split_slack_usd: 0, split_external_slack_points: 0,
  split_external_slack_usd: 0, split_covey_slack_points: 0, split_covey_slack_usd: 0, point_rate: 0, point_rate_mixed: false,
  step_count: 0, split_steps: 0, unsplit_steps: 0, outside_unpriced_steps: 0, outside_unpriced_real_usd: 0,
  unpriced_steps: 0, unpriced_points: 0, unpriced_calls: 0, unpriced_tokens: 0, unmetered_list_usd: 0,
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
    hours_with_reading: steps.length, agents: [], points_per_list_usd: null,
    real_usd: sum((s) => s.real_usd ?? 0), external_real_usd: out, covey_real_usd: cov,
    unsplit_real_usd: sum((s) => s.unsplit_real_usd), share: out + cov > 0 ? out / (out + cov) : null,
    unattributed_real_usd: sum((s) => s.unattributed_real_usd), unattributed_points: sum((s) => s.unattributed_points),
    unattributed_steps: steps.filter((s) => s.unattributed_points > 0).length,
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

/** F3b's fixture (TestSplitSeatUnattributed in Go), placed in the three
 *  hours: run A (5 and 4 points over $50 of list each, 30 / 20) is the
 *  clean one, k = 10 / 100; a reading the outside sessions had not reported
 *  (1 point) ends it; run B (20 points over 50 / 30, 10 over 0 / 20) moved
 *  30 points over $100, U = 30 − 1 − 0.1 × 100 = 19 of them unattributed
 *  (12⅔ and 6⅓); then 2 points with no call at all. */
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
const U_EXT = (extra: Partial<ExternalMeterTotal> = {}) => external([{ steps: U_STEPS, total: U_SEAT(extra) }]);

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

  it("gives each part a lower bound at the low end of its own range, as the server computes it (finding 19, F3b)", () => {
    // The meter is shared with whatever does not report: each multiple is
    // list / (real + slack), at least — never an estimate with an upper end.
    const d = subscriptionDent(EXT())!;
    const split = 6 * PP;
    expect(d.together.multiple).toEqual({ kind: "floor", low: 758 / (split + PP) });
    expect(d.covey.multiple!.kind).toBe("floor");
    expect(d.covey.multiple!.low).toBeCloseTo(303 / (d.covey.real + 0.228514), 9);
    expect(d.outside.multiple!.low).toBeCloseTo(455 / (d.outside.real + 0.307010), 9);
  });

  it("books the movement neither covey nor the reported sessions explain to nobody, and the parts add up (F3b)", () => {
    const d = subscriptionDent(U_EXT())!;
    expect(d.real).toBeCloseTo(42 * PP, 9);
    expect(d.unattributed.real).toBeCloseTo(21 * PP, 9);
    expect(d.unattributed.points).toBeCloseTo(21, 9);
    expect(d.unattributed.steps).toBe(3);
    expect(d.unattributed.share).toBeCloseTo(0.5, 9);
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
    expect(d.rateUnknown).toBe(false);
    expect(subscriptionDent(U_EXT({ points_per_list_usd: null }))!.rateUnknown).toBe(true);
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
    // F3b: a run's unattributed part and a reading with no call land where
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
    // covey's part is an upper bound (F3b), so each agent's share is one too.
    for (const x of s.agents) expect(x.realBound).toBe("upTo");
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

  it("keeps covey-only bounds on a seat no source names, never ≈ $0.00 (finding 9)", () => {
    // Seat A carries the link; no source names this seat, so the server
    // returns its steps reported with L_out = 0: all covey's (#126 fix 1).
    const b = STEPS.map((s) => step(s.at, s.from, s.points, true, 0, s.covey_list_usd));
    const ext = external([{ steps: b, total: seat(b, { sources: [] }) }]);
    const [s] = credentialFigures({ cells: cells(ext), bucketMs: 3600_000, windowEnd: new Date("2026-11-17T21:00:00Z") });
    expect(s.split).toBeNull();
    expect(s.outsideUnreported).toBe(true);
    expect(s.noMultiple).not.toBe("outside");
    expect(s.multiple?.kind).toBe("floor");
    for (const a of s.agents) {
      expect(a.realBound).toBe("upTo");
      expect(a.realShare).toBeGreaterThan(0);
    }
    expect(s.agents.reduce((a, x) => a + x.realShare!, 0)).toBeCloseTo(s.real!.value, 9);
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
    // covey's and the outside part are upper bounds (F3b): "≤", not "≈".
    const bound = (name: string) => within(group(name)).getByText(/^≤ /).textContent;
    expect(value("Real cost of the meters")).toBe(`≈ ${fmtUSD(d.real!)}`);
    expect(group("Real cost of the meters").textContent).toContain(`${fmtNumber(9, 1)} points`);
    expect(bound("covey's agents")).toBe(`≤ ${fmtUSD(d.covey.real)}`);
    expect(bound("Sessions outside covey")).toBe(`≤ ${fmtUSD(d.outside.real)}`);
    expect(group("Sessions outside covey").textContent).toContain(`≈ ${fmtNumber(d.outside.points, 1)} points`);
    expect(value("Not split")).toBe(`≈ ${fmtUSD(d.unsplit.real)}`);
    expect(group("Not split").textContent).toMatch(/readings the outside sessions had not reported: 1/);

    // The sentence names the dent, then what the split readings paid (finding 11).
    expect(row.querySelector("[data-dent-sentence]")!.textContent).toBe(
      `Your subscription paid ≈ ${fmtUSD(d.real!)} real. Of it, the readings the outside sessions reported paid ≤ ${fmtUSD(d.together.real)} ` +
      `for ${fmtUSD(758)} of list-price work: ${fmtUSD(303)} by covey's agents, ${fmtUSD(455)} by the sessions outside covey that drive them.`);
    // The multiples are lower bounds: the meter is shared with whatever does
    // not report, whose use would only raise them.
    const m = (x: typeof d.covey) => times(x.multiple!.low);
    expect(row.querySelector("[data-dent-multiples]")!.textContent).toBe(
      `List price per real dollar, at least: ≥ ${m(d.covey)} for covey's agents, ≥ ${m(d.outside)} for the sessions outside covey ` +
      `and ≥ ${m(d.together)} for both together. Unattributed use beyond the estimate would only raise them.`);
    expect(row.querySelector("[data-dent-unsplit]")!.textContent).toMatch(/not booked to covey's agents/);
    // No movement beyond the rate here: the unattributed part is shown, at
    // $0.00, and no sentence names it.
    expect(value("Unattributed")).toBe(`≈ ${fmtUSD(0)}`);
    expect(row.querySelector("[data-dent-unattributed]")).toBeNull();
  });

  it("names the unattributed part, the rate it rests on and the multiples as lower bounds (F3b)", async () => {
    const data = cells(U_EXT(), [9, 21, 12]);
    const d = subscriptionDent(data.external)!;
    serve(data);
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    const group = (name: string) => within(row).getByRole("group", { name });
    expect(within(group("Unattributed")).getByText(/^≈ /).textContent).toBe(`≈ ${fmtUSD(21 * PP)}`);
    expect(group("Unattributed").textContent).toContain(`50 % of the dent · ≈ ${fmtNumber(21, 0)} points · at least this much · booked to nobody`);
    expect(within(group("covey's agents")).getByText(/^≤ /).textContent).toBe(`≤ ${fmtUSD(d.covey.real)}`);
    expect(group("covey's agents").textContent).toContain(`≥ ${times(d.covey.multiple!.low)}`);
    expect(row.querySelector("[data-dent-sentence]")!.textContent).toBe(
      `Your subscription paid ≈ ${fmtUSD(42 * PP)} real. Of it, ≤ ${fmtUSD(d.together.real)} went to ${fmtUSD(200)} of list-price work: ` +
      `${fmtUSD(90)} by covey's agents, ${fmtUSD(110)} by the sessions outside covey that drive them.`);
    expect(row.querySelector("[data-dent-unattributed]")!.textContent).toBe(
      `≈ ${fmtUSD(21 * PP)} of the meter's movement is not explained by covey's agents or the reported sessions — ` +
      "another machine on the same account, for example; it is booked to nobody.");
    expect(row.querySelector("[data-dent-rate]")!.textContent).toBe(
      `1 point ≈ ${fmtUSD(10)} of list price, the lowest rate seen in the range; assumes the meter charges in proportion to list price. ` +
      "Movement beyond that rate counts as unattributed, so the unattributed part is a lower bound, and the parts of covey's agents " +
      "and the outside sessions are at most what they show.");
    expect(row.querySelector("[data-dent-multiples]")!.textContent).toMatch(
      /^List price per real dollar, at least: ≥ .* Unattributed use beyond the estimate would only raise them\.$/);
    // The chart: the 19:00 real bar carries an unattributed segment beside
    // covey's and the outside sessions' parts, and the key names it.
    const bar = document.querySelector(`[data-period="${H19}"]`)!;
    fireEvent.focus(bar);
    const tip = await screen.findByRole("tooltip");
    expect(tip.textContent).toMatch(new RegExp(`Unattributed${fmtUSD(38 / 3 * PP).replace(/[$.]/g, "\\$&")}`));
    expect(tip.textContent).toMatch(/covey's agents/);
    expect(document.querySelector('[data-split-key="unattributed"]')!.textContent).toMatch(/Real cost unattributed/);
    expect(document.querySelector("[data-split-note]")!.textContent).toContain(
      `≈ ${fmtUSD(21 * PP)} is unattributed: not explained by covey's agents or the reported sessions, booked to nobody.`);
  });

  it("says when there is no rate yet, and still books a reading that moved with no call to nobody (F3b)", async () => {
    serve(cells(U_EXT({ points_per_list_usd: null }), [9, 21, 12]));
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(row.querySelector("[data-dent-rate]")!.textContent).toMatch(
      /^No run of split readings moved the meter 5 points or more, so there is no rate yet to measure unattributed use by/);
    expect(row.querySelector("[data-dent-rate]")!.textContent).not.toMatch(/lower bound/);
  });

  it("says how many points have no plan price, and shows such a seat as – (finding 23)", async () => {
    const unpriced = seat([], { secret_key: "seat_c", sources: [], step_count: 1, unpriced_steps: 1, unpriced_points: 30, point_rate: 0 });
    serve(cells(external([{ steps: STEPS, total: SEAT_A() }, { steps: [], total: unpriced }])));
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(row.querySelector("[data-dent-unpriced]")!.textContent).toBe("30 points without a plan price are not in the dent.");
    expect(row.querySelector('[data-dent-seat="seat_c #0"]')!.textContent).toMatch(/^seat_c #0: – \(30 points without a plan price\) · no outside source/);
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
      `≈ ${fmtUSD(3 * PP)} of the movement fell in readings whose outside calls all had no price (1): their share is unknown, so it is split to nobody, not booked to covey's agents.`);
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
    expect(document.querySelector("[data-no-multiple]")!.textContent).toMatch(/The split multiples are in the headline/);
    const shares = [...document.querySelectorAll("[data-real-share]")].map((e) => e.textContent);
    expect(shares).toEqual(expect.arrayContaining([`≤ ${fmtUSD(0.413440)}`, `≤ ${fmtUSD(0.661702)}`]));
    // The 20:00 bar: its real cost is the unreported reading's, split to nobody.
    const bar = document.querySelector(`[data-period="${H20}"]`)!;
    fireEvent.focus(bar);
    const tip = await screen.findByRole("tooltip");
    expect(tip.textContent).not.toMatch(/covey's agents/);
    fireEvent.blur(bar);
    const bar18 = document.querySelector(`[data-period="${H18}"]`)!;
    fireEvent.focus(bar18);
    const tip18 = await screen.findByRole("tooltip");
    expect(tip18.textContent).toMatch(/covey's agents/);
    expect(tip18.textContent).toMatch(/Sessions outside covey/);
  });

  it("never claims outside sessions share a seat no source names (finding 9)", async () => {
    const b = STEPS.map((s) => step(s.at, s.from, s.points, true, 0, s.covey_list_usd));
    serve(cells(external([{ steps: b, total: seat(b, { sources: [] }) }])));
    await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(document.body.textContent).not.toMatch(/share this meter/);
    expect(document.querySelector("[data-split-note]")).toBeNull();
    expect(document.querySelector("[data-outside-unreported]")!.textContent).toMatch(/No outside source reports on this meter/);
    const shares = [...document.querySelectorAll("[data-real-share]")].map((e) => e.textContent);
    expect(shares.every((v) => v!.startsWith("≤ "))).toBe(true);
    expect(shares).not.toContain(`≈ ${fmtUSD(0)}`);
  });
});
