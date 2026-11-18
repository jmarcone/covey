import { describe, it, expect, beforeEach } from "vitest";
import { fireEvent, screen, within } from "@testing-library/react";
import Costs from "./Costs";
import OutsideCosts from "./OutsideCosts";
import type { CostCells, ExternalMeterSums, ExternalModelCell, ExternalModelUsage, ExternalUsage } from "../api";
import i18n from "../i18n";
import { fmtCount, fmtNumber, fmtUSD } from "../format";
import { mockFetch, renderWithProviders } from "../test/render";
import { bucketOptions, outsideTotals, splitBy, originOf, TOKEN_KINDS } from "./outsideFigures";

// #120, part C and the detailed view: the Claude Code sessions outside covey
// on the cost page. Pinned here (criterion 9 and the human's two comments):
// the series is on in the org-wide view and off once agents are filtered;
// the tile shows the API's figures; 10 min stops at 2 days and 10 h at 30,
// each with a reason; Real and Billed are off at 10 min; combined, the total
// says it includes the outside sessions and the agents' own figure stays;
// the detailed view's splits add up to its total.

const SEAT = "claude_code_oauth_token";
const H18 = "2026-11-17T18:00:00Z", H19 = "2026-11-17T19:00:00Z";
const t = (k: string, o: Record<string, unknown> = {}) => i18n.t(k, o) as string;

function sums(over: Partial<ExternalMeterSums> = {}): ExternalMeterSums {
  return {
    real_usd: 6, external_real_usd: 4.2, covey_real_usd: 1.8, unsplit_real_usd: 0, share: 0.7,
    split_external_list_usd: 12.5, split_covey_list_usd: 5.4, points: 13, split_points: 13, split_external_points: 9.1,
    split_covey_points: 3.9, split_runs: 1, split_slack_points: 1, split_slack_usd: 0.46, split_external_slack_points: 0.7,
    split_external_slack_usd: 0.32, split_covey_slack_points: 0.3, split_covey_slack_usd: 0.14, point_rate: 0.46,
    point_rate_mixed: false, step_count: 2, split_steps: 2, unsplit_steps: 0, unpriced_steps: 0, unpriced_points: 0,
    unpriced_calls: 0, unpriced_tokens: 0, unmetered_list_usd: 0, ...over,
  };
}

function external(): ExternalUsage {
  const src = (source: string, list: number, linked: boolean, unpriced: number) => ({
    source, meter_credential: linked ? { secret_key: SEAT, secret_slot: 0 } : null, calls: 30, list_usd: list, reported_usd: null,
    unpriced_tokens: unpriced, unpriced_calls: unpriced > 0 ? 1 : 0, unpriced_models: unpriced > 0 ? ["claude-sonnet-5-7"] : [],
    first_at: H18, last_at: H19, hosts: [],
  });
  const cell = (period: string, source: string, origin: string | null, list: number) => ({
    period, source, origin, calls: 10, list_usd: list, measured: { cache_read: list }, reported_usd: null, reported_calls: 0,
    tokens: { cache_read: 1000 }, unpriced_tokens: 0, unpriced_calls: 0,
  });
  return {
    sources: [src("operator: home", 12.5, true, 2060), src("operator: work", 3.25, false, 0)],
    cells: [cell(H18, "operator: home", "main", 8), cell(H19, "operator: home", "subagent", 4.5), cell(H19, "operator: work", null, 3.25)],
    meter: { ...sums(), steps: [], hours: [], credentials: [] },
  };
}

function cells(): CostCells {
  const cell = (period: string, agent: string, output: number) => ({ period, agent_id: agent, secret_key: SEAT, secret_slot: 0,
    credential_kind: "subscription", reported_usd: output, measured: { output }, engine_usd: output, tokens: { output: output * 50_000 } });
  return {
    periods: [H18, H19],
    agents: [{ id: "a1", label: "Coordinator", reported_usd: 0, measured_usd: 7 }],
    types: ["cache_read", "cache_write", "input", "output", "web_search", "unpriced", "unattributed"],
    cells: [cell(H18, "a1", 3), cell(H19, "a1", 4)],
    subscription: [], meter: [], plans: [], bookings: [],
    external: external(),
  };
}

