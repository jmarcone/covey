import { describe, it, expect, beforeEach } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import Costs from "./Costs";
import type { CostCells, ExternalCell, ExternalMeterStep, ExternalMeterSums, ExternalMeterTotal, ExternalUsage } from "../api";
import i18n from "../i18n";
import { fmtCount, fmtUSD } from "../format";
import { mockFetch, renderWithProviders } from "../test/render";
import { subscriptionDent } from "./costFigures";
import { OPERATOR_SOURCE, outsideKpis, outsideSplit } from "./outsideFigures";
import { withinSplit } from "./StackedCostChart";

// #163: the sessions outside covey are not one number. Where the cost page
// names them, it names three parts: covey's own agents (internal), the covey
// operator (the Claude Code session that drives covey from outside) and the
// other projects on the same machine and plan. The parts of a figure add up
// to the figure, the operator is the source labelled "covey operator", and
// the line of the other projects is there only where there are other
// projects, with each of them in its hover.

const SEAT = "claude_code_oauth_token";
const PP = 200 * 12 / 52 / 100;
const H18 = "2026-11-17T18:00:00Z", H19 = "2026-11-17T19:00:00Z";
const OP = OPERATOR_SOURCE, SY = "projects-sylius", WORK = "operator: work";
const t = (k: string, o: Record<string, unknown> = {}) => i18n.t(k, o) as string;
const LEAD = "At token prices (as reported by Claude Code)";
const MARK = "(recomputed for sources without a reported cost)";

const zero: ExternalMeterSums = {
  real_usd: 0, external_real_usd: 0, covey_real_usd: 0, unsplit_real_usd: 0, share: null,
  unattributed_real_usd: 0, unattributed_points: 0, unattributed_steps: 0,
  unattributed_no_call_real_usd: 0, unattributed_no_call_points: 0, incomplete_chunks: 0,
  split_external_list_usd: 0, split_covey_list_usd: 0, points: 0, split_points: 0, split_external_points: 0,
  split_covey_points: 0, split_runs: 0, split_slack_points: 0, split_slack_usd: 0, split_external_slack_points: 0,
  split_external_slack_usd: 0, split_covey_slack_points: 0, split_covey_slack_usd: 0, point_rate: 0, point_rate_mixed: false,
  step_count: 0, split_steps: 0, unsplit_steps: 0, outside_unpriced_steps: 0, outside_unpriced_real_usd: 0,
  unpriced_steps: 0, unpriced_points: 0, unpriced_calls: 0, unpriced_tokens: 0, unmetered_list_usd: 0,
};

/** Two readings of 3 points each, split: covey's lines $6 + $4 at the list
 *  ($5 + $4 as Claude Code reported them); the outside calls $1 + $4 at the
 *  list, $1 + $4.50 at their lead price. */
function steps(): ExternalMeterStep[] {
  const st = (at: string, from: string, cov: number, covLead: number, out: number, outLead: number): ExternalMeterStep => ({
    at, from, secret_key: SEAT, secret_slot: 0, points: 3, reset: false, real_usd: 3 * PP,
    external_list_usd: out, covey_list_usd: cov, external_lead_usd: outLead, covey_lead_usd: covLead, reported: true, split: true,
    external_real_usd: PP, covey_real_usd: 2 * PP, unattributed_real_usd: 0, unattributed_points: 0, unsplit_real_usd: 0,
    source_parts: [{ source: OP, list_usd: out / 2, real_usd: PP / 2 }, { source: SY, list_usd: out / 2, real_usd: PP / 2 }],
  });
  return [st("2026-11-17T18:40:00Z", "2026-11-17T17:40:00Z", 6, 5, 1, 1), st("2026-11-17T19:40:00Z", "2026-11-17T18:40:00Z", 4, 4, 4, 4.5)];
}

/** The seat as the server sends it, the lead counterparts included: of the
 *  list in no reading's span, covey's $0.25 ($0.20 lead) and the outside
 *  sessions' $0.50 ($0.40 lead); before the meter's first reading covey's
 *  $0.10 ($0.05) and theirs $0.20 ($0.15), the rest after its last. */
