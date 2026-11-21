import { describe, it, expect, beforeEach } from "vitest";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import Costs from "./Costs";
import OutsideCosts from "./OutsideCosts";
import { readFileSync } from "node:fs";
import type { CostCells, CostHealthCheck, CostHealthReport, ExternalMeterStep, ExternalMeterSums, ExternalMeterTotal, ExternalModelCell, ExternalUsage } from "../api";
import i18n from "../i18n";
import { fmtBucket, fmtCount, fmtNumber, fmtUSD } from "../format";
import { mockFetch, renderWithProviders, openDentRow } from "../test/render";
import { CostAnswer } from "./CostAnswer";
import { credentialFigures, everythingOnMeter, headline, inRange, meterSlack, seatLists, subscriptionDent } from "./costFigures";
import { answerTable } from "./costAnswerModel";
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
/** The operator's input tokens in the fixture: two cells of 1,000 plus 300,000 read from the cache. */
const OP_IN = 2000 + 300_000;

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

function serve(data: CostCells = cells(), route = "/costs", health?: CostHealthReport) {
  const ext = data.external;
  mockFetch({
    ...(health ? { "/api/v1/cost/health": health } : {}),
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
/** The answer table (#195): a row's cells, its token price and its real share. */
const answerRow = (key: string) => document.querySelector<HTMLElement>(`[data-answer-row="${key}"]`);
/** The visible text: without the screen-reader-only words. */
const visible = (e: Element | null) => { const c = e!.cloneNode(true) as Element; c.querySelectorAll(".sr-only").forEach((x) => x.remove()); return c.textContent; };
const rowList = (key: string) => visible(answerRow(key)!.querySelector("[data-answer-list]"));
const rowReal = (key: string) => answerRow(key)!.querySelector("[data-answer-real]")!.textContent;
/** The explanations live in the drawer (#204): open "How is this computed" once, then read one of its notes by key. */
const openHow = () => { if (!document.querySelector("[data-answer-drawer]")) fireEvent.click(document.querySelector("[data-answer-how]")!); };
const howNote = (k: string) => { openHow(); return document.querySelector(`[data-answer-note="${k}"]`); };
const answerReady = () => waitFor(() => expect(document.querySelector("[data-answer-total]")).not.toBeNull());

describe("the sessions outside covey on the cost page, included by default (#120)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("1: the answer table counts them as rows and the collapsed tokens name both parts", async () => {
    serve(cells());
    await answerReady();
    // covey's agents at Claude Code's figure ($9), the operator's reported $2.50 and sylius's $2 at the list (no reported cost, so marked).
    // work is billed per use, on no meter: a line of its own, not a row.
    expect(rowList("covey")).toBe(fmtUSD(9));
    expect(rowList("operator")).toBe(fmtUSD(2.5));
    // A recomputed figure carries a footnote mark; its legend is in the drawer, not repeated in every cell (#204).
    expect(rowList(`project:${SY}`)).toBe(`${fmtUSD(2)}*`);
    expect(howNote("recomputed")!.textContent).toBe(`* ${t("costs.answer.recomputedMark")}`);
    expect(answerRow(`project:${WORK}`)).toBeNull();
    expect(howNote("per-use")!.textContent).toBe(
      `Not in the table: sessions outside covey billed per use, on no subscription meter (${fmtUSD(1)}).`);
    // The real shares, as the server split the meter: 4, 1.2 and 0.8 points of 6.
    expect(rowReal("covey")).toBe(`≈ ${fmtUSD(4 * PP)}`);
    expect(rowReal("operator")).toBe(`≈ ${fmtUSD(1.2 * PP)}`);
    expect(rowReal(`project:${SY}`)).toBe(`≈ ${fmtUSD(0.8 * PP)}`);
    // Tokens: covey's 500 + 500 input, 10M cache read, 500k output; outside 4 × 1000 input, 300k cache read, 170k output.
    const coveyIn = 1000 + 10_000_000, outIn = 4000 + 300_000;
    expect(kpiValue(t("costs.inputTokens"))).toBe(fmtCount(coveyIn + outIn));
    expect(kpiParts(t("costs.inputTokens"))).toBe(
      `covey agents (internal) ${fmtCount(coveyIn)} · covey operator ${fmtCount(OP_IN)} · other projects ${fmtCount(1000)} · per use ${fmtCount(outIn - OP_IN - 1000)}`);
    expect(kpiValue(t("costs.outputTokens"))).toBe(fmtCount(500_000 + 170_000));
    expect(kpiValue(t("costs.totalTokens"))).toBe(fmtCount(coveyIn + outIn + 670_000));
    // The tokens are a collapsed section, not above the table.
    expect(document.querySelector<HTMLDetailsElement>("details[data-tokens]")!.open).toBe(false);
    // With the answer table its rows name the sources with calls (no separate inclusion note, #204) — never one of 0 calls.
    expect(document.querySelector("[data-inclusion-note]")).toBeNull();
    expect(answerRow("operator")).not.toBeNull();
    expect(answerRow(`project:${SY}`)).not.toBeNull();
    expect(document.body.textContent).not.toContain(CLAIM);
  });

  it("1: “covey only” gives the token figures back exactly, and is kept in the URL", async () => {
    serve(cells(null));
    await waitFor(() => expect(kpiValue(t("costs.inputTokens"))).toBe(fmtCount(1000 + 10_000_000)));
    const today = [t("costs.inputTokens"), t("costs.outputTokens"), t("costs.totalTokens")].map((l) => kpiValue(l));
    cleanup();
    serve(cells(), "/costs?only=covey");
    await screen.findByRole("button", { name: "covey only" });
    await waitFor(() => expect(kpiValue(t("costs.inputTokens"))).toBe(fmtCount(1000 + 10_000_000)));
    expect([t("costs.inputTokens"), t("costs.outputTokens"), t("costs.totalTokens")].map((l) => kpiValue(l))).toEqual(today);
    expect(document.querySelector("[data-parts]")).toBeNull();
    expect(screen.getByRole("button", { name: "covey only" }).getAttribute("aria-pressed")).toBe("true");
    expect(document.querySelector("[data-inclusion-note]")!.textContent).toMatch(/^covey only: the sessions outside covey/);
    // No outside series or rows in the chart; the table is the whole dent, as the note says.
    expect(document.querySelector("[data-seg^='out:']")).toBeNull();
    expect(document.querySelector("[data-outside-row]")).toBeNull();
    // A click switches back.
    fireEvent.click(screen.getByRole("button", { name: "covey only" }));
    await waitFor(() => expect(kpiParts(t("costs.inputTokens"))).not.toBeNull());
  });

  it("1: with agents selected they are the operator and other projects, never agents; the table says it is organisation-wide", async () => {
    serve(cells());
    fireEvent.click(await screen.findByRole("button", { name: /^Coordinator/ }));
    await waitFor(() => expect(kpiParts(t("costs.inputTokens"))).toMatch(/^selected agents/));
    // With agents selected the note stays: the outside sessions are not agents and stay in the KPIs and chart below (#204 re-review).
    expect(document.querySelector("[data-inclusion-note]")!.textContent).toBe(
      t("costs.incl.includedNoteSelected", { sources: document.querySelector("[data-inclusion-note]")!.textContent!.match(/\(([^)]*)\)/)![1] }));
    expect(document.querySelector("[data-answer] [data-org-wide]")).toBeTruthy();
    expect(rowList("covey")).toBe(fmtUSD(9));
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
    expect(document.body.textContent).toContain("covey agents (internal)");
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
    await answerReady();
    // ≈ list ÷ real, and at least list ÷ (real + 2 points): the difference row of the answer table.
    const multiple = document.querySelector("[data-answer-multiple]")!;
    expect(multiple.textContent).toContain(`≈ ${times(12.75 / (6 * PP))}`);
    expect(multiple.textContent).toContain(`a floor: at least ${times(12.75 / (8 * PP))}`);
    expect(document.body.textContent).not.toContain("No multiple here");
    // The split multiples stay in the drawer's headline.
    await openDentRow();
    expect(document.querySelector("[data-dent-multiples]")).toBeTruthy();
  });

  it("5: list price after the last reading is in the totals, its real share pending — said once, under the table", async () => {
    serve();
    await answerReady();
    expect(document.querySelector("[data-answer-after]")!.textContent).toBe(t("costs.answer.after", { usd: fmtUSD(0.75) }));
    // Nothing before the first reading here (the server sends no such part).
    expect(document.querySelector("[data-answer-before]")).toBeNull();
    // The seat's block does not say it again.
    expect(document.querySelector("[data-unmetered-note]")).toBeNull();
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
    const row = await openDentRow();
    expect(row.querySelector("[data-head-sentence]")!.textContent).toBe(
      `All this work on the subscription meters at token prices (as reported by Claude Code): ${fmtUSD(13.5)} = covey agents (internal) ${fmtUSD(9)} ` +
      `+ covey operator ${fmtUSD(2.5)} + other projects ${fmtUSD(2)} (recomputed for sources without a reported cost), ` +
      `against the real cost of the subscription for the same period: ${fmtUSD(6 * PP)} (${fmtNumber(6, 1)} points of the meter). ` +
      `→ ${fmtUSD(12.75)} ÷ ${fmtUSD(6 * PP)} ≈ ${times(12.75 / (6 * PP))}.`);
    expect(row.querySelector("[data-head-floor]")!.textContent).toBe(
      `At least ${times(12.75 / (8 * PP))}: the real cost is the meter's movement, known to ±2 points per seat and 2 more per weekly reset ` +
      `(whole percents and lag), so ${fmtUSD(12.75)} ÷ ${fmtUSD(8 * PP)}. Use nobody reported only raises the multiple of the known work.`);
    expect(row.querySelector("[data-head-unmetered]")!.textContent).toBe(
      `Of ${fmtUSD(13.5)}, ${fmtUSD(12.75)} lies within the meter's priced readings and is what the multiple divides. ` +
      `The rest has no real cost here: ${fmtUSD(0.75)} after its last reading (real share pending until the meter is read again).`);
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
    const row = await openDentRow();
    expect(row.querySelector("[data-head-sentence]")!.textContent).toContain(`covey operator no list price (${fmtCount(500)} tokens) + other projects no list price (${fmtCount(500)} tokens)`);
    // The rows say it too, with the tokens they leave out — never $0.
    expect(rowList("operator")).toBe(`no list price (${fmtCount(500)} tokens)`);
    expect(rowList(`project:${SY}`)).toBe(`no list price (${fmtCount(500)} tokens)`);
    // The meter card no longer repeats them (#195).
    expect(document.querySelector(`[data-meter="${SEAT}#0"]`)?.textContent ?? "").not.toContain(t("costs.incl.meterListOperator"));
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
    serve(cells(ext));
    // The answer table names it by its real part alone.
    await answerReady();
    expect(rowList(`project:${EARLY}`)).toBe("calls before the range");
    expect(rowReal(`project:${EARLY}`)).toBe(`≈ ${fmtUSD(0.1)}`);
    await openDentRow();
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
    await answerReady();
    expect(document.querySelector("[data-answer] [data-org-wide]")).toBeTruthy();
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

describe("the answer table (#195)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });
  const sumOf = (sel: string) => [...document.querySelectorAll(sel)].reduce((a, e) => a + Number((e.textContent ?? "").match(/\$([\d,.]+)/)?.[1].replace(/,/g, "") ?? 0), 0);

  it("its total is the sum of its rows, in both columns, and is the headline's figure", async () => {
    const data = cells();
    const h = headline(data)!;
    const a = answerTable(h, subscriptionDent(data.external)!, data.external!, data.meter ?? [], new Set());
    expect(a.list).toBeCloseTo(h.list, 9);
    expect(a.real!).toBeCloseTo(a.rows.reduce((x, r) => x + (r.real ?? 0), 0), 9);
    expect(a.real!).toBeCloseTo(h.real!, 9);
    serve(data);
    await answerReady();
    expect(sumOf("[data-answer-row] [data-answer-list]")).toBeCloseTo(sumOf("[data-answer-total] [data-answer-list]"), 2);
    expect(sumOf("[data-answer-row] [data-answer-real]")).toBeCloseTo(sumOf("[data-answer-total] [data-answer-real]"), 1);
  });

  it("the real cost is the meter's points times the plan's price of a point, and names the points per week across a reset", async () => {
    const data = cells();
    const a = answerTable(headline(data)!, subscriptionDent(data.external)!, data.external!, data.meter ?? [], new Set());
    expect(a.real!).toBeCloseTo(a.points! * a.rate!, 9);
    serve(data);
    await answerReady();
    expect(document.querySelector("[data-answer-total]")!.textContent).toContain(t("costs.answer.totalSub", { pts: "6", rate: "$0.4615" }));
    cleanup();
    const weeks = cells();
    weeks.meter![0].segments = [
      { start_at: "2026-11-17T17:40:00Z", end_at: "2026-11-17T18:40:00Z", from_percent: 10, to_percent: 14, reset: false, points: 4 },
      { start_at: "2026-11-17T18:40:00Z", end_at: "2026-11-17T19:40:00Z", from_percent: 0, to_percent: 2, reset: true, points: 2 },
    ];
    serve(weeks);
    await answerReady();
    expect(document.querySelector("[data-answer-total]")!.textContent).toContain(t("costs.answer.totalSubWeeks", { weeks: "4 + 2", pts: "6", rate: "$0.4615" }));
  });

  it("switching an outside source off removes its row and updates the total", async () => {
    serve();
    await answerReady();
    expect(answerRow(`project:${SY}`)).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${SY}`) }));
    await waitFor(() => expect(answerRow(`project:${SY}`)).toBeNull());
    expect(document.querySelector("[data-answer-total] [data-answer-list]")!.textContent).toBe(fmtUSD(9 + 2.5));
    expect(sumOf("[data-answer-total] [data-answer-real]")).toBeCloseTo(6 * PP - 0.8 * PP, 1);
    expect(document.querySelector("[data-answer-hidden]")!.textContent).toContain(SY);
    expect(document.querySelector("[data-answer-multiple]")!.textContent).toBe("–");
    fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${SY}`) }));
    await waitFor(() => expect(answerRow(`project:${SY}`)).not.toBeNull());
  });

  /** The figures the table holds, read off it; none of them may stand on the page outside the table. exempt: the selection's own
   *  tiles, which are the selection's figures and not the table's. */
  const figuresOnceOnly = (exempt: string[] = [], within?: string) => {
    const table = document.querySelector("[data-answer]")!;
    const figures = new Set<string>();
    for (const sel of ["[data-answer-total] [data-answer-list]", "[data-answer-total] [data-answer-real]", "[data-answer-multiple] span",
      '[data-answer-row="operator"] [data-answer-list]', `[data-answer-row="project:${SY}"] [data-answer-list]`,
      '[data-answer-row="operator"] [data-answer-real]', `[data-answer-row="project:${SY}"] [data-answer-real]`]) {
      figures.add(table.querySelector(sel)!.textContent!.replace(/^≈ /, "").replace(/ \(.*$/, "").replace(/ \+ .*$/, ""));
    }
    const clone = (within ? document.querySelector(within)! : document.body).cloneNode(true) as HTMLElement;
    clone.querySelector("[data-answer]")?.remove();
    // The chart's screen-reader summary is not a figure on the screen.
    clone.querySelectorAll("[data-desc]").forEach((e) => e.remove());
    clone.querySelectorAll("[data-tile]").forEach((e) => { if (exempt.some((l) => e.textContent!.includes(l))) e.remove(); });
    const rest = clone.textContent!;
    for (const f of figures) expect(rest, f).not.toContain(f);
  };

  it("no figure of the table stands on the page a second time", async () => {
    serve();
    await answerReady();
    figuresOnceOnly();
  });

  it("15: nor with one of two agents selected, but the selection's own lead and share of real", async () => {
    const data = cells();
    data.agents.push({ id: "a2", label: "Reviewer", reported_usd: 3, measured_usd: 3 });
    data.cells.push({ ...data.cells[0], agent_id: "a2", reported_usd: 3, measured: { output: 3 }, engine_usd: 3 });
    serve(data);
    await answerReady();
    fireEvent.click(await screen.findByRole("button", { name: /^Reviewer/ }));
    await waitFor(() => expect(document.querySelectorAll(`[data-credential^="${SEAT}"] [data-tile]`).length).toBeGreaterThan(0));
    const seat = document.querySelector(`[data-credential^="${SEAT}"]`)!;
    // Only the selection's own tiles stand; the everything tile, the split caption and the sources' rows are the table's.
    expect([...seat.querySelectorAll("[data-tile]")].map((x) => x.textContent!.includes(t("costs.incl.everything")))).not.toContain(true);
    // The seat's block: the page's per-model table below lists the selected agents with the outside sessions by design.
    figuresOnceOnly([t("costs.lead.label"), t("costs.fig.shareOfReal")], `[data-credential^="${SEAT}"]`);
  });
});

