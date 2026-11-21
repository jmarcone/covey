import { describe, it, expect, beforeEach } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import type { ExternalModelUsage, ModelUsageReport, RunCost } from "../api";
import i18n from "../i18n";
import { fmtUSD } from "../format";
import { mockFetch, renderWithProviders } from "../test/render";
import CostModels, { modelRows, topModel } from "./CostModels";
import CostWork, { runUSD } from "./CostWork";

/* The cost views (Models, Work): one row per model at its lead token price, agents and outside sessions apart; the
 * runs ranked by token price, waste and idle runs said in their own tiles; both follow the page's Who. */

const t = (k: string, o: Record<string, unknown> = {}) => i18n.t(k, o) as string;
const mrow = (agent: string, model: string, runs: number, usd: number) => ({
  agent_id: agent, agent_slug: agent, agent_name: agent.toUpperCase(), model, credential_kind: "subscription", runs,
  input_tokens: 100, output_tokens: 50, thinking_tokens: 0, cache_read_tokens: 900, cache_write_5m_tokens: 0, cache_write_1h_tokens: 0,
  web_search_requests: 0, engine_usd: usd, list_usd: usd,
});
const MODELS: ModelUsageReport = { rows: [mrow("a1", "opus", 10, 8), mrow("a2", "opus", 5, 2), mrow("a2", "sonnet", 20, 4)], runs_without_breakdown: 0, usd_without_breakdown: 0 };
const cell = (model: string, calls: number, list: number | null, reported: number | null, unpriced = 0) => ({
  period: "2026-11-20T00:00:00Z", source: "covey operator", origin: "main", model, calls, list_usd: list, measured: {}, type_usd: {},
  reported_usd: reported, reported_calls: reported != null ? calls : 0, tokens: { input: 10, output: 5, cache_read: 85 },
  unpriced_tokens: unpriced, unpriced_calls: unpriced > 0 ? 1 : 0,
});
const OUTSIDE = { bucket: "day", since: "2026-11-14T00:00:00Z", source: null, sources: [], models: [],
  cells: [cell("opus", 30, 6, null), cell("fable", 2, null, null, 1000)], meter: undefined } as unknown as ExternalModelUsage;

const run = (id: string, agent: string, state: string, engine: number | null, actions = 3, measured: number | null = engine): RunCost => ({
  task_id: id, agent_id: agent, slug: agent, title: `task ${id}`, state, origin: "manual", total_usd: 0, measured_usd: measured, engine_usd: engine,
  entries: 1, actions, started_at: "2026-11-20T10:00:00Z", ended_at: "2026-11-20T11:00:00Z",
  input_tokens: 0, output_tokens: 0, cache_read_tokens: 100, cache_creation_tokens: 10,
} as unknown as RunCost);
const RUNS = [run("r1", "a1", "done", 5), run("r2", "a2", "failed", 9), run("r3", "a1", "cancelled", 1, 0), run("r4", "a2", "done", null, 2, 3)];

function serve(route: string, page: "models" | "work", runs: unknown = RUNS) {
  // The agent's own runs first: the org route would match its path's end too.
  mockFetch({ "/api/v1/agents/a1/cost/runs": [RUNS[0], RUNS[2], run("r9", "a1", "done", 0.5)],
    "/api/v1/cost/models": MODELS, "/api/v1/cost/external-usage": OUTSIDE, ...(runs ? { "/api/v1/cost/runs": runs } : {}),
    "/api/v1/cost/indicators": { indicators: [], failed: 0, total_usd: 0, quality: {} } });
  return renderWithProviders(page === "models" ? <CostModels /> : <CostWork />, { route, path: page === "models" ? "/costs/models" : "/costs/work" });
}

