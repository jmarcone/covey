import { describe, it, expect, beforeEach } from "vitest";
import { waitFor } from "@testing-library/react";
import type { BilledWindow, CostCells, ExternalMeterStep, ExternalMeterSums, ExternalMeterTotal, ExternalModelUsage, ExternalUsage, ModelUsageReport, RunCost } from "../api";
import i18n from "../i18n";
import { fmtUSD } from "../format";
import { mockFetch, renderWithProviders } from "../test/render";
import CostHome from "./CostHome";
import CostModels from "./CostModels";
import CostWork from "./CostWork";
import Costs from "./Costs";
import { basisSupport } from "./costBasisSwitch";
import OutsideCosts, { sourceFigure } from "./OutsideCosts";
import { answerTable, billedAbsentReason, billedKeys, partsMoney, type AnswerRow } from "./costAnswerModel";
import { headline, subscriptionDent } from "./costFigures";
import { runUSD } from "./costView";

/* The Basis switch on the cost home, Models, Work and Outside (cost home PR 9): the parts table's lead money column
 * follows the basis (capacity: the server's figure per row, ≈, adding up to the total; billed: no figure per party,
 * and the API keys' bill on its own line), the blocks that keep the token price say so once, and no absent figure is
 * ever $0.00. Assertions use the data-* attributes: the new strings are not in the catalogue until merged. */

const SEAT = "claude_code_oauth_token";
const PP = 200 * 12 / 52 / 100;
const H18 = "2026-11-17T18:00:00Z", H19 = "2026-11-17T19:00:00Z";
const OP = "covey operator", SY = "projects-sylius", WORK = "operator: work";

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
    ...zero, secret_key: SEAT, secret_slot: 0, meter_source: "usage_cmd", sources: [OP, SY].sort(),
    hours_with_reading: 2, points_per_list_usd: null, plan_rates: [],
    real_usd: 6 * PP, external_real_usd: 2 * PP, covey_real_usd: 4 * PP, share: 1 / 3,
    split_external_list_usd: 5, split_covey_list_usd: 10, points: 6, split_points: 6, split_external_points: 2, split_covey_points: 4,
    split_runs: 1, split_slack_points: 2, split_slack_usd: 2 * PP, split_external_slack_usd: 2 * PP / 3, split_covey_slack_usd: 4 * PP / 3,
    point_rate: PP, step_count: 2, split_steps: 2, unmetered_list_usd: 0.5, unmetered_covey_list_usd: 0.25,
    source_parts: [{ source: OP, list_usd: 3, real_usd: 1.2 * PP }, { source: SY, list_usd: 2, real_usd: 0.8 * PP }],
    agents: [{ agent_id: "a1", split_list_usd: 10, real_usd: 4 * PP, unsplit_list_usd: 0 }],
  };
}

function external(): ExternalUsage {
  const total = seatTotal();
  const { secret_key: _k, secret_slot: _s, meter_source: _m, sources: _src, hours_with_reading: _h, points_per_list_usd: _p,
    plan_rates: _r, agents: _a, ...sums } = total;
  const src = (source: string, link: boolean, calls: number, list: number | null, reported: number | null) => ({
    source, meter_credential: link ? { secret_key: SEAT, secret_slot: 0 } : null, calls, list_usd: list, reported_usd: reported,
    unpriced_tokens: 0, unpriced_calls: 0, unpriced_models: [], first_at: H18, last_at: H19,
    hosts: [{ host: "laptop", reported_through: "2026-11-17T20:00:00Z", pushed_at: "2026-11-17T20:01:00Z" }],
  });
  const cell = (period: string, source: string, calls: number, list: number, reported: number | null) => ({
    period, source, origin: "main", calls, list_usd: list, measured: { output: list }, reported_usd: reported,
    reported_calls: reported != null ? calls : 0, tokens: { input: 1000, output: 10_000, cache_read: 0, cache_write_5m: 0, cache_write_1h: 0 },
    unpriced_tokens: 0, unpriced_calls: 0,
  });
  return {
    sources: [src(OP, true, 10, 3, 2.5), src(WORK, false, 4, 1.5, 1), src(SY, true, 5, 2, null)],
    cells: [cell(H18, OP, 4, 1, 1), cell(H19, OP, 6, 2, 1.5), cell(H19, SY, 5, 2, null), cell(H18, WORK, 4, 1.5, 1)],
    meter: { ...sums, steps: steps(), hours: [], credentials: [total] },
  } as unknown as ExternalUsage;
}

