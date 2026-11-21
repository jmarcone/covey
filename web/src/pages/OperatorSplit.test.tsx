import { describe, it, expect, beforeEach } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import Costs from "./Costs";
import type { CostCells, ExternalCell, ExternalMeterStep, ExternalMeterSums, ExternalMeterTotal, ExternalUsage } from "../api";
import i18n from "../i18n";
import { fmtCount, fmtUSD } from "../format";
import { mockFetch, renderWithProviders, openDentRow } from "../test/render";
import { headline, subscriptionDent } from "./costFigures";
import { OPERATOR_SOURCE, isOperatorSource, kpiLead, outsideKpis, outsideSplit } from "./outsideFigures";
import { withinSplit } from "./StackedCostChart";
import { SubscriptionDentRow } from "./SubscriptionDent";

// #163: the sessions outside covey are not one number. Where the cost page
// names them, it names its parts: covey's own agents (internal), the covey
// operator (the Claude Code session that drives covey from outside), the
// other projects on the same machine and plan, and what is billed per use,
// on no subscription meter (#166 review, F1). The parts of a figure add up
// to the figure, the operator is the source labelled "covey operator", and
// the line of the other projects is there only where there are other
// projects, with each of them in its hover.

const SEAT = "claude_code_oauth_token";
const PP = 200 * 12 / 52 / 100;
const H18 = "2026-11-17T18:00:00Z", H19 = "2026-11-17T19:00:00Z";
const OP = OPERATOR_SOURCE, SY = "projects-sylius", WORK = "operator: work";
const t = (k: string, o: Record<string, unknown> = {}) => i18n.t(k, o) as string;
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
/** The answer table (#195): the rows' token prices and the total. */
const answerReady = () => waitFor(() => expect(document.querySelector("[data-answer-total]")).not.toBeNull());
const openHow = () => { if (!document.querySelector("[data-answer-drawer]")) fireEvent.click(document.querySelector("[data-answer-how]")!); };
const howNote = (k: string) => { openHow(); return document.querySelector(`[data-answer-note="${k}"]`); };
/** The visible text: without the screen-reader-only words. */
const visible = (e: Element | null) => { const c = e!.cloneNode(true) as Element; c.querySelectorAll(".sr-only").forEach((x) => x.remove()); return c.textContent; };
const rowList = (key: string) => visible(document.querySelector(`[data-answer-row="${key}"] [data-answer-list]`));
const rowKeys = () => [...document.querySelectorAll("[data-answer-row]")].map((e) => e.getAttribute("data-answer-row"));
const answerTotal = () => document.querySelector("[data-answer-total] [data-answer-list]")!.textContent;
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
  // The other projects' real share is gone with them, from the seat and the meter: it is unattributed now, so the parts still add up to the real cost.
  seat.external_real_usd = PP;
  ext.meter.external_real_usd = PP;
  seat.unattributed_real_usd = PP;
  ext.meter.unattributed_real_usd = PP;
  return data;
}