function seatTotal(): ExternalMeterTotal {
  return {
    ...zero, secret_key: SEAT, secret_slot: 0, meter_source: "usage_cmd", sources: [OP, SY],
    hours_with_reading: 2, points_per_list_usd: null, plan_rates: [],
    real_usd: 6 * PP, external_real_usd: 2 * PP, covey_real_usd: 4 * PP, share: 1 / 3,
    split_external_list_usd: 5, split_covey_list_usd: 10, split_external_lead_usd: 5.5, split_covey_lead_usd: 9,
    points: 6, split_points: 6, split_external_points: 2, split_covey_points: 4,
    split_runs: 1, split_slack_points: 2, split_slack_usd: 2 * PP, split_external_slack_usd: 2 * PP / 3, split_covey_slack_usd: 4 * PP / 3,
    point_rate: PP, step_count: 2, split_steps: 2,
    unmetered_list_usd: 0.5, unmetered_covey_list_usd: 0.25, unmetered_before_list_usd: 0.2, unmetered_covey_before_list_usd: 0.1,
    unmetered_lead_usd: 0.4, unmetered_covey_lead_usd: 0.2, unmetered_before_lead_usd: 0.15, unmetered_covey_before_lead_usd: 0.05,
    floor_list_usd: 13, floor_lead_usd: 12,
    source_parts: [{ source: OP, list_usd: 2.5, real_usd: PP }, { source: SY, list_usd: 2.5, real_usd: PP }],
    agents: [{ agent_id: "a1", split_list_usd: 10, real_usd: 4 * PP, unsplit_list_usd: 0 }],
  };
}

const host = [{ host: "laptop", reported_through: "2026-11-17T20:00:00Z", pushed_at: "2026-11-17T20:01:00Z" }];

function cell(period: string, source: string, o: Partial<ExternalCell> & { calls: number }): ExternalCell {
  return {
    period, source, origin: "main", list_usd: null, measured: {}, reported_usd: null, reported_calls: 0,
    tokens: { input: 100, output: 10_000, cache_read: 0, cache_write_5m: 0, cache_write_1h: 0 },
    unpriced_tokens: 0, unpriced_calls: 0, lead_usd: null, lead_unpriced_calls: 0, lead_unpriced_tokens: 0, ...o,
  };
}

/** The operator: at H18 4 calls, all reported ($1); at H19 6 calls, 3
 *  reported ($1.50 for $1 of list) and 3 not ($1 of list): lead $2.50.
 *  sylius: 5 calls with no reported cost ($2 at the list) and one of a model
 *  the list does not know, with no reported cost either: 1,000 tokens with
 *  no price at all. work (per use): 4 calls, all reported ($1 for $1.50). */
function external(): ExternalUsage {
  const total = seatTotal();
  const { secret_key: _k, secret_slot: _s, meter_source: _m, sources: _src, hours_with_reading: _h, points_per_list_usd: _p,
    plan_rates: _r, agents: _a, ...sums } = total;
  const src = (source: string, link: boolean, calls: number, list: number, reported: number | null, lead: number, unpriced = 0) => ({
    source, meter_credential: link ? { secret_key: SEAT, secret_slot: 0 } : null, calls, list_usd: list, reported_usd: reported,
    unpriced_tokens: unpriced, unpriced_calls: unpriced > 0 ? 1 : 0, unpriced_models: unpriced > 0 ? ["claude-sonnet-5-7"] : [],
    lead_usd: lead, lead_unpriced_calls: unpriced > 0 ? 1 : 0, lead_unpriced_tokens: unpriced,
    first_at: H18, last_at: H19, hosts: host,
  });
  return {
    sources: [src(OP, true, 10, 3, 2.5, 3.5), src(WORK, false, 4, 1.5, 1, 1), src(SY, true, 6, 2, null, 2, 1000)],
    cells: [
      cell(H18, OP, { calls: 4, list_usd: 1, measured: { output: 1 }, reported_usd: 1, reported_calls: 4, lead_usd: 1 }),
      cell(H19, OP, { calls: 6, list_usd: 2, measured: { output: 2 }, reported_usd: 1.5, reported_calls: 3, lead_usd: 2.5 }),
      cell(H19, SY, { calls: 6, list_usd: 2, measured: { output: 2 }, unpriced_calls: 1, unpriced_tokens: 1000, lead_usd: 2,
        lead_unpriced_calls: 1, lead_unpriced_tokens: 1000 }),
      cell(H18, WORK, { calls: 4, list_usd: 1.5, measured: { output: 1.5 }, reported_usd: 1, reported_calls: 4, lead_usd: 1 }),
    ],
    meter: { ...sums, steps: steps(), hours: [], credentials: [total] },
  };
}