function cells(): CostCells {
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
    external: external(),
  } as unknown as CostCells;
}

const SYNC = { configured: true, last_attempt_at: null, last_success_at: null, last_daily_at: null, data_refreshed_at: null, last_status: "", last_error: "" };
const total = (key: string, has: boolean, usd: string) => ({ secret_key: key, secret_slot: 0, has_billed: has, billed_usd: usd, billed_list_usd: usd,
  reported_usd: 0, list_usd: 0 });
const WINDOW: BilledWindow = { start: H18, end: "2026-11-17T20:00:00Z", width: "1h", compared_until: "2026-11-17T20:00:00Z",
  totals: [total("k1", true, "1.25"), total("k2", true, "2.50"), total("k3", false, "9.00")] };

const run = (id: string, measured: number | null, engine: number | null): RunCost => ({
  task_id: id, agent_id: "a1", slug: "a1", title: `task ${id}`, state: "done", origin: "manual", total_usd: 0, measured_usd: measured,
  engine_usd: engine, entries: 1, actions: 2, started_at: H18, ended_at: H19, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_creation_tokens: 0,
} as unknown as RunCost);

function serveHome(route: string, billed: unknown = { bucket: "hour", points: [], window: WINDOW, sync: SYNC }) {
  mockFetch({
    "/api/v1/cost/cells": cells(),
    "/api/v1/cost/billed": billed,
    "/api/v1/cost/runs": [run("r1", null, 4)],
    "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
  });
  return renderWithProviders(<CostHome />, { route, path: "/costs" });
}

const tableOf = () => {
  const data = cells();
  return answerTable(headline(data)!, subscriptionDent(data.external)!, data.external!, data.meter ?? [], new Set());
};
const ready = () => waitFor(() => expect(document.querySelector("[data-answer-total]")).not.toBeNull());
const lockOf = (sel: string) => document.querySelector(`${sel} [data-basis-lock]`)?.getAttribute("data-basis-lock") ?? null;
const row = (over: Partial<AnswerRow>): AnswerRow => ({ key: "x", kind: "project", source: "x", list: 3, recomputed: false, unpricedTokens: 0,
  before: false, real: 1, notSplit: false, partlySplit: false, pending: false, beforeMeter: null, folded: [], ...over });