const NO_BILL = { bucket: "hour", points: [], sync: { configured: false, last_attempt_at: null, last_success_at: null,
  last_daily_at: null, data_refreshed_at: null, last_status: "", last_error: "" } };

function serveCosts(data: CostCells = cells()) {
  mockFetch({
    "/api/v1/cost/cells": data,
    "/api/v1/cost/billed": NO_BILL,
    "/api/v1/cost/runs": [],
    "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
    "/api/v1/cost/usage-readings": [],
  });
  return renderWithProviders(<Costs />);
}

const seriesChip = () => screen.getByRole("button", { name: t("costs.outside.show") });
const outsideTile = () => screen.getByRole("group", { name: t("costs.outside.tile") });

describe("the operator sessions on the cost page (#120)", () => {
  beforeEach(() => { i18n.changeLanguage("en"); });

  it("shows the series by default org-wide, hides it once an agent is filtered, and lets the user switch it on", async () => {
    serveCosts();
    await screen.findByRole("group", { name: t("costs.outside.tile") });
    expect(seriesChip().getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(screen.getAllByRole("button", { name: /Coordinator/ })[0]);
    expect(seriesChip().getAttribute("aria-pressed")).toBe("false");
    expect(screen.queryByRole("group", { name: t("costs.outside.tile") })).toBeNull();
    expect(screen.getByText(t("costs.outside.offFiltered"))).toBeTruthy();

    fireEvent.click(seriesChip());
    expect(seriesChip().getAttribute("aria-pressed")).toBe("true");
    expect(outsideTile()).toBeTruthy();
  });

  it("puts the API's figures on the tile: list total, real share and %, unpriced tokens", async () => {
    serveCosts();
    const tile = await screen.findByRole("group", { name: t("costs.outside.tile") });
    const ext = external();
    const o = outsideTotals(ext)!;
    expect(o.list).toBeCloseTo(12.5 + 3.25, 9);
    expect(within(tile).getByText(fmtUSD(12.5 + 3.25))).toBeTruthy();
    expect(within(tile).getByText(t("costs.outside.realShare", { usd: fmtUSD(ext.meter.external_real_usd), pct: `${fmtNumber(0.7 * 100, 1)} %` }))).toBeTruthy();
    expect(within(tile).getByText(t("costs.outside.unpriced", { n: fmtCount(2060) }))).toBeTruthy();
  });

  it("changes the total only when combined, says so, and leaves the agents' own figure alone", async () => {
    serveCosts();
    await screen.findByRole("group", { name: t("costs.outside.tile") });
    const agentsKpi = () => screen.getAllByText(t("costs.fig.recomputed"), { exact: true })
      .map((el) => el.closest(".card")).find((c) => c?.parentElement?.className.includes("gap-3"))!.textContent;
    const before = agentsKpi();
    expect(before).toContain(fmtUSD(7));
    expect(screen.queryByRole("group", { name: t("costs.outside.combinedTotal") })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: t("costs.outside.modeCombined") }));
    const combined = screen.getByRole("group", { name: t("costs.outside.combinedTotal") });
    expect(within(combined).getByText(fmtUSD(7 + 15.75))).toBeTruthy();
    expect(within(combined).getByText(t("costs.outside.combinedSub", { outside: fmtUSD(15.75), agents: fmtUSD(7) }))).toBeTruthy();
    expect(agentsKpi()).toBe(before);

    fireEvent.click(screen.getByRole("button", { name: t("costs.outside.modeApart") }));
    expect(screen.queryByRole("group", { name: t("costs.outside.combinedTotal") })).toBeNull();
    expect(agentsKpi()).toBe(before);
  });

  it("offers 10 min up to 2 days and 10 h up to 30 days, each disabled beyond with a reason", async () => {
    const at = (days: number, v: string) => bucketOptions(i18n.t, days).find((o) => o.v === v)!;
    expect(at(1, "10min").disabled).toBeFalsy();
    expect(at(2, "10min").disabled).toBeFalsy();
    expect(at(7, "10min")).toMatchObject({ disabled: true, title: t("costs.outside.tenMinTooLong", { days: 2 }) });
    expect(at(30, "10hour").disabled).toBeFalsy();
    expect(at(90, "10hour")).toMatchObject({ disabled: true, title: t("costs.outside.tenHourTooLong", { days: 30 }) });

    serveCosts();
    await screen.findByRole("group", { name: t("costs.outside.tile") });
    const b10 = () => screen.getByRole("button", { name: t("costs.outside.b10min") });
    const b10h = () => screen.getByRole("button", { name: t("costs.outside.b10hour") });
    expect(screen.getByRole("button", { name: t("costs.rangeDays", { count: 2 }) })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: t("costs.rangeDays", { count: 7 }) }));
    expect((b10() as HTMLButtonElement).disabled).toBe(true);
    expect(b10().getAttribute("title")).toBe(t("costs.outside.tenMinTooLong", { days: 2 }));
    expect((b10h() as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: t("costs.rangeDays", { count: 90 }) }));
    expect((b10h() as HTMLButtonElement).disabled).toBe(true);
    expect(b10h().getAttribute("title")).toBe(t("costs.outside.tenHourTooLong", { days: 30 }));
  });

  it("disables Real and Billed at 10 min, with the reason", async () => {
    serveCosts();
    await screen.findByRole("group", { name: t("costs.outside.tile") });
    const chips = (name: string) => screen.getAllByRole("button", { name }).filter((b) => b.getAttribute("aria-pressed") != null);
    for (const name of [t("costs.fig.realEstimate"), t("costs.fig.billed")]) {
      expect(chips(name).some((b) => (b as HTMLButtonElement).disabled)).toBe(false);
    }
    fireEvent.click(screen.getByRole("button", { name: t("costs.outside.b10min") }));
    for (const name of [t("costs.fig.realEstimate"), t("costs.fig.billed")]) {
      const off = chips(name).filter((b) => b.getAttribute("title") === t("costs.outside.tenMinNoMeter"));
      expect(off.length).toBeGreaterThan(0);
      for (const b of off) {
        expect((b as HTMLButtonElement).disabled).toBe(true);
        expect(b.getAttribute("aria-pressed")).toBe("false");
      }
    }
    // The recomputed chip stays.
    expect(chips(t("costs.fig.recomputed")).every((b) => !(b as HTMLButtonElement).disabled)).toBe(true);
  });

  it("links the series to its detailed view with the range", async () => {
    serveCosts();
    await screen.findByRole("group", { name: t("costs.outside.tile") });
    expect(screen.getByRole("link", { name: t("costs.outside.details") }).getAttribute("href")).toBe("/costs/outside?days=1&bucket=hour");
  });
});