describe("the Models view", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("groups by model, agents and outside sessions apart, at the lead price; an unpriced model is never $0", () => {
    const rows = modelRows(MODELS, OUTSIDE, new Set());
    expect(rows.map((r) => `${r.who}:${r.model}`)).toEqual(["agents:opus", "outside:opus", "agents:sonnet", "outside:fable"]);
    const opus = rows[0];
    expect(opus.usd).toBe(10);
    expect(opus.runs).toBe(15);
    expect(opus.agents.map((a) => a.label)).toEqual(["A1", "A2"]);
    // Outside: no reported cost, so covey's list price, marked recomputed.
    expect(rows[1].usd).toBe(6);
    expect(rows[1].recomputed).toBe(true);
    expect(rows[3].usd).toBe(0);
    expect(rows[3].unpricedTokens).toBe(1000);
  });

  it("a selection of agents keeps their rows only, and no outside sessions (they are no agents)", () => {
    const rows = modelRows(MODELS, OUTSIDE, new Set(["a2"]));
    expect(rows.map((r) => `${r.who}:${r.model}:${r.usd}`)).toEqual(["agents:sonnet:4", "agents:opus:2"]);
  });

  it("an outside cell with a price for only some of its calls is priced at the list over all of them, marked", () => {
    const part = { ...OUTSIDE, cells: [{ ...cell("opus", 30, 6, 1), reported_calls: 10 }, cell("haiku", 4, 0.4, 0.3)] } as unknown as ExternalModelUsage;
    const rows = modelRows(undefined, part, new Set());
    expect(rows.map((r) => [r.model, r.usd, r.recomputed])).toEqual([["opus", 6, true], ["haiku", 0.3, false]]);
  });

  it("an agent on two credential kinds is one line; the top model adds agents and outside sessions", () => {
    const two: ModelUsageReport = { ...MODELS, rows: [...(MODELS.rows ?? []), { ...mrow("a1", "opus", 2, 1), credential_kind: "api_key" }] };
    const rows = modelRows(two, OUTSIDE, new Set());
    expect(rows[0].agents.map((a) => [a.label, a.runs, a.usd])).toEqual([["A1", 12, 9], ["A2", 5, 2]]);
    // opus: $11 agents + $6 outside.
    expect(topModel(rows)).toEqual({ model: "opus", usd: 17 });
    expect(topModel([{ ...rows[0], model: "x", who: "agents", usd: 5 }, { ...rows[0], model: "y", who: "agents", usd: 3 }, { ...rows[0], model: "y", who: "outside", usd: 3 }])!.model).toBe("y");
  });

  // opus: $10 agents + $6 outside of $20 in all.
  it("the page leads with the model that took most of the money and says per run; unpriced reads as tokens", async () => {
    serve("/costs/models?tz=utc", "models");
    await waitFor(() => expect(document.querySelector("[data-models-tiles]")).not.toBeNull());
    expect(document.querySelector("[data-models-tiles]")!.textContent).toContain(t("costs.models.share", { pct: "80", usd: fmtUSD(16) }));
    const fable = document.querySelector("[data-model-row='outside:fable']")!;
    expect(fable.textContent).toContain(t("costs.incl.noListTokens", { n: "1,000" }));
    expect(fable.textContent).not.toContain("$0.00");
    // Per run only for agents: opus $10 over 15 runs.
    expect(document.querySelector("[data-model-row='agents:opus']")!.textContent).toContain(fmtUSD(10 / 15));
  });
});

describe("the Work view", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  it("ranks the runs by their lead price, Claude Code's first, else covey's list price", async () => {
    expect(runUSD(RUNS[3])).toBe(3);
    serve("/costs/work?tz=utc", "work");
    await waitFor(() => expect(document.querySelectorAll("[data-work-row]").length).toBe(4));
    expect([...document.querySelectorAll("[data-work-row]")].map((r) => r.getAttribute("data-work-row"))).toEqual(["r2", "r1", "r4", "r3"]);
    expect(document.querySelector("[data-work-top]")!.textContent).toContain(fmtUSD(9));
  });

  it("says the money of runs without a result, and the runs that changed nothing", async () => {
    serve("/costs/work?tz=utc", "work");
    const waste = await waitFor(() => { const w = document.querySelector("[data-work-waste]"); if (!w) throw new Error("no tiles"); return w; });
    // failed $9 + cancelled $1 of $18 in all.
    expect(waste.textContent).toContain(fmtUSD(10));
    expect(waste.textContent).toContain(t("costs.work.wasteSub", { count: 2, pct: 56 }));
    expect(document.querySelector("[data-work-idle]")!.textContent).toContain("1");
    expect(document.querySelector("[data-work-row='r2'] [data-work-outcome]")!.textContent).toBe(t("costs.work.state.failed"));
  });

  it("follows the page's Who: the agent's own runs, fetched for it (not a cut of the org's top runs)", async () => {
    serve("/costs/work?tz=utc&agents=a1", "work");
    await waitFor(() => expect(document.querySelectorAll("[data-work-row]").length).toBe(3));
    expect(document.querySelector("[data-work-row='r9']")).not.toBeNull();
    expect(screen.getByText(/^a1$/, { selector: "b" })).toBeTruthy();
  });

  it("ranks on the measured basis, as the server and the cost home do", () => {
    expect(runUSD(run("x", "a1", "done", 0, 1, 4))).toBe(4);
    expect(runUSD(run("y", "a1", "done", 2, 1, null))).toBe(2);
  });

  it("says so when the runs cannot be loaded, instead of a blank page", async () => {
    serve("/costs/work?tz=utc", "work", null);
    await waitFor(() => expect(document.querySelector("[data-work-error]")).not.toBeNull());
    expect(document.querySelector("[data-work-tiles]")).toBeNull();
  });
});
