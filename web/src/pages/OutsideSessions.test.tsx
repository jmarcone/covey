import { describe, it, expect, beforeEach } from "vitest";
import { fireEvent, screen, within } from "@testing-library/react";
import Costs from "./Costs";
import OutsideCosts from "./OutsideCosts";
import type {
  CostCells,
  ExternalMeterStep,
  ExternalMeterSums,
  ExternalMeterTotal,
  ExternalModelCell,
  ExternalModelUsage,
  ExternalUsage,
} from "../api";
import i18n from "../i18n";
import { fmtCount, fmtNumber, fmtUSD } from "../format";
import { mockFetch, renderWithProviders } from "../test/render";
import { listLine } from "./OutsideSessions";
import { bucketOptions, outsideTotals, splitBy, stepKind, originOf, TOKEN_KINDS } from "./outsideFigures";

// #120, part C and the detailed view: the Claude Code sessions outside covey
// on the cost page. Pinned here (criterion 9 and the human's two comments):
// the series is on in the org-wide view and off once agents are filtered;
// the tile shows the API's figures; 10 min stops at 2 days and 10 h at 30,
// each with a reason; Real and Billed are off at 10 min; combined, the total
// says it includes the outside sessions and the agents' own figure stays;
// the detailed view's splits add up to its total. Where the server has no
// figure (null), the page never shows $0: a reading not split names its
// reason, a period without a list price says so.

const SEAT = "claude_code_oauth_token";
const OTHER_SEAT = "other_oauth_token";
const H18 = "2026-11-17T18:00:00Z", H19 = "2026-11-17T19:00:00Z", H20 = "2026-11-17T20:00:00Z";
const t = (k: string, o: Record<string, unknown> = {}) => i18n.t(k, o) as string;
/** count picks the plural, n is the number as shown. */
const counted = (v: number) => ({ count: v, n: fmtNumber(v, 0) });

function sums(over: Partial<ExternalMeterSums> = {}): ExternalMeterSums {
  return {
    real_usd: 6, external_real_usd: 4.2, covey_real_usd: 1.8, unsplit_real_usd: 0, share: 0.7,
    split_external_list_usd: 12.5, split_covey_list_usd: 5.4, points: 13, split_points: 13, split_external_points: 9.1,
    split_covey_points: 3.9, split_runs: 1, split_slack_points: 1, split_slack_usd: 0.46, split_external_slack_points: 0.7,
    split_external_slack_usd: 0.32, split_covey_slack_points: 0.3, split_covey_slack_usd: 0.14, point_rate: 0.46,
    point_rate_mixed: false, step_count: 2, split_steps: 2, unsplit_steps: 0, outside_unpriced_steps: 0, outside_unpriced_real_usd: 0,
    unpriced_steps: 0, unpriced_points: 0, unpriced_calls: 0, unpriced_tokens: 0, unmetered_list_usd: 0, ...over,
  };
}

type Kind = "split" | NonNullable<ExternalMeterStep["reason"]>;

/** One meter reading of a seat, as the server sends it: split (real into
 *  outside and covey), or not, for a reason. */
function step(at: string, kind: Kind, real: number | null, opt: { key?: string; outside?: number; points?: number } = {}): ExternalMeterStep {
  const split = kind === "split";
  const out = opt.outside ?? 0;
  return {
    at, from: new Date(Date.parse(at) - 3_600_000).toISOString(), secret_key: opt.key ?? SEAT, secret_slot: 0,
    points: opt.points ?? (kind === "idle" ? 0 : 2), reset: false, real_usd: real,
    external_list_usd: split ? 1 : 0, covey_list_usd: split ? 1 : 0,
    reported: kind !== "not_reported", split, reason: split ? undefined : kind,
    external_real_usd: split ? out : null, covey_real_usd: split ? (real ?? 0) - out : null,
    unsplit_real_usd: split || kind === "unpriced" || kind === "idle" ? 0 : real ?? 0,
  };
}

/** A seat's sums, with the sources linked to it. */
function seat(key: string, sources: string[], over: Partial<ExternalMeterSums> = {}): ExternalMeterTotal {
  return { ...sums(over), secret_key: key, secret_slot: 0, meter_source: "usage_cmd", sources, hours_with_reading: 2, agents: [] };
}

/** The outside usage of /cost/cells. unpriced: every outside call has no
 *  list price (a model the list does not know). */
