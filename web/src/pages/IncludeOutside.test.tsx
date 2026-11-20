import { describe, it, expect, beforeEach } from "vitest";
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import Costs from "./Costs";
import OutsideCosts from "./OutsideCosts";
import { readFileSync } from "node:fs";
import type { CostCells, ExternalMeterStep, ExternalMeterSums, ExternalMeterTotal, ExternalModelCell, ExternalUsage } from "../api";
import i18n from "../i18n";
import { fmtCount, fmtNumber, fmtUSD } from "../format";
import { mockFetch, renderWithProviders } from "../test/render";
import { credentialFigures, everythingOnMeter, headline, inRange, meterSlack, seatLists } from "./costFigures";
import { hostsBehind, listedSources, outsideKpis, outsideModelRows, sourceRealPerPeriod, sourcesWithoutSeat } from "./outsideFigures";
import { OTHER_SOURCES, SOURCE_SERIES_MAX, externalStamp, outsideKpisBySource, sourceStyles } from "./outsideFigures";
import { HATCH_ANGLES, sourceSeriesSegs } from "./StackedCostChart";
import { RANGES, TEN_MIN_MAX_DAYS, bucketOptions } from "./outsideFigures";

// #120, the human's must: if all the work on the subscription is counted —
// covey's agents and the operator's own sessions — what would it have cost by
// token consumption and Claude Code's reports, against what the subscription
// really cost? The page includes the sessions outside covey by default, names
// the two parts of every figure that includes them, and "covey only" gives
// today's view back. The agents' own figures are never changed.

const SEAT = "claude_code_oauth_token";
const PP = 200 * 12 / 52 / 100;
const H18 = "2026-11-17T18:00:00Z", H19 = "2026-11-17T19:00:00Z";
const OP = "covey operator", SY = "projects-sylius", WORK = "operator: work", CLAIM = "AppData-Local-Temp-claim-only";
const times = (v: number) => `${fmtNumber(v, 1)}×`;

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

/** One chunk of 6 points over two readings: covey's list 6 + 4, the
 *  operator's 1 + 2 and sylius's 0 + 2 (Lo 5, share 1/3). Each reading's
 *  outside part, pp, goes 3/5 to the operator and 2/5 to sylius — the
 *  operator's in the second reading too, where it had a call, and sylius's
 *  in the first, where it had none. */
function steps(): ExternalMeterStep[] {
  const st = (at: string, from: string, cov: number, op: number, sy: number): ExternalMeterStep => ({
    at, from, secret_key: SEAT, secret_slot: 0, points: 3, reset: false, real_usd: 3 * PP,
    external_list_usd: op + sy, covey_list_usd: cov, reported: true, split: true,
    external_real_usd: PP, covey_real_usd: 2 * PP, unattributed_real_usd: 0, unattributed_points: 0, unsplit_real_usd: 0,
    source_parts: [{ source: OP, list_usd: op, real_usd: PP * 3 / 5 }, { source: SY, list_usd: sy, real_usd: PP * 2 / 5 }],
  });
  return [st("2026-11-17T18:40:00Z", "2026-11-17T17:40:00Z", 6, 1, 0), st("2026-11-17T19:40:00Z", "2026-11-17T18:40:00Z", 4, 2, 2)];
}

function seatTotal(): ExternalMeterTotal {
  return {
    ...zero, secret_key: SEAT, secret_slot: 0, meter_source: "usage_cmd", sources: [OP, SY, CLAIM].sort(),
    hours_with_reading: 2, points_per_list_usd: null, plan_rates: [],
    real_usd: 6 * PP, external_real_usd: 2 * PP, covey_real_usd: 4 * PP, share: 1 / 3,
    split_external_list_usd: 5, split_covey_list_usd: 10, points: 6, split_points: 6, split_external_points: 2, split_covey_points: 4,
    split_runs: 1, split_slack_points: 2, split_slack_usd: 2 * PP, split_external_slack_usd: 2 * PP / 3, split_covey_slack_usd: 4 * PP / 3,
    point_rate: PP, step_count: 2, split_steps: 2, unmetered_list_usd: 0.5, unmetered_covey_list_usd: 0.25,
    source_parts: [{ source: CLAIM, list_usd: 0, real_usd: 0 }, { source: OP, list_usd: 3, real_usd: 1.2 * PP }, { source: SY, list_usd: 2, real_usd: 0.8 * PP }],
    agents: [{ agent_id: "a1", split_list_usd: 10, real_usd: 4 * PP, unsplit_list_usd: 0 }],
  };
}

const host = (through: string | null) => [{ host: "laptop", reported_through: through, pushed_at: "2026-11-17T20:01:00Z" }];

function external(): ExternalUsage {
  const total = seatTotal();
  const { secret_key: _k, secret_slot: _s, meter_source: _m, sources: _src, hours_with_reading: _h, points_per_list_usd: _p,
    plan_rates: _r, agents: _a, ...sums } = total;
  const src = (source: string, link: boolean, calls: number, list: number | null, reported: number | null, unpricedTokens = 0) => ({
    source, meter_credential: link ? { secret_key: SEAT, secret_slot: 0 } : null, calls, list_usd: list, reported_usd: reported,
    unpriced_tokens: unpricedTokens, unpriced_calls: unpricedTokens > 0 ? 1 : 0, unpriced_models: unpricedTokens > 0 ? ["claude-sonnet-5-7"] : [],
    first_at: calls > 0 ? H18 : null, last_at: calls > 0 ? H19 : null, hosts: host("2026-11-17T20:00:00Z"),
  });
  const cell = (period: string, source: string, calls: number, list: number | null, reported: number | null, output: number, cacheRead: number) => ({
    period, source, origin: "main", calls, list_usd: list, measured: (list != null ? { output: list } : {}) as Record<string, number>, reported_usd: reported,
    reported_calls: reported != null ? calls : 0, tokens: { input: 1000, output, cache_read: cacheRead, cache_write_5m: 0, cache_write_1h: 0 },
    unpriced_tokens: 0, unpriced_calls: 0,
  });
  return {
    sources: [src(CLAIM, true, 0, null, null), src(OP, true, 10, 3, 2.5), src(WORK, false, 4, 1.5, 1), src(SY, true, 5, 2, null, 1000)],
    cells: [
      cell(H18, OP, 4, 1, 1, 50_000, 100_000), cell(H19, OP, 6, 2, 1.5, 80_000, 200_000),
      cell(H19, SY, 5, 2, null, 30_000, 0), cell(H18, WORK, 4, 1.5, 1, 10_000, 0),
    ],
    meter: { ...sums, steps: steps(), hours: [], credentials: [total] },
  };
}

