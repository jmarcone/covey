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
import { bucketOptions, meterPerPeriod, outsideTotals, splitBy, stepKind, originOf, unsplitUSD, TOKEN_KINDS } from "./outsideFigures";

// #120, part C and the detailed view: the Claude Code sessions outside covey
// on the cost page. Pinned here (criterion 9 and the human's two comments):
// the series is on in the org-wide view and off once agents are filtered;
// the tile shows the API's figures; 10 min stops at 2 days and 10 h at 30,
// each with a reason; Real and Billed are off at 10 min; combined, the total
// says it includes the outside sessions and the agents' own figure stays;
// the detailed view's splits add up to its total. Where the server has no
// figure (null), the page never shows $0: a reading not split names its
// reason, a period without a list price says so. F3b: per period and per
// seat an unattributed part (booked to nobody: beyond the rate "≥", moved
// with no call "≈") stands beside the outside and covey parts (≈, F3c) and
// the part not split, and they add up to the real cost; a reading that moved
// with no reported call is unattributed whole only where the server says so,
// and split with its chunk otherwise (F3c);
// the share is the outside part over the dent, at most.

const SEAT = "claude_code_oauth_token";
const OTHER_SEAT = "other_oauth_token";
const H18 = "2026-11-17T18:00:00Z", H19 = "2026-11-17T19:00:00Z", H20 = "2026-11-17T20:00:00Z";
const t = (k: string, o: Record<string, unknown> = {}) => i18n.t(k, o) as string;
/** count picks the plural, n is the number as shown. */
const counted = (v: number) => ({ count: v, n: fmtNumber(v, 0) });
const pct = (v: number) => `${fmtNumber(v * 100, 1)} %`;

function sums(over: Partial<ExternalMeterSums> = {}): ExternalMeterSums {
  return {
    real_usd: 6, external_real_usd: 4.2, covey_real_usd: 1.8, unsplit_real_usd: 0, share: 0.7,
    unattributed_real_usd: 0, unattributed_points: 0, unattributed_steps: 0,
    unattributed_no_call_real_usd: 0, unattributed_no_call_points: 0, incomplete_chunks: 0,
    split_external_list_usd: 12.5, split_covey_list_usd: 5.4, points: 13, split_points: 13, split_external_points: 9.1,
    split_covey_points: 3.9, split_runs: 1, split_slack_points: 1, split_slack_usd: 0.46, split_external_slack_points: 0.7,
    split_external_slack_usd: 0.32, split_covey_slack_points: 0.3, split_covey_slack_usd: 0.14, point_rate: 0.46,
    point_rate_mixed: false, step_count: 2, split_steps: 2, unsplit_steps: 0, outside_unpriced_steps: 0, outside_unpriced_real_usd: 0,
    unpriced_steps: 0, unpriced_points: 0, unpriced_calls: 0, unpriced_tokens: 0, unmetered_list_usd: 0, ...over,
  };
}

type Kind = "split" | NonNullable<ExternalMeterStep["reason"]>;

/** One meter reading of a seat, as the server sends it: split (real into
 *  outside, covey and its chunk's unattributed part beyond the rate),
 *  unattributed whole (it moved with no reported call in a chunk with no
 *  list), or not split, for a reason. folded: a reading that moved with no
 *  call and joined a chunk (F3c): split, its reason kept. */
function step(at: string, kind: Kind, real: number | null,
  opt: { key?: string; outside?: number; unattributed?: number; points?: number; folded?: boolean } = {}): ExternalMeterStep {
  const split = kind === "split" || !!opt.folded;
  const noCall = kind === "moved_without_reported_call" && !split;
  const out = opt.outside ?? 0;
  const un = split ? opt.unattributed ?? 0 : noCall ? real ?? 0 : 0;
  return {
    at, from: new Date(Date.parse(at) - 3_600_000).toISOString(), secret_key: opt.key ?? SEAT, secret_slot: 0,
    points: opt.points ?? (kind === "idle" ? 0 : 2), reset: false, real_usd: real,
    external_list_usd: split ? 1 : 0, covey_list_usd: split ? 1 : 0,
    reported: kind !== "not_reported", split, reason: kind === "split" ? undefined : kind,
    external_real_usd: split ? out : null, covey_real_usd: split ? (real ?? 0) - out - un : null,
    unattributed_real_usd: un, unattributed_points: un > 0 ? 1 : 0,
    unsplit_real_usd: split || noCall || kind === "unpriced" || kind === "idle" ? 0 : real ?? 0,
  };
}