describe("the cost home under a basis", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("token (the default) is the table as before: token price leads, no chip, no bill line", async () => {
    serveHome("/costs?days=1&tz=utc");
    await ready();
    expect([...document.querySelectorAll("[data-answer-col]")].map((x) => x.getAttribute("data-answer-col"))).toEqual(["list", "real"]);
    expect(document.querySelector("[data-basis-lock]")).toBeNull();
    expect(document.querySelector("[data-home-billed]")).toBeNull();
  });

  it("capacity: the capacity column leads, each row is the server's figure ≈, and the rows add up to the real total", async () => {
    const a = tableOf();
    const figures = a.rows.map((r) => partsMoney(r, "capacity")).filter((m): m is { usd: number; estimate: boolean } => m != null && "usd" in m);
    expect(Math.abs(figures.reduce((s, m) => s + m.usd, 0) - a.real!)).toBeLessThan(0.01);
    serveHome("/costs?days=1&tz=utc&basis=capacity");
    await ready();
    expect([...document.querySelectorAll("[data-answer-col]")].map((x) => x.getAttribute("data-answer-col"))).toEqual(["real", "list"]);
    expect(document.querySelector("[data-answer-col='real'] [data-basis-tag='capacity']")).not.toBeNull();
    const shown = [...document.querySelectorAll("[data-answer-row] [data-answer-capacity]")].map((x) => Number(x.getAttribute("data-answer-capacity")));
    expect(shown.length).toBeGreaterThan(1);
    expect(Math.abs(shown.reduce((s, v) => s + v, 0) - a.real!)).toBeLessThan(0.01);
    for (const x of document.querySelectorAll("[data-answer-row] [data-answer-capacity]")) expect(x.textContent!.startsWith("≈ ")).toBe(true);
    expect(document.querySelector("[data-answer-total] [data-answer-real]")!.textContent).toBe(`≈ ${fmtUSD(a.real!)}`);
    // The hero is token ÷ capacity: honoured under capacity, no chip; the details door leads with the capacity; the run
    // door keeps token price.
    expect(document.querySelector("[data-home-hero-lock]")).toBeNull();
    expect(document.querySelector("[data-home-door='details'] .cost-home-door-value")!.textContent).toBe(`≈ ${fmtUSD(a.real!)}`);
    await waitFor(() => expect(lockOf("[data-home-door-lock='run']")).toBe("notPerRun"));
  });

  it("capacity: a pending, before-the-meter or unsplit row has no figure, never ≈ $0.00", () => {
    expect(partsMoney(row({ real: null, pending: true }), "capacity")).toEqual({ none: "pending" });
    expect(partsMoney(row({ real: null, beforeMeter: H18 }), "capacity")).toEqual({ none: "beforeMeter" });
    expect(partsMoney(row({ real: null, notSplit: true }), "capacity")).toEqual({ none: "notSplit" });
    expect(partsMoney(row({ kind: "unsplit", list: null, real: 2 }), "capacity")).toEqual({ usd: 2, estimate: true });
    expect(partsMoney(row({ kind: "unsplit", list: null, real: 2 }), "token")).toBeNull();
  });

  it("billed: no bill per party row, and the mapped API keys' bill on its own line (decimal strings summed)", async () => {
    serveHome("/costs?days=1&tz=utc&basis=billed");
    await ready();
    expect(document.querySelector("[data-answer-col='billed'] [data-basis-tag='billed']")).not.toBeNull();
    for (const key of ["covey", "operator", `project:${SY}`]) {
      expect(document.querySelector(`[data-answer-row="${key}"] [data-answer-billed] [data-no-figure]`)!.getAttribute("data-no-figure")).toBe("notAtThisLevel");
    }
    expect(lockOf("[data-home-hero-lock]")).toBe("answerIsTokenOverCapacity");
    await waitFor(() => expect(document.querySelector("[data-home-billed]")!.getAttribute("data-home-billed")).toBe("3.75"));
    expect(document.querySelector("[data-home-billed-usd]")!.textContent).toBe(fmtUSD(3.75));
    // The details door keeps the work at token price under billed, with the chip (bills are per API key).
    expect(document.querySelector("[data-home-door='details'] .cost-home-door-value")!.textContent).toBe(fmtUSD(tableOf().list));
    expect(lockOf("[data-home-door-lock='details']")).toBe("billedPerKey");
    await waitFor(() => expect(lockOf("[data-home-door-lock='run']")).toBe("noCredentialOnRun"));
    expect(document.body.textContent).not.toContain("$0.00");
  });

  it("billed: no mapped key with a bill is a reason, never $0", async () => {
    expect(billedKeys(null)).toBeNull();
    expect(billedKeys({ ...WINDOW, totals: [] })).toBeNull();
    expect(billedKeys({ ...WINDOW, totals: [total("k3", false, "9.00")] })).toBeNull();
    expect(billedKeys(WINDOW)).toEqual({ usd: 3.75, until: WINDOW.compared_until });
    serveHome("/costs?days=1&tz=utc&basis=billed", { bucket: "hour", points: [], window: { ...WINDOW, totals: [] }, sync: SYNC });
    await waitFor(() => expect(document.querySelector("[data-home-billed]")?.getAttribute("data-home-billed")).toBe("none"));
    expect(document.querySelector("[data-home-billed] [data-no-figure]")!.getAttribute("data-no-figure")).toBe("noBilledKeys");
  });

  it("billed: keys mapped but no bill landed is notLanded; a bill that could not be fetched is loadError (H)", async () => {
    expect(billedAbsentReason(false, { ...WINDOW, totals: [total("k3", false, "0")] })).toBe("notLanded");
    expect(billedAbsentReason(false, { ...WINDOW, totals: null })).toBe("noBilledKeys");
    expect(billedAbsentReason(false, null)).toBe("noBilledKeys");
    expect(billedAbsentReason(true, WINDOW)).toBe("loadError");
    const first = serveHome("/costs?days=1&tz=utc&basis=billed", { bucket: "hour", points: [], window: { ...WINDOW, totals: [total("k3", false, "0")] }, sync: SYNC });
    await waitFor(() => expect(document.querySelector("[data-home-billed] [data-no-figure]")?.getAttribute("data-no-figure")).toBe("notLanded"));
    first.unmount();
    // No /cost/billed route: the request fails (404), and the line says so.
    mockFetch({
      "/api/v1/cost/cells": cells(),
      "/api/v1/cost/runs": [run("r1", null, 4)],
      "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
    });
    renderWithProviders(<CostHome />, { route: "/costs?days=1&tz=utc&basis=billed", path: "/costs" });
    await waitFor(() => expect(document.querySelector("[data-home-billed] [data-no-figure]")?.getAttribute("data-no-figure")).toBe("loadError"));
    expect(document.querySelector("[data-home-billed]")!.textContent).not.toContain("$0.00");
  });

  it("the top-run door is the run's token price as Work shows it (runUSD), not measured_usd alone", async () => {
    expect(runUSD(run("r1", null, 4))).toBe(4);
    serveHome("/costs?days=1&tz=utc");
    await waitFor(() => expect(document.querySelector("[data-home-door='run'] .cost-home-door-value")!.textContent).toBe(fmtUSD(4)));
  });
});