/** covey's agent a1 on the seat: $6 and $4 at the list, reported $5 and $4 (`engine` overrides). */
function cells(ext: ExternalUsage | null = external(), engine: [number, number] = [5, 4]): CostCells {
  const c = (period: string, list: number, eng: number) => ({ period, agent_id: "a1", secret_key: SEAT, secret_slot: 0,
    credential_kind: "subscription", reported_usd: eng, measured: { output: list }, engine_usd: eng,
    tokens: { input: 500, output: list * 50_000, cache_read: 0 } });
  const meterStep = (period: string) => ({ period, secret_key: SEAT, secret_slot: 0, credential_kind: "subscription",
    source: "usage_cmd", window_kind: "seven_day", points: 3, readings: 2, resets: 0, longest_gap_secs: 900, usd: 3 * PP, unpriced_points: 0 });
  return {
    periods: [H18, H19],
    agents: [{ id: "a1", label: "Coordinator", reported_usd: 9, measured_usd: 10 }],
    types: ["cache_read", "cache_write", "input", "output", "web_search", "unpriced", "unattributed"],
    cells: [c(H18, 6, engine[0]), c(H19, 4, engine[1])],
    subscription: [meterStep(H18), meterStep(H19)],
    meter: [{
      secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd", window_kind: "seven_day",
      baseline_at: "2026-11-17T17:40:00Z", baseline_percent: 10, baseline_before_window: true, last_at: "2026-11-17T19:40:00Z",
      last_percent: 16, readings: 3, points: 6, points_low: 5, points_high: 7, longest_gap_secs: 900, segments: null, measured_usd: 10,
    }],
    plans: [{
      runtime_id: null, ord: 0, secret_key: SEAT, secret_slot: 0, plan_name: "Max", amount: 200, currency: "USD", period: "month",
      tax_basis: "net", fx_to_usd: 1, fx_date: null, valid_from: "2026-11-01T00:00:00Z", valid_to: null,
      weekly_usd: 200 * 12 / 52, usd_per_point: PP,
    }],
    bookings: [],
    external: ext ?? undefined,
  };
}


function serve(data: CostCells = cells(), route = "/costs") {
  mockFetch({
    "/api/v1/cost/cells": data,
    "/api/v1/cost/billed": { bucket: "hour", points: [], sync: { configured: false, last_attempt_at: null, last_success_at: null,
      last_daily_at: null, data_refreshed_at: null, last_status: "", last_error: "" } },
    "/api/v1/cost/runs": [],
    "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
    "/api/v1/cost/external-usage": { bucket: "day", since: H18, source: null, sources: data.external?.sources ?? [], models: [], cells: [],
      meter: data.external?.meter },
    "/api/v1/cost/usage-readings": [],
    "/api/v1/cost/indicators": null,
  });
  return renderWithProviders(<Costs />, { route, path: "/costs" });
}


const kpi = (label: string) => document.querySelector<HTMLElement>(`[data-kpi="${label}"]`)!;
const lines = (label: string) => [...kpi(label).querySelectorAll("[data-parts] [data-part]")].map((e) => e.textContent);
const usd = (text: string | null | undefined) => Number((text ?? "").match(/\$([\d,.]+)/)?.[1].replace(/,/g, ""));

/** Only the operator reports: the other projects' sources, cells and parts are gone. */
function operatorOnly(): CostCells {
  const data = cells();
  const ext = data.external!;
  ext.sources = ext.sources.filter((s) => s.source === OP);
  ext.cells = ext.cells.filter((c) => c.source === OP);
  for (const st of ext.meter.steps) st.source_parts = st.source_parts?.filter((p) => p.source === OP);
  const seat = ext.meter.credentials[0];
  seat.sources = [OP];
  seat.source_parts = seat.source_parts?.filter((p) => p.source === OP);
  ext.meter.source_parts = ext.meter.source_parts?.filter((p) => p.source === OP);
  return data;
}