/** The meter as the server sends it with `patch` applied to the seat and to the sums over all seats. */
function patched(patch: Partial<ExternalMeterTotal>, steps?: ExternalMeterStep[]): ExternalUsage {
  const e = external();
  Object.assign(e.meter, patch);
  Object.assign(e.meter.credentials[0], patch);
  if (steps) e.meter.steps = steps;
  return e;
}

describe("the answer table, review round 1 (#204)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });
  const rowsIn = () => [...document.querySelectorAll("[data-answer-row]")].map((r) => r.getAttribute("data-answer-row"));
  const real = (key: string) => rowReal(key);

  it("1: with no split step no part reads ≈ $0.00: – and why, and the whole movement is the not-split row", async () => {
    const e = patched({
      covey_real_usd: 0, external_real_usd: 0, unsplit_real_usd: 6 * PP, unsplit_steps: 2, split_steps: 0, split_runs: 0, split_points: 0,
      split_external_points: 0, split_covey_points: 0, split_covey_list_usd: 0, split_external_list_usd: 0, source_parts: undefined,
    }, steps().map((s) => ({ ...s, split: false, covey_real_usd: 0, external_real_usd: 0, unsplit_real_usd: 3 * PP, source_parts: undefined })));
    const data = cells(e);
    const a = answerTable(headline(data)!, subscriptionDent(e)!, e, data.meter ?? [], new Set());
    expect(a.rows.filter((r) => r.kind !== "unsplit").every((r) => r.real == null)).toBe(true);
    serve(data);
    await answerReady();
    for (const key of ["covey", "operator", `project:${SY}`]) {
      expect(real(key)).toContain("–");
      expect(real(key)).toContain(t("costs.answer.notSplit"));
      expect(real(key)).not.toContain("$0.00");
    }
    // Their work at token prices stays; the money is the one row that holds it.
    expect(rowList("covey")).toBe(fmtUSD(9));
    expect(rowReal("unsplit")).toBe(`≈ ${fmtUSD(6 * PP)}`);
    expect(rowsIn()).not.toContain("unattributed");
    expect(document.querySelector("[data-answer-total] [data-answer-real]")!.textContent).toBe(`≈ ${fmtUSD(6 * PP)}`);
    expect(document.querySelector("[data-answer]")!.textContent).not.toContain("$0.00");
  });

  it("2: the remainder is the server's own parts, each with its reason, and the rows add up to the meter's real cost", async () => {
    // real = covey 2.5 + outside 2 + unattributed 1 + unsplit 0.5 points' worth.
    const e = patched({ covey_real_usd: 2.5 * PP, external_real_usd: 2 * PP, unattributed_real_usd: PP, unsplit_real_usd: 0.5 * PP, unsplit_steps: 1 });
    const data = cells(e);
    const h = headline(data)!;
    const a = answerTable(h, subscriptionDent(e)!, e, data.meter ?? [], new Set());
    expect(a.rows.map((r) => r.kind)).toEqual(["covey", "operator", "project", "unsplit", "unattributed"]);
    expect(a.rows.reduce((x, r) => x + (r.real ?? 0), 0)).toBeCloseTo(h.real!, 9);
    serve(data);
    await answerReady();
    expect(rowReal("unsplit")).toBe(`≈ ${fmtUSD(0.5 * PP)}`);
    expect(rowReal("unattributed")).toBe(`≈ ${fmtUSD(PP)}`);
    expect(answerRow("unsplit")!.textContent).toContain(t("costs.answer.fromUnsplit"));
    expect(answerRow("unattributed")!.textContent).toContain(t("costs.answer.fromUnattributed"));
    expect(answerRow("rest")).toBeNull();
    expect(sumOf("[data-answer-row] [data-answer-real]")).toBeCloseTo(sumOf("[data-answer-total] [data-answer-real]"), 1);
  });

  const sumOf = (sel: string) => [...document.querySelectorAll(sel)].reduce((a, e) => a + Number((e.textContent ?? "").match(/\$([\d,.]+)/)?.[1].replace(/,/g, "") ?? 0), 0);

  it("3: covey's work on a seat outside the meter is said under the table, and that seat keeps its tiles", async () => {
    const data = cells();
    data.cells.push({ period: H18, agent_id: "a1", secret_key: "seat_two", secret_slot: 0, credential_kind: "subscription", reported_usd: 7.77,
      measured: { output: 7.77 }, engine_usd: 7.77, tokens: { input: 500, output: 100_000, cache_read: 1_000_000 } });
    serve(data);
    await answerReady();
    expect(headline(data)!.coveyElsewhere).toBeCloseTo(7.77, 9);
    expect(rowList("covey")).toBe(fmtUSD(9));
    expect(answerRow("covey")!.textContent).toContain(t("costs.answer.fromCovey"));
    expect(howNote("elsewhere")!.textContent).toBe(t("costs.answer.elsewhere", { usd: fmtUSD(7.77) }));
    // The seat the table does not cover is not answered by it: its tiles stand; the metered seat's are gone.
    await waitFor(() => expect(document.querySelector('[data-credential^="seat_two"]')).not.toBeNull());
    expect(document.querySelectorAll('[data-credential^="seat_two"] [data-tile]').length).toBeGreaterThan(0);
    expect(document.querySelectorAll(`[data-credential^="${SEAT}"] [data-tile]`).length).toBe(0);
  });

  it("4: a seat partly on the usage basis: list = metered + before + after + unpriced + usage, each said under the table", async () => {
    const st = steps();
    const usageStep: ExternalMeterStep = { ...st[1], at: "2026-11-17T20:40:00Z", from: "2026-11-17T19:40:00Z", reported: true, split: false, reason: "usage",
      real_usd: null, external_real_usd: null, covey_real_usd: null, unattributed_real_usd: 0, unattributed_points: 0, unsplit_real_usd: 0,
      external_list_usd: 0.4, covey_list_usd: 3, source_parts: undefined };
    const e = patched({ usage_steps: 1, usage_points: 3, unmetered_before_list_usd: 0.2, unmetered_before_lead_usd: 0.2 }, [...st, usageStep]);
    const data = cells(e);
    data.usage_charges = [{ secret_key: SEAT, secret_slot: 0, window_amount: 5, counted_amount: 5, counted_usd: 5, currency: "USD" } as never];
    const h = headline(data)!;
    expect(h.usage).toBeGreaterThan(0);
    expect(h.before).toBeGreaterThan(0);
    expect(h.after).toBeGreaterThan(0);
    expect(h.list).toBeCloseTo(h.metered + h.before + h.after + h.unpriced + h.usage, 9);
    serve(data);
    await answerReady();
    expect(document.querySelector("[data-answer-total] [data-answer-list]")!.textContent).toBe(fmtUSD(h.list));
    // Under the table only the work after the last reading; the rest of the list's parts are explained in the drawer (#204).
    expect(document.querySelector('[data-answer-note]:not([data-answer-note="recomputed"])')).toBeNull();
    expect(howNote("usage")!.textContent).toBe(
      t("costs.dent.head.usageOut", { seats: h.usageSeats.join(", "), n: "3", list: fmtUSD(h.usage) }));
    expect(howNote("usage-charges")).not.toBeNull();
    expect(howNote("before")!.textContent).toBe(t("costs.answer.before", { usd: fmtUSD(h.before) }));
    expect(document.querySelector("[data-answer-after]")!.textContent).toBe(t("costs.answer.after", { usd: fmtUSD(h.after) }));
  });

  it("4: readings with no plan price are said under the table too", async () => {
    const st = steps();
    const unpricedStep: ExternalMeterStep = { ...st[1], at: "2026-11-17T20:40:00Z", from: "2026-11-17T19:40:00Z", split: false, real_usd: null,
      external_real_usd: null, covey_real_usd: null, unattributed_real_usd: 0, unattributed_points: 0, unsplit_real_usd: 0, source_parts: undefined };
    const e = patched({ unpriced_steps: 1, unpriced_points: 3 }, [...st, unpricedStep]);
    const data = cells(e);
    const h = headline(data)!;
    expect(h.unpriced).toBeGreaterThan(0);
    serve(data);
    await answerReady();
    expect(howNote("unpriced")!.textContent).toBe(t("costs.answer.unpriced", { usd: fmtUSD(h.unpriced) }));
  });

  it("5: covey's share of the split is in no caption under the chart", async () => {
    serve();
    await answerReady();
    await waitFor(() => expect(document.querySelector('[data-credential^="' + SEAT + '"]')).not.toBeNull());
    expect(document.querySelector("[data-split-share]")).toBeNull();
    // The per-agent rows stay: with one agent its share of covey's real cost is the covey row's figure, as a figure of its own.
    expect(document.querySelector("[data-credential] table")!.textContent).toContain(`≈ ${fmtUSD(4 * PP)}`);
  });

  it("6: “covey only” ignores the sources switched off before it", async () => {
    serve();
    await answerReady();
    fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${SY}`) }));
    await waitFor(() => expect(answerRow(`project:${SY}`)).toBeNull());
    fireEvent.click(screen.getByRole("button", { name: t("costs.incl.coveyOnly") }));
    await waitFor(() => expect(answerRow(`project:${SY}`)).not.toBeNull());
    expect(document.querySelector("[data-answer-hidden]")).toBeNull();
  });

  it("7: the per-use line follows the chips, and a chip keeps the price of a source the table has no row for", async () => {
    serve();
    await answerReady();
    expect(howNote("per-use")).not.toBeNull();
    // work is billed per use: no row, so its chip keeps its price; the operator's has a row, so no price.
    const chip = (name: string) => screen.getByRole("button", { name: new RegExp(`^${name}`) });
    expect(chip(WORK).textContent).toContain(fmtUSD(1.5));
    expect(chip(OP).textContent).not.toContain("$");
    fireEvent.click(chip(WORK));
    await waitFor(() => expect(document.querySelector('[data-answer-note="per-use"]')).toBeNull());
  });

  it("10: the drawer button names the drawer only while it is open", async () => {
    serve();
    await answerReady();
    const how = screen.getByRole("button", { name: t("costs.answer.how") });
    expect(how.getAttribute("aria-controls")).toBeNull();
    fireEvent.click(how);
    await waitFor(() => expect(document.getElementById(how.getAttribute("aria-controls")!)).not.toBeNull());
  });
});

/** Two seats: the operator and the claim-only folder on SEAT (split), sylius on seat_b, whose one step was not split. covey's
 *  agent has work on seat_b too where `coveyOnB`. Each seat's real cost is its own steps' (6 and 3 points). */
function twoSeats(coveyOnB = false): CostCells {
  const e = external();
  const a = e.meter.credentials[0];
  const opPart = { source: OP, list_usd: 3, real_usd: 2 * PP };
  a.sources = [CLAIM, OP].sort();
  a.source_parts = [{ source: CLAIM, list_usd: 0, real_usd: 0 }, opPart];
  const b = otherSeat("seat_b", {
    sources: [SY], points: 3, step_count: 1, real_usd: 3 * PP, unsplit_real_usd: 3 * PP, unsplit_steps: 1, split_steps: 0,
    source_parts: [{ source: SY, list_usd: 2, real_usd: 0 }], plan_rates: [], hours_with_reading: 1,
  });
  e.meter.credentials.push(b);
  Object.assign(e.meter, { real_usd: 9 * PP, points: 9, step_count: 3, unsplit_real_usd: 3 * PP, unsplit_steps: 1,
    source_parts: [...a.source_parts!, ...b.source_parts!] });
  e.sources = e.sources.map((s) => (s.source === SY ? { ...s, meter_credential: { secret_key: "seat_b", secret_slot: 0 } } : s));
  const st = steps()[0];
  e.meter.steps.push({ ...st, secret_key: "seat_b", at: "2026-11-17T19:40:00Z", from: "2026-11-17T18:40:00Z", points: 3, real_usd: 3 * PP, split: false,
    covey_list_usd: coveyOnB ? 2 : 0, external_list_usd: 2, covey_real_usd: 0, external_real_usd: 0, unsplit_real_usd: 3 * PP, source_parts: undefined });
  const data = cells(e);
  data.meter = [...(data.meter ?? []), { ...data.meter![0], secret_key: "seat_b", points: 3, points_low: 2, points_high: 4 }];
  data.plans = [...data.plans!, { ...data.plans![0], secret_key: "seat_b" }];
  data.subscription = [...data.subscription!, { ...data.subscription![0], secret_key: "seat_b", points: 3, usd: 3 * PP }];
  if (coveyOnB) data.cells.push({ ...data.cells[0], secret_key: "seat_b", reported_usd: 2, measured: { output: 2 }, engine_usd: 2 });
  return data;
}

describe("the answer table, review round 2 (#204)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });
  const tableOf = (data: CostCells) => answerTable(headline(data)!, subscriptionDent(data.external)!, data.external!, data.meter ?? [], new Set());
  const dollars = (s: string | null | undefined) => Number((s ?? "").match(/\$([\d,.]+)/)?.[1].replace(/,/g, ""));

  it("11: a source on a seat with no split step reads – and “not split yet”, not ≈ $0.00, though another seat is split", async () => {
    const data = twoSeats();
    const a = tableOf(data);
    const sy = a.rows.find((r) => r.source === SY)!;
    expect(sy.real).toBeNull();
    expect(sy.notSplit).toBe(true);
    // The operator's source sits on the split seat only: its figure stands, whole.
    const op = a.rows.find((r) => r.kind === "operator")!;
    expect(op.real).toBeCloseTo(2 * PP, 9);
    expect(op.notSplit || op.partlySplit).toBe(false);
    serve(data);
    await answerReady();
    expect(rowReal(`project:${SY}`)).toContain("–");
    expect(rowReal(`project:${SY}`)).toContain(t("costs.answer.notSplit"));
    expect(rowReal(`project:${SY}`)).not.toContain("$0.00");
    expect(rowReal("operator")).toBe(`≈ ${fmtUSD(2 * PP)}`);
    // covey has no work on seat_b: its row is whole.
    expect(answerRow("covey")!.querySelector("[data-answer-partly]")).toBeNull();
  });

  it("11: covey's row says it is partly not split where covey worked on a seat with no split step", async () => {
    const data = twoSeats(true);
    const covey = tableOf(data).rows.find((r) => r.kind === "covey")!;
    expect(covey.partlySplit).toBe(true);
    expect(covey.real).not.toBeNull();
    serve(data);
    await answerReady();
    expect(answerRow("covey")!.querySelector("[data-answer-partly]")!.textContent).toBe(t("costs.answer.partlyNotSplit"));
    expect(answerRow(`project:${SY}`)!.querySelector("[data-answer-partly]")).toBeNull();
  });

  it("16: a source no seat names is never partly split: no row claims a seat behind it", async () => {
    // One seat split, one not, and a source whose meter is a seat but which neither seat names (its calls lie before the range)
    // and that still has a real part.
    const data = twoSeats();
    const e = data.external!;
    const GHOST = "projects-ghost";
    e.sources = [...e.sources, { ...e.sources.find((x) => x.source === SY)!, source: GHOST, meter_credential: { secret_key: SEAT, secret_slot: 0 } }];
    e.meter.source_parts = [...e.meter.source_parts!, { source: GHOST, list_usd: 1, real_usd: 0.5 * PP }];
    const a = tableOf(data);
    const ghost = a.rows.find((r) => r.source === GHOST)!;
    expect(ghost).toBeTruthy();
    expect(ghost.partlySplit).toBe(false);
    expect(ghost.notSplit).toBe(false);
    expect(ghost.real).toBeCloseTo(0.5 * PP, 9);
    // A source a seat does name keeps its own rule: seat_b has no split step.
    expect(a.rows.find((r) => r.source === SY)!.notSplit).toBe(true);
    serve(data);
    await answerReady();
    expect(answerRow(`project:${GHOST}`)!.querySelector("[data-answer-partly]")).toBeNull();
    expect(rowReal(`project:${GHOST}`)).toBe(`≈ ${fmtUSD(0.5 * PP)}`);
  });

  it("12: the multiple is the figure above the Difference row over the Total's real cost, and the sentence names it", async () => {
    const data = cells();
    const a = tableOf(data);
    expect(a.metered / a.real!).toBeCloseTo(a.multiple!, 9);
    expect(a.metered).toBeLessThan(a.list);
    serve(data);
    await answerReady();
    // The Difference row names its numerator where it is less than the Total's figure (#204 re-review): read it there.
    const from = document.querySelector("[data-answer-multiple-from]")!.textContent!;
    expect(from).toBe(t("costs.answer.fromDifferenceOf", { metered: fmtUSD(a.metered) }));
    const numerator = dollars(from);
    const total = dollars(document.querySelector("[data-answer-total] [data-answer-real]")!.textContent);
    const shown = Number(document.querySelector("[data-answer-multiple]")!.textContent!.match(/≈ ([\d.]+)×/)![1]);
    expect(numerator).not.toBeCloseTo(dollars(document.querySelector("[data-answer-total] [data-answer-list]")!.textContent), 2);
    expect(numerator / total).toBeCloseTo(shown, 0);
    expect(Math.abs(numerator / total - shown)).toBeLessThan(0.1);
    // The sentence does not pair the Total list with the multiple.
    const sentence = document.querySelector("[data-answer-sentence]")!.textContent!;
    expect(sentence).toContain(`${fmtUSD(a.metered)} of it within the meter's readings is ≈ ${times(a.multiple!)}`);
  });

  it("13: with incomplete chunks the table says its parts may hold use that is not theirs; without, it does not", async () => {
    serve(cells(patched({ incomplete_chunks: 1 })));
    await answerReady();
    // The caveat is in the drawer (#204): not under the table while it is closed, there once it is opened.
    expect(document.querySelector("[data-answer-drawer]")).toBeNull();
    expect(document.querySelector('[data-answer-note="incomplete"]')).toBeNull();
    expect(howNote("incomplete")!.textContent).toBe(t("costs.dent.incompleteNote", { n: "1" }));
    cleanup();
    serve(cells(patched({ incomplete_chunks: 0 })));
    await answerReady();
    expect(howNote("incomplete")).toBeNull();
  });

  it("14: with agents selected the seat shows the selection's lead and share of the real cost, not the table's", async () => {
    const data = cells();
    data.agents.push({ id: "a2", label: "Reviewer", reported_usd: 3, measured_usd: 3 });
    data.cells.push({ ...data.cells[0], agent_id: "a2", reported_usd: 3, measured: { output: 3 }, engine_usd: 3 });
    serve(data);
    await answerReady();
    expect(document.querySelectorAll(`[data-credential^="${SEAT}"] [data-tile]`).length).toBe(0);
    fireEvent.click(await screen.findByRole("button", { name: /^Coordinator/ }));
    fireEvent.click(await screen.findByRole("button", { name: /^Reviewer/ }));
    await waitFor(() => expect(document.querySelectorAll(`[data-credential^="${SEAT}"] [data-tile]`).length).toBeGreaterThan(0));
    const tiles = [...document.querySelectorAll(`[data-credential^="${SEAT}"] [data-tile]`)].map((x) => x.textContent!);
    // The selection's lead: the two agents' list on the seat; and its share of the real cost.
    expect(tiles.some((x) => x.includes(fmtUSD(16.5)) && x.includes(`selected agents (internal) ${fmtUSD(12)}`))).toBe(true);
    expect(tiles.some((x) => x.includes("share of real"))).toBe(true);
    // The table stays organisation-wide, and holds what the seat's block no longer says under a filter either: the everything
    // tile, covey's share of the split, the sources' rows of the agent table and the notes before and after the readings.
    expect(document.querySelector("[data-answer] [data-org-wide]")).toBeTruthy();
    const seat = document.querySelector(`[data-credential^="${SEAT}"]`)!;
    expect(tiles.some((x) => x.includes(t("costs.incl.everything")))).toBe(false);
    for (const sel of ["[data-split-share]", "[data-outside-row]", "[data-incl-total]", "[data-unmetered-note]", "[data-no-multiple]"]) {
      expect(seat.querySelector(sel), sel).toBeNull();
    }
  });

  it("14: with two seats each keeps its own real cost and multiple on its card and tiles; the table adds them up", async () => {
    serve(twoSeats(true));
    await answerReady();
    await waitFor(() => expect(document.querySelectorAll("[data-meter]").length).toBe(2));
    for (const m of document.querySelectorAll("[data-meter]")) expect(m.textContent).toContain(t("costs.fig.realEstimate"));
    expect(document.querySelectorAll(`[data-credential^="${SEAT}"] [data-tile]`).length).toBeGreaterThan(0);
  });

  it("14: parts that add up to more than the meter show a signed row, so the real column still adds up", async () => {
    // The sources' parts carry 1 point's worth more than the outside part the server split.
    const e = patched({ source_parts: [{ source: CLAIM, list_usd: 0, real_usd: 0 }, { source: OP, list_usd: 3, real_usd: 1.2 * PP + PP }, { source: SY, list_usd: 2, real_usd: 0.8 * PP }] });
    e.meter.credentials[0].source_parts = e.meter.source_parts;
    const data = cells(e);
    const a = tableOf(data);
    const over = a.rows.find((r) => r.kind === "outsideRest")!;
    expect(over.real!).toBeCloseTo(-PP, 9);
    expect(a.rows.reduce((x, r) => x + (r.real ?? 0), 0)).toBeCloseTo(a.real!, 9);
    serve(data);
    await answerReady();
    expect(rowReal("outsideRest")).toBe(`≈ ${fmtUSD(-PP)}`);
    expect(answerRow("outsideRest")!.textContent).toContain(t("costs.answer.rowOutsideOver"));
    const val = (sel: string) => [...document.querySelectorAll(sel)].reduce((x, el) => x + (el.textContent!.includes("-") ? -1 : 1) * dollars(el.textContent), 0);
    expect(val("[data-answer-row] [data-answer-real]")).toBeCloseTo(val("[data-answer-total] [data-answer-real]"), 1);
  });
});