const mrow = (agent: string, model: string, usd: number) => ({
  agent_id: agent, agent_slug: agent, agent_name: agent, model, credential_kind: "subscription", runs: 3,
  input_tokens: 100, output_tokens: 50, thinking_tokens: 0, cache_read_tokens: 900, cache_write_5m_tokens: 0, cache_write_1h_tokens: 0,
  web_search_requests: 0, engine_usd: usd, list_usd: usd,
});
const NO_OUTSIDE = { bucket: "day", since: H18, source: null, sources: [], models: [], cells: [] } as unknown as ExternalModelUsage;

function serveModels(route: string, rows = [mrow("a1", "opus", 8)]) {
  mockFetch({ "/api/v1/cost/models": { rows, runs_without_breakdown: 0, usd_without_breakdown: 0 } as ModelUsageReport, "/api/v1/cost/external-usage": NO_OUTSIDE });
  return renderWithProviders(<CostModels />, { route, path: "/costs/models" });
}

describe("Models and Work under a basis", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("Models: the chip says why the token price stays (capacity: not per model; billed: per key); the figures are unchanged", async () => {
    serveModels("/costs/models?tz=utc&basis=capacity");
    await waitFor(() => expect(lockOf("[data-models-lock]")).toBe("notPerModel"));
    expect(document.querySelector("[data-model-row='agents:opus']")!.textContent).toContain(fmtUSD(8));
    expect(document.querySelectorAll("[data-basis-lock]").length).toBe(1);
  });

  it("Models: billed gives the per-key chip; token none", async () => {
    serveModels("/costs/models?tz=utc&basis=billed");
    await waitFor(() => expect(lockOf("[data-models-lock]")).toBe("billedPerKey"));
  });

  it("Models: a zero total is no data, never $0.00 in the top-model tile", async () => {
    serveModels("/costs/models?tz=utc", [mrow("a1", "opus", 0)]);
    await waitFor(() => expect(document.querySelector("[data-models-top-share]")).not.toBeNull());
    expect(document.querySelector("[data-models-top-share] [data-no-figure]")!.getAttribute("data-no-figure")).toBe("noData");
    expect(document.querySelector("[data-models-top-share]")!.textContent).not.toContain("$0.00");
    expect(document.querySelector("[data-basis-lock]")).toBeNull();
  });

  it("Work: the chip says a run carries no capacity and no credential; the top run is unchanged", async () => {
    for (const [basis, reason] of [["capacity", "notPerRun"], ["billed", "noCredentialOnRun"]] as const) {
      mockFetch({ "/api/v1/cost/runs": [run("r1", 9, 7), run("r2", 2, 2)],
        "/api/v1/cost/indicators": { indicators: [], failed: 0, total_usd: 0, quality: {} } });
      const { unmount } = renderWithProviders(<CostWork />, { route: `/costs/work?tz=utc&basis=${basis}`, path: "/costs/work" });
      await waitFor(() => expect(lockOf("[data-work-lock]")).toBe(reason));
      expect(document.querySelector("[data-work-top]")!.textContent).toContain(fmtUSD(9));
      unmount();
    }
  });
});

