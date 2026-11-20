import { describe, it, expect, beforeEach } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { useLocation } from "react-router";
import Costs from "./Costs";
import type { CostCells, ExternalCell, ExternalMeterStep, ExternalMeterSums, ExternalMeterTotal, ExternalUsage } from "../api";
import i18n from "../i18n";
import { fmtCount, fmtNumber, fmtUSD } from "../format";
import { mockFetch, renderWithProviders } from "../test/render";
import { PRICE_CHECK, everythingOnMeter, headline, priceTableDifference, seatLists, subscriptionDent } from "./costFigures";
import { cellLead, outsideKpis, outsideTotals } from "./outsideFigures";

// #120, the human's rule for the cost page: one list-price figure leads —
// "At token prices (as reported by Claude Code)" — over every source: each
// call at the price Claude Code reported for it, at covey's list price where
// it reported none (marked), and "no list price" with the tokens where
// neither exists. covey's own price table is a check: a line only where it
// differs by more than 1 % and $0.50. The full Recomputed view is a
// secondary toggle in the URL. The headline's X is this figure, its floor
// the server's floor at the lead price, and what the multiple does not
// divide is said by where it lies: before the meter's first reading, after
// its last one, in readings with no plan price.

const SEAT = "claude_code_oauth_token";
const PP = 200 * 12 / 52 / 100;
const H18 = "2026-11-17T18:00:00Z", H19 = "2026-11-17T19:00:00Z";
const OP = "covey operator", SY = "projects-sylius", WORK = "operator: work";
const times = (v: number) => `${fmtNumber(v, 1)}×`;
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

/** The page's URL query, shown so a test can read what a toggle keeps there. */
function Search() {
  return <output data-search>{useLocation().search}</output>;
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
  return renderWithProviders(<><Costs /><Search /></>, { route, path: "/costs" });
}

const kpi = (label: string) => document.querySelector<HTMLElement>(`[data-kpi="${label}"]`);
const block = () => document.querySelector<HTMLElement>(`[data-credential="${SEAT}#0"]`);
const search = () => document.querySelector("[data-search]")!.textContent;

describe("the price-table check's threshold (#120)", () => {
  it("is one constant: more than 1 % AND more than $0.50", () => {
    expect(PRICE_CHECK).toEqual({ pct: 1, usd: 0.5 });
    // Both above: said, with covey's amount and the difference in % of the lead.
    expect(priceTableDifference(100, 101.01)).toEqual({ usd: 101.01, pct: expect.closeTo(1.01, 9) });
    expect(priceTableDifference(100, 97)).toEqual({ usd: 97, pct: expect.closeTo(-3, 9) });
    // 0.9 %, though $0.90: silent.
    expect(priceTableDifference(100, 100.9)).toBeNull();
    expect(priceTableDifference(100, 99.1)).toBeNull();
    // 6 % but exactly $0.50, and $0.45: silent; $0.51: said.
    expect(priceTableDifference(10, 10.5)).toBeNull();
    expect(priceTableDifference(10, 9.55)).toBeNull();
    expect(priceTableDifference(10, 10.51)).not.toBeNull();
    // No lead to compare with: silent, never a % of 0.
    expect(priceTableDifference(0, 5)).toBeNull();
    expect(priceTableDifference(10, 10)).toBeNull();
  });
});