// --- The detailed view -----------------------------------------------------------

function mc(period: string, origin: string | null, model: string, toks: [number, number, number, number, number], priced = true): ExternalModelCell {
  const price: Record<string, number> = { input: 4, output: 20, cache_read: 0.2, cache_write_5m: 5, cache_write_1h: 8 };
  const tokens = Object.fromEntries(TOKEN_KINDS.map((k, i) => [k, toks[i]]));
  const typeUSD = priced ? Object.fromEntries(TOKEN_KINDS.map((k) => [k, tokens[k] * price[k] / 1e6])) : {};
  const list = priced ? Object.values(typeUSD).reduce((a, v) => a + v, 0) : null;
  return { period, source: "operator: home", origin, model, calls: 3, list_usd: list, measured: {}, type_usd: typeUSD,
    reported_usd: null, reported_calls: 0, tokens, unpriced_tokens: priced ? 0 : toks.reduce((a, v) => a + v, 0), unpriced_calls: priced ? 0 : 3 };
}

const VIEW_CELLS = [
  mc(H18, "main", "claude-opus-5-5", [100, 20_000, 1_000_000, 5_000, 2_000]),
  mc(H18, "subagent", "claude-opus-5-5", [50, 9_000, 400_000, 0, 1_000]),
  mc(H19, "workflow", "claude-sonnet-5-5", [10, 3_000, 200_000, 1_000, 0]),
  mc(H19, null, "claude-sonnet-5-7", [10, 50, 2_000, 0, 0], false),
];