function serveOutside(route: string) {
  const ext = external();
  mockFetch({ "/api/v1/cost/external-usage": { bucket: "hour", since: H18, source: null, sources: ext.sources, models: [],
    cells: ext.cells.map((c) => ({ ...c, model: "claude-opus-5-5", type_usd: {} })), meter: ext.meter } });
  return renderWithProviders(<OutsideCosts />, { route, path: "/costs/outside" });
}

describe("Outside under a basis", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("capacity: each source's own part of the meter ≈; a per-use source has none; per model the chip", async () => {
    serveOutside("/costs/outside?days=1&bucket=hour&basis=capacity");
    await waitFor(() => expect(document.querySelector(`[data-source-figure="${OP}"]`)).not.toBeNull());
    expect(document.querySelector(`[data-source-figure="${OP}"]`)!.textContent).toBe(`≈ ${fmtUSD(1.2 * PP)}`);
    expect(document.querySelector(`[data-source-figure="${SY}"]`)!.textContent).toBe(`≈ ${fmtUSD(0.8 * PP)}`);
    expect(document.querySelector(`[data-source-figure="${WORK}"] [data-no-figure]`)!.getAttribute("data-no-figure")).toBe("usageBasis");
    await waitFor(() => expect(lockOf("[data-outside-model-lock]")).toBe("notPerModel"));
  });

  it("billed: no source has a bill (not linked to an API key), and per model none (billed by usage)", async () => {
    serveOutside("/costs/outside?days=1&bucket=hour&basis=billed");
    await waitFor(() => expect(document.querySelector(`[data-source-figure="${OP}"]`)).not.toBeNull());
    for (const s of [OP, SY, WORK]) {
      expect(document.querySelector(`[data-source-figure="${s}"] [data-no-figure]`)!.getAttribute("data-no-figure")).toBe("notAtThisLevel");
    }
    await waitFor(() => expect(document.querySelector("[data-outside-model-none]")!.getAttribute("data-outside-model-none")).toBe("usageBasis"));
  });

  it("token: the list price per source, as before; a source with no split part under capacity is not split, never ≈ $0.00", async () => {
    const real = new Map([[OP, 0]]);
    expect(sourceFigure("token", { source: OP, meter_credential: {}, list_usd: 3 }, real)).toEqual({ usd: 3, estimate: false });
    expect(sourceFigure("capacity", { source: OP, meter_credential: {}, list_usd: 3 }, real)).toEqual({ none: "notSplit" });
    serveOutside("/costs/outside?days=1&bucket=hour");
    await waitFor(() => expect(document.querySelector(`[data-source-figure="${OP}"]`)!.textContent).toBe(fmtUSD(3)));
    expect(document.querySelector("[data-basis-lock]")).toBeNull();
  });
});