describe("the lead figure per source (#120)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("takes each call's reported cost, the list price where it has none, and counts the calls with neither", () => {
    const ext = external();
    // The server's lead per cell, and how many calls it priced at the list.
    expect(cellLead(ext.cells[1])).toEqual({ lead: 2.5, recomputed: 3, unpricedCalls: 0, unpricedTokens: 0 });
    expect(cellLead(ext.cells[2])).toEqual({ lead: 2, recomputed: 5, unpricedCalls: 1, unpricedTokens: 1000 });
    // An older server's cell: all reported leads with that, else with the list.
    const { lead_usd: _l, lead_unpriced_calls: _c, lead_unpriced_tokens: _t, ...old } = ext.cells[0];
    expect(cellLead(old).lead).toBe(1);
    expect(cellLead({ ...old, reported_calls: 2 }).lead).toBe(1);
    const k = outsideKpis(ext)!;
    expect(k.lead).toBe(1 + 2.5 + 2 + 1);
    expect(k.leadRecomputed).toBe(3 + 5);
    expect(k.leadUnpricedTokens).toBe(1000);
  });

  it("leads the top tiles with one figure over every source, marks the recomputed part, and says the price table only where it differs", async () => {
    serve();
    // covey $9 reported; outside: op $3.50, sylius $2 (at the list), work $1.
    await waitFor(() => expect(kpi(LEAD)?.querySelector("[data-kpi-value]")?.textContent).toBe(fmtUSD(9 + 6.5)));
    // One line per part (#163): covey's agents, the operator, the other projects on the meter (sylius), per use (work); the mark said once.
    const lines = [...kpi(LEAD)!.querySelectorAll("[data-parts] [data-part]")].map((e) => e.textContent);
    expect(lines).toEqual([
      `covey agents (internal) ${fmtUSD(9)}`,
      `covey operator ${fmtUSD(3.5)} *`,
      `other projects ${fmtUSD(2)} + ${fmtCount(1000)} tokens with no list price *`,
      `per use ${fmtUSD(1)}`,
    ]);
    expect(kpi(LEAD)!.querySelector("[data-part-footnote]")!.textContent).toBe(`* ${MARK.slice(1, -1)}`);
    // The other projects' hover lists each project with its amount.
    expect(kpi(LEAD)!.querySelector("[data-part='projects']")!.getAttribute("title")).toBe(
      `${SY}: ${fmtUSD(2)} + ${fmtCount(1000)} tokens with no list price`);
    expect(kpi(LEAD)!.querySelector("[data-part='perUse']")!.getAttribute("title")).toBe(`${WORK}: ${fmtUSD(1)}`);
    // covey's price table: $10 + $6.50 = $16.50, $1 (6.5 %) more: one line.
    expect(kpi(LEAD)!.querySelector("[data-price-check]")!.textContent).toBe(
      `covey's own price table gives ${fmtUSD(16.5)}, ${fmtNumber(6.5, 1)} % more than Claude Code reported`);
    // Not two figures.
    expect(kpi("Reported by Claude Code")).toBeNull();
    expect(kpi("Recomputed from tokens")).toBeNull();
  });

  it("says nothing where covey's price table agrees within the threshold", async () => {
    // covey only, Claude Code's $9.60 against covey's $10: 4 %, but $0.40.
    serve(cells(external(), [5.6, 4]), "/costs?only=covey");
    await waitFor(() => expect(kpi(LEAD)?.querySelector("[data-kpi-value]")?.textContent).toBe(fmtUSD(9.6)));
    expect(kpi(LEAD)!.querySelector("[data-price-check]")).toBeNull();
    expect(kpi(LEAD)!.querySelector("[data-parts]")).toBeNull();
    // The seat's tile: no line either.
    const tile = within(block()!).getByRole("group", { name: LEAD });
    expect(tile.querySelector("[data-price-check]")).toBeNull();
  });

  it("the seat's tile, chart, tooltip and table carry one list-price figure; the check only where it differs", async () => {
    serve();
    await waitFor(() => expect(block()?.querySelector("[data-incl-total]")).toBeTruthy());
    const b = block()!;
    // One lead tile, no Recomputed tile; covey $9 + op $3.50 + sylius $2 against the price table's $15: $0.50 apart, silent.
    const tile = within(b).getByRole("group", { name: LEAD });
    expect(tile.textContent).toContain(fmtUSD(14.5));
    expect(tile.querySelector("[data-price-check]")).toBeNull();
    expect(within(b).queryByRole("group", { name: "Recomputed from tokens" })).toBeNull();
    // The chips: the lead figure, real, billed; covey's price table is the toggle.
    const row = screen.getByRole("checkbox", { name: t("costs.lead.toggle") }).closest("div")!;
    const chips = [...row.querySelectorAll("button[aria-pressed]")].map((x) => x.textContent);
    expect(chips).toContain(LEAD);
    expect(chips).not.toContain("Recomputed from tokens");
    expect((screen.getByRole("checkbox", { name: t("costs.lead.toggle") }) as HTMLInputElement).checked).toBe(false);
    // No recomputed bar; sylius's segment says it is recomputed.
    expect(b.querySelector("rect[data-seg='m']")).toBeNull();
    fireEvent.focus(b.querySelector(`[data-period="${H18}"]`)!);
    let tip = await screen.findByRole("tooltip");
    // 18:00: Claude Code's $5 + $1 against covey's $6 + $1: said.
    expect(tip.querySelector("[data-price-check]")!.textContent).toBe(
      `covey's own price table gives ${fmtUSD(7)}, ${fmtNumber(17, 0)} % more than Claude Code reported`);
    fireEvent.blur(b.querySelector(`[data-period="${H18}"]`)!);
    fireEvent.focus(b.querySelector(`[data-period="${H19}"]`)!);
    tip = await screen.findByRole("tooltip");
    // 19:00: $8.50 against $8: $0.50 apart, silent.
    expect(tip.querySelector("[data-price-check]")).toBeNull();
    expect(tip.textContent).toContain(`${SY} (outside covey) · recomputed for sources without a reported cost`);
    expect(tip.textContent).toContain(`${LEAD}, incl. outside sessions`);
    // The table: one list-price column, and the check only in the period where it differs.
    fireEvent.click(screen.getAllByRole("button", { name: t("costs.fig.showAsTable") })[0]);
    const table = [...b.querySelectorAll("table")].find((x) => x.querySelector("tr[data-totals]"))!;
    const heads = [...table.querySelectorAll("thead th")].map((x) => x.textContent);
    expect(heads).toEqual(["Period", `${LEAD}, incl. outside sessions`, t("costs.fig.realEstimate"),
      `At token prices · ${OP} (outside covey)`, `At token prices · ${SY} (outside covey)`, "covey's price table"]);
    const checks = [...table.querySelectorAll("tbody td[data-check]")].map((x) => x.textContent);
    expect(checks).toEqual([`covey's own price table gives ${fmtUSD(7)}, ${fmtNumber(17, 0)} % more than Claude Code reported`, ""]);
    // The agents' table: one list-price column; the check where a row differs.
    const agents = [...b.querySelectorAll("table")].find((x) => x.querySelector("[data-incl-total]"))!;
    expect([...agents.querySelectorAll("thead th")].map((x) => x.textContent)).toEqual(
      [t("costs.fig.agent"), LEAD, "covey's price table", t("costs.fig.share"), t("costs.fig.shareOfReal")]);
    const a1 = agents.querySelector("tbody tr")!;
    expect(a1.querySelector("[data-lead]")!.textContent).toBe(fmtUSD(9));
    expect(a1.querySelector("[data-check]")!.textContent).toBe(`+${fmtNumber(100 / 9, 2)} %`);
    expect(agents.querySelector(`tr[data-outside-row="${SY}"] [data-lead]`)!.textContent).toBe(
      `${fmtUSD(2)} + ${fmtCount(1000)} tokens with no list price ${MARK}`);
    expect(agents.querySelector(`tr[data-outside-row="${SY}"] [data-check]`)!.textContent).toBe("");
    expect(agents.querySelector("[data-incl-lead]")!.textContent).toBe(`${fmtUSD(14.5)} (with unpriced) ${MARK}`);
  });

  it("keeps the full view of covey's price table behind its toggle, in the URL", async () => {
    serve();
    const box = await screen.findByRole("checkbox", { name: t("costs.lead.toggle") });
    fireEvent.click(box);
    await waitFor(() => expect(search()).toBe("?prices=recomputed"));
    // Both figures again: the tiles, the chips, the bars.
    await waitFor(() => expect(kpi("Recomputed from tokens")).not.toBeNull());
    expect(kpi("Reported by Claude Code")!.querySelector("[data-kpi-value]")!.textContent).toBe(fmtUSD(9 + 3.5));
    expect(kpi("Recomputed from tokens")!.querySelector("[data-kpi-value]")!.textContent).toBe(fmtUSD(10 + 6.5));
    expect(kpi(LEAD)).toBeNull();
    expect(within(block()!).getByRole("group", { name: "Recomputed from tokens" })).toBeTruthy();
    expect(block()!.querySelector("rect[data-seg='m']")).toBeTruthy();
    fireEvent.click(screen.getByRole("checkbox", { name: t("costs.lead.toggle") }));
    await waitFor(() => expect(search()).toBe(""));
    expect(kpi(LEAD)).not.toBeNull();
  });

  it("opens in the full view from the URL, beside “covey only”", async () => {
    serve(cells(), "/costs?only=covey&prices=recomputed");
    await waitFor(() => expect(kpi("Reported by Claude Code")?.querySelector("[data-kpi-value]")?.textContent).toBe(fmtUSD(9)));
    expect((screen.getByRole("checkbox", { name: t("costs.lead.toggle") }) as HTMLInputElement).checked).toBe(true);
    expect(screen.getByRole("button", { name: "covey only" }).getAttribute("aria-pressed")).toBe("true");
    expect(document.querySelector("[data-seg^='out:']")).toBeNull();
  });
});