/** A seat's sums, with the sources linked to it; k: its points per list $
 *  (F3b), null when no run moved 5 points. */
function seat(key: string, sources: string[], over: Partial<ExternalMeterSums> = {}, k: number | null = null): ExternalMeterTotal {
  return { ...sums(over), secret_key: key, secret_slot: 0, meter_source: "usage_cmd", sources, hours_with_reading: 2,
    points_per_list_usd: k, plan_rates: [{ usd_per_point: 0.46, points_per_list_usd: k }], agents: [] };
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
    // not reported. A seat no source names is all covey's; a reading that
    // moved there with no call and no chunk beside it (unattributed whole)
    // says nothing about the
    // outside sessions.
    meter: {
      ...sums({ real_usd: 13.7, covey_real_usd: 6.8, unsplit_real_usd: 1.5, share: 4.2 / 11, unsplit_steps: 1,
        unattributed_real_usd: 1.2, unattributed_points: 1, unattributed_steps: 1,
        unattributed_no_call_real_usd: 1.2, unattributed_no_call_points: 1 }),
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
        seat(OTHER_SEAT, [], { real_usd: 6.2, external_real_usd: 0, covey_real_usd: 5, share: 0,
          unattributed_real_usd: 1.2, unattributed_points: 1, unattributed_steps: 1,
          unattributed_no_call_real_usd: 1.2, unattributed_no_call_points: 1 }),
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

  it("shows the series by default, under an agent filter too (one rule for the page), and lets the user switch it off and on", async () => {
    serveCosts();
    await screen.findByRole("group", { name: t("costs.outside.tile") });
    expect(seriesChip().getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(screen.getAllByRole("button", { name: /Coordinator/ })[0]);
    expect(seriesChip().getAttribute("aria-pressed")).toBe("true");
    expect(outsideTile()).toBeTruthy();

    fireEvent.click(seriesChip());
    expect(seriesChip().getAttribute("aria-pressed")).toBe("false");
    expect(screen.queryByRole("group", { name: t("costs.outside.tile") })).toBeNull();
    expect(screen.getByText(t("costs.outside.off"))).toBeTruthy();

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
    // The outside part is of the seat a source names (4.2), and its share is
    // of the whole dent, every seat's (4.2 of 13.7), as the headline's is
    // (#120 review: one denominator). It is an estimate (≈, F3c).
    expect(o.meter.share).toBeCloseTo(4.2 / 13.7, 12);
    expect(o.meter.dent).toBeCloseTo(13.7, 12);
    expect(within(tile).getByText(t("costs.outside.realShare", { usd: fmtUSD(4.2), pct: pct(4.2 / 13.7) }))).toBeTruthy();
    expect(t("costs.outside.realShare", { usd: "$1", pct: "1 %" })).toContain("≈ $1");
    // Beside the share, what unattributed use is — none measured on this seat.
    expect(within(tile).getByText(t("costs.outside.unattributedNone"))).toBeTruthy();
    expect(within(tile).getByText(t("costs.outside.unpriced", { n: fmtCount(2060) }))).toBeTruthy();
  });

  it("puts the unattributed part beside the share on the tile, with the readings that moved with no call", async () => {
    const ext = external();
    // A reading of the linked seat moved with no reported call and no chunk
    // beside it: the server books it to nobody whole (not split). 4.2 + 1.8 + 0.5 + 1.5 = 8.
    ext.meter.steps.push(step("2026-11-17T19:10:00Z", "moved_without_reported_call", 0.5));
    ext.meter.credentials[0] = seat(SEAT, ["operator: home"], { real_usd: 8, unsplit_steps: 1, unsplit_real_usd: 1.5,
      unattributed_real_usd: 0.5, unattributed_points: 1, unattributed_steps: 1,
      unattributed_no_call_real_usd: 0.5, unattributed_no_call_points: 1 });
    serveCosts(cells(ext));
    const tile = await screen.findByRole("group", { name: t("costs.outside.tile") });
    const o = outsideTotals(ext)!;
    // Of the whole dent: the linked seat's 8 and the other seat's 6.2.
    expect(o.meter.share).toBeCloseTo(4.2 / 14.2, 12);
    expect(o.meter.unattributed).toBeCloseTo(0.5, 12);
    expect(o.noCall).toEqual({ steps: 1, usd: 0.5 });
    expect(o.unsplit.not_reported).toEqual({ steps: 1, usd: 1.5 });
    expect(within(tile).getByText(t("costs.outside.realShare", { usd: fmtUSD(4.2), pct: pct(4.2 / 14.2) }))).toBeTruthy();
    const sentence = t("costs.dent.unattributedSentence", { usd: fmtUSD(0.5) });
    expect(sentence).toContain("another machine on the same account");
    expect(within(tile).getByText(sentence)).toBeTruthy();
    expect(within(tile).getByText(t("costs.outside.noCall", { ...counted(1), usd: fmtUSD(0.5) }))).toBeTruthy();
    expect(tile.textContent).not.toContain(t("costs.outside.unattributedNone"));
    // Its half: moved with no call (≈); nothing beyond the rate here.
    expect(o.meter.noCall).toBeCloseTo(0.5, 12);
    expect(o.meter.beyond).toBe(0);
    expect(within(tile).getByText(t("costs.dent.unattributedNoCall", { usd: fmtUSD(0.5) }))).toBeTruthy();
    expect(tile.textContent).not.toContain(t("costs.dent.unattributedBeyond", { usd: fmtUSD(0) }));
  });

  it("counts only the readings of seats a source names, one line per reason, in the singular for one", async () => {
    serveCosts();
    const tile = await screen.findByRole("group", { name: t("costs.outside.tile") });
    const o = outsideTotals(external())!;
    expect(o.unsplit.not_reported).toEqual({ steps: 1, usd: 1.5 });
    // The unlinked seat's reading that moved with no call is not counted.
    expect(o.noCall.steps).toBe(0);
    expect(o.meter.unattributed).toBe(0);
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
    fireEvent.click(screen.getByRole("button", { name: t("costs.outside.modeCombined") }));
    const combined = screen.getByRole("group", { name: t("costs.outside.combinedTotal") });
    // The page's own words for a narrowed figure: "selected agents … plus outside sessions".
    expect(combined.textContent).toContain(`selected agents ${fmtUSD(7)} · plus outside sessions ${fmtUSD(15.75)}`);
    expect(combined.textContent).not.toContain(`covey agents (internal) ${fmtUSD(7)}`);
  });

  it("changes the total only when combined, says so, and leaves the agents' own figure alone", async () => {
    serveCosts();
    await screen.findByRole("group", { name: t("costs.outside.tile") });
    // the page's lead figure (#120): one list-price tile
    const agentsKpi = () => screen.getAllByText(t("costs.lead.label"), { exact: true })
      .map((el) => el.closest(".card")).find((c) => c?.parentElement?.className.includes("gap-3"))!.textContent;
    const before = agentsKpi();
    expect(before).toContain(fmtUSD(7));
    expect(screen.queryByRole("group", { name: t("costs.outside.combinedTotal") })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: t("costs.outside.modeCombined") }));
    const combined = screen.getByRole("group", { name: t("costs.outside.combinedTotal") });
    expect(within(combined).getByText(fmtUSD(7 + 15.75))).toBeTruthy();
    expect(combined.textContent).toContain(`covey agents (internal) ${fmtUSD(7)} · outside sessions ${fmtUSD(15.75)}`);
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
    expect(combined.textContent).toContain(`covey agents (internal) ${fmtUSD(7)} · outside sessions no list price (`);
    expect(combined.textContent).not.toContain(`outside sessions ${fmtUSD(0)}`);
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
    // The lead figure's chip stays (#120: covey's price table is not a chip of its own by default).
    expect(chips(t("costs.lead.label")).length).toBeGreaterThan(0);
    expect(chips(t("costs.lead.label")).every((b) => !(b as HTMLButtonElement).disabled)).toBe(true);
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
    // F3b: at most, and of the whole dent (4.2 of 6).
    expect(real.querySelector("[data-value]")!.textContent).toBe(`≈ ${fmtUSD(4.2)}`);
    expect(real.textContent).toContain(t("costs.outside.ofMeter", { pct: pct(0.7), dent: fmtUSD(6) }));
    expect(real.textContent).toContain(t("costs.outside.unattributedNone"));
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

  it("words readings under the usage basis as billed by usage: no dent, not unpriced, never $0 (0151)", async () => {
    // The seat went to the usage basis at 19:00: the 19:30 reading is out of
    // the dent and of the split, its points the usage charges'.
    const usage = { ...step("2026-11-17T19:30:00Z", "usage", null, { points: 4 }) };
    expect(stepKind(usage)).toBe("usage");
    const { container } = serveView(view({ steps: [step("2026-11-17T18:30:00Z", "split", 6, { outside: 4.2 }), usage],
      seatSums: { step_count: 2, split_steps: 1, usage_steps: 1, usage_points: 4 } }));
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const line = container.querySelector(`[data-seat='${SEAT}#0']`)!.textContent!;
    expect(line).toContain(t("costs.outside.usageReadings", counted(1)));
    expect(line).not.toContain(t("costs.outside.unpricedReadings", counted(1)));
    expect(line).toContain(fmtUSD(6));
    const row = container.querySelector("[data-meter-row='usage']")!;
    expect(row.getAttribute("data-meter-period")).toBe(String(Date.parse(H19)));
    expect(row.textContent).toContain(t("costs.outside.usageReadings", counted(1)));
    expect(row.textContent).not.toContain(fmtUSD(0));
  });

  it("shows a seat on usage all through with no real cost of its own: –, never $0 (0151)", async () => {
    const usage = step("2026-11-17T19:30:00Z", "usage", null, { points: 4 });
    const { container: all } = serveView(view({ steps: [usage],
      seatSums: { real_usd: 0, external_real_usd: 0, covey_real_usd: 0, share: null, step_count: 1, split_steps: 0, usage_steps: 1, usage_points: 4 } }));
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const only = all.querySelector(`[data-seat='${SEAT}#0']`)!.textContent!;
    expect(only).toContain(t("costs.outside.seatNoShare", { real: "–" }));
    expect(only).not.toContain(fmtUSD(0));
    expect(only).not.toContain(t("costs.dent.rateUnknownSeat", { seat: `${SEAT} #0` }));
  });

  it("says no reading for a source whose seat has none in a period, though another seat has one", async () => {
    const lab = VIEW_CELLS.slice(2, 3).map((c) => ({ ...c, source: "operator: lab" }));
    const { container } = serveView(view({ extra: { source: "operator: lab", key: OTHER_SEAT }, cells: [...VIEW_CELLS, ...lab],
      steps: [step("2026-11-17T18:30:00Z", "split", 6, { outside: 4.2 }), step("2026-11-17T19:30:00Z", "split", 2, { outside: 1 })] }));
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const row = container.querySelector<HTMLTableRowElement>(`[data-meter-period='${Date.parse(H19)}']`)!;
    expect(row.getAttribute("data-meter-row")).toBe("split");
    expect(row.textContent).toContain(t("costs.outside.noReading"));
    expect(row.querySelectorAll("td")[1].textContent).toBe(`≈ ${fmtUSD(1)}`);
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
    expect(steps.map(stepKind)).toEqual(["split", "not_reported", "not_reported", "unattributed",
      "outside_unpriced", "unpriced", "idle", "split", "outside_unpriced"]);
    const { container } = serveView(view({ steps }));
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const row = (h: number) => {
      const rows = [...container.querySelectorAll<HTMLTableRowElement>("[data-meter-table] tbody tr")];
      return rows.find((r) => r.getAttribute("data-meter-period") === String(Date.parse(at(h, 0))))!;
    };
    const cellsOf = (h: number) => [...row(h).querySelectorAll("td")].map((c) => c.textContent);
    const kind = (h: number) => row(h).getAttribute("data-meter-row");

    // The columns: real, outside (≈), covey (≈), unattributed (beyond the
    // rate ≥ where a reading was split on a seat with a rate — this one has
    // none, so – —, moved with no call ≈), not split (≈), note.
    expect(kind(17)).toBe("split");
    expect(cellsOf(17).slice(0, 5)).toEqual([fmtUSD(2), `≈ ${fmtUSD(1.4)}`, `≈ ${fmtUSD(0.6)}`, "–", "–"]);

    // Not reported, twice in one hour: their real cost not split, no part, the plural.
    expect(kind(18)).toBe("unsplit");
    expect(cellsOf(18)).toEqual([fmtUSD(2), "–", "–", "–", `≈ ${fmtUSD(2)}`,
      t("costs.outside.unsplit_not_reported", { ...counted(2), usd: fmtUSD(2) })]);
    // Moved with no reported call, no chunk beside it: unattributed whole as
    // the server has it, not split to
    // nobody; never Outside $0.00 / covey $0.00.
    expect(kind(19)).toBe("unattributed");
    expect(cellsOf(19)).toEqual([fmtUSD(0.9), "–", "–", `≈ ${fmtUSD(0.9)}`, "–",
      t("costs.outside.noCall", { ...counted(1), usd: fmtUSD(0.9) })]);
    expect(kind(20)).toBe("unsplit");
    expect(cellsOf(20)).toEqual([fmtUSD(0.46), "–", "–", "–", `≈ ${fmtUSD(0.46)}`,
      t("costs.outside.unsplit_outside_unpriced", { ...counted(1), usd: fmtUSD(0.46) })]);
    // No plan price: no real cost at all, counted.
    expect(kind(21)).toBe("unpriced");
    expect(cellsOf(21)).toEqual(["–", "–", "–", "–", "–", t("costs.outside.unpricedReadings", counted(1))]);
    // Idle: neither split nor unsplit nor unattributed.
    expect(kind(22)).toBe("idle");
    expect(cellsOf(22).slice(1)).toEqual(["–", "–", "–", "–", t("costs.outside.idleHere")]);
    // Split and not split in one hour: the split part, and the rest named.
    expect(kind(23)).toBe("partial");
    expect(cellsOf(23)).toEqual([fmtUSD(1.3), `≈ ${fmtUSD(0.25)}`, `≈ ${fmtUSD(0.75)}`, "–", `≈ ${fmtUSD(0.3)}`,
      t("costs.outside.unsplit_outside_unpriced", { ...counted(1), usd: fmtUSD(0.3) })]);
    for (const h of [18, 19, 20, 21]) expect(cellsOf(h).slice(1, 3).join()).not.toContain(fmtUSD(0));
    for (const h of [18, 20, 21, 22]) expect(cellsOf(h)[3]).toBe("–");

    // The seat line counts them per reason, as the rows do.
    const seatLine = container.querySelector(`[data-seat='${SEAT}#0']`)!.textContent!;
    expect(seatLine).toContain(t("costs.outside.unsplit_not_reported", { ...counted(2), usd: fmtUSD(2) }));
    expect(seatLine).toContain(t("costs.outside.noCall", { ...counted(1), usd: fmtUSD(0.9) }));
    expect(seatLine).toContain(t("costs.outside.unsplit_outside_unpriced", { ...counted(2), usd: fmtUSD(0.76) }));
    expect(seatLine).toContain(t("costs.outside.unpricedReadings", counted(1)));
    // A split seat without a rate (no run of 5 points) says so.
    expect(container.querySelector(`[data-seat='${SEAT}#0'] [data-seat-rate]`)!.textContent)
      .toBe(t("costs.dent.rateUnknownSeat", { seat: `${SEAT} #0` }));
  });

  it("shows an unattributed part per period and per seat, and the parts add up to the real cost", async () => {
    const at = (h: number, m: number) => `2026-11-17T${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:00Z`;
    // 18:00 — a split reading with 1.5 of its 6 unattributed (its chunk's
    // points beyond the rate) and one that moved with no call and no chunk
    // beside it (0.8, unattributed whole);
    // 19:00 — one not reported (1) and a split one with none unattributed.
    const steps = [
      step(at(18, 30), "split", 6, { outside: 3, unattributed: 1.5 }),
      step(at(18, 50), "moved_without_reported_call", 0.8),
      step(at(19, 20), "not_reported", 1),
      step(at(19, 40), "split", 2, { outside: 1 }),
    ];
    const seatSums = { real_usd: 9.8, external_real_usd: 4, covey_real_usd: 2.5, unattributed_real_usd: 2.3, unattributed_points: 2,
      unattributed_steps: 2, unattributed_no_call_real_usd: 0.8, unattributed_no_call_points: 1,
      unsplit_real_usd: 1, unsplit_steps: 1, step_count: 4, split_steps: 2, share: 4 / 6.5 };
    const v = view({ steps, seatSums });
    v.meter.credentials[0] = seat(SEAT, ["operator: home"], seatSums, 2);
    const { container } = serveView(v);
    await screen.findByRole("table", { name: t("costs.outside.byModel") });

    // Per period: outside + covey + unattributed + not split = real.
    const rows = meterPerPeriod(v, v.cells, "hour", null);
    expect(rows.length).toBe(2);
    expect(rows[0].unattributed.usd).toBeCloseTo(2.3, 12);
    expect(rows[0].unattributed.noCall).toEqual({ steps: 1, usd: 0.8 });
    expect(rows[0].unattributed.beyond).toBeCloseTo(1.5, 12);
    expect(rows[1].unattributed.usd).toBe(0);
    expect(rows.map((r) => r.rated)).toEqual([true, true]);
    for (const r of rows) {
      expect(r.outside + r.covey + r.unattributed.usd + unsplitUSD(r.unsplit)).toBeCloseTo(r.real!, 12);
    }
    const row = (iso: string) => container.querySelector<HTMLTableRowElement>(`[data-meter-period='${Date.parse(iso)}']`)!;
    const cellsOf = (iso: string) => [...row(iso).querySelectorAll("td")].map((c) => c.textContent);
    expect(row(H18).getAttribute("data-meter-row")).toBe("partial");
    expect(cellsOf(H18)).toEqual([fmtUSD(6.8), `≈ ${fmtUSD(3)}`, `≈ ${fmtUSD(1.5)}`, `≥ ${fmtUSD(1.5)}≈ ${fmtUSD(0.8)}`, "–",
      t("costs.outside.noCall", { ...counted(1), usd: fmtUSD(0.8) })]);
    expect(cellsOf(H19).slice(0, 5)).toEqual([fmtUSD(3), `≈ ${fmtUSD(1)}`, `≈ ${fmtUSD(1)}`, `≥ ${fmtUSD(0)}`, `≈ ${fmtUSD(1)}`]);

    // Per seat: the four parts, the share of its whole real cost (at most),
    // the readings that moved with no call, and its rate (1 / k).
    const seatLine = container.querySelector(`[data-seat='${SEAT}#0']`)!.textContent!;
    expect(seatLine).toContain(t("costs.outside.seatLine", { real: fmtUSD(9.8), outside: fmtUSD(4), pct: pct(4 / 9.8),
      covey: fmtUSD(2.5), unattributed: `${t("costs.dent.unattributedBeyond", { usd: fmtUSD(1.5) })}, ${t("costs.dent.unattributedNoCall", { usd: fmtUSD(0.8) })}` }));
    expect(seatLine).toContain(t("costs.outside.noCall", { ...counted(1), usd: fmtUSD(0.8) }));
    expect(seatLine).toContain(t("costs.outside.unsplit_not_reported", { ...counted(1), usd: fmtUSD(1) }));
    expect(seatLine).toContain(t("costs.dent.rateSeat", { seat: `${SEAT} #0`, usd: fmtUSD(0.5) }));

    // The tile: ≈, of the dent, with the unattributed sentence and its halves beside it.
    const tile = screen.getByRole("group", { name: t("costs.outside.meterShare") });
    expect(tile.querySelector("[data-value]")!.textContent).toBe(`≈ ${fmtUSD(4)}`);
    expect(within(tile).getByText(t("costs.outside.ofMeter", { pct: pct(4 / 9.8), dent: fmtUSD(9.8) }))).toBeTruthy();
    expect(within(tile).getByText(t("costs.dent.unattributedSentence", { usd: fmtUSD(2.3) }))).toBeTruthy();
    expect(within(tile).getByText(`${t("costs.dent.unattributedBeyond", { usd: fmtUSD(1.5) })} · ${t("costs.dent.unattributedNoCall", { usd: fmtUSD(0.8) })}`)).toBeTruthy();
  });

  it("names a seat whose only movement is unattributed, with no $0 for the parts", async () => {
    const steps = [step("2026-11-17T18:30:00Z", "moved_without_reported_call", 0.7)];
    const seatSums = { real_usd: 0.7, external_real_usd: 0, covey_real_usd: 0, unattributed_real_usd: 0.7, unattributed_points: 1,
      unattributed_steps: 1, unattributed_no_call_real_usd: 0.7, unattributed_no_call_points: 1, step_count: 1, split_steps: 0, share: null };
    const { container } = serveView(view({ steps, seatSums }));
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const line = container.querySelector(`[data-seat='${SEAT}#0']`)!.textContent!;
    expect(line).toContain(t("costs.outside.seatNoShareUnattributed", { real: fmtUSD(0.7), unattributed: t("costs.dent.unattributedNoCall", { usd: fmtUSD(0.7) }) }));
    expect(line).toContain(t("costs.outside.noCall", { ...counted(1), usd: fmtUSD(0.7) }));
    expect(line).not.toContain(fmtUSD(0));
    expect(container.querySelector(`[data-seat='${SEAT}#0'] [data-seat-rate]`)).toBeNull();
    const tile = screen.getByRole("group", { name: t("costs.outside.meterShare") });
    expect(tile.querySelector("[data-value]")!.textContent).toBe("–");
    expect(within(tile).getByText(t("costs.outside.noShare"))).toBeTruthy();
    expect(within(tile).getByText(t("costs.dent.unattributedSentence", { usd: fmtUSD(0.7) }))).toBeTruthy();
    const row = container.querySelector<HTMLTableRowElement>(`[data-meter-period='${Date.parse(H18)}']`)!;
    expect(row.getAttribute("data-meter-row")).toBe("unattributed");
    expect([...row.querySelectorAll("td")].map((c) => c.textContent).slice(0, 5)).toEqual([fmtUSD(0.7), "–", "–", `≈ ${fmtUSD(0.7)}`, "–"]);
  });

  it("splits a reading that moved with no call with its chunk where the server does, and notes incomplete chunks", async () => {
    // F3c: the meter's lag after a chunk (+1 with no call) joins it and takes
    // its share: split, its reason kept, nothing unattributed — the page
    // reads the server's figures, never the reason.
    const lag = step("2026-11-17T18:50:00Z", "moved_without_reported_call", 1, { folded: true, outside: 0.7 });
    expect(lag.split).toBe(true);
    expect(lag.reason).toBe("moved_without_reported_call");
    expect(stepKind(lag)).toBe("split");
    const steps = [step("2026-11-17T18:30:00Z", "split", 6, { outside: 4.2 }), lag];
    const seatSums = { real_usd: 7, external_real_usd: 4.9, covey_real_usd: 2.1, step_count: 2, split_steps: 2, incomplete_chunks: 1, share: 0.7 };
    const v = view({ steps, seatSums });
    const { container } = serveView(v);
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const rows = meterPerPeriod(v, v.cells, "hour", null);
    expect(rows[0].unattributed).toEqual({ usd: 0, beyond: 0, noCall: { steps: 0, usd: 0 } });
    // The seat has no rate (k): the period's part beyond the rate is no
    // bound, so "–", never "≥ $0.00" (the per-period column, as the seat line).
    expect(rows[0].rated).toBe(false);
    const row = container.querySelector<HTMLTableRowElement>(`[data-meter-period='${Date.parse(H18)}']`)!;
    expect(row.getAttribute("data-meter-row")).toBe("split");
    expect([...row.querySelectorAll("td")].map((c) => c.textContent)).toEqual([fmtUSD(7), `≈ ${fmtUSD(4.9)}`, `≈ ${fmtUSD(2.1)}`,
      "–", "–", ""]);
    const seatLine = container.querySelector(`[data-seat='${SEAT}#0']`)!.textContent!;
    expect(seatLine).not.toContain(t("costs.outside.noCall", { ...counted(1), usd: fmtUSD(1) }));
    // The seat has no rate (k): never "beyond the rate: at least $0.00",
    // but "no rate yet", as the headline says (F3c review).
    expect(seatLine).toContain(t("costs.outside.seatLine", { real: fmtUSD(7), outside: fmtUSD(4.9), pct: pct(0.7), covey: fmtUSD(2.1),
      unattributed: t("costs.dent.unattributedNoRate") }));
    expect(seatLine).not.toContain(t("costs.dent.unattributedBeyond", { usd: fmtUSD(0) }));
    // A chunk with use not in its list price: its parts are approximate.
    const note = t("costs.dent.incompleteNote", { n: "1" });
    expect(seatLine).toContain(note);
    expect(within(screen.getByRole("group", { name: t("costs.outside.meterShare") })).getByText(note)).toBeTruthy();
  });

  it("says over the drawn chart's slot that a period had no list price, not only in the table", async () => {
    const onlyUnpriced = [...VIEW_CELLS, mc(H20, "main", "claude-sonnet-5-7", [10, 50, 2_000, 0, 0], false)];
    const { container } = serveView(view({ cells: onlyUnpriced }));
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const plot = container.querySelector("svg[role=group]")!;
    expect([...plot.querySelectorAll("text[data-note]")].map((n) => n.textContent)).toEqual([t("costs.outside.noPrice")]);
  });

  it("gives a period whose calls had no list price no bar and says so", async () => {
    const onlyUnpriced = [...VIEW_CELLS, mc(H20, "main", "claude-sonnet-5-7", [10, 50, 2_000, 0, 0], false)];
    serveView(view({ cells: onlyUnpriced }));
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    fireEvent.click(screen.getByRole("button", { name: t("costs.fig.showAsTable") }));
    const chart = screen.getByRole("table", { name: t("costs.outside.chartTitle") });
    // The notes column is headed as a note, not as the meter's.
    expect(within(chart).getByRole("columnheader", { name: t("costs.outside.colNote") })).toBeTruthy();
    const rows = within(chart).getAllByRole("row").filter((r) => r.closest("tbody"));
    expect(rows.length).toBe(3);
    const last = rows[2];
    expect(last.textContent).toContain(t("costs.outside.noPrice"));
    expect(last.textContent).not.toContain(fmtUSD(0));
  });

  it("with ?source=, says the meter share covers the other sources on the meter", async () => {
    // lab has calls on the same seat; a project folder with no usage (claim
    // only, 0 calls) is on the seat too, and is never named (#120).
    const v = view({ source: "operator: home", seatSources: ["operator: home", "operator: lab", "AppData-Local-Temp-claim-only"],
      steps: [step("2026-11-17T18:30:00Z", "split", 6, { outside: 4.2 })] });
    v.sources = [...v.sources, { ...v.sources[0], source: "operator: lab" },
      { ...v.sources[0], source: "AppData-Local-Temp-claim-only", calls: 0, list_usd: null, reported_usd: null }];
    serveView(v, "/costs/outside?days=1&bucket=hour&source=operator%3A%20home");
    await screen.findByRole("table", { name: t("costs.outside.byModel") });
    const real = screen.getByRole("group", { name: t("costs.outside.meterShare") });
    expect(within(real).getByText(t("costs.outside.tileOtherSources", { sources: "operator: lab" }))).toBeTruthy();
    expect(screen.getByText(t("costs.outside.sourceNote"))).toBeTruthy();
    // Not in the source chips, the tile or the seat line.
    expect(document.body.textContent).not.toContain("AppData-Local-Temp-claim-only");
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