// --- Review fixes (A–K) ------------------------------------------------------------------------------------------------

describe("the contract after review", () => {
  it("A: the home hero is honoured under capacity (it is token ÷ capacity), locked only under billed", () => {
    expect(basisSupport("home", "hero", "token")).toEqual({ state: "honoured" });
    expect(basisSupport("home", "hero", "capacity")).toEqual({ state: "honoured" });
    expect(basisSupport("home", "hero", "billed")).toEqual({ state: "locked", reason: "answerIsTokenOverCapacity" });
  });

  it("C/D/E: the details door, the outside sections and the meter card name what they show", () => {
    expect(basisSupport("home", "doorDetails", "capacity")).toEqual({ state: "honoured" });
    expect(basisSupport("home", "doorDetails", "billed")).toEqual({ state: "locked", reason: "billedPerKey" });
    expect(basisSupport("details", "outsideOverview", "token")).toEqual({ state: "honoured" });
    expect(basisSupport("details", "outsideOverview", "capacity")).toEqual({ state: "locked", reason: "outsideSeeSources" });
    expect(basisSupport("details", "outsideOverview", "billed")).toEqual({ state: "locked", reason: "outsideNoBill" });
    expect(basisSupport("outside", "overview", "capacity")).toEqual({ state: "locked", reason: "outsideTokenOnly" });
    expect(basisSupport("outside", "overview", "billed")).toEqual({ state: "locked", reason: "outsideNoBill" });
    expect(basisSupport("details", "insightsMeter", "billed")).toEqual({ state: "locked", reason: "meterPerSeat" });
  });

  it("D: the lock chip claims no token price of its own; the reason says what is shown", async () => {
    await i18n.changeLanguage("en");
    expect(i18n.t("costs.basisSwitch.lockChip", { basis: "X", reason: "r" })).toBe("X not applied here: r");
  });
});

/** The home's data with no plan price on the seat: the meter moved, but none of it is priced (K). */
function cellsNoPrice(): CostCells {
  const c = cells();
  const m = c.external!.meter;
  m.unpriced_steps = m.step_count; m.unpriced_points = m.points;
  for (const x of m.credentials) { x.unpriced_steps = x.step_count; x.unpriced_points = x.points; }
  return c;
}

describe("capacity with no plan price (K)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("the parts column and its total say no plan price, not no data", async () => {
    expect(partsMoney(row({ real: null }), "capacity", { noPlanPrice: true })).toEqual({ none: "noPlanPrice" });
    expect(partsMoney(row({ real: null }), "capacity", { noPlanPrice: true, usage: true })).toEqual({ none: "usageBasis" });
    expect(partsMoney(row({ real: null }), "capacity")).toEqual({ none: "noData" });
    mockFetch({
      "/api/v1/cost/cells": cellsNoPrice(),
      "/api/v1/cost/runs": [],
      "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
    });
    renderWithProviders(<CostHome />, { route: "/costs?days=1&tz=utc&basis=capacity", path: "/costs" });
    await ready();
    const rows = [...document.querySelectorAll("[data-answer-row] [data-answer-real] [data-no-figure]")].map((x) => x.getAttribute("data-no-figure"));
    expect(rows.length).toBeGreaterThan(0);
    expect(new Set(rows)).toEqual(new Set(["noPlanPrice"]));
    expect(document.querySelector("[data-answer-total] [data-answer-real] [data-no-figure]")!.getAttribute("data-no-figure")).toBe("noPlanPrice");
    expect(document.querySelector("[data-home-door='details'] [data-no-figure]")!.getAttribute("data-no-figure")).toBe("noPlanPrice");
    expect(document.querySelector("[data-answer]")!.textContent).not.toContain("$0.00");
  });
});