describe("the outside sessions in three parts (#163)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("splits the answer table into covey's agents, the covey operator and the other projects, one row each, and they add up to its total", async () => {
    serve();
    // covey $9 as Claude Code reported it; the operator $3.50; sylius $2 (on the meter); work $1 (per use, on no meter: a line, not a row).
    await answerReady();
    expect(rowKeys()).toEqual(["covey", "operator", `project:${SY}`]);
    expect(rowList("covey")).toBe(fmtUSD(9));
    expect(rowList("operator")).toBe(`${fmtUSD(3.5)}*`);
    expect(rowList(`project:${SY}`)).toBe(`${fmtUSD(2)} + ${fmtCount(1000)} tokens with no list price*`);
    // The rows add up to the total's figure.
    expect(answerTotal()).toBe(fmtUSD(9 + 3.5 + 2));
    expect(howNote("per-use")!.textContent).toContain(fmtUSD(1));
  });

  it("has no row for other projects where only the operator reports", async () => {
    serve(operatorOnly());
    await answerReady();
    // The server's real shares are not all of the meter here: the remainder has a row of its own, with no token price.
    expect(rowKeys()).toEqual(["covey", "operator", "unattributed"]);
    expect(answerTotal()).toBe(fmtUSD(9 + 3.5));
    // Nor anywhere else the parts are named.
    const row = await openDentRow();
    expect(row.querySelector("[data-head-sentence]")!.textContent).not.toContain("other projects");
    expect(row.querySelector("[data-dent='projects']")).toBeNull();
    expect(row.querySelector("[data-dent='operator']")).toBeTruthy();
    expect(document.body.textContent).not.toContain("other projects");
  });

  it("names the parts in the token tiles, per use included, and they add up to each tile", async () => {
    serve();
    // covey: 1,000 input and 500,000 output; the operator 200 and 20,000; sylius 100 and 10,000; work (per use) 100 and 10,000.
    const tiles: [string, number, number, number, number][] = [
      [t("costs.inputTokens"), 1000, 200, 100, 100],
      [t("costs.outputTokens"), 500_000, 20_000, 10_000, 10_000],
      [t("costs.totalTokens"), 501_000, 20_200, 10_100, 10_100],
    ];
    for (const [label, covey, op, projects, perUse] of tiles) {
      await waitFor(() => expect(kpi(label)?.querySelector("[data-kpi-value]")?.textContent).toBe(fmtCount(covey + op + projects + perUse)));
      expect(lines(label)).toEqual([
        `covey agents (internal) ${fmtCount(covey)}`, `covey operator ${fmtCount(op)}`, `other projects ${fmtCount(projects)}`, `per use ${fmtCount(perUse)}`]);
    }
  });

  it("writes the headline sentence as covey's agents (internal) + covey operator + other projects, adding up to the work at token prices", async () => {
    serve();
    const row = await openDentRow();
    // $9 + $3.50 + $2 (sylius, on the meter; work is billed per use, on none) = $14.50.
    expect(row.querySelector("[data-head-sentence]")!.textContent).toContain(
      `${fmtUSD(14.5)} = covey agents (internal) ${fmtUSD(9)} + covey operator ${fmtUSD(3.5)} + other projects ${fmtUSD(2)} + ${fmtCount(1000)} tokens with no list price ${MARK}`);
    // The per-use source is said on its own line in the how drawer, not in this sentence (#166 review, F1).
    expect(howNote("per-use")!.textContent).toContain(fmtUSD(1));
  });

  it("splits the estimate's outside card into the operator and the other projects, and they add up to it", async () => {
    serve();
    const row = await openDentRow();
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
    // The seat's outside part within the readings is $5.10 at the lead price: the answer table holds it as two rows now,
    // and the meter card says only the meter's span (#195).
    await answerReady();
    const card = document.querySelector(`[data-meter="${SEAT}#0"]`)!;
    expect(card).not.toBeNull();
    expect(card.textContent).not.toContain(t("costs.incl.meterListOperator"));
    expect(card.querySelector("[data-meter-shared]")).toBeNull();
    expect(3.25 + 1.85).toBeCloseTo(5.1, 9);
  });

  it("says each seat's outside part in the estimate as the operator and the other projects, from its source parts", async () => {
    serve();
    const row = await openDentRow();
    const d = subscriptionDent(cells().external)!;
    expect(d.seats[0].outsideParts).toEqual({ operator: PP, projects: PP });
    // The seat lines show only where there are several seats or one has no figure: the same seat twice shows them.
    const { container } = renderWithProviders(<SubscriptionDentRow d={{ ...d, seats: [d.seats[0], { ...d.seats[0], slot: 1 }] }} filtered={false} />);
    expect(container.querySelector(`[data-dent-seat="${SEAT} #0"]`)!.textContent)
      .toContain(`covey operator ≈ ${fmtUSD(PP)}, other projects ≈ ${fmtUSD(PP)}`);
    expect(row.querySelector("[data-dent='operator']")).toBeTruthy();
    // Only the operator on the seat: no other projects in the numbers.
    expect(subscriptionDent(operatorOnly().external)!.seats[0].outsideParts).toEqual({ operator: PP, projects: null });
  });

  it("keeps the table's rows adding up to its total whatever the source chips say (#163 review, M1)", async () => {
    serve();
    await answerReady();
    const real = () => usd(document.querySelector("[data-answer-total] [data-answer-real]")!.textContent);
    const rows = () => [...document.querySelectorAll("[data-answer-row] [data-answer-real]")].reduce((a, e) => a + usd(e.textContent), 0);
    const list = () => [...document.querySelectorAll("[data-answer-row] [data-answer-list]")].reduce((a, e) => a + usd(e.textContent), 0);
    expect(real()).toBeCloseTo(6 * PP, 2);
    expect(rows()).toBeCloseTo(real(), 2);
    expect(list()).toBeCloseTo(usd(answerTotal()), 9);
    // The operator chip off: its row and its share leave the table, and the rest still adds up.
    fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${OP}`) }));
    await waitFor(() => expect(document.querySelector('[data-answer-row="operator"]')).toBeNull());
    expect(real()).toBeCloseTo(6 * PP - PP, 2);
    expect(rows()).toBeCloseTo(real(), 2);
    expect(list()).toBeCloseTo(usd(answerTotal()), 9);
    expect(usd(answerTotal())).toBeCloseTo(9 + 2, 9);
    // Both chips off: covey's row alone, the unattributed remainder of the meter beside it.
    fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${SY}`) }));
    await waitFor(() => expect(document.querySelector(`[data-answer-row="project:${SY}"]`)).toBeNull());
    expect(rows()).toBeCloseTo(real(), 2);
    expect(usd(answerTotal())).toBe(9);
  });

  it("says the split is not known, never gives the priced part all of it, where one part has no price (#163 review, N4; #166 review, F4)", async () => {
    const data = cells();
    const ext = data.external!;
    // The operator's calls carry no reported cost and no list price.
    for (const c of ext.cells.filter((x) => x.source === OP)) {
      Object.assign(c, { list_usd: null, measured: {}, reported_usd: null, reported_calls: 0, lead_usd: null, unpriced_calls: c.calls,
        unpriced_tokens: 500, lead_unpriced_calls: c.calls, lead_unpriced_tokens: 500 });
    }
    const src = ext.sources.find((x) => x.source === OP)!;
    Object.assign(src, { list_usd: null, reported_usd: null, lead_usd: null, unpriced_tokens: 1000, lead_unpriced_tokens: 1000 });
    expect(withinSplit(1.6, outsideSplit(ext))).toEqual({ operator: 0, projects: 0, shared: true, known: false });
    serve(data);
    await answerReady();
    // The operator has no price: its row says so with its tokens, never $0.
    expect(rowList("operator")).toBe(`no list price (${fmtCount(1000)} tokens)`);
    expect(document.querySelector('[data-answer-row="operator"]')!.textContent).not.toContain(fmtUSD(0));
    expect(document.querySelector(`[data-meter="${SEAT}#0"]`)?.querySelector("[data-meter-shared]") ?? null).toBeNull();
  });

  it("splits the part of everything on the meter between the operator and the other projects, to the cent, whatever the total", () => {
    const ext = cells().external!;
    const split = outsideSplit(ext);
    expect(split.operator!.sources).toEqual([OP]);
    expect(split.projects!.sources).toEqual([SY]);
    expect(split.projectList.map((p) => p.source)).toEqual([SY]);
    // work names no meter: per use, a part of its own, not another project.
    expect(split.perUse!.sources).toEqual([WORK]);
    expect(split.perUseList.map((p) => p.source)).toEqual([WORK]);
    // Operator $3.50, projects $2 (work is per use, in neither): 7/11 and 4/11 of the whole.
    for (const total of [0, 0.01, 4, 5.1, 13.37, 1234.56]) {
      const w = withinSplit(total, split);
      expect(w.operator + w.projects).toBeCloseTo(total, 9);
      expect(w.operator).toBe(Math.round(w.operator * 100) / 100);
      expect(w.shared).toBe(true);
    }
    expect(withinSplit(11, split).operator).toBe(7);
    // One part alone has all of it, and nothing is shared.
    expect(withinSplit(5.1, outsideSplit(ext, new Set([OP])))).toEqual({ operator: 5.1, projects: 0, shared: false, known: true });
    expect(withinSplit(5.1, outsideSplit(ext, new Set([SY])))).toEqual({ operator: 0, projects: 5.1, shared: false, known: true });
    // The two parts hold every source's calls: nothing is lost between them.
    expect(split.operator!.calls + split.projects!.calls + split.perUse!.calls).toBe(outsideKpis(ext)!.calls);
  });
});