/** covey's agent a1 on the seat: $6 and $4 of list, reported $5 and $4. */
function cells(ext: ExternalUsage | null = external()): CostCells {
  const cell = (period: string, list: number, engine: number) => ({ period, agent_id: "a1", secret_key: SEAT, secret_slot: 0,
    credential_kind: "subscription", reported_usd: engine, measured: { output: list }, engine_usd: engine,
    tokens: { input: 500, output: list * 50_000, cache_read: list * 1_000_000 } });
  const meterStep = (period: string) => ({ period, secret_key: SEAT, secret_slot: 0, credential_kind: "subscription",
    source: "usage_cmd", window_kind: "seven_day", points: 3, readings: 2, resets: 0, longest_gap_secs: 900, usd: 3 * PP, unpriced_points: 0 });
  return {
    periods: [H18, H19],
    agents: [{ id: "a1", label: "Coordinator", reported_usd: 9, measured_usd: 10 }],
    types: ["cache_read", "cache_write", "input", "output", "web_search", "unpriced", "unattributed"],
    cells: [cell(H18, 6, 5), cell(H19, 4, 4)],
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

const modelCell = (source: string, model: string, calls: number, list: number | null, reported: number | null, output: number): ExternalModelCell => ({
  period: H18, source, origin: "main", model, calls, list_usd: list, measured: {}, type_usd: {}, reported_usd: reported,
  reported_calls: reported != null ? calls : 0, tokens: { input: 0, output, cache_read: 10, cache_write_5m: 1, cache_write_1h: 2 },
  unpriced_tokens: list == null ? output + 13 : 0, unpriced_calls: list == null ? calls : 0,
});

function serve(data: CostCells = cells(), route = "/costs") {
  const ext = data.external;
  mockFetch({
    "/api/v1/cost/cells": data,
    "/api/v1/cost/billed": { bucket: "hour", points: [], sync: { configured: false, last_attempt_at: null, last_success_at: null,
      last_daily_at: null, data_refreshed_at: null, last_status: "", last_error: "" } },
    "/api/v1/cost/runs": [],
    "/api/v1/cost/models": { rows: [{ agent_id: "a1", agent_slug: "coordinator", agent_name: "Coordinator", model: "claude-opus-5-5",
      credential_kind: "subscription", runs: 2, input_tokens: 1000, output_tokens: 500_000, thinking_tokens: 0, cache_read_tokens: 0,
      cache_write_5m_tokens: 0, cache_write_1h_tokens: 0, web_search_requests: 0, engine_usd: 9, list_usd: 10 }],
      runs_without_breakdown: 0, usd_without_breakdown: 0 },
    "/api/v1/cost/external-usage": { bucket: "day", since: H18, source: null, sources: ext?.sources ?? [], models: [],
      cells: [modelCell(OP, "claude-opus-5-5", 10, 3, 2.5, 130_000), modelCell(SY, "claude-opus-5-5", 4, 2, null, 30_000),
        modelCell(SY, "claude-sonnet-5-7", 1, null, null, 1000), modelCell(WORK, "claude-opus-5-5", 4, 1.5, 1, 10_000)],
      meter: ext?.meter },
    "/api/v1/cost/usage-readings": [],
    "/api/v1/cost/indicators": null,
  });
  return renderWithProviders(<Costs />, { route, path: "/costs" });
}

/** The full view of covey's price table (#120): Reported and Recomputed apart, as before the lead figure. */
const FULL = "/costs?prices=recomputed";
const kpi = (label: string) => document.querySelector<HTMLElement>(`[data-kpi="${label}"]`)!;
const kpiValue = (label: string) => kpi(label).querySelector("[data-kpi-value]")!.textContent;
const kpiParts = (label: string) => kpi(label).querySelector("[data-parts]")?.textContent ?? null;
const t = (k: string, o: Record<string, unknown> = {}) => i18n.t(k, o) as string;

describe("the sessions outside covey on the cost page, included by default (#120)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("1: the top tiles include them and name both parts", async () => {
    serve(cells(), FULL);
    await waitFor(() => expect(kpiParts("Reported by Claude Code")).not.toBeNull());
    // covey reported 9, the outside sessions 2.5 + 1 (sylius reported none).
    expect(kpiValue("Reported by Claude Code")).toBe(fmtUSD(9 + 3.5));
    // Split by where they count (M6): on the meter (op, sylius) and per use (work).
    expect(kpiParts("Reported by Claude Code")).toBe(
      `covey agents ${fmtUSD(9)} · outside sessions on the meter ${fmtUSD(2.5)} (10 of 15 calls reported a cost) · per use ${fmtUSD(1)}`);
    expect(kpiValue("Recomputed from tokens")).toBe(fmtUSD(10 + 6.5));
    expect(kpiParts("Recomputed from tokens")).toBe(
      `covey agents ${fmtUSD(10)} · outside sessions on the meter ${fmtUSD(5)} + ${fmtCount(1000)} tokens with no list price · per use ${fmtUSD(1.5)}`);
    // Tokens: covey's 500 + 500 input, 10M cache read, 500k output; outside 4 × 1000 input, 300k cache read, 170k output.
    const coveyIn = 1000 + 10_000_000, outIn = 4000 + 300_000;
    expect(kpiValue(t("costs.inputTokens"))).toBe(fmtCount(coveyIn + outIn));
    expect(kpiParts(t("costs.inputTokens"))).toBe(
      `covey agents ${fmtCount(coveyIn)} · outside sessions on the meter ${fmtCount(outIn - 1000)} · per use ${fmtCount(1000)}`);
    expect(kpiValue(t("costs.outputTokens"))).toBe(fmtCount(500_000 + 170_000));
    expect(kpiValue(t("costs.totalTokens"))).toBe(fmtCount(coveyIn + outIn + 670_000));
    // The note names the sources with calls — never one of 0 calls.
    const note = document.querySelector("[data-inclusion-note]")!.textContent!;
    expect(note).toContain(`${OP}, ${WORK}, ${SY}`);
    expect(document.body.textContent).not.toContain(CLAIM);
  });

  it("1: “covey only” gives today's numbers back exactly, and is kept in the URL", async () => {
    serve(cells(null), FULL);
    await waitFor(() => expect(kpiValue("Reported by Claude Code")).toBe(fmtUSD(9)));
    const today = ["Reported by Claude Code", "Recomputed from tokens", t("costs.inputTokens"), t("costs.outputTokens"), t("costs.totalTokens")]
      .map((l) => kpiValue(l));
    cleanup();
    serve(cells(), "/costs?only=covey&prices=recomputed");
    await screen.findByRole("button", { name: "covey only" });
    await waitFor(() => expect(kpiValue("Reported by Claude Code")).toBe(fmtUSD(9)));
    expect(["Reported by Claude Code", "Recomputed from tokens", t("costs.inputTokens"), t("costs.outputTokens"), t("costs.totalTokens")]
      .map((l) => kpiValue(l))).toEqual(today);
    expect(document.querySelector("[data-parts]")).toBeNull();
    expect(screen.getByRole("button", { name: "covey only" }).getAttribute("aria-pressed")).toBe("true");
    expect(document.querySelector("[data-inclusion-note]")!.textContent).toMatch(/^covey only: the sessions outside covey/);
    // No outside series, rows or multiple of everything.
    expect(document.querySelector("[data-seg^='out:']")).toBeNull();
    expect(document.querySelector("[data-outside-row]")).toBeNull();
    expect(screen.queryByRole("group", { name: t("costs.incl.everything") })).toBeNull();
    // A click switches back.
    fireEvent.click(screen.getByRole("button", { name: "covey only" }));
    await waitFor(() => expect(kpiParts("Reported by Claude Code")).not.toBeNull());
  });

  it("1: with agents selected they are 'plus outside sessions', never agents", async () => {
    serve(cells(), FULL);
    fireEvent.click(await screen.findByRole("button", { name: /^Coordinator/ }));
    await waitFor(() => expect(kpiParts("Recomputed from tokens")).toMatch(/^selected agents/));
    expect(kpiParts("Recomputed from tokens")).toBe(
      `selected agents ${fmtUSD(10)} · plus outside sessions on the meter ${fmtUSD(5)} + ${fmtCount(1000)} tokens with no list price · per use ${fmtUSD(1.5)}`);
    expect(document.querySelector("[data-inclusion-note]")!.textContent).toMatch(/the outside sessions are not agents/);
  });

  it("2: the chart stacks one hatched series per source on the reported and recomputed bars, named in the legend, tooltip and table", async () => {
    const { container } = serve(cells(), FULL);
    await waitFor(() => expect(container.querySelector(`[data-outside-key="${OP}"]`)).toBeTruthy());
    expect(container.querySelector(`[data-outside-key="${SY}"]`)).toBeTruthy();
    expect(container.querySelector(`[data-outside-key="${CLAIM}"]`)).toBeNull();
    // The seat's sources stack on its bars, each with a hatch of its own.
    const segs = [...container.querySelectorAll(`[data-credential="${SEAT}#0"] rect[data-seg^="out:"]`)];
    expect(new Set(segs.map((r) => r.getAttribute("data-seg")))).toEqual(new Set([`out:${OP}`, `out:${SY}`]));
    expect(segs.every((r) => /^url\(#/.test(r.getAttribute("fill")!))).toBe(true);
    const fills = new Set(segs.filter((r) => r.getAttribute("data-seg") === `out:${OP}`).map((r) => r.getAttribute("fill")));
    const fillsSy = new Set(segs.filter((r) => r.getAttribute("data-seg") === `out:${SY}`).map((r) => r.getAttribute("fill")));
    expect([...fills].some((f) => fillsSy.has(f))).toBe(false);
    // The tooltip names each source; the bar says it includes them.
    fireEvent.focus(container.querySelector(`[data-credential="${SEAT}#0"] [data-period="${H19}"]`)!);
    const tip = await screen.findByRole("tooltip");
    expect(tip.textContent).toContain(`${OP} (outside covey)`);
    expect(tip.textContent).toContain(`${SY} (outside covey)`);
    expect(tip.textContent).toContain("Recomputed from tokens, incl. outside sessions");
    // The real bar's outside part per source, from source_parts.
    expect(tip.textContent).toContain(fmtUSD(PP * 3 / 5));
    // Table mode: a column per source, "no reported cost" where sylius has none.
    fireEvent.click(screen.getAllByRole("button", { name: t("costs.fig.showAsTable") })[0]);
    const table = container.querySelector(`[data-credential="${SEAT}#0"] table`)!;
    const heads = [...table.querySelectorAll("th[data-extra-col]")].map((e) => e.textContent);
    expect(heads).toEqual([`Reported · ${OP} (outside covey)`, `Reported · ${SY} (outside covey)`,
      `Recomputed · ${OP} (outside covey)`, `Recomputed · ${SY} (outside covey)`]);
    expect(table.textContent).toContain("no reported cost");
    // The per-use source has a block of its own.
    expect(container.querySelector("[data-outside-block]")!.textContent).toContain(WORK);
  });

  it("2: a source can be switched off in the chart", async () => {
    const { container } = serve();
    fireEvent.click(await screen.findByRole("button", { name: new RegExp(`^${SY}`) }));
    await waitFor(() => expect(container.querySelector(`rect[data-seg="out:${SY}"]`)).toBeNull());
    expect(container.querySelector(`rect[data-seg="out:${OP}"]`)).toBeTruthy();
  });

  it("3: the agent table has a row per source on the seat, marked, and a total with them", async () => {
    const { container } = serve(cells(), FULL);
    const block = await waitFor(() => {
      const b = container.querySelector<HTMLElement>(`[data-credential="${SEAT}#0"]`);
      if (!b?.querySelector("[data-incl-total]")) throw new Error("no total yet");
      return b;
    });
    const rows = [...block.querySelectorAll("tr[data-outside-row]")];
    expect(rows.map((r) => r.getAttribute("data-outside-row"))).toEqual([OP, SY]);
    expect(rows[0].textContent).toContain("outside covey");
    expect(rows[0].textContent).toContain(`≈ ${fmtUSD(1.2 * PP)}`);
    expect(rows[1].textContent).toContain("no reported cost");
    const total = block.querySelector("[data-incl-total]")!.textContent!;
    expect(total).toContain("Total incl. outside sessions");
    expect(total).toContain(fmtUSD(9 + 2.5));
    expect(total).toContain(fmtUSD(10 + 5));
    expect(total).toContain(`≈ ${fmtUSD(6 * PP)}`);
    // Shares of the seat's whole list: covey 10 of 15.
    expect(block.querySelector("tbody tr")!.textContent).toContain(`${fmtNumber(100 * 10 / 15, 1)} %`);
  });

  it("3: Usage per model has a row per source and model, marked, and a total including them; no $0 for a model with no price", async () => {
    serve();
    const row = await screen.findByText((_, el) => el?.tagName === "TR" && el.getAttribute("data-incl-total") != null && el.closest("table")?.querySelector("[data-col]") != null);
    expect(row.textContent).toContain("Total incl. outside sessions");
    expect(row.textContent).toContain(fmtUSD(10 + 3 + 2 + 1.5));
    const out = [...document.querySelectorAll("tr[data-outside-row]")].filter((r) => r.querySelector("[data-col]"));
    expect(out.map((r) => r.getAttribute("data-outside-row"))).toEqual([OP, WORK, SY, SY]);
    const sonnet = out.find((r) => r.textContent!.includes("sonnet-5-7"))!;
    expect(sonnet.textContent).toContain("no list price");
    expect(sonnet.textContent).not.toContain(fmtUSD(0));
    expect(document.body.textContent).toContain("covey agents");
  });

  it("4: the seat's multiple is of everything on its meter, a lower bound, replacing “No multiple here”", async () => {
    // At the lead price (#120): covey's $9 as Claude Code reported it, the
    // operator's $2.50 reported and sylius's $2 at the list (no reported
    // cost), less what came after the last reading ($0.25 and $0.50); at
    // covey's price table, $10 and $5 less the same.
    const e = everythingOnMeter(cells(), SEAT, 0)!;
    expect(e.coveyList).toBe(8.75);
    expect(e.outsideList).toBe(4);
    expect(e.table).toEqual({ coveyList: 9.75, outsideList: 4.5 });
    expect(e.outsideRecomputed).toBe(5);
    expect(e.real).toBeCloseTo(6 * PP, 12);
    expect(e.slack).toBeCloseTo(2 * PP, 12);
    expect(e.multiple!.kind).toBe("floor");
    expect(e.multiple!.low).toBeCloseTo(12.75 / (8 * PP), 12);
    const small = cells();
    small.external = { ...external(), meter: { ...external().meter, credentials: [{ ...seatTotal(), points: 2 }] } };
    expect(everythingOnMeter(small, SEAT, 0)!.why).toBe("movement");
    serve();
    const tile = await screen.findByRole("group", { name: "List $ per real $ — everything on this meter" });
    // ≈ list ÷ real, and at least list ÷ (real + 2 points).
    expect(tile.textContent).toContain(`≈ ${times(12.75 / (6 * PP))}`);
    expect(tile.textContent).toContain(`at least ${times(12.75 / (8 * PP))}`);
    // Labelled apart from the tiles' pairs, which are the range's (L5).
    expect(tile.textContent).toContain(`within the meter's readings: at token prices ${fmtUSD(12.75)} (covey agents ${fmtUSD(8.75)} · outside sessions ` +
      `${fmtUSD(4)} (recomputed for sources without a reported cost)) over the meter's real cost ≈ ${fmtUSD(6 * PP)}`);
    expect(tile.textContent).toContain(`at least list ÷ (real + ${fmtUSD(2 * PP)})`);
    const rec = screen.getAllByRole("group", { name: t("costs.lead.label") }).find((g) => g.closest(`[data-credential="${SEAT}#0"]`))!;
    expect(rec.querySelector("[data-parts]")!.textContent).toMatch(/^in the range: covey agents /);
    expect(document.body.textContent).not.toContain("No multiple here");
    // The split multiples stay in the headline.
    expect(document.querySelector("[data-dent-multiples]")).toBeTruthy();
  });

  it("5: list price after the last reading is in the totals, its real share pending — said next to the multiple", async () => {
    serve();
    const note = await waitFor(() => {
      const n = document.querySelector("[data-unmetered-note='after']");
      if (!n) throw new Error("no note yet");
      return n;
    });
    expect(note.textContent).toBe(`After the meter's last reading, at token prices: covey agents ${fmtUSD(0.25)} · outside sessions ${fmtUSD(0.5)}. ` +
      "It is in the totals above; its real share is pending until the meter is read again, so it is not in the multiple.");
    // Nothing before the first reading here (the server sends no such part).
    expect(document.querySelector("[data-unmetered-note='before']")).toBeNull();
  });

  it("figures: kpis, sources per seat, rows per model, real parts per period; sources of 0 calls nowhere", () => {
    const ext = external();
    expect(listedSources(ext).map((s) => s.source)).toEqual([OP, WORK, SY]);
    const k = outsideKpis(ext)!;
    expect(k.list).toBe(6.5);
    expect(k.reported).toBe(3.5);
    expect(k.sources).not.toContain(CLAIM);
    expect(sourcesWithoutSeat(ext, new Set([`${SEAT}#0`])).map((s) => s.source)).toEqual([WORK]);
    const real = sourceRealPerPeriod(ext, SEAT, 0, "hour");
    expect(real.get(Date.parse(H18))!.get(OP)).toBeCloseTo(PP * 3 / 5, 12);
    expect(real.get(Date.parse(H18))!.get(SY)).toBeCloseTo(PP * 2 / 5, 12);
    const rows = outsideModelRows([modelCell(SY, "x", 1, null, null, 5), modelCell(SY, "x", 2, null, 1, 5)]);
    expect(rows).toHaveLength(1);
    expect(rows[0].list).toBeNull();
    expect(rows[0].reported).toBe(1);
    // Excluded (covey only), the credential has no multiple of everything.
    const [f] = credentialFigures({ cells: cells(), bucketMs: 3600_000, windowEnd: new Date("2026-11-17T21:00:00Z") });
    expect(f.everything).toBeNull();
    const [g] = credentialFigures({ cells: cells(), bucketMs: 3600_000, windowEnd: new Date("2026-11-17T21:00:00Z"), includeOutside: true });
    expect(g.everything!.multiple!.low).toBeCloseTo(12.75 / (8 * PP), 12);
  });
});

describe("the headline: all the work at token prices against the one real cost (#120, the human's core)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("adds up the list prices, takes the one real cost and one multiple, its floor at ±2 points, and the unmetered part", () => {
    const h = headline(cells())!;
    // At the lead price (#120): covey's $9 on the seat as Claude Code
    // reported it, and the two sources that name it: the operator's $2.50
    // reported, sylius's $2 at the list (it reported none); covey's price
    // table gives $10 + $3 + $2. The per-use source and the source of 0
    // calls are not on the meter.
    expect(h.coveyList).toBe(9);
    expect(h.outsideList).toBe(4.5);
    expect(h.outsideRecomputed).toBe(5);
    expect(h.list).toBe(13.5);
    expect(h.table).toBe(15);
    expect(h.sources.map((x) => x.source)).toEqual([OP, SY]);
    expect(h.coveyReported).toBe(9);
    expect(h.outsideReported).toBe(2.5);
    // The real cost: 6 points at the plan's price; the list within the
    // readings ($15 less the $0.75 after the last reading) over it, and at
    // least over 8 points.
    expect(h.real).toBeCloseTo(6 * PP, 12);
    expect(h.points).toBe(6);
    expect(h.metered).toBe(12.75);
    expect(h.multiple).toBeCloseTo(12.75 / (6 * PP), 12);
    expect(h.floor).toBeCloseTo(12.75 / (8 * PP), 12);
    // After the last reading: covey's $0.25 and the outside sessions' $0.50
    // (this server sends no lead counterpart: the list stands in); nothing
    // before the first one.
    expect(h.unmetered).toBeCloseTo(0.75, 12);
    expect(h.after).toBeCloseTo(0.75, 12);
    expect(h.before).toBe(0);
    expect(h.unpriced).toBe(0);
    expect(h.list).toBeCloseTo(h.metered + h.before + h.after + h.unpriced + h.usage, 12);
    // The per-use source is apart, named, at its lead price (reported, every call).
    expect(h.perUse).toMatchObject({ list: 1.5, reported: 1, calls: 4, reportedCalls: 4, unpricedTokens: 0, lead: 1, leadRecomputed: 0 });
    // Below 3 points, no multiple.
    const small = cells();
    small.external!.meter = { ...small.external!.meter, points: 2 };
    expect(headline(small)!.why).toBe("movement");
    expect(headline(small)!.multiple).toBeNull();
    expect(headline(cells(null))).toBeNull();
  });

  it("leads with the sentence, the floor, the unmetered part and both figures; the split is a collapsed estimate", async () => {
    serve();
    const row = await screen.findByRole("region", { name: t("costs.dent.title") });
    expect(row.querySelector("[data-head-sentence]")!.textContent).toBe(
      `All this work on the subscription meters at token prices (as reported by Claude Code): ${fmtUSD(13.5)} = covey's agents ${fmtUSD(9)} ` +
      `+ sessions outside covey ${fmtUSD(4.5)} (recomputed for sources without a reported cost), ` +
      `against the real cost of the subscription for the same period: ${fmtUSD(6 * PP)} (${fmtNumber(6, 1)} points of the meter). ` +
      `→ ${fmtUSD(12.75)} ÷ ${fmtUSD(6 * PP)} ≈ ${times(12.75 / (6 * PP))}.`);
    expect(row.querySelector("[data-head-floor]")!.textContent).toBe(
      `At least ${times(12.75 / (8 * PP))}: the real cost is the meter's movement, known to ±2 points per seat and 2 more per weekly reset ` +
      `(whole percents and lag), so ${fmtUSD(12.75)} ÷ ${fmtUSD(8 * PP)}. Use nobody reported only raises the multiple of the known work.`);
    expect(row.querySelector("[data-head-unmetered]")!.textContent).toBe(
      `Of ${fmtUSD(13.5)}, ${fmtUSD(12.75)} lies within the meter's priced readings and is what the multiple divides. ` +
      `The rest has no real cost here: ${fmtUSD(0.75)} after its last reading (real share pending until the meter is read again).`);
    expect(row.querySelector("[data-head-per-use]")!.textContent).toBe(
      `Not in this sum: sessions outside covey billed per use, on no subscription meter (${fmtUSD(1)}). The tiles below include them.`);
    // covey's own price table: $15 against $13.50, a check line, not a second figure.
    expect(row.querySelector("[data-price-check]")!.textContent).toBe(
      `covey's own price table gives ${fmtUSD(15)}, ${fmtNumber(11, 0)} % more than Claude Code reported`);
    expect(row.querySelector("[data-head-reported]")).toBeNull();
    expect([...row.querySelectorAll("[data-head-source]")].map((e) => e.textContent)).toEqual([
      `${OP} (outside covey): ${fmtUSD(2.5)}`, `${SY} (outside covey): ${fmtUSD(2)} (recomputed for sources without a reported cost)`]);
    expect(row.textContent).not.toContain(CLAIM);
    // The per-party real shares: a closed detail, labelled as an estimate,
    // with the parts, the split multiples and the outside part per source.
    const detail = row.querySelector<HTMLDetailsElement>("details[data-dent-detail]")!;
    expect(detail.open).toBe(false);
    expect(detail.querySelector("summary")!.textContent).toBe("How the real cost splits (estimate)");
    expect(detail.querySelector("[data-dent='covey']")).toBeTruthy();
    expect(detail.querySelector("[data-dent-multiples]")).toBeTruthy();
    expect([...detail.querySelectorAll("[data-dent-source-part]")].map((e) => e.textContent)).toEqual([
      `${OP}: real share ≈ ${fmtUSD(1.2 * PP)} (list price ${fmtUSD(3)} in the split readings)`,
      `${SY}: real share ≈ ${fmtUSD(0.8 * PP)} (list price ${fmtUSD(2)} in the split readings)`]);
    // The headline's sentence is not inside the detail.
    expect(detail.querySelector("[data-head-sentence]")).toBeNull();
  });
});