function serveDetails(route: string) {
  mockFetch({
    "/api/v1/cost/cells": cells(),
    "/api/v1/cost/billed": { bucket: "hour", points: [], window: WINDOW, sync: SYNC },
    "/api/v1/cost/runs": [],
    "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
    "/api/v1/cost/usage-readings": [],
  });
  return renderWithProviders(<Costs />, { route, path: "/costs/details" });
}
const colsOf = () => [...document.querySelectorAll("[data-answer-col]")].map((x) => x.getAttribute("data-answer-col"));

describe("Details: the answer table and the outside section under a basis (B, C)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("capacity: the capacity column leads as on the home; the hero has no chip; the outside section has its chip", async () => {
    serveDetails("/costs/details?days=1&tz=utc&basis=capacity");
    await ready();
    expect(colsOf()).toEqual(["real", "list"]);
    expect(document.querySelector("[data-answer-col='real'] [data-basis-tag='capacity']")).not.toBeNull();
    expect(document.querySelector("[data-answer-hero]")).not.toBeNull();
    expect(document.querySelector("[data-answer-hero] [data-basis-lock]")).toBeNull();
    await waitFor(() => expect(lockOf("[data-outside-overview-lock]")).toBe("outsideSeeSources"));
  });

  it("billed: the bill column leads with no figure per party; the hero chip; the outside section has no bill", async () => {
    serveDetails("/costs/details?days=1&tz=utc&basis=billed");
    await ready();
    expect(colsOf()).toEqual(["billed", "list", "real"]);
    expect(document.querySelector("[data-answer-row='covey'] [data-answer-billed] [data-no-figure]")!.getAttribute("data-no-figure")).toBe("notAtThisLevel");
    expect(lockOf("[data-answer-hero]")).toBe("answerIsTokenOverCapacity");
    await waitFor(() => expect(lockOf("[data-outside-overview-lock]")).toBe("outsideNoBill"));
  });

  it("token: as before, no chip anywhere", async () => {
    serveDetails("/costs/details?days=1&tz=utc");
    await ready();
    expect(colsOf()).toEqual(["list", "real"]);
    await waitFor(() => expect(document.querySelector("[data-outside]")).not.toBeNull());
    expect(document.querySelector("[data-basis-lock]")).toBeNull();
  });
});

describe("the switch itself (F, G)", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("F: /costs/outside says a cash link awaits sign-off, with the switch (once)", async () => {
    serveOutside("/costs/outside?days=1&bucket=hour&basis=cash");
    await waitFor(() => expect(document.querySelector("[data-basis-pruned]")).not.toBeNull());
    expect(document.querySelectorAll("[data-basis-pruned]").length).toBe(1);
    expect(document.querySelector("[data-basis-pruned]")!.getAttribute("data-basis-pruned")).toBe("cash");
    expect(document.querySelector("[data-basis-control]")!.getAttribute("data-basis-control")).toBe("token");
  });

  it("F: an unknown basis on the home is said once, in the bar", async () => {
    serveHome("/costs?days=1&tz=utc&basis=gold");
    await ready();
    expect([...document.querySelectorAll("[data-basis-pruned]")].map((x) => x.getAttribute("data-basis-pruned"))).toEqual(["unknown"]);
  });

  it("G: below 1600px the select is described by visible lines (the basis, and why cash is off); the group is not", async () => {
    serveOutside("/costs/outside?days=1&bucket=hour&basis=capacity");
    const select = await waitFor(() => {
      const s = document.querySelector<HTMLSelectElement>(".cost-basis-select");
      if (!s) throw new Error("no select yet");
      return s;
    });
    const ids = select.getAttribute("aria-describedby")!.split(" ");
    expect(ids).toHaveLength(2);
    const lines = ids.map((id) => document.getElementById(id)!);
    expect(lines.map((l) => l.getAttribute("data-basis-desc"))).toEqual(["capacity", "cash"]);
    for (const l of lines) expect(l.classList.contains("sr-only")).toBe(false);
    expect(lines[0].textContent).toBe(i18n.t("costs.basisSwitch.desc.capacity"));
    expect(lines[1].textContent).toBe(i18n.t("costs.basisSwitch.desc.cash"));
    expect(document.querySelector("[data-basis-control]")!.hasAttribute("aria-describedby")).toBe(false);
  });
});