/** The outside sessions as three sources of one figure each (#166 review, F1): the operator $10 and a project $30, both on the seat's
 *  meter, and a per-use source $20 (an API key: no meter), each one cell of one call at the list price. */
function scenario(): CostCells {
  const data = cells();
  const ext = data.external!;
  const one = (source: string, usd: number, link: boolean) => ({
    ...ext.sources[0], source, meter_credential: link ? { secret_key: SEAT, secret_slot: 0 } : null, calls: 1, list_usd: usd, reported_usd: usd,
    unpriced_tokens: 0, unpriced_calls: 0, unpriced_models: [], lead_usd: usd, lead_unpriced_calls: 0, lead_unpriced_tokens: 0 });
  ext.sources = [one(OP, 10, true), one(SY, 30, true), one(WORK, 20, false)];
  ext.cells = [cell(H18, OP, { calls: 1, list_usd: 10, measured: { output: 10 }, reported_usd: 10, reported_calls: 1, lead_usd: 10 }),
    cell(H18, SY, { calls: 1, list_usd: 30, measured: { output: 30 }, reported_usd: 30, reported_calls: 1, lead_usd: 30 }),
    cell(H19, WORK, { calls: 1, list_usd: 20, measured: { output: 20 }, reported_usd: 20, reported_calls: 1, lead_usd: 20 })];
  return data;
}