function external({ unpriced = false } = {}): ExternalUsage {
  const src = (source: string, list: number, linked: boolean, unpricedTokens: number) => ({
    source, meter_credential: linked ? { secret_key: SEAT, secret_slot: 0 } : null, calls: 30,
    list_usd: unpriced ? null : list, reported_usd: null,
    unpriced_tokens: unpriced ? 3000 : unpricedTokens, unpriced_calls: unpriced ? 30 : unpricedTokens > 0 ? 1 : 0,
    unpriced_models: unpriced || unpricedTokens > 0 ? ["claude-sonnet-5-7"] : [],
    first_at: H18, last_at: H19, hosts: [],
  });
  const cell = (period: string, source: string, origin: string | null, list: number) => ({
    period, source, origin, calls: 10, list_usd: unpriced ? null : list, measured: (unpriced ? {} : { cache_read: list }) as Record<string, number>,
    reported_usd: null, reported_calls: 0, tokens: { cache_read: 1000 }, unpriced_tokens: unpriced ? 1000 : 0, unpriced_calls: unpriced ? 10 : 0,
  });
  return {
    sources: [src("operator: home", 12.5, true, 2060), src("operator: work", 3.25, false, 0)],
    cells: [cell(H18, "operator: home", "main", 8), cell(H19, "operator: home", "subagent", 4.5), cell(H19, "operator: work", null, 3.25)],
    // The linked seat: one split reading and one the outside sessions had
    // not reported. A seat no source names is all covey's; a reading not
    // split there says nothing about the outside sessions.
    meter: {
      ...sums({ real_usd: 13.7, covey_real_usd: 6.8, unsplit_real_usd: 2.7, share: 4.2 / 11, unsplit_steps: 2 }),
      steps: [
        step("2026-11-17T18:30:00Z", "split", 6, { outside: 4.2 }),
        step("2026-11-17T19:30:00Z", "not_reported", 1.5),
        step("2026-11-17T20:30:00Z", "unpriced", null),
        step("2026-11-17T18:40:00Z", "split", 5, { key: OTHER_SEAT }),
        step("2026-11-17T19:40:00Z", "moved_without_reported_call", 1.2, { key: OTHER_SEAT }),
      ],
      hours: [],
      credentials: [
        seat(SEAT, ["operator: home"], { real_usd: 7.5, unsplit_steps: 1, unsplit_real_usd: 1.5 }),
        seat(OTHER_SEAT, [], { real_usd: 6.2, external_real_usd: 0, covey_real_usd: 5, share: 0, unsplit_steps: 1, unsplit_real_usd: 1.2 }),
      ],
    },
  };
}

