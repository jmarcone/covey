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
 *  when reported and some list is there, else unsplit — idle when reported
 *  with neither list nor movement. */
function step(at: string, from: string, points: number, reported: boolean, outList: number, covList: number, key = SEAT): ExternalMeterStep {
  const real = points * PP;
  const split = reported && outList + covList > 0;
  const out = split ? real * outList / (outList + covList) : null;
  return {
    at, from, secret_key: key, secret_slot: 0, points, reset: false, real_usd: real,
    external_list_usd: outList, covey_list_usd: covList, reported, split,
    reason: split ? undefined : reported && points === 0 && outList + covList === 0 ? "idle" : "not_reported",
    external_real_usd: out, covey_real_usd: out == null ? null : real - out, unsplit_real_usd: split ? 0 : real,
  };
}

const zero: ExternalMeterSums = {
  real_usd: 0, external_real_usd: 0, covey_real_usd: 0, unsplit_real_usd: 0, share: null,
  split_external_list_usd: 0, split_covey_list_usd: 0, points: 0, split_points: 0, split_external_points: 0,
  split_covey_points: 0, split_runs: 0, split_slack_points: 0, split_slack_usd: 0, split_external_slack_points: 0,
  split_external_slack_usd: 0, split_covey_slack_points: 0, split_covey_slack_usd: 0, point_rate: 0, point_rate_mixed: false,
  step_count: 0, split_steps: 0, unsplit_steps: 0, unpriced_steps: 0, unpriced_points: 0, unpriced_calls: 0,
  unpriced_tokens: 0, unmetered_list_usd: 0,
};

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
    hours_with_reading: steps.length, agents: [],
    real_usd: sum((s) => s.real_usd ?? 0), external_real_usd: out, covey_real_usd: cov,
    unsplit_real_usd: sum((s) => s.unsplit_real_usd), share: out + cov > 0 ? out / (out + cov) : null,
    split_external_list_usd: sum((s) => s.external_list_usd, split), split_covey_list_usd: sum((s) => s.covey_list_usd, split),
    points: sum((s) => s.points), split_points: sum((s) => s.points, split),
    split_external_points: sum((s) => s.points * share(s), split), split_covey_points: sum((s) => s.points * (1 - share(s)), split),
    split_runs: 1, split_slack_points: 1, split_slack_usd: PP, point_rate: PP,
    step_count: steps.length, split_steps: steps.filter(split).length, unsplit_steps: steps.filter((s) => !s.split && s.points > 0).length,
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
const SEAT_A = () => seat(STEPS, { agents: AGENTS, split_external_slack_usd: 0.307010, split_covey_slack_usd: 0.228514 });
const EXT = () => external([{ steps: STEPS, total: SEAT_A() }]);

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

  it("gives each part the range of its own share, as the server computes it, not a share of the whole's (finding 19)", () => {
    const d = subscriptionDent(EXT())!;
    const split = 6 * PP;
    expect(d.together.multiple).toEqual({ kind: "estimate", value: 758 / split, low: 758 / (split + PP), high: 758 / (split - PP) });
    const cv = d.covey.multiple!, out = d.outside.multiple!;
    expect(cv.low).toBeCloseTo(303 / (d.covey.real + 0.228514), 9);
    expect(cv.high).toBeCloseTo(303 / (d.covey.real - 0.228514), 9);
    expect(out.low).toBeCloseTo(455 / (d.outside.real + 0.307010), 9);
    // A part whose real cost could be 0 within the whole percents has no
    // upper end: shown as "≥ low", never ∞.
    const thin = subscriptionDent(external([{ steps: STEPS, total: SEAT_A() }].map((x) => ({ ...x, total: { ...x.total, split_covey_slack_usd: 2 } }))))!;
    expect(thin.covey.multiple!.high).toBe(Infinity);
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
    expect(hourly.get(Date.parse(H20))).toEqual({ covey: 0, outside: 0, unsplit: 3 * PP, unsplitSteps: 1 });
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
    // Seat A carries the link; this seat has readings only, all unsplit.
    const b = STEPS.map((s) => step(s.at, s.from, s.points, false, 0, s.covey_list_usd));
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
    expect(value("Real cost of the meters")).toBe(`≈ ${fmtUSD(d.real!)}`);
    expect(group("Real cost of the meters").textContent).toContain(`${fmtNumber(9, 1)} points`);
    expect(value("covey's agents")).toBe(`≈ ${fmtUSD(d.covey.real)}`);
    expect(value("Sessions outside covey")).toBe(`≈ ${fmtUSD(d.outside.real)}`);
    expect(group("Sessions outside covey").textContent).toContain(`≈ ${fmtNumber(d.outside.points, 1)} points`);
    expect(value("Not split")).toBe(`≈ ${fmtUSD(d.unsplit.real)}`);
    expect(group("Not split").textContent).toMatch(/readings the outside sessions had not reported: 1/);

    // The sentence names the dent, then what the split readings paid (finding 11).
    expect(row.querySelector("[data-dent-sentence]")!.textContent).toBe(
      `Your subscription paid ≈ ${fmtUSD(d.real!)} real. Of it, the readings the outside sessions reported paid ≈ ${fmtUSD(d.together.real)} ` +
      `for ${fmtUSD(758)} of list-price work: ${fmtUSD(303)} by covey's agents, ${fmtUSD(455)} by the sessions outside covey that drive them.`);
    const m = (x: typeof d.covey) => (x.multiple?.kind === "estimate" ? times(x.multiple.value) : "");
    expect(row.querySelector("[data-dent-multiples]")!.textContent).toBe(
      `List price per real dollar: ≈ ${m(d.covey)} for covey's agents, ≈ ${m(d.outside)} for the sessions outside covey ` +
      `and ≈ ${m(d.together)} for both together (range ${times(d.together.multiple!.low)} to ${times(d.together.multiple!.high)}).`);
    expect(row.querySelector("[data-dent-unsplit]")!.textContent).toMatch(/not booked to covey's agents/);
  });

  it("says how many points have no plan price, and shows such a seat as – (finding 23)", async () => {
    const unpriced = seat([], { secret_key: "seat_c", sources: [], step_count: 1, unpriced_steps: 1, unpriced_points: 30, point_rate: 0 });
    serve(cells(external([{ steps: STEPS, total: SEAT_A() }, { steps: [], total: unpriced }])));
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(row.querySelector("[data-dent-unpriced]")!.textContent).toBe("30 points without a plan price are not in the dent.");
    expect(row.querySelector('[data-dent-seat="seat_c #0"]')!.textContent).toMatch(/^seat_c #0: – \(30 points without a plan price\) · no outside source/);
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
    expect(shares).toEqual(expect.arrayContaining([`≈ ${fmtUSD(0.413440)}`, `≈ ${fmtUSD(0.661702)}`]));
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
    const b = STEPS.map((s) => step(s.at, s.from, s.points, false, 0, s.covey_list_usd));
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