/** A seat's meter total with a different seat name and its own numbers. */
const otherSeat = (key: string, over: Partial<ExternalMeterTotal> = {}): ExternalMeterTotal => ({
  ...seatTotal(), ...zero, secret_key: key, secret_slot: 0, sources: [], source_parts: [], agents: [], hours_with_reading: 0,
  points_per_list_usd: null, plan_rates: [], ...over,
});

describe("the #126 review of the cost page", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("H1: a swatch draws the very pattern of its bar — same key, same angle, same marks", async () => {
    const { container } = serve();
    await waitFor(() => expect(container.querySelector(`[data-outside-key="${OP}"] svg[data-swatch]`)).toBeTruthy());
    const block = container.querySelector(`[data-credential="${SEAT}#0"]`)!;
    const bar = block.querySelector(`rect[data-seg="out:${OP}"]`)!;
    const barPattern = document.getElementById(bar.getAttribute("fill")!.slice(5, -1))!;
    // The legend's swatch of the same bar (the reported one, drawn first).
    const sw = [...container.querySelectorAll(`[data-outside-key="${OP}"] svg[data-swatch]`)][0];
    const swPattern = sw.querySelector("pattern")!;
    expect(sw.getAttribute("data-pattern-key")).toBe(barPattern.getAttribute("data-pattern-key"));
    expect(swPattern.getAttribute("data-pattern-key")).toBe(barPattern.getAttribute("data-pattern-key"));
    expect(swPattern.getAttribute("patternTransform")).toBe(barPattern.getAttribute("patternTransform"));
    expect(swPattern.innerHTML).toBe(barPattern.innerHTML);
    // No CSS gradient anywhere: its angle runs perpendicular to the SVG's.
    expect(container.innerHTML).not.toContain("repeating-linear-gradient");
  });

  it("M1: 2 points at its price for every seat with list or movement, 2 more per reset; the headline and the seat agree", () => {
    const data = cells();
    // A second seat with list price but no movement, priced by its plan.
    const IDLE = "idle_oauth_token";
    data.plans!.push({ ...data.plans![0], secret_key: IDLE });
    data.cells.push({ period: H18, agent_id: "a1", secret_key: IDLE, secret_slot: 0, credential_kind: "subscription", reported_usd: 1,
      measured: { output: 1 }, engine_usd: 1, tokens: { output: 1000 } });
    data.external!.meter.credentials.push(otherSeat(IDLE, { unmetered_covey_list_usd: 1 }));
    // A weekly reset on the first seat.
    data.external!.meter.steps[1] = { ...data.external!.meter.steps[1], reset: true };
    const h = headline(data)!;
    expect(h.slack).toBeCloseTo(2 * PP * 2 + 2 * PP, 12);
    const seat = everythingOnMeter(data, SEAT, 0)!;
    expect(seat.slack).toBeCloseTo(4 * PP, 12);
    // One helper: the headline's slack is the seats' sum.
    expect(h.slack).toBeCloseTo(meterSlack(data.external!.meter.credentials, data.external!.meter.steps, () => PP,
      (c) => c.secret_key === IDLE), 12);
    // A seat with neither list nor movement adds nothing.
    expect(meterSlack([otherSeat("none")], [], () => PP)).toBe(0);
  });

  it("M2: the list is metered + after the last reading + in unpriced readings, a reading straddling the range's start cut at it", () => {
    const data = cells();
    // The range begins half-way through the first reading's span (17:40 → 18:40),
    // and that reading has no plan price: half its $7 lies in the range.
    data.since = "2026-11-17T18:10:00Z";
    data.external!.meter.steps[0] = { ...data.external!.meter.steps[0], real_usd: null, split: false, reason: "unpriced" };
    expect(inRange(data.external!.meter.steps[0], Date.parse(data.since))).toBe(0.5);
    const h = headline(data)!;
    // The lead figure (#120): $13.50; this server sends no lead per step, so
    // the unpriced reading's list stands in for it.
    expect(h.unpriced).toBeCloseTo(3.5, 12);
    expect(h.unmetered).toBeCloseTo(0.75, 12);
    expect(h.metered).toBeCloseTo(13.5 - 0.75 - 3.5, 12);
    expect(h.metered + h.before + h.after + h.unpriced + h.usage).toBeCloseTo(h.list, 12);
    // Each seat's parts add up too, at both prices.
    const l = seatLists(data, data.external!.meter.credentials[0]);
    for (const p of [l, l.lead]) {
      expect(p.covey.metered + p.covey.before + p.covey.after + p.covey.unpriced).toBeCloseTo(p.covey.range, 12);
      expect(p.outside.metered + p.outside.before + p.outside.after + p.outside.unpriced).toBeCloseTo(p.outside.range!, 12);
    }
  });

  it("M1/M2: the floor takes the server's floor_lead_usd where it sends one, the estimate keeps the metered lead", () => {
    const data = cells();
    // The floor of the lead figure (#120), not of covey's price table.
    data.external!.meter = { ...data.external!.meter, floor_list_usd: 12, floor_lead_usd: 11.5 };
    data.external!.meter.credentials[0] = { ...data.external!.meter.credentials[0], floor_list_usd: 11, floor_lead_usd: 10.5 };
    const h = headline(data)!;
    expect(h.floor).toBeCloseTo(11.5 / (8 * PP), 12);
    expect(h.floorList).toBe(11.5);
    expect(h.multiple).toBeCloseTo(12.75 / (6 * PP), 12);
    const e = everythingOnMeter(data, SEAT, 0)!;
    expect(e.multiple!.low).toBeCloseTo(10.5 / (8 * PP), 12);
    expect(e.estimate).toBeCloseTo(12.75 / (6 * PP), 12);
    // An older server sends only the list's floor: the metered lead stands in, never the list's.
    const old = cells();
    old.external!.meter = { ...old.external!.meter, floor_list_usd: 12 };
    expect(headline(old)!.floorList).toBe(12.75);
  });

  it("M3: no $0 where the outside sessions on the meter have no list price — the headline, the tile and the meter card say so", async () => {
    const ext = external();
    for (const s of ext.sources) if (s.meter_credential) { s.list_usd = null; s.unpriced_tokens = 500; }
    for (const st of ext.meter.steps) st.external_list_usd = 0;
    // Neither a list price nor a reported cost in their calls: no lead price either.
    for (const c of ext.cells) {
      if (c.source === WORK) continue;
      Object.assign(c, { list_usd: null, reported_usd: null, reported_calls: 0, unpriced_calls: c.calls, unpriced_tokens: c.source === SY ? 500 : 250 });
    }
    const data = cells(ext);
    const h = headline(data)!;
    expect(h.outsideList).toBeNull();
    serve(data);
    const row = await screen.findByRole("region", { name: t("costs.dent.title") });
    expect(row.querySelector("[data-head-sentence]")!.textContent).toContain(`sessions outside covey no list price (${fmtCount(1000)} tokens)`);
    const tile = await screen.findByRole("group", { name: t("costs.incl.everything") });
    expect(tile.textContent).toContain(`outside sessions no list price (${fmtCount(1000)} tokens)`);
    const card = await waitFor(() => {
      const c = document.querySelector(`[data-meter="${SEAT}#0"]`);
      if (!c) throw new Error("no meter card yet");
      return c;
    });
    expect(card.textContent).toContain(`${t("costs.incl.meterListOutside")}no list price (${fmtCount(1000)} tokens)`);
    expect(card.textContent).not.toContain(`${t("costs.incl.meterListOutside")}${fmtUSD(0)}`);
  });

  it("M4: a source with calls only before the range but a real part is listed, noted, and has a pattern of its own", async () => {
    const ext = external();
    const EARLY = "projects-early";
    ext.sources.push({ ...ext.sources[1], source: EARLY, calls: 0, list_usd: null, reported_usd: null, unpriced_tokens: 0, first_at: null, last_at: null });
    ext.meter.source_parts = [...(ext.meter.source_parts ?? []), { source: EARLY, list_usd: 1, real_usd: 0.1 }];
    ext.meter.credentials[0].source_parts!.push({ source: EARLY, list_usd: 1, real_usd: 0.1 });
    ext.meter.steps[0].source_parts!.push({ source: EARLY, list_usd: 0, real_usd: 0.1 });
    expect(listedSources(ext).map((s) => s.source)).toContain(EARLY);
    const st = sourceStyles(ext);
    const keys = listedSources(ext).map((s) => `${st.style(s.source).hatch}|${st.style(s.source).ink}`);
    expect(new Set(keys).size).toBe(keys.length);
    // A source the page does not list is never drawn as another source.
    expect(st.series("nobody")).toBe(OTHER_SOURCES);
    expect(keys).not.toContain(`${st.style("nobody").hatch}|${st.style("nobody").ink}`);
    const h = headline(cells(ext))!;
    expect(h.sources.find((s) => s.source === EARLY)!.before).toBe(true);
    const { container } = serve(cells(ext));
    const block = await waitFor(() => {
      const b = container.querySelector<HTMLElement>(`[data-credential="${SEAT}#0"]`);
      if (!b?.querySelector(`tr[data-outside-row="${EARLY}"]`)) throw new Error("no row yet");
      return b;
    });
    const row = block.querySelector(`tr[data-outside-row="${EARLY}"]`)!;
    expect(row.querySelector("[data-before-range]")!.textContent).toBe("calls before the range");
    expect(row.textContent).toContain(`≈ ${fmtUSD(0.1)}`);
    expect(document.querySelector(`[data-head-source="${EARLY}"]`)!.textContent).toBe(`${EARLY} (outside covey): calls before the range`);
  });

  it("M5: angle and ink change from one source to the next; beyond the limit the rest fold into “other sources”, named in the tooltip", () => {
    const ext = external();
    const names = Array.from({ length: SOURCE_SERIES_MAX + 3 }, (_, i) => `src-${String(i).padStart(2, "0")}`);
    ext.sources = names.map((n, i) => ({ ...ext.sources[1], source: n, list_usd: 100 - i }));
    const st = sourceStyles(ext);
    expect(st.folded).toEqual(names.slice(SOURCE_SERIES_MAX - 1));
    const own = names.slice(0, SOURCE_SERIES_MAX - 1).map((n) => st.style(n));
    for (let i = 1; i < own.length; i++) {
      expect(own[i].hatch).not.toBe(own[i - 1].hatch);
      expect(own[i].ink).not.toBe(own[i - 1].ink);
    }
    expect(new Set(own.map((s) => `${s.hatch}|${s.ink}`)).size).toBe(own.length);
    // Only distinguishable angles: 45°, 90°, 135° and 0°.
    for (const s of own) expect([0, 45, 90, 135]).toContain(HATCH_ANGLES[s.hatch]);
    for (const n of st.folded) expect(st.series(n)).toBe(OTHER_SOURCES);
    const segs = sourceSeriesSegs(i18n.t, st, "c", names.map((n) => ({ source: n, value: 1 })), { keyPrefix: "out:" });
    expect(segs).toHaveLength(SOURCE_SERIES_MAX);
    const other = segs.find((s) => s.key === "out:other")!;
    expect(other.value).toBe(4);
    expect(other.label).toBe(`other sources outside covey (4): ${names.slice(SOURCE_SERIES_MAX - 1).join(", ")}`);
  });

  it("M6 and L1: the per-use source is apart from the meters; the agent table's totals follow the chips and mark what has no price", async () => {
    const { container } = serve(cells(), FULL);
    const block = await waitFor(() => {
      const b = container.querySelector<HTMLElement>(`[data-credential="${SEAT}#0"]`);
      if (!b?.querySelector("[data-incl-total]")) throw new Error("no total yet");
      return b;
    });
    const total = () => block.querySelector("[data-incl-total]")!;
    // sylius reported no cost and has tokens with no list price: both sums say so.
    expect(total().querySelector("[data-incl-reported]")!.textContent).toBe(`${fmtUSD(11.5)} (with unpriced)`);
    expect(total().querySelector("[data-incl-list]")!.textContent).toBe(`${fmtUSD(15)} (with unpriced)`);
    expect(total().querySelector("[data-incl-real]")!.textContent).toBe(`≈ ${fmtUSD(6 * PP)}`);
    // Switched off by its chip, sylius leaves the real total as well.
    fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${SY}`) }));
    await waitFor(() => expect(total().querySelector("[data-incl-real]")!.textContent).toBe(`≈ ${fmtUSD(5.2 * PP)}`));
    expect(total().querySelector("[data-incl-reported]")!.textContent).toBe(fmtUSD(11.5));
    expect(total().querySelector("[data-incl-list]")!.textContent).toBe(fmtUSD(13));
  });

  it("L2: under an agent filter the overview stays on, labelled, and the multiple of everything stays, org-wide", async () => {
    serve();
    fireEvent.click(await screen.findByRole("button", { name: /^Coordinator/ }));
    const tile = await screen.findByRole("group", { name: t("costs.incl.everything") });
    expect(tile.querySelector("[data-org-wide]")).toBeTruthy();
    expect(screen.getByRole("button", { name: t("costs.outside.show") }).getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: t("costs.outside.modeCombined") }));
    const combined = screen.getByRole("group", { name: t("costs.outside.combinedTotal") });
    expect(combined.textContent).toMatch(/selected agents .* · plus outside sessions /);
  });

  it("L3: a part is ≈, never ≤ — only a multiple is a bound", async () => {
    // A seat sources name, with no split reading: covey's parts on it are estimates.
    const ext = external();
    ext.meter.steps = ext.meter.steps.map((s) => ({ ...s, split: false, reason: "not_reported", external_real_usd: null, covey_real_usd: null,
      unsplit_real_usd: s.real_usd!, source_parts: undefined }));
    ext.meter.credentials[0] = { ...ext.meter.credentials[0], split_steps: 0, unsplit_steps: 2, covey_real_usd: 0, external_real_usd: 0 };
    const { container } = serve(cells(ext));
    const block = await waitFor(() => {
      const b = container.querySelector<HTMLElement>(`[data-credential="${SEAT}#0"]`);
      if (!b?.querySelector("[data-real-share]")) throw new Error("no table yet");
      return b;
    });
    expect(block.querySelector("table")!.textContent).not.toContain("≤");
    expect(block.querySelector("[data-real-share]")!.textContent).toMatch(/^≈ /);
  });

  it("L4: the sources' figures once per answer, the same as one at a time; a stamp that changes only with a call", () => {
    const ext = external();
    const by = outsideKpisBySource(ext);
    for (const s of listedSources(ext)) expect(by.get(s.source)).toEqual(outsideKpis(ext, new Set([s.source])));
    const a = externalStamp(ext);
    expect(externalStamp({ ...ext, meter: { ...ext.meter, real_usd: 99 } })).toBe(a);
    ext.sources[1] = { ...ext.sources[1], calls: 11 };
    expect(externalStamp(ext)).not.toBe(a);
  });
});

describe("the outside review's minors (#120)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("names the machine that is behind where readings were not reported", () => {
    const ext = external();
    ext.meter.steps.push({ ...steps()[0], at: "2026-11-17T21:40:00Z", from: "2026-11-17T19:40:00Z", split: false, reason: "not_reported",
      reported: false, external_real_usd: null, covey_real_usd: null, unsplit_real_usd: PP, source_parts: undefined });
    ext.sources[1] = { ...ext.sources[1], hosts: [...host("2026-11-17T20:00:00Z"), { host: "desktop", reported_through: null, pushed_at: H18 }] };
    expect(hostsBehind(ext, SEAT, 0)).toEqual([
      { source: OP, host: "desktop", through: null },
      { source: OP, host: "laptop", through: "2026-11-17T20:00:00Z" },
      { source: SY, host: "laptop", through: "2026-11-17T20:00:00Z" },
    ]);
    expect(hostsBehind(external(), SEAT, 0)).toEqual([]);
  });

  it("says in visible text why a bucket or a chip is not offered, and offers 2 days with every bucket up to 10 minutes", async () => {
    expect(RANGES.map((r) => r.v)).toContain(2);
    expect(TEN_MIN_MAX_DAYS).toBe(2);
    expect(bucketOptions(i18n.t, 2).every((o) => !o.disabled)).toBe(true);
    serve();
    // 2 days is a range button between 24 h and 7 days.
    const two = await screen.findByRole("button", { name: "2 days" });
    fireEvent.click(two);
    fireEvent.click(screen.getByRole("button", { name: "10 min" }));
    const real = await waitFor(() => {
      const b = document.querySelector<HTMLButtonElement>("button[disabled][aria-describedby]");
      if (!b) throw new Error("no disabled chip yet");
      return b;
    });
    const id = real.getAttribute("aria-describedby")!;
    expect(document.getElementById(id)!.textContent).toBe(t("costs.outside.tenMinNoMeter"));
    // 30 days: the finer buckets are disabled, each described by visible text.
    fireEvent.click(screen.getByRole("button", { name: "30 days" }));
    const ten = screen.getByRole("button", { name: "10 min" });
    expect(ten).toBeDisabled();
    expect(document.getElementById(ten.getAttribute("aria-describedby")!)!.textContent).toBe(t("costs.outside.tenMinTooLong", { days: 2 }));
  });

  it("marks the billed bars before the hourly rows begin as incomplete, and says so", async () => {
    const KEY = "anthropic_api_key";
    const data = cells(null);
    data.cells.push(
      { period: H18, agent_id: "a1", secret_key: KEY, secret_slot: 0, credential_kind: "api_key", reported_usd: 4, measured: { output: 4 },
        engine_usd: 4, tokens: { output: 200_000 } },
      { period: H19, agent_id: "a1", secret_key: KEY, secret_slot: 0, credential_kind: "api_key", reported_usd: 1, measured: { output: 1 },
        engine_usd: 1, tokens: { output: 50_000 } });
    const point = (bucket: string, usd: string) => ({ bucket, api_key_id: "apikey_01", workspace_id: "", model: "claude-opus-5-5",
      secret_key: KEY, secret_slot: 0, billed_usd: usd, billed_list_usd: usd, complete: true });
    mockFetch({
      "/api/v1/cost/cells": data,
      "/api/v1/cost/billed": { bucket: "hour", hourly_from: H19, points: [point(H19, "1.00")],
        window: { start: H18, end: "2026-11-17T23:00:00Z", compared_until: "2026-11-17T20:00:00Z", width: "1h",
          totals: [{ secret_key: KEY, secret_slot: 0, has_billed: true, billed_usd: "1.00", billed_list_usd: "1.00", reported_usd: 1, list_usd: 1 }] },
        sync: { configured: true, last_attempt_at: null, last_success_at: null, last_daily_at: null,
          data_refreshed_at: "2026-11-18T00:00:00Z", last_status: "ok", last_error: "" } },
      "/api/v1/cost/runs": [],
      "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
      "/api/v1/cost/usage-readings": [],
      "/api/v1/cost/indicators": null,
    });
    const { container } = renderWithProviders(<Costs />, { route: "/costs", path: "/costs" });
    const note = await waitFor(() => {
      const n = container.querySelector(`[data-credential="${KEY}#0"] [data-billed-hourly-from]`);
      if (!n) throw new Error("no note yet");
      return n;
    });
    expect(note.textContent).toMatch(/^The bill is kept per hour from .* on: bars before it are missing or incomplete\.$/);
    const h18 = container.querySelector(`[data-credential="${KEY}#0"] [data-period="${H18}"]`)!;
    expect(h18.getAttribute("aria-label")).toContain(t("costs.fig.billedBeforeHourly"));
    expect(container.querySelector(`[data-credential="${KEY}#0"] [data-period="${H19}"]`)!.getAttribute("aria-label"))
      .not.toContain(t("costs.fig.billedBeforeHourly"));
    // the notes' column of the table is headed "Note", not left blank
    fireEvent.click(screen.getAllByRole("button", { name: "Show as table" })[0]);
    const table = container.querySelector<HTMLElement>(`[data-credential="${KEY}#0"] table`)!;
    expect(within(table).getByRole("columnheader", { name: t("costs.outside.colNote") })).toBeTruthy();
    expect(within(table).getAllByText(t("costs.fig.billedBeforeHourly"))).toHaveLength(1);
  });

  it("the detailed view shows the meter of a seat with a dent although the range has no outside call", async () => {
    const ext = external();
    mockFetch({ "/api/v1/cost/external-usage": { bucket: "hour", since: H18, source: null, sources: ext.sources, models: [], cells: [], meter: ext.meter } });
    const { container } = renderWithProviders(<OutsideCosts />, { route: "/costs/outside?days=1&bucket=hour", path: "/costs/outside" });
    await waitFor(() => expect(container.querySelector("[data-outside-meter]")).toBeTruthy());
    expect(container.querySelector("[data-outside-empty]")!.textContent).toBe(t("costs.outside.empty"));
    expect(container.querySelector(`[data-seat="${SEAT}#0"]`)).toBeTruthy();
    // A split seat with no rate (k) never shows "beyond the rate: at least $0".
    expect(container.querySelector(`[data-seat="${SEAT}#0"]`)!.textContent).not.toContain(t("costs.dent.unattributedBeyond", { usd: fmtUSD(0) }));
    expect(container.textContent).not.toContain(CLAIM);
  });

  it("draws the meter's parts and the origins at least 3:1 against the card, in both schemes", () => {
    const lum = (hex: string) => {
      const c = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255).map((x) => (x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4));
      return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
    };
    const ratio = (a: string, b: string) => { const [x, y] = [lum(a), lum(b)].sort((m, n) => n - m); return (x + 0.05) / (y + 0.05); };
    const styles = readFileSync(`${process.cwd()}/src/styles.css`, "utf8");
    const token = (name: string) => {
      const m = styles.match(new RegExp(`--${name}: light-dark\\((#[0-9a-f]{6}), (#[0-9a-f]{6})\\)`));
      if (!m) throw new Error(`no ${name}`);
      return [m[1], m[2]];
    };
    for (const name of ["cost-outside", "cost-unattributed", "cost-unsplit", "cost-origin-main", "cost-origin-subagent",
      "cost-origin-workflow", "cost-origin-unknown", "cost-source-1", "cost-source-2", "cost-source-3"]) {
      const [light, dark] = token(name);
      expect(ratio(light, "#ffffff"), `${name} light`).toBeGreaterThanOrEqual(3);
      expect(ratio(dark, "#1f1f22"), `${name} dark`).toBeGreaterThanOrEqual(3);
    }
    // In the dark scheme the outside sessions and the unattributed part differ in lightness too, not by hue alone.
    expect(ratio(token("cost-outside")[1], token("cost-unattributed")[1])).toBeGreaterThanOrEqual(2.5);
  });
});