describe("Outside capacity chips: why a source has no figure (J)", () => {
  const meter = () => external().meter;
  const src = (over: Partial<{ first_at: string | null; last_at: string | null }>) =>
    ({ source: OP, meter_credential: { secret_key: SEAT, secret_slot: 0 }, list_usd: 3, first_at: H18, last_at: H19, ...over });
  const none = new Map<string, number>();

  it("pending: all its calls after the seat's last reading", () => {
    expect(sourceFigure("capacity", src({ first_at: "2026-11-17T20:00:00Z", last_at: "2026-11-17T21:00:00Z" }), none, meter())).toEqual({ none: "pending" });
  });

  it("beforeMeter: all its calls before the seat's first reading", () => {
    expect(sourceFigure("capacity", src({ first_at: "2026-11-17T16:00:00Z", last_at: "2026-11-17T17:00:00Z" }), none, meter())).toEqual({ none: "beforeMeter" });
  });

  it("noPlanPrice: the seat moved at no plan price (checked first)", () => {
    const m = meter();
    m.credentials = m.credentials.map((c) => ({ ...c, unpriced_steps: c.step_count }));
    expect(sourceFigure("capacity", src({ first_at: "2026-11-17T20:00:00Z" }), none, m)).toEqual({ none: "noPlanPrice" });
  });

  it("notSplit: calls within the readings but no part; and without the meter (cannot tell)", () => {
    expect(sourceFigure("capacity", src({}), none, meter())).toEqual({ none: "notSplit" });
    expect(sourceFigure("capacity", src({ first_at: "2026-11-17T20:00:00Z" }), none)).toEqual({ none: "notSplit" });
    const m = meter(); m.steps = [];
    expect(sourceFigure("capacity", src({ first_at: "2026-11-17T20:00:00Z" }), none, m)).toEqual({ none: "notSplit" });
  });

  it("on the page: a source with calls only after the last reading reads pending, never ≈ $0.00", async () => {
    await i18n.changeLanguage("en");
    const ext = external();
    ext.sources = ext.sources.map((s) => (s.source === SY ? { ...s, first_at: "2026-11-17T20:00:00Z", last_at: "2026-11-17T20:30:00Z" } : s));
    ext.meter.credentials = ext.meter.credentials.map((c) => ({ ...c, source_parts: (c.source_parts ?? []).filter((p) => p.source !== SY) }));
    ext.meter.source_parts = (ext.meter.source_parts ?? []).filter((p) => p.source !== SY);
    ext.meter.steps = ext.meter.steps.map((x) => ({ ...x, source_parts: (x.source_parts ?? []).filter((p) => p.source !== SY) }));
    mockFetch({ "/api/v1/cost/external-usage": { bucket: "hour", since: H18, source: null, sources: ext.sources, models: [],
      cells: ext.cells.map((c) => ({ ...c, model: "claude-opus-5-5", type_usd: {} })), meter: ext.meter } });
    renderWithProviders(<OutsideCosts />, { route: "/costs/outside?days=1&bucket=hour&basis=capacity", path: "/costs/outside" });
    await waitFor(() => expect(document.querySelector(`[data-source-figure="${SY}"] [data-no-figure]`)?.getAttribute("data-no-figure")).toBe("pending"));
    expect(document.querySelector(`[data-source-figure="${SY}"]`)!.textContent).not.toContain("$0.00");
  });
});