function view(): ExternalModelUsage {
  const ext = external();
  return {
    bucket: "hour", since: "2026-11-17T00:00:00Z", source: null, sources: ext.sources,
    models: [{ model: "claude-opus-5-5", priced: true }, { model: "claude-sonnet-5-5", priced: true }, { model: "claude-sonnet-5-7", priced: false }],
    cells: VIEW_CELLS,
    meter: { ...sums(), steps: [], hours: [], credentials: [{ ...sums(), secret_key: SEAT, secret_slot: 0, meter_source: "usage_cmd",
      sources: ["operator: home"], hours_with_reading: 2, agents: [] }] },
  };
}

describe("the detailed view of the operator sessions", () => {
  beforeEach(() => { i18n.changeLanguage("en"); });

  it("splits per model and per origin into rows that add up to the same total", () => {
    const m = splitBy(VIEW_CELLS, (c) => c.model), o = splitBy(VIEW_CELLS, (c) => originOf(c));
    const listOf = (rows: { list: number | null }[]) => rows.reduce((a, r) => a + (r.list ?? 0), 0);
    expect(listOf(m.rows)).toBeCloseTo(m.total.list!, 12);
    expect(listOf(o.rows)).toBeCloseTo(m.total.list!, 12);
    expect(o.total.list).toBeCloseTo(m.total.list!, 12);
    for (const k of TOKEN_KINDS) {
      const all = VIEW_CELLS.reduce((a, c) => a + c.tokens[k], 0);
      expect(m.rows.reduce((a, r) => a + r.tokens[k], 0)).toBe(all);
      expect(o.rows.reduce((a, r) => a + r.tokens[k], 0)).toBe(all);
      expect(m.total.tokens[k]).toBe(all);
      expect(m.rows.reduce((a, r) => a + (r.typeUSD[k] ?? 0), 0)).toBeCloseTo(m.total.typeUSD[k], 12);
    }
    // The unknown model has no figure, not $0, and its tokens are unpriced.
    const unknown = m.rows.find((r) => r.key === "claude-sonnet-5-7")!;
    expect(unknown.list).toBeNull();
    expect(unknown.unpricedTokens).toBe(2060);
  });

  it("shows the tables, their totals and the unpriced tokens on the page", async () => {
    mockFetch({ "/api/v1/cost/external-usage": view() });
    renderWithProviders(<OutsideCosts />, { route: "/costs/outside?days=1&bucket=hour", path: "/costs/outside" });
    const models = await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const origins = screen.getByRole("table", { name: t("costs.outside.byOrigin") });
    const total = splitBy(VIEW_CELLS, (c) => c.model).total.list!;
    for (const table of [models, origins]) {
      const foot = table.querySelector("[data-total] [data-list]")!;
      expect(foot.textContent).toBe(fmtUSD(total));
    }
    expect(within(models).getByText(t("costs.outside.noPrice"))).toBeTruthy();
    expect(within(origins).getByText(t("costs.outside.origin_unknown"))).toBeTruthy();
    expect(screen.getByRole("group", { name: t("costs.outside.colUnpriced") }).textContent).toContain(fmtCount(2060));
    const real = screen.getByRole("group", { name: t("costs.outside.meterShare") });
    expect(real.textContent).toContain(fmtUSD(4.2));
    expect(real.textContent).toContain(t("costs.outside.ofMeter", { pct: `${fmtNumber(70, 1)} %` }));
  });

  it("states a period without a meter reading instead of showing 0", async () => {
    mockFetch({ "/api/v1/cost/external-usage": view() });
    const { container } = renderWithProviders(<OutsideCosts />, { route: "/costs/outside?days=1&bucket=hour", path: "/costs/outside" });
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const rows = container.querySelectorAll("[data-meter-row='no-reading']");
    expect(rows.length).toBe(2);
    for (const r of rows) {
      expect(r.textContent).toContain(t("costs.outside.noReading"));
      expect(r.textContent).not.toContain(fmtUSD(0));
    }
  });
});