/** Extra projects on SEAT, each with one priced cell at H19. */
function withProjects(data: CostCells, xs: { source: string; list: number; firstAt?: string }[]): CostCells {
  const e = data.external!;
  for (const x of xs) {
    const base = e.sources.find((s) => s.source === SY)!;
    e.sources.push({ ...base, source: x.source, meter_credential: { secret_key: SEAT, secret_slot: 0 }, calls: 1, list_usd: x.list,
      reported_usd: null, unpriced_tokens: 0, unpriced_calls: 0, unpriced_models: [], first_at: x.firstAt ?? H18, last_at: x.firstAt ?? H19 });
    e.cells.push({ ...e.cells.find((c) => c.source === SY)!, source: x.source, calls: 1, list_usd: x.list, measured: { output: x.list },
      reported_usd: null, reported_calls: 0 });
    e.meter.credentials[0].sources = [...e.meter.credentials[0].sources, x.source].sort();
  }
  return data;
}

describe("the answer table, re-review (#204)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });
  const tableOf = (data: CostCells) => answerTable(headline(data)!, subscriptionDent(data.external)!, data.external!, data.meter ?? [], new Set());

  it("a source whose calls all come after its own seat's last reading is pending, though another seat was read later", async () => {
    const data = twoSeats();
    const e = data.external!;
    // seat_b is read two hours after SEAT's last reading (19:40); the operator, on SEAT only, starts in between.
    const b = e.meter.steps.find((x) => x.secret_key === "seat_b")!;
    Object.assign(b, { at: "2026-11-17T21:40:00Z", from: "2026-11-17T20:40:00Z" });
    e.sources = e.sources.map((s) => (s.source === OP ? { ...s, first_at: "2026-11-17T20:00:00Z", last_at: "2026-11-17T20:30:00Z" } : s));
    const a = tableOf(data);
    const op = a.rows.find((r) => r.kind === "operator")!;
    expect(op.pending).toBe(true);
    expect(op.real).toBeNull();
    // sylius started before seat_b's reading: not pending.
    expect(a.rows.find((r) => r.source === SY)!.pending).toBe(false);
    serve(data);
    await answerReady();
    expect(answerRow("operator")!.querySelector("[data-answer-pending]")!.textContent).toBe(t("costs.answer.pending"));
    expect(rowReal("operator")).not.toContain("$");
  });

  it("a source that started before its seat's last reading is not pending", () => {
    const a = tableOf(cells());
    expect(a.rows.some((r) => r.pending)).toBe(false);
  });

  it("folds two or more projects under $1 into one row whose figures are their sums; a lone small one keeps its row", async () => {
    const data = withProjects(cells(), [{ source: "projects-a", list: 0.3 }, { source: "projects-b", list: 0.4 }, { source: "projects-c", list: 0.2 }]);
    const a = tableOf(data);
    const small = a.rows.find((r) => r.kind === "small")!;
    expect(small.folded).toEqual(["projects-a", "projects-b", "projects-c"]);
    expect(small.list).toBeCloseTo(0.9, 9);
    for (const x of small.folded) expect(a.rows.some((r) => r.source === x)).toBe(false);
    // sylius has tokens with no price: never folded.
    expect(a.rows.some((r) => r.source === SY)).toBe(true);
    // The total still adds up the rows.
    expect(a.list).toBeCloseTo(a.rows.reduce((acc, r) => acc + (r.list ?? 0), 0), 9);
    serve(data);
    await answerReady();
    expect(answerRow("small")!.querySelector("th")!.textContent).toContain(t("costs.answer.rowSmall", { count: 3 }));
    expect(answerRow("small")!.querySelector("td.cost-answer-from")!.textContent).toBe("projects-a, projects-b, projects-c");
    expect(rowList("small")).toBe(`${fmtUSD(0.9)}*`);
    cleanup();
    const one = tableOf(withProjects(cells(), [{ source: "projects-a", list: 0.3 }]));
    expect(one.rows.some((r) => r.kind === "small")).toBe(false);
    expect(one.rows.some((r) => r.source === "projects-a")).toBe(true);
  });

  it("a pending small project keeps its own row instead of being folded", () => {
    const late = "2026-11-17T20:00:00Z";
    const a = tableOf(withProjects(cells(), [{ source: "projects-a", list: 0.3 }, { source: "projects-b", list: 0.4 },
      { source: "projects-late", list: 0.2, firstAt: late }]));
    expect(a.rows.find((r) => r.kind === "small")!.folded).toEqual(["projects-a", "projects-b"]);
    expect(a.rows.find((r) => r.source === "projects-late")!.pending).toBe(true);
  });

  it("a late project too small to show a figure still folds, never its own '≈ $0.00' row", () => {
    const a = tableOf(withProjects(cells(), [{ source: "projects-a", list: 0.4 }, { source: "projects-b", list: 0.3 },
      { source: "projects-tiny", list: 0.003, firstAt: "2026-11-17T20:00:00Z" }]));
    expect(a.rows.find((r) => r.kind === "small")!.folded).toEqual(["projects-a", "projects-b", "projects-tiny"]);
    expect(a.rows.some((r) => r.source === "projects-tiny")).toBe(false);
  });

  it("the answer at a glance is an equation: the metered work ÷ the capacity used = the multiple, never the total ÷ it", async () => {
    const data = cells();
    const a = tableOf(data);
    const h = headline(data)!;
    serve(data);
    await answerReady();
    const hero = document.querySelector("[data-answer-hero]")!;
    // The numerator is the metered work, not the Total row's figure.
    expect(hero.querySelector("[data-hero-list]")!.textContent).toBe(fmtUSD(a.metered));
    expect(fmtUSD(a.metered)).not.toBe(document.querySelector("[data-answer-total] [data-answer-list]")!.textContent);
    expect(hero.querySelector("[data-hero-real]")!.textContent).toBe(document.querySelector("[data-answer-total] [data-answer-real]")!.textContent);
    expect(hero.querySelector("[data-hero-multiple]")!.textContent).toBe(`≈ ${times(a.multiple!)}`);
    expect(a.metered / a.real!).toBeCloseTo(a.multiple!, 9);
    expect(hero.textContent).toContain(t("costs.answer.heroMetered"));
    expect(hero.textContent).toContain(t("costs.answer.heroCapacity"));
    expect(hero.textContent).toContain(t("costs.answer.heroPoints", { pts: "6" }));
    // The reconciliation: the Total's figure = the metered work + what lies outside the readings, and it adds up.
    const parts = [h.before, h.after, h.unpriced, h.usage].filter((v) => v >= 0.005);
    expect(parts.length).toBeGreaterThan(0);
    expect(a.metered + parts.reduce((x, y) => x + y, 0)).toBeCloseTo(a.list, 2);
    const recon = hero.querySelector("[data-answer-recon]")!.textContent!;
    expect(recon.startsWith(t("costs.answer.reconAll", { all: fmtUSD(a.list), metered: fmtUSD(a.metered) }))).toBe(true);
    if (h.after >= 0.005) expect(recon).toContain(t("costs.answer.reconAfter", { usd: fmtUSD(h.after) }));
  });


  it("a project whose work all came before the meter's first reading says so, never ≈ $0.00", async () => {
    // SEAT's first reading starts 17:40 on 11/17; this project's calls are at 10:00 that morning.
    const data = withProjects(cells(), [{ source: "projects-early", list: 5, firstAt: "2026-11-17T10:00:00Z" }]);
    const a = tableOf(data);
    const row = a.rows.find((r) => r.source === "projects-early")!;
    expect(row.real).toBeNull();
    expect(row.beforeMeter).toBe("2026-11-17T17:40:00.000Z");
    expect(row.pending).toBe(false);
    serve(data);
    await answerReady();
    const cell = answerRow("project:projects-early")!.querySelector("[data-answer-real]")!;
    expect(cell.querySelector("[data-answer-before-meter]")!.textContent).toBe(t("costs.answer.beforeMeter"));
    expect(cell.textContent).toContain(t("costs.answer.beforeMeterWhy", { date: fmtBucket("2026-11-17T17:40:00Z", "hour") }));
    expect(cell.textContent).not.toContain("$0.00");
    // Its token price still counts in the Total: it is work at token prices, with no meter data to share.
    expect(rowList("project:projects-early")).toContain(fmtUSD(5));
  });

  it("a project with calls on both sides of the first reading keeps its figure", () => {
    const data = withProjects(cells(), [{ source: "projects-early", list: 5, firstAt: "2026-11-17T10:00:00Z" }]);
    const e = data.external!;
    e.sources = e.sources.map((s) => (s.source === "projects-early" ? { ...s, last_at: H19 } : s));
    expect(tableOf(data).rows.find((r) => r.source === "projects-early")!.beforeMeter).toBeNull();
  });

  it("a row none of whose work lies in a split reading says it is not assigned yet, never ≈ $0.00", async () => {
    // Calls on both sides of the readings and none inside them: neither before the meter nor pending, and no part.
    const data = withProjects(cells(), [{ source: "projects-gap", list: 4 }]);
    const e = data.external!;
    e.sources = e.sources.map((s) => (s.source === "projects-gap" ? { ...s, first_at: "2026-11-17T10:00:00Z", last_at: "2026-11-17T21:00:00Z" } : s));
    const row = tableOf(data).rows.find((r) => r.source === "projects-gap")!;
    expect(row.beforeMeter).toBeNull();
    expect(row.pending).toBe(false);
    expect(row.real).toBeNull();
    expect(row.notSplit).toBe(true);
    serve(data);
    await answerReady();
    expect(rowReal("project:projects-gap")).toContain(t("costs.answer.notSplit"));
    expect(rowReal("project:projects-gap")).not.toContain("$0.00");
    // A row with work in a split reading keeps its figure, however small.
    expect(rowReal(`project:${SY}`)).toMatch(/^≈ \$/);
  });

  it("a row of several sources whose work lies mostly in no split reading says it may be too low, not a bare figure", async () => {
    // Two small projects fold into one row: a ($0.40, all of it in a split reading) and b ($0.90, none of it).
    const data = withProjects(cells(), [{ source: "projects-a", list: 0.4 }, { source: "projects-b", list: 0.9 }]);
    data.external!.meter.source_parts = [...data.external!.meter.source_parts!, { source: "projects-a", list_usd: 0.4, real_usd: 0.001 }];
    const small = tableOf(data).rows.find((r) => r.kind === "small")!;
    expect(small.folded).toEqual(["projects-a", "projects-b"]);
    expect(small.real).toBeCloseTo(0.001, 9);
    expect(small.partlySplit).toBe(true);
    serve(data);
    await answerReady();
    expect(answerRow("small")!.querySelector("[data-answer-partly]")!.textContent).toBe(t("costs.answer.partlyNotSplit"));
    // With most of the row's work in a split reading, no note.
    const most = withProjects(cells(), [{ source: "projects-a", list: 0.9 }, { source: "projects-b", list: 0.4 }]);
    most.external!.meter.source_parts = [...most.external!.meter.source_parts!, { source: "projects-a", list_usd: 0.9, real_usd: 0.02 }];
    expect(tableOf(most).rows.find((r) => r.kind === "small")!.partlySplit).toBe(false);
  });

  it("hides the reconciliation line while a source is switched off: its parts would not add up to the rows shown", () => {
    const data = cells();
    const h = headline(data)!;
    const a = answerTable(h, subscriptionDent(data.external)!, data.external!, data.meter ?? [], new Set([SY]));
    renderWithProviders(<CostAnswer a={a} h={h} filtered={false} />);
    expect(document.querySelector("[data-answer-hero]")).not.toBeNull();
    expect(document.querySelector("[data-answer-recon]")).toBeNull();
    cleanup();
    const all = tableOf(data);
    renderWithProviders(<CostAnswer a={all} h={h} filtered={false} />);
    expect(document.querySelector("[data-answer-recon]")).not.toBeNull();
    // The operators are not read out; each figure keeps its label.
    for (const op of document.querySelectorAll(".cost-answer-hero-op")) expect(op.getAttribute("aria-hidden")).toBe("true");
  });

  it("each row's 'where it comes from' is in the column and, for phones, under the part, the same text", async () => {
    serve(cells());
    await answerReady();
    for (const r of document.querySelectorAll("[data-answer-row]")) {
      const col = r.querySelector("td.cost-answer-from")!.textContent;
      expect(col).not.toBe("");
      expect(r.querySelector(".cost-answer-from-inline")!.textContent).toBe(col);
    }
    expect(answerRow("covey")!.querySelector("td.cost-answer-from")!.textContent).toBe(t("costs.answer.fromCovey"));
  });

  it("the * mark is explained under the table, always, and read out to screen readers", async () => {
    serve(cells());
    await answerReady();
    expect(document.querySelector("[data-answer-drawer]")).toBeNull();
    expect(document.querySelector('[data-answer-note="recomputed"]')!.textContent).toBe(`* ${t("costs.answer.recomputedMark")}`);
    const mark = document.querySelector("[data-answer-recomputed]")!;
    expect(mark.getAttribute("aria-hidden")).toBe("true");
    expect(mark.parentElement!.querySelector(".sr-only")!.textContent).toContain(t("costs.answer.recomputedMark"));
  });

  it("with the answer table the filters come after it, once; under 'covey only' the inclusion note is back", async () => {
    serve(cells());
    await answerReady();
    const table = document.querySelector("[data-answer]")!;
    const chips = document.querySelectorAll("[data-inclusion]");
    expect(chips.length).toBe(1);
    expect(table.compareDocumentPosition(chips[0]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(document.querySelector("[data-inclusion-note]")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "covey only" }));
    await waitFor(() => expect(document.querySelector("[data-inclusion-note]")).not.toBeNull());
  });
});