describe("the headline's X is the lead figure (#120)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("X = Xm + before + after + unpriced + usage at the lead price; the multiple divides Xm, the floor the server's floor_lead_usd", () => {
    const data = cells();
    const h = headline(data)!;
    expect(h.coveyList).toBe(9);
    expect(h.outsideList).toBe(5.5);
    expect(h.list).toBe(14.5);
    expect(h.before).toBeCloseTo(0.05 + 0.15, 12);
    expect(h.after).toBeCloseTo(0.15 + 0.25, 12);
    expect(h.unmetered).toBeCloseTo(0.6, 12);
    expect(h.metered).toBeCloseTo(14.5 - 0.6, 12);
    expect(h.list).toBeCloseTo(h.metered + h.before + h.after + h.unpriced + h.usage, 12);
    expect(h.multiple).toBeCloseTo((14.5 - 0.6) / (6 * PP), 12);
    expect(h.floorList).toBe(12);
    expect(h.floor).toBeCloseTo(12 / (6 * PP + h.slack), 12);
    expect(h.table).toBe(15);
    // The seat at both prices, each in the same parts.
    const l = seatLists(data, data.external!.meter.credentials[0]);
    expect(l.lead.covey).toMatchObject({ range: 9, before: 0.05, unpriced: 0, usage: 0 });
    expect(l.lead.covey.after).toBeCloseTo(0.15, 12);
    expect(l.covey).toMatchObject({ range: 10, before: 0.1, unmetered: 0.25 });
    expect(l.outside.after).toBeCloseTo(0.3, 12);
    // Everything on the meter: the same figure, and its floor.
    const e = everythingOnMeter(data, SEAT, 0)!;
    expect(e.coveyList + e.outsideList).toBeCloseTo(h.metered, 12);
    expect(e.floorList).toBe(12);
    expect(e.before).toEqual({ covey: 0.05, outside: 0.15 });
    expect(e.after.covey).toBeCloseTo(0.15, 12);
    expect(e.after.outside).toBeCloseTo(0.25, 12);
    // The dent's parts at the lead price; the outside part outside every span by where it lies.
    const d = subscriptionDent(data.external)!;
    expect(d.covey.list).toBe(9);
    expect(d.outside.list).toBe(5.5);
    expect(d.unmeteredBefore).toBe(0.15);
    expect(d.unmeteredAfter).toBeCloseTo(0.25, 12);
  });

  it("says the sentence, the floor and the parts the multiple does not divide in the lead figure's terms", async () => {
    serve();
    const row = await screen.findByRole("region", { name: t("costs.dent.title") });
    const xm = 14.5 - 0.6;
    expect(row.querySelector("[data-head-sentence]")!.textContent).toBe(
      `All this work on the subscription meters at token prices (as reported by Claude Code): ${fmtUSD(14.5)} = covey agents (internal) ${fmtUSD(9)} ` +
      `+ covey operator ${fmtUSD(3.5)} + other projects ${fmtUSD(2)} + ${fmtCount(1000)} tokens with no list price ${MARK}, ` +
      `against the real cost of the subscription for the same period: ${fmtUSD(6 * PP)} (${fmtNumber(6, 1)} points of the meter). ` +
      `→ ${fmtUSD(xm)} ÷ ${fmtUSD(6 * PP)} ≈ ${times(xm / (6 * PP))}.`);
    expect(row.querySelector("[data-head-floor]")!.textContent).toContain(`so ${fmtUSD(12)} ÷ ${fmtUSD(8 * PP)}`);
    expect(row.querySelector("[data-head-unmetered]")!.textContent).toBe(
      `Of ${fmtUSD(14.5)}, ${fmtUSD(xm)} lies within the meter's priced readings and is what the multiple divides. ` +
      `The rest has no real cost here: ${fmtUSD(0.2)} before the meter's first reading in the range (no meter data for that time); ` +
      `${fmtUSD(0.4)} after its last reading (real share pending until the meter is read again).`);
    // covey's price table gives $15 against $14.50: $0.50 apart, silent.
    expect(row.querySelector("[data-head]")!.querySelector("[data-price-check]")).toBeNull();
    // The per-use source at its lead price: reported, every call.
    expect(row.querySelector("[data-head-per-use]")!.textContent).toContain(`(${fmtUSD(1)})`);
    // The dent's detail: the outside part outside every span, by where it lies.
    expect(row.querySelector("[data-dent-unmetered='before']")!.textContent).toBe(t("costs.dent.unmeteredBefore", { usd: fmtUSD(0.15) }));
    expect(row.querySelector("[data-dent-unmetered='after']")!.textContent).toBe(t("costs.dent.unmeteredAfter", { usd: fmtUSD(0.25) }));
  });

  it("says the price table's check under the headline where it differs", async () => {
    // Claude Code's $4 + $4 for covey's $10: the price table gives $15 against $13.50.
    serve(cells(external(), [4, 4]));
    const row = await screen.findByRole("region", { name: t("costs.dent.title") });
    expect(row.querySelector("[data-head] [data-price-check]")!.textContent).toBe(
      `covey's own price table gives ${fmtUSD(15)}, ${fmtNumber(11, 0)} % more than Claude Code reported`);
  });

  it("names the seat's list outside every reading's span by where it lies, next to its multiple", async () => {
    serve();
    await waitFor(() => expect(block()?.querySelector("[data-unmetered-note='after']")).toBeTruthy());
    expect(block()!.querySelector("[data-unmetered-note='before']")!.textContent).toBe(
      `Before the meter's first reading in the range, at token prices: covey agents (internal) ${fmtUSD(0.05)} · outside sessions ${fmtUSD(0.15)}. ` +
      "It is in the totals above, but there is no meter data for that time: it gets no real share and is not in the multiple.");
    expect(block()!.querySelector("[data-unmetered-note='after']")!.textContent).toBe(
      `After the meter's last reading, at token prices: covey agents (internal) ${fmtUSD(0.15)} · outside sessions ${fmtUSD(0.25)}. ` +
      "It is in the totals above; its real share is pending until the meter is read again, so it is not in the multiple.");
    // The multiple of everything on the meter divides the same lead figure.
    const tile = screen.getByRole("group", { name: t("costs.incl.everything") });
    expect(tile.textContent).toContain(`at token prices ${fmtUSD(14.5 - 0.6)} (covey agents (internal) ${fmtUSD(9 - 0.2)} · covey operator ≈ ${fmtUSD(3.25)} · other projects ≈ ${fmtUSD(5.1 - 3.25)}`);
  });

  it("splits the outside overview's unmetered list price the same way", () => {
    const o = outsideTotals(external())!;
    expect(o.unmeteredList).toBe(0.5);
    expect(o.unmeteredBefore).toBe(0.2);
  });
});