const sum = (xs: number[]) => xs.reduce((a, x) => a + x, 0);

describe("a source billed per use is a part of its own, everywhere the parts are named (#166 review, F1)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("keeps it out of the other projects: the operator $10, the projects $30, per use $20", () => {
    const split = outsideSplit(scenario().external);
    expect(split.operator!.sources).toEqual([OP]);
    expect(split.projects!.sources).toEqual([SY]);
    expect(split.perUse!.sources).toEqual([WORK]);
    expect([split.operator, split.projects, split.perUse].map((k) => kpiLead(k!).lead)).toEqual([10, 30, 20]);
    // The headline carves out the same source by the same rule.
    const h = headline(scenario())!;
    expect(h.perUse!.sources).toEqual([WORK]);
    expect(kpiLead(h.perUse!).lead).toBe(20);
    expect(h.sources.map((s) => s.source).sort()).toEqual([OP, SY].sort());
  });

  it("says it as its own line under the table, and the rows add up to the total, the per-use figure in none of them", async () => {
    serve(scenario());
    await answerReady();
    expect(rowKeys()).toEqual(["covey", "operator", `project:${SY}`]);
    expect([rowList("covey"), rowList("operator"), rowList(`project:${SY}`)]).toEqual([fmtUSD(9), fmtUSD(10), fmtUSD(30)]);
    expect(answerTotal()).toBe(fmtUSD(9 + 10 + 30));
    expect(howNote("per-use")!.textContent).toContain(fmtUSD(20));
    // The sentence's other projects are the table's: $30, and the per-use $20 stands apart, not inside either.
    const row = await openDentRow();
    expect(row.querySelector("[data-head-sentence]")!.textContent).toContain(`covey operator ${fmtUSD(10)} + other projects ${fmtUSD(30)}`);
  });

  it("splits the headline's sentence into parts that add up to its list price, a source with no calls in the range included", () => {
    const data = scenario();
    // A source listed for its real share only: no calls in the range, no list price.
    data.external!.sources.push({ ...data.external!.sources[1], source: "projects-early", calls: 0, list_usd: null, reported_usd: null, lead_usd: null,
      unpriced_tokens: 0, first_at: null, last_at: null });
    for (const d of [data, cells()]) {
      const h = headline(d)!;
      expect(h.coveyList + sum(h.sources.map((s) => s.list ?? 0))).toBeCloseTo(h.list, 9);
    }
  });
});