function cells(ext: ExternalUsage = external()): CostCells {
  const cell = (period: string, agent: string, output: number) => ({ period, agent_id: agent, secret_key: SEAT, secret_slot: 0,
    credential_kind: "subscription", reported_usd: output, measured: { output }, engine_usd: output, tokens: { output: output * 50_000 } });
  return {
    periods: [H18, H19],
    agents: [{ id: "a1", label: "Coordinator", reported_usd: 0, measured_usd: 7 }],
    types: ["cache_read", "cache_write", "input", "output", "web_search", "unpriced", "unattributed"],
    cells: [cell(H18, "a1", 3), cell(H19, "a1", 4)],
    subscription: [], meter: [], plans: [], bookings: [],
    external: ext,
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

  it("puts the API's figures on the tile: list total, real share and % of the linked seats, unpriced tokens", async () => {
    serveCosts();
    const tile = await screen.findByRole("group", { name: t("costs.outside.tile") });
    const o = outsideTotals(external())!;
    expect(o.list).toBeCloseTo(12.5 + 3.25, 9);
    expect(within(tile).getByText(fmtUSD(12.5 + 3.25))).toBeTruthy();
    expect(within(tile).getByText(t("costs.outside.tileSub", counted(60)))).toBeTruthy();
    // The share is of the seat a source names (4.2 of 6), not of every
    // seat (4.2 of 11): the unlinked seat is all covey's.
    expect(o.share).toBeCloseTo(0.7, 12);
    expect(within(tile).getByText(t("costs.outside.realShare", { usd: fmtUSD(4.2), pct: `${fmtNumber(0.7 * 100, 1)} %` }))).toBeTruthy();
    expect(within(tile).getByText(t("costs.outside.unpriced", { n: fmtCount(2060) }))).toBeTruthy();
  });

  it("counts only the readings of seats a source names, one line per reason, in the singular for one", async () => {
    serveCosts();
    const tile = await screen.findByRole("group", { name: t("costs.outside.tile") });
    const o = outsideTotals(external())!;
    expect(o.unsplit.not_reported).toEqual({ steps: 1, usd: 1.5 });
    expect(o.unsplit.moved_without_reported_call.steps).toBe(0);
    const line = t("costs.outside.unsplit_not_reported", { ...counted(1), usd: fmtUSD(1.5) });
    expect(line).toContain("1 reading not reported");
    expect(within(tile).getByText(line)).toBeTruthy();
    expect(tile.textContent).not.toContain(fmtUSD(1.2));
    expect(t("costs.outside.unsplit_not_reported", { ...counted(2), usd: fmtUSD(3) })).toContain("2 readings");
    // The readings at no plan price are counted here too, as on the detail page.
    expect(o.unpricedSteps).toBe(1);
    expect(within(tile).getByText(t("costs.outside.unpricedReadings", counted(1)))).toBeTruthy();
  });

  it("says selected agents in combined mode once the page is narrowed to agents", async () => {
    serveCosts();
    await screen.findByRole("group", { name: t("costs.outside.tile") });
    fireEvent.click(screen.getAllByRole("button", { name: /Coordinator/ })[0]);
    fireEvent.click(seriesChip());
    fireEvent.click(screen.getByRole("button", { name: t("costs.outside.modeCombined") }));
    const combined = screen.getByRole("group", { name: t("costs.outside.combinedTotal") });
    expect(within(combined).getByText(t("costs.outside.combinedSubSelected", { outside: fmtUSD(15.75), agents: fmtUSD(7) }))).toBeTruthy();
    expect(combined.textContent).not.toContain(t("costs.outside.combinedSub", { outside: fmtUSD(15.75), agents: fmtUSD(7) }));
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

  it("never shows $0 for outside calls without a list price: the tile, the combined total and the chart say so", async () => {
    const { container } = serveCosts(cells(external({ unpriced: true })));
    const tile = await screen.findByRole("group", { name: t("costs.outside.tile") });
    expect(within(tile).getByText(t("costs.outside.noPrice"))).toBeTruthy();
    expect(tile.querySelector("[data-value]")!.textContent).toBe("–");

    const section = container.querySelector("[data-outside]") as HTMLElement;
    expect(section.querySelector("[data-outside-unpriced-note]")!.textContent).toBe(t("costs.outside.chartUnpriced"));
    fireEvent.click(within(section).getByRole("button", { name: t("costs.fig.showAsTable") }));
    const table = within(section).getByRole("table");
    const rows = within(table).getAllByRole("row").filter((r) => r.closest("tbody"));
    expect(rows.length).toBe(2);
    for (const r of rows) {
      expect(r.textContent).toContain(t("costs.outside.noPrice"));
      expect(r.textContent).not.toContain(fmtUSD(0));
    }

    fireEvent.click(within(section).getByRole("button", { name: t("costs.outside.modeCombined") }));
    const combined = screen.getByRole("group", { name: t("costs.outside.combinedTotal") });
    expect(within(combined).getByText(t("costs.outside.combinedSubNoPrice", { agents: fmtUSD(7) }))).toBeTruthy();
    expect(combined.textContent).not.toContain(t("costs.outside.combinedSub", { outside: fmtUSD(0), agents: fmtUSD(7) }));
    for (const r of within(within(section).getByRole("table")).getAllByRole("row").filter((r) => r.closest("tbody"))) {
      expect(r.textContent).toContain(t("costs.outside.chartNoPriceCombined"));
    }
  });

  it("tells no calls from no list price", () => {
    expect(listLine(i18n.t, null, 0)).toBe(t("costs.outside.noCalls"));
    expect(listLine(i18n.t, null, 3)).toBe(t("costs.outside.noPrice"));
    expect(listLine(i18n.t, 1.5, 1)).toBe(t("costs.outside.tileSub", counted(1)));
    expect(listLine(i18n.t, 1.5, 1)).toContain("1 model call");
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

function mc(period: string, origin: string | null, model: string, toks: [number, number, number, number, number], priced = true,
  reported: { usd: number; calls: number } | null = null): ExternalModelCell {
  const price: Record<string, number> = { input: 4, output: 20, cache_read: 0.2, cache_write_5m: 5, cache_write_1h: 8 };
  const tokens = Object.fromEntries(TOKEN_KINDS.map((k, i) => [k, toks[i]]));
  const typeUSD = priced ? Object.fromEntries(TOKEN_KINDS.map((k) => [k, tokens[k] * price[k] / 1e6])) : {};
  const list = priced ? Object.values(typeUSD).reduce((a, v) => a + v, 0) : null;
  return { period, source: "operator: home", origin, model, calls: 3, list_usd: list, measured: {}, type_usd: typeUSD,
    reported_usd: reported?.usd ?? null, reported_calls: reported?.calls ?? 0, tokens,
    unpriced_tokens: priced ? 0 : toks.reduce((a, v) => a + v, 0), unpriced_calls: priced ? 0 : 3 };
}

const VIEW_CELLS = [
  mc(H18, "main", "claude-opus-5-5", [100, 20_000, 1_000_000, 5_000, 2_000]),
  mc(H18, "subagent", "claude-opus-5-5", [50, 9_000, 400_000, 0, 1_000]),
  mc(H19, "workflow", "claude-sonnet-5-5", [10, 3_000, 200_000, 1_000, 0]),
  mc(H19, null, "claude-sonnet-5-7", [10, 50, 2_000, 0, 0], false),
];

function view(over: { steps?: ExternalMeterStep[]; cells?: ExternalModelCell[]; seatSources?: string[]; source?: string | null;
  seatSums?: Partial<ExternalMeterSums>; extra?: { source: string; key: string } } = {}): ExternalModelUsage {
  const ext = external();
  const sources = over.extra
    ? [...ext.sources, { ...ext.sources[0], source: over.extra.source, meter_credential: { secret_key: over.extra.key, secret_slot: 0 } }]
    : ext.sources;
  return {
    bucket: "hour", since: "2026-11-17T00:00:00Z", source: over.source ?? null, sources,
    models: [{ model: "claude-opus-5-5", priced: true }, { model: "claude-sonnet-5-5", priced: true }, { model: "claude-sonnet-5-7", priced: false }],
    cells: over.cells ?? VIEW_CELLS,
    meter: { ...sums(), steps: over.steps ?? [], hours: [], credentials: [
      seat(SEAT, over.seatSources ?? ["operator: home"], over.seatSums),
      ...(over.extra ? [seat(over.extra.key, [over.extra.source])] : []),
    ] },
  };
}

function serveView(v: ExternalModelUsage, route = "/costs/outside?days=1&bucket=hour") {
  mockFetch({ "/api/v1/cost/external-usage": v });
  return renderWithProviders(<OutsideCosts />, { route, path: "/costs/outside" });
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
    serveView(view({ steps: [step("2026-11-17T18:30:00Z", "split", 6, { outside: 4.2 })] }));
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
    // The $ per type of a row with unpriced tokens cover its priced calls
    // only: marked, with the footnote; a row priced whole is not marked.
    expect(origins.querySelector("[data-total] [data-priced-only]")).not.toBeNull();
    expect(models.querySelector("[data-row='claude-opus-5-5'] [data-priced-only]")).toBeNull();
    expect(screen.getByText(t("costs.outside.typePricedOnly"))).toBeTruthy();
  });

  it("shows no real cost, not $0, for a seat whose readings all had no plan price", async () => {
    const { container } = serveView(view({ steps: [step("2026-11-17T18:30:00Z", "unpriced", null)],
      seatSums: { real_usd: 0, external_real_usd: 0, covey_real_usd: 0, share: null, step_count: 1, split_steps: 0, unpriced_steps: 1, unpriced_points: 2 } }));
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const line = container.querySelector(`[data-seat='${SEAT}#0']`)!.textContent!;
    expect(line).toContain(t("costs.outside.seatNoShare", { real: "–" }));
    expect(line).not.toContain(fmtUSD(0));
    expect(line).toContain(t("costs.outside.unpricedReadings", counted(1)));
  });

  it("says no reading for a source whose seat has none in a period, though another seat has one", async () => {
    const lab = VIEW_CELLS.slice(2, 3).map((c) => ({ ...c, source: "operator: lab" }));
    const { container } = serveView(view({ extra: { source: "operator: lab", key: OTHER_SEAT }, cells: [...VIEW_CELLS, ...lab],
      steps: [step("2026-11-17T18:30:00Z", "split", 6, { outside: 4.2 }), step("2026-11-17T19:30:00Z", "split", 2, { outside: 1 })] }));
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const row = container.querySelector<HTMLTableRowElement>(`[data-meter-period='${Date.parse(H19)}']`)!;
    expect(row.getAttribute("data-meter-row")).toBe("split");
    expect(row.textContent).toContain(t("costs.outside.noReading"));
    expect(row.querySelectorAll("td")[1].textContent).toBe(fmtUSD(1));
    // Where every working source's seat has a reading: no such note.
    const h18 = container.querySelector<HTMLTableRowElement>(`[data-meter-period='${Date.parse(H18)}']`)!;
    expect(h18.textContent).not.toContain(t("costs.outside.noReading"));
  });

  it("shows the calls that carried their own reported cost, with plurals", async () => {
    const withReported = [...VIEW_CELLS.slice(0, 3), mc(H19, null, "claude-sonnet-5-7", [10, 50, 2_000, 0, 0], false, { usd: 0.3, calls: 2 })];
    serveView(view({ cells: withReported }));
    const models = await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const tile = screen.getByRole("group", { name: t("costs.fig.reported") });
    expect(tile.textContent).toContain(fmtUSD(0.3));
    expect(within(tile).getByText(t("costs.outside.reportedCalls", counted(2)))).toBeTruthy();
    expect(t("costs.outside.reportedCalls", counted(2))).toContain("2 calls");
    const row = models.querySelector("[data-row='claude-sonnet-5-7'] [data-reported]")!;
    expect(row.textContent).toBe(fmtUSD(0.3) + t("costs.outside.callsShort", counted(2)));
    expect(t("costs.outside.callsShort", counted(1))).toBe("1 call");
  });

  it("states a period without a meter reading instead of showing 0", async () => {
    const { container } = serveView(view());
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const rows = container.querySelectorAll("[data-meter-row='no-reading']");
    expect(rows.length).toBe(2);
    for (const r of rows) {
      expect(r.textContent).toContain(t("costs.outside.noReading"));
      expect(r.textContent).not.toContain(fmtUSD(0));
    }
  });

  it("words every reason a reading was not split, and never shows $0 for its parts", async () => {
    const at = (h: number, m: number) => `2026-11-17T${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:00Z`;
    const steps = [
      step(at(17, 40), "split", 2, { outside: 1.4 }),
      step(at(18, 20), "not_reported", 1.5),
      step(at(18, 50), "not_reported", 0.5),
      step(at(19, 20), "moved_without_reported_call", 0.9),
      step(at(20, 20), "outside_unpriced", 0.46),
      step(at(21, 20), "unpriced", null),
      step(at(22, 20), "idle", 0),
      step(at(23, 20), "split", 1, { outside: 0.25 }),
      step(at(23, 40), "outside_unpriced", 0.3),
    ];
    expect(steps.map(stepKind)).toEqual(["split", "not_reported", "not_reported", "moved_without_reported_call",
      "outside_unpriced", "unpriced", "idle", "split", "outside_unpriced"]);
    const { container } = serveView(view({ steps }));
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const row = (h: number) => {
      const rows = [...container.querySelectorAll<HTMLTableRowElement>("[data-meter-table] tbody tr")];
      return rows.find((r) => r.getAttribute("data-meter-period") === String(Date.parse(at(h, 0))))!;
    };
    const cellsOf = (h: number) => [...row(h).querySelectorAll("td")].map((c) => c.textContent);
    const kind = (h: number) => row(h).getAttribute("data-meter-row");

    expect(kind(17)).toBe("split");
    expect(cellsOf(17).slice(0, 3)).toEqual([fmtUSD(2), fmtUSD(1.4), fmtUSD(0.6)]);

    // Not reported, twice in one hour: their real cost, no part, the plural.
    expect(kind(18)).toBe("unsplit");
    expect(cellsOf(18)).toEqual([fmtUSD(2), "–", "–", t("costs.outside.unsplit_not_reported", { ...counted(2), usd: fmtUSD(2) })]);
    // Moved with no reported call: the bug was Outside $0.00 / covey $0.00.
    expect(kind(19)).toBe("unsplit");
    expect(cellsOf(19)).toEqual([fmtUSD(0.9), "–", "–",
      t("costs.outside.unsplit_moved_without_reported_call", { ...counted(1), usd: fmtUSD(0.9) })]);
    expect(kind(20)).toBe("unsplit");
    expect(cellsOf(20)).toEqual([fmtUSD(0.46), "–", "–",
      t("costs.outside.unsplit_outside_unpriced", { ...counted(1), usd: fmtUSD(0.46) })]);
    // No plan price: no real cost at all, counted.
    expect(kind(21)).toBe("unpriced");
    expect(cellsOf(21)).toEqual(["–", "–", "–", t("costs.outside.unpricedReadings", counted(1))]);
    // Idle: neither split nor unsplit.
    expect(kind(22)).toBe("idle");
    expect(cellsOf(22).slice(1)).toEqual(["–", "–", t("costs.outside.idleHere")]);
    // Split and not split in one hour: the split part, and the rest named.
    expect(kind(23)).toBe("partial");
    expect(cellsOf(23)).toEqual([fmtUSD(1.3), fmtUSD(0.25), fmtUSD(0.75),
      t("costs.outside.unsplit_outside_unpriced", { ...counted(1), usd: fmtUSD(0.3) })]);
    for (const h of [18, 19, 20, 21]) expect(cellsOf(h).slice(1, 3).join()).not.toContain(fmtUSD(0));

    // The seat line counts them per reason, as the rows do.
    const seatLine = container.querySelector(`[data-seat='${SEAT}#0']`)!.textContent!;
    expect(seatLine).toContain(t("costs.outside.unsplit_not_reported", { ...counted(2), usd: fmtUSD(2) }));
    expect(seatLine).toContain(t("costs.outside.unsplit_moved_without_reported_call", { ...counted(1), usd: fmtUSD(0.9) }));
    expect(seatLine).toContain(t("costs.outside.unsplit_outside_unpriced", { ...counted(2), usd: fmtUSD(0.76) }));
    expect(seatLine).toContain(t("costs.outside.unpricedReadings", counted(1)));
  });

  it("gives a period whose calls had no list price no bar and says so", async () => {
    const onlyUnpriced = [...VIEW_CELLS, mc(H20, "main", "claude-sonnet-5-7", [10, 50, 2_000, 0, 0], false)];
    serveView(view({ cells: onlyUnpriced }));
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    fireEvent.click(screen.getByRole("button", { name: t("costs.fig.showAsTable") }));
    const chart = screen.getByRole("table", { name: t("costs.outside.chartTitle") });
    // The notes column is headed as a note, not as the meter's.
    expect(within(chart).getByRole("columnheader", { name: t("costs.outside.colNote") })).toBeTruthy();
    expect(within(chart).queryByRole("columnheader", { name: t("costs.fig.meterCol") })).toBeNull();
    const rows = within(chart).getAllByRole("row").filter((r) => r.closest("tbody"));
    expect(rows.length).toBe(3);
    const last = rows[2];
    expect(last.textContent).toContain(t("costs.outside.noPrice"));
    expect(last.textContent).not.toContain(fmtUSD(0));
  });

  it("with ?source=, says the meter share covers the other sources on the meter", async () => {
    serveView(view({ source: "operator: home", seatSources: ["operator: home", "operator: lab"],
      steps: [step("2026-11-17T18:30:00Z", "split", 6, { outside: 4.2 })] }),
    "/costs/outside?days=1&bucket=hour&source=operator%3A%20home");
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const real = screen.getByRole("group", { name: t("costs.outside.meterShare") });
    expect(within(real).getByText(t("costs.outside.tileOtherSources", { sources: "operator: lab" }))).toBeTruthy();
    expect(screen.getByText(t("costs.outside.sourceNote"))).toBeTruthy();
  });

  it("with ?source= of a source that names no meter, says so on the tile", async () => {
    serveView(view({ source: "operator: work", cells: VIEW_CELLS.map((c) => ({ ...c, source: "operator: work" })) }),
      "/costs/outside?days=1&bucket=hour&source=operator%3A%20work");
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const real = screen.getByRole("group", { name: t("costs.outside.meterShare") });
    expect(within(real).getByText(t("costs.outside.noMeterSource"))).toBeTruthy();
    expect(real.querySelector("[data-value]")!.textContent).toBe("–");
    expect(screen.getByText(t("costs.outside.perUse"))).toBeTruthy();
  });
});