describe("the outside sessions in three parts (#163)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("splits the lead tile into covey's agents (internal), the covey operator and the other projects, one line each, and they add up to the tile", async () => {
    serve();
    // covey $9 as Claude Code reported it; the operator $3.50; sylius $2 and work $1.
    await waitFor(() => expect(kpi(LEAD)?.querySelector("[data-kpi-value]")?.textContent).toBe(fmtUSD(9 + 3.5 + 3)));
    const parts = lines(LEAD);
    expect(parts).toHaveLength(3);
    expect(parts[0]).toBe(`covey agents (internal) ${fmtUSD(9)}`);
    expect(parts[1]).toBe(`covey operator ${fmtUSD(3.5)} *`);
    expect(parts[2]).toBe(`other projects ${fmtUSD(3)} + ${fmtCount(1000)} tokens with no list price *`);
    // The lines are one under the other, and add up to the tile's figure.
    expect(kpi(LEAD).querySelectorAll("[data-parts] > div")).toHaveLength(4);
    expect(parts.reduce((a, p) => a + usd(p), 0)).toBeCloseTo(usd(kpi(LEAD).querySelector("[data-kpi-value]")!.textContent), 9);
    // The mark, said once: a short footnote, not on every line.
    expect(kpi(LEAD).querySelector("[data-part-footnote]")!.textContent).toBe("* recomputed for sources without a reported cost");
    expect(kpi(LEAD).textContent).not.toContain(MARK);
    // The other projects: each one with its amount, as the hover.
    expect(kpi(LEAD).querySelector("[data-part='projects']")!.getAttribute("title")).toBe(
      `${WORK}: ${fmtUSD(1)}\n${SY}: ${fmtUSD(2)} + ${fmtCount(1000)} tokens with no list price`);
    expect(kpi(LEAD).querySelector("[data-part='operator']")!.getAttribute("title")).toBeNull();
    // The hover is also the line's description, for the keyboard and touch screens (N6).
    expect(kpi(LEAD).querySelector("[data-part='projects']")!.getAttribute("aria-description")).toBe(
      `${WORK}: ${fmtUSD(1)}; ${SY}: ${fmtUSD(2)} + ${fmtCount(1000)} tokens with no list price`);
    expect(kpi(LEAD).querySelector("[data-part='operator']")!.getAttribute("aria-description")).toBeNull();
  });

  it("has no line for other projects where only the operator reports", async () => {
    serve(operatorOnly());
    await waitFor(() => expect(kpi(LEAD)?.querySelector("[data-kpi-value]")?.textContent).toBe(fmtUSD(9 + 3.5)));
    expect(lines(LEAD)).toEqual([`covey agents (internal) ${fmtUSD(9)}`, `covey operator ${fmtUSD(3.5)} *`]);
    expect(kpi(LEAD).querySelector("[data-part='projects']")).toBeNull();
    // Nor anywhere else the parts are named.
    const row = await screen.findByRole("region", { name: t("costs.dent.title") });
    expect(row.querySelector("[data-head-sentence]")!.textContent).not.toContain("other projects");
    expect(row.querySelector("[data-dent='projects']")).toBeNull();
    expect(row.querySelector("[data-dent='operator']")).toBeTruthy();
    expect(document.body.textContent).not.toContain("other projects");
  });

  it("names the three parts in the token tiles, and they add up to each tile", async () => {
    serve();
    // covey: 1,000 input and 500,000 output; the operator 200 and 20,000; sylius and work 200 and 20,000.
    const tiles: [string, number, number, number][] = [
      [t("costs.inputTokens"), 1000, 200, 200],
      [t("costs.outputTokens"), 500_000, 20_000, 20_000],
      [t("costs.totalTokens"), 501_000, 20_200, 20_200],
    ];
    for (const [label, covey, op, others] of tiles) {
      await waitFor(() => expect(kpi(label)?.querySelector("[data-kpi-value]")?.textContent).toBe(fmtCount(covey + op + others)));
      expect(lines(label)).toEqual([
        `covey agents (internal) ${fmtCount(covey)}`, `covey operator ${fmtCount(op)}`, `other projects ${fmtCount(others)}`]);
    }
  });

  it("writes the headline sentence as covey's agents (internal) + covey operator + other projects, adding up to the work at token prices", async () => {
    serve();
    const row = await screen.findByRole("region", { name: t("costs.dent.title") });
    // $9 + $3.50 + $2 (sylius, on the meter; work is billed per use, on none) = $14.50.
    expect(row.querySelector("[data-head-sentence]")!.textContent).toContain(
      `${fmtUSD(14.5)} = covey agents (internal) ${fmtUSD(9)} + covey operator ${fmtUSD(3.5)} + other projects ${fmtUSD(2)} + ${fmtCount(1000)} tokens with no list price ${MARK}`);
  });

  it("splits the estimate's outside card into the operator and the other projects, and they add up to it", async () => {
    serve();
    const row = await screen.findByRole("region", { name: t("costs.dent.title") });
    const op = row.querySelector<HTMLElement>("[data-dent='operator']")!, pr = row.querySelector<HTMLElement>("[data-dent='projects']")!;
    expect(row.querySelector("[data-dent='outside']")).toBeNull();
    expect(op.getAttribute("aria-label")).toBe("covey operator");
    expect(pr.getAttribute("aria-label")).toBe("Other projects");
    expect(op.textContent).toContain(`≈ ${fmtUSD(PP)}`);
    expect(pr.textContent).toContain(`≈ ${fmtUSD(PP)}`);
    expect(pr.getAttribute("title")).toBe(`${SY}: ≈ ${fmtUSD(PP)}`);
    expect(pr.getAttribute("aria-description")).toBe(`${SY}: ≈ ${fmtUSD(PP)}`);
    // Each card says its real share and its share of the dent, and no list
    // price: the server's per source is the list price, the covey card beside
    // them is at the lead price, so a list there would be on another basis
    // than the sentence below (#163 review, M2).
    expect(op.textContent).not.toMatch(/list price/i);
    expect(pr.textContent).not.toMatch(/list price/i);
    expect(op.textContent).toContain("of the dent");
    expect(pr.textContent).toContain("of the dent");
    // The per-source list stays in the lines below, at the list price they name.
    const lines = [...row.querySelectorAll("[data-dent-source-part]")].map((e) => e.textContent);
    expect(lines).toEqual([
      `${OP}: real share ≈ ${fmtUSD(PP)} (list price ${fmtUSD(2.5)} in the split readings)`,
      `${SY}: real share ≈ ${fmtUSD(PP)} (list price ${fmtUSD(2.5)} in the split readings)`]);
    const d = subscriptionDent(cells().external)!;
    expect(PP + PP).toBeCloseTo(d.outside.real, 12);
    // The wording: the agents' part says internal.
    expect(row.querySelector("[data-dent='covey']")!.getAttribute("aria-label")).toBe("covey agents (internal)");
  });

  it("says the meter card's outside list price as two rows, the operator's and the other projects', adding up to the seat's part", async () => {
    serve();
    const card = await waitFor(() => {
      const c = document.querySelector(`[data-meter="${SEAT}#0"]`);
      if (!c) throw new Error("no meter card yet");
      return c;
    });
    // The seat's outside part within the readings is $5.10 at the lead price (the operator and sylius; work is per use).
    expect(card.textContent).toContain(`${t("costs.incl.meterListOperator")}${fmtUSD(3.25)}`);
    expect(card.textContent).toContain(`${t("costs.incl.meterListProjects")}${fmtUSD(1.85)}`);
    expect(3.25 + 1.85).toBeCloseTo(5.1, 9);
    expect(card.textContent).not.toContain("List price, outside sessions");
  });

  it("says each seat's outside part in the estimate as the operator and the other projects, from its source parts", async () => {
    serve();
    const row = await screen.findByRole("region", { name: t("costs.dent.title") });
    const d = subscriptionDent(cells().external)!;
    expect(d.seats[0].outsideParts).toEqual({ operator: PP, projects: PP });
    // The seat lines show only where there are several seats or one has no figure: the wording is read directly.
    expect(t("costs.dent.seatLineSplit", { seat: `${SEAT} #0`, real: "$1", pts: "6", covey: "$2", operator: "$0.50", projects: "$0.25",
      unattributed: "$0", unsplit: "$0" })).toContain("covey operator ≈ $0.50, other projects ≈ $0.25");
    expect(row.querySelector("[data-dent='operator']")).toBeTruthy();
    // Only the operator on the seat: no other projects in the numbers.
    expect(subscriptionDent(operatorOnly().external)!.seats[0].outsideParts).toEqual({ operator: PP, projects: null });
  });

  it("keeps the everything tile's parts adding up to its figure whatever the source chips say (#163 review, M1)", async () => {
    serve();
    const text = async () => (await screen.findByRole("group", { name: t("costs.incl.everything") })).textContent ?? "";
    const parts = async () => {
      const m = (await text()).match(/\(covey agents \(internal\) \$([\d.]+)(?: · covey operator \$([\d.]+))?(?: · other projects \$([\d.]+))?/);
      return m ? [m[1], m[2], m[3]].map((x) => (x == null ? 0 : Number(x))) : null;
    };
    await waitFor(async () => expect(await parts()).not.toBeNull());
    const [covey, op0, pr0] = (await parts())!;
    expect(op0).toBeCloseTo(3.25, 2);
    expect(pr0).toBeCloseTo(1.85, 2);
    const whole = op0 + pr0;
    // The operator chip off: the tile is the whole seat's still; the operator's share is not booked as other projects.
    fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${OP}`) }));
    const [c1, op1, pr1] = (await parts())!;
    expect([c1, op1, pr1]).toEqual([covey, op0, pr0]);
    // Both chips off: the same figure, the same parts.
    fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${SY}`) }));
    const [c2, op2, pr2] = (await parts())!;
    expect([c2, op2, pr2]).toEqual([covey, op0, pr0]);
    expect(op2 + pr2).toBeCloseTo(whole, 9);
  });

  it("says 'no list price' for a part whose calls have none, never $0.00, though the other part is priced (#163 review, N4)", async () => {
    const data = cells();
    const ext = data.external!;
    // The operator's calls carry no reported cost and no list price.
    for (const c of ext.cells.filter((x) => x.source === OP)) {
      Object.assign(c, { list_usd: null, measured: {}, reported_usd: null, reported_calls: 0, lead_usd: null, unpriced_calls: c.calls,
        unpriced_tokens: 500, lead_unpriced_calls: c.calls, lead_unpriced_tokens: 500 });
    }
    const src = ext.sources.find((x) => x.source === OP)!;
    Object.assign(src, { list_usd: null, reported_usd: null, lead_usd: null, unpriced_tokens: 1000, lead_unpriced_tokens: 1000 });
    serve(data);
    const tile = await screen.findByRole("group", { name: t("costs.incl.everything") });
    await waitFor(() => expect(tile.textContent).toContain("covey operator no list price"));
    expect(tile.textContent).not.toContain("covey operator $0.00");
    const card = await waitFor(() => {
      const c = document.querySelector(`[data-meter="${SEAT}#0"]`);
      if (!c) throw new Error("no meter card yet");
      return c;
    });
    expect(card.textContent).toContain(`${t("costs.incl.meterListOperator")}no list price`);
  });

  it("splits the part of everything on the meter between the operator and the other projects, to the cent, whatever the total", () => {
    const ext = cells().external!;
    const split = outsideSplit(ext);
    expect(split.operator!.sources).toEqual([OP]);
    expect(split.projects!.sources).toEqual([WORK, SY]);
    expect(split.projectList.map((p) => p.source)).toEqual([WORK, SY]);
    // Operator $3.50, projects $3: 7/13 and 6/13 of the whole.
    for (const total of [0, 0.01, 4, 5.1, 13.37, 1234.56]) {
      const w = withinSplit(total, split);
      expect(w.operator + w.projects).toBeCloseTo(total, 9);
      expect(w.operator).toBe(Math.round(w.operator * 100) / 100);
      expect(w.shared).toBe(true);
    }
    expect(withinSplit(13, split).operator).toBe(7);
    // One part alone has all of it, and nothing is shared.
    expect(withinSplit(5.1, outsideSplit(ext, new Set([OP])))).toEqual({ operator: 5.1, projects: 0, shared: false });
    expect(withinSplit(5.1, outsideSplit(ext, new Set([SY])))).toEqual({ operator: 0, projects: 5.1, shared: false });
    // The two parts hold every source's calls: nothing is lost between them.
    expect(split.operator!.calls + split.projects!.calls).toBe(outsideKpis(ext)!.calls);
  });
});