describe("the operator is one source however it is written (#166 review, F3)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("matches without regard to case or surrounding space, and nothing else", () => {
    for (const n of ["covey operator", "Covey Operator", " covey operator ", "COVEY OPERATOR\t"]) expect(isOperatorSource(n)).toBe(true);
    for (const n of ["covey operators", "covey  operator", "operator", "projects-sylius", ""]) expect(isOperatorSource(n)).toBe(false);
  });

  it("is the operator on the tile and in the dent, not another project", async () => {
    const data = cells();
    const NAME = "Covey Operator ";
    const ext = data.external!;
    for (const s of ext.sources) if (s.source === OP) s.source = NAME;
    for (const c of ext.cells) if (c.source === OP) c.source = NAME;
    const seat = ext.meter.credentials[0];
    seat.sources = [NAME, SY];
    for (const list of [seat.source_parts, ext.meter.source_parts, ...ext.meter.steps.map((st) => st.source_parts)]) {
      for (const p of list ?? []) if (p.source === OP) p.source = NAME;
    }
    expect(outsideSplit(ext).operator!.sources).toEqual([NAME]);
    expect(outsideSplit(ext).projects!.sources).toEqual([SY]);
    expect(subscriptionDent(ext)!.seats[0].outsideParts).toEqual({ operator: PP, projects: PP });
    serve(data);
    await answerReady();
    expect(rowKeys()).toEqual(["covey", "operator", `project:${SY}`]);
    expect(rowList("operator")).toBe(`${fmtUSD(3.5)}*`);
    const row = await openDentRow();
    expect(row.querySelector("[data-dent='operator']")!.textContent).toContain(`≈ ${fmtUSD(PP)}`);
    expect(row.querySelector("[data-dent='projects']")!.textContent).toContain(`≈ ${fmtUSD(PP)}`);
    expect(row.querySelector("[data-head-sentence]")!.textContent).toContain(`covey operator ${fmtUSD(3.5)} + other projects ${fmtUSD(2)}`);
  });
});

describe("the dent's outside cards add up to the outside part (#166 review, F2)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  /** One project's real share is on the seat and in the dent's total but in no source part of the meter. */
  function lostShare(): CostCells {
    const data = cells();
    data.external!.meter.source_parts = data.external!.meter.source_parts?.filter((p) => p.source === OP);
    return data;
  }

  it("shows what the sources' shares do not carry as a card of its own, and the cards add up to the dent's outside part", async () => {
    serve(lostShare());
    const row = await openDentRow();
    const d = subscriptionDent(lostShare().external)!;
    const cards = ["operator", "projects", "rest"].map((k) => row.querySelector<HTMLElement>(`[data-dent='${k}']`));
    expect(cards[0]).toBeTruthy();
    expect(cards[1]).toBeNull();
    expect(cards[2]!.getAttribute("aria-label")).toBe("Outside, not split");
    expect(row.querySelector("[data-dent='outside']")).toBeNull();
    expect(sum(cards.map((c) => (c ? usd(c.textContent) : 0)))).toBeCloseTo(d.outside.real, 2);
    expect(usd(cards[2]!.textContent)).toBeCloseTo(PP, 2);
  });

  it("adds up with every source's share present too, and has no rest card then", async () => {
    serve();
    const row = await openDentRow();
    const d = subscriptionDent(cells().external)!;
    expect(row.querySelector("[data-dent='rest']")).toBeNull();
    expect(sum(["operator", "projects"].map((k) => usd(row.querySelector<HTMLElement>(`[data-dent='${k}']`)!.textContent)))).toBeCloseTo(d.outside.real, 2);
  });

  it("gives a seat's line the one outside figure where its parts do not add up to it", () => {
    const ext = lostShare().external!;
    ext.meter.credentials[0].source_parts = ext.meter.credentials[0].source_parts?.filter((p) => p.source === OP);
    expect(subscriptionDent(ext)!.seats[0].outsideParts).toBeNull();
    // Every part present: the split stands.
    expect(subscriptionDent(cells().external)!.seats[0].outsideParts).toEqual({ operator: PP, projects: PP });
  });
});