describe("the chart's source chips from the address (#215 review)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });
  it("a link that hides every source falls back to all of them; one that hides some keeps them hidden", async () => {
    serve(cells(), "/costs?hide=reported,real,billed");
    await answerReady();
    const chip = (name: string) => screen.getAllByRole("button", { name }).find((b) => b.hasAttribute("aria-pressed"))!;
    await waitFor(() => expect(chip(t("costs.lead.label")).getAttribute("aria-pressed")).toBe("true"));
    cleanup();
    serve(cells(), "/costs?hide=billed");
    await answerReady();
    await waitFor(() => expect(chip(t("costs.lead.label")).getAttribute("aria-pressed")).toBe("true"));
  });
});

// #178: a failed data check marks every figure it leaves incomplete, and
// only those — where the figure is, never instead of it.
describe("the incomplete marks of the data checks (#178)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  const failed = (id: string, reason: string, facts: CostHealthCheck["facts"] = {}): CostHealthCheck =>
    ({ id, status: "fail", reason, facts, checked_at: H19 });
  const report = (...checks: CostHealthCheck[]): CostHealthReport => ({ status: "fail", checked_at: H19, checks });
  const c1 = failed("c1", "stale", { last_push_minutes: 80,
    stale_hosts: [{ source: OP, host: "laptop", pushed_at: H18, minutes: 80 }] });
  const parts = (scope: string) => [...document.querySelectorAll(`${scope} [data-incomplete]`)].map((e) => e.getAttribute("data-incomplete"));
  // The headline is the answer card since #204: its marks stand beside its title.
  const headlineMarks = async () => {
    await answerReady();
    return waitFor(() => {
      const marks = [...document.querySelectorAll("[data-answer-incomplete] [data-incomplete]")].map((e) => e.getAttribute("data-incomplete"));
      if (marks.length === 0) throw new Error("no marks yet");
      return marks;
    });
  };

  it("a stopped uploader (c1): the outside part in the headline, beside the inclusion chip and on the outside sessions", async () => {
    serve(cells(), "/costs", report(c1));
    expect(await headlineMarks()).toEqual(["outside"]);
    expect(parts("[data-inclusion]")).toEqual(["outside"]);
    expect(parts("[data-outside]")).toEqual(["outside"]);
    expect(document.querySelectorAll("[data-incomplete='tokenPrice'], [data-incomplete='multiple']").length).toBe(0);
  });

  it("a reset nobody recorded (c3) or a seat with no reading (c2): the multiple in the headline", async () => {
    serve(cells(), "/costs", report(failed("c3", "drops", { drops: 1, early_resets: 0, last_drop_at: H19 })));
    expect(await headlineMarks()).toEqual(["multiple"]);
    cleanup();
    serve(cells(), "/costs", report(failed("c2", "none", { credential: `${SEAT} #0` })));
    expect(await headlineMarks()).toEqual(["multiple"]);
    expect(document.querySelectorAll("[data-incomplete='outside'], [data-incomplete='tokenPrice']").length).toBe(0);
  });

  it("a model without a list price (c5): every figure at token prices — tiles, chart, model table, headline", async () => {
    serve(cells(), "/costs", report(failed("c5", "unpriced", { models: 2, unpriced: ["claude-sonnet-5-7"], unpriced_agents: ["claude-sonnet-5-7"] })));
    expect(await headlineMarks()).toEqual(["tokenPrice"]);
    expect(parts("[data-kpi-incomplete]")).toEqual([]);
    expect(parts("[data-chart-title]")).toEqual(["tokenPrice"]);
    await waitFor(() => expect(parts("[data-models-title]")).toEqual(["tokenPrice"]));
    expect(document.querySelectorAll("[data-incomplete='outside'], [data-incomplete='multiple']").length).toBe(0);
  });

  it("a call booked twice (c4): the same figures, and every mark at once when several checks fail", async () => {
    serve(cells(), "/costs", report(c1, failed("c3", "drops", { drops: 1, last_drop_at: H19 }),
      failed("c4", "duplicates", { external_duplicates: 0, agent_duplicates: 2 })));
    expect(await headlineMarks()).toEqual(["multiple", "outside", "tokenPrice"]);
    expect(parts("[data-kpi-incomplete]")).toEqual([]);
    expect(parts("[data-chart-title]")).toEqual(["tokenPrice"]);
  });

  it("“covey only” leaves the outside part out of the chips and tiles, but the answer card keeps its outside rows and their marks", async () => {
    serve(cells(), "/costs?only=covey", report(c1, failed("c5", "unpriced", { unpriced: ["x"], unpriced_agents: ["x"] })));
    // The card is the whole dent under covey only (#195): its operator and project rows stand, and the stopped
    // uploader's mark with them (#179 merge review).
    expect(await headlineMarks()).toEqual(["outside", "tokenPrice"]);
    expect(answerRow("operator")).not.toBeNull();
    expect(parts("[data-inclusion]")).toEqual([]);
  });

  // Review round 2, finding 17: a model only outside calls used marks the token prices where the outside sessions are
  // counted: everywhere they are included, and on the answer card always (its outside rows stand under covey only too).
  it("a model only outside calls used: marked where the outside sessions are counted, the answer card always", async () => {
    const outsideOnly = failed("c5", "unpriced", { unpriced: ["x"], unpriced_outside: ["x"] });
    serve(cells(), "/costs", report(outsideOnly));
    expect(await headlineMarks()).toEqual(["tokenPrice"]);
    cleanup();
    serve(cells(), "/costs?only=covey", report(failed("c3", "drops", { drops: 1, last_drop_at: H19 }), outsideOnly));
    expect(await headlineMarks()).toEqual(["multiple", "tokenPrice"]);
    // Off the card, covey's own figures carry no outside model's mark.
    expect(document.querySelectorAll("[data-chart-title] [data-incomplete='tokenPrice'], [data-models-title] [data-incomplete='tokenPrice']").length).toBe(0);
  });

  it("a call booked twice (c4) marks the token counts in the Tokens drawer", async () => {
    serve(cells(), "/costs", report(failed("c4", "duplicates", { external_duplicates: 0, agent_duplicates: 2 })));
    await answerReady();
    await waitFor(() => expect(parts("[data-tokens-incomplete]")).toEqual(["tokenPrice"]));
    cleanup();
    serve(cells(), "/costs", report(failed("c5", "unpriced", { unpriced: ["x"], unpriced_agents: ["x"] })));
    await answerReady();
    await waitFor(() => expect(parts("[data-answer-incomplete]")).toEqual(["tokenPrice"]));
    // An unpriced model changes no token count: no mark there.
    expect(parts("[data-tokens-incomplete]")).toEqual([]);
  });

  it("/costs/outside: a stopped uploader and an unpriced model mark the page; an agent line booked twice does not", async () => {
    const ext = external();
    const view = { bucket: "hour", since: H18, source: null, sources: ext.sources, models: [], cells: [], meter: ext.meter };
    mockFetch({ "/api/v1/cost/external-usage": view, "/api/v1/cost/health": report(c1, failed("c5", "unpriced", { unpriced: ["x"], unpriced_outside: ["x"] })) });
    renderWithProviders(<OutsideCosts />, { route: "/costs/outside?days=1&bucket=hour", path: "/costs/outside" });
    await waitFor(() => expect(parts("[data-outside-incomplete]")).toEqual(["outside", "tokenPrice"]));
    cleanup();
    mockFetch({ "/api/v1/cost/external-usage": view, "/api/v1/cost/health": report(failed("c4", "duplicates", { external_duplicates: 0, agent_duplicates: 1 }),
      failed("c5", "unpriced", { unpriced: ["y"], unpriced_agents: ["y"] })) });
    renderWithProviders(<OutsideCosts />, { route: "/costs/outside?days=1&bucket=hour", path: "/costs/outside" });
    await waitFor(() => expect(document.querySelector("[data-outside-meter]")).toBeTruthy());
    expect(document.querySelector("[data-outside-incomplete]")).toBeNull();
  });

  // Outside review round 2, major 1: /costs/outside divides by the same
  // meter — its meter-share tile, its meter card and the seat's line carry
  // the mark of a reset on a seat shown there, and not of one elsewhere.
  it("/costs/outside: a reset on the seat's meter (c3) marks the meter share, the meter card and the seat; one elsewhere does not", async () => {
    const ext = external();
    const call = modelCell(OP, "claude-opus-5-5", 2, 0.5, null, 100);
    const view = { bucket: "hour", since: H18, source: null, sources: ext.sources, models: [call], cells: [call], meter: ext.meter };
    const meterParts = () => ({
      tile: parts("[data-figure='real']"), title: parts("[data-outside-meter-title]"), seat: parts(`[data-seat='${SEAT}#0']`),
    });
    mockFetch({ "/api/v1/cost/external-usage": view, "/api/v1/cost/health": report(failed("c3", "drops", { drops: 1, last_drop_at: H19, credentials: [`${SEAT} #0`] })) });
    renderWithProviders(<OutsideCosts />, { route: "/costs/outside?days=1&bucket=hour", path: "/costs/outside" });
    await waitFor(() => expect(meterParts()).toEqual({ tile: ["multiple"], title: ["multiple"], seat: ["multiple"] }));
    expect(document.querySelector("[data-outside-incomplete]")).toBeNull();
    cleanup();
    mockFetch({ "/api/v1/cost/external-usage": view, "/api/v1/cost/health": report(failed("c3", "drops", { drops: 1, last_drop_at: H19, credentials: ["seat_b #0"] })) });
    renderWithProviders(<OutsideCosts />, { route: "/costs/outside?days=1&bucket=hour", path: "/costs/outside" });
    await waitFor(() => expect(document.querySelector(`[data-seat='${SEAT}#0']`)).toBeTruthy());
    expect(meterParts()).toEqual({ tile: [], title: [], seat: [] });
    cleanup();
    // A seat that spent with no reading (c2 names it): the same three.
    mockFetch({ "/api/v1/cost/external-usage": view, "/api/v1/cost/health": report(failed("c2", "none", { credential: `${SEAT} #0` })) });
    renderWithProviders(<OutsideCosts />, { route: "/costs/outside?days=1&bucket=hour", path: "/costs/outside" });
    await waitFor(() => expect(meterParts()).toEqual({ tile: ["multiple"], title: ["multiple"], seat: ["multiple"] }));
    cleanup();
    // Outside lane review (finding 7): a seat with no reading that is not
    // shown here (a Codex seat, another meter) leaves this page unmarked.
    mockFetch({ "/api/v1/cost/external-usage": view, "/api/v1/cost/health": report(failed("c2", "none", { credential: "seat_b #0", credentials: ["seat_b #0"] })) });
    renderWithProviders(<OutsideCosts />, { route: "/costs/outside?days=1&bucket=hour", path: "/costs/outside" });
    await waitFor(() => expect(document.querySelector(`[data-seat='${SEAT}#0']`)).toBeTruthy());
    expect(meterParts()).toEqual({ tile: [], title: [], seat: [] });
  });

  // Review round 2, finding 14: a credential's own real cost and multiples
  // carry the mark of a reset on its meter (c3 names the credentials), and
  // their outside part the mark of a stopped uploader of a source on its
  // seat — in its block on /costs and at its meter in the insights.
  const c3 = (credential: string) => failed("c3", "drops", { drops: 1, last_drop_at: H19, credentials: [credential] });
  const tileMarks = () => Object.fromEntries([...document.querySelectorAll("[data-credential] [data-tile]")].map((tile) =>
    [tile.getAttribute("aria-label"), [...tile.querySelectorAll("[data-incomplete]")].map((e) => e.getAttribute("data-incomplete"))]));
  // Each marked row of the meter: its label and its marks.
  const meterMarks = () => [...document.querySelectorAll("[data-meter] > div")].flatMap((row) => {
    const marks = [...row.querySelectorAll("[data-incomplete]")].map((e) => e.getAttribute("data-incomplete"));
    return marks.length ? [[row.firstElementChild!.textContent, marks]] : [];
  });
  const blocks = async () => {
    await waitFor(() => { if (!document.querySelector("[data-credential] [data-tile]")) throw new Error("no tiles yet"); });
    await waitFor(() => { if (!document.querySelector("[data-meter]")) throw new Error("no meter yet"); });
  };
  const real = t("costs.fig.realEstimate"), everything = t("costs.incl.everything");

  // With an answer table the seat's real cost and multiple are the table's (#204): marked there (headline), and on the
  // seat's own tiles where the full view shows them (?prices=recomputed). Its meter card keeps the span, not the figures.
  const rep = t("costs.fig.reported"), rec = t("costs.fig.recomputed");
  const FULL178 = "/costs?prices=recomputed";

  it("a reset on a seat's meter (c3): the answer's multiple, and its real cost and multiple tiles in the full view", async () => {
    serve(cells(), FULL178, report(c3(`${SEAT} #0`)));
    expect(await headlineMarks()).toEqual(["multiple"]);
    await blocks();
    await waitFor(() => expect(tileMarks()).toEqual({ [rep]: [], [rec]: [], [real]: ["multiple"], [everything]: ["multiple"] }));
    expect(meterMarks()).toEqual([]);
    // A seat no outside source names: its plain multiple.
    cleanup();
    serve(cells(null), FULL178, report(c3(`${SEAT} #0`)));
    await blocks();
    const multiple = t("costs.fig.multiple");
    await waitFor(() => expect(tileMarks()).toEqual({ [rep]: [], [rec]: [], [real]: ["multiple"], [multiple]: ["multiple"] }));
  });

  it("a reset on another seat's meter leaves this one's figures unmarked", async () => {
    serve(cells(), FULL178, report(c3("seat_b #0")));
    expect(await headlineMarks()).toEqual(["multiple"]);
    await blocks();
    expect(document.querySelectorAll("[data-credential] [data-incomplete], [data-meter] [data-incomplete]").length).toBe(0);
  });

  it("a stopped uploader of a source on the seat (c1): the outside part of its figures while included, and not under “covey only”", async () => {
    serve(cells(), FULL178, report(c1));
    await blocks();
    await waitFor(() => expect(tileMarks()).toEqual({ [rep]: ["outside"], [rec]: ["outside"], [real]: [], [everything]: ["outside"] }));
    cleanup();
    serve(cells(), `${FULL178}&only=covey`, report(c1));
    await blocks();
    expect(document.querySelectorAll("[data-credential] [data-incomplete], [data-meter] [data-incomplete]").length).toBe(0);
  });

  it("a stopped uploader of a source on no seat of the page leaves the seat's figures unmarked", async () => {
    serve(cells(), FULL178, report(failed("c1", "stale", { last_push_minutes: 80,
      stale_hosts: [{ source: "elsewhere", host: "laptop", pushed_at: H18, minutes: 80 }] })));
    expect(await headlineMarks()).toEqual(["outside"]);
    await blocks();
    expect(document.querySelectorAll("[data-credential] [data-incomplete], [data-meter] [data-incomplete]").length).toBe(0);
  });
});
