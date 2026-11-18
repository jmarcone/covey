import { describe, it, expect, beforeEach } from "vitest";
import { fireEvent, screen, within } from "@testing-library/react";
import Costs from "./Costs";
import type { CostCells, ExternalMeterHour, ExternalMeterTotal, ExternalUsage } from "../api";
import i18n from "../i18n";
import { fmtNumber, fmtUSD } from "../format";
import { mockFetch, renderWithProviders } from "../test/render";
import { credentialFigures, perCredentialTotals, splitPerPeriod, subscriptionDent } from "./costFigures";

// #120, the headline: once Claude Code sessions outside covey report on a
// subscription meter, the page shows the whole dent in the meters, split
// between covey's agents and those sessions, with list $ per real $ for
// each and for both together. An hour the outside sessions did not report
// is split to nobody — never booked to covey whole — and says so. The
// headline, the chart's real bars and the agents' real share read the same
// hours, so they add up to the same figures.

const SEAT = "claude_code_oauth_token";
const PP = 200 * 12 / 52 / 100; // $200 a month: $0.4615 per point
const H18 = "2026-11-17T18:00:00Z", H19 = "2026-11-17T19:00:00Z", H20 = "2026-11-17T20:00:00Z";
const times = (v: number) => `${fmtNumber(v, 1)}×`;

function hour(at: string, points: number, reported: boolean, outList: number, covList: number): ExternalMeterHour {
  const real = points * PP;
  const out = reported ? (outList > 0 ? real * outList / (outList + covList) : 0) : null;
  return {
    hour: at, secret_key: SEAT, secret_slot: 0, real_usd: real, points, outside_reported: reported,
    external_list_usd: outList, covey_list_usd: covList,
    external_real_usd: out, covey_real_usd: out == null ? null : real - out,
  };
}

/** The server's split as it would come for these hours: the sums are the
 *  hours', as meterShare builds them. */
function external(hours: ExternalMeterHour[], runs = 1): ExternalUsage {
  const split = hours.filter((h) => h.outside_reported && h.real_usd != null);
  const unsplit = hours.filter((h) => !h.outside_reported && h.real_usd != null);
  const sum = (hs: ExternalMeterHour[], f: (h: ExternalMeterHour) => number) => hs.reduce((a, h) => a + f(h), 0);
  const outR = sum(split, (h) => h.external_real_usd!), covR = sum(split, (h) => h.covey_real_usd!);
  const sums = {
    real_usd: sum(hours, (h) => h.real_usd ?? 0), external_real_usd: outR, covey_real_usd: covR,
    unsplit_real_usd: sum(unsplit, (h) => h.real_usd!), share: outR + covR > 0 ? outR / (outR + covR) : null,
    split_external_list_usd: sum(split, (h) => h.external_list_usd), split_covey_list_usd: sum(split, (h) => h.covey_list_usd),
    split_points: sum(split, (h) => h.points), split_runs: runs, point_rate: split.length > 0 ? PP : 0,
    unsplit_hours: unsplit.length, unmetered_list_usd: 0,
  };
  const cred: ExternalMeterTotal = { ...sums, secret_key: SEAT, secret_slot: 0, meter_source: "usage_cmd",
    sources: ["operator: home"], hours_with_reading: hours.length, hours_without_reading: 0, points: sum(hours, (h) => h.points) };
  return {
    sources: [{ source: "operator: home", meter_credential: { secret_key: SEAT, secret_slot: 0 }, calls: 30,
      list_usd: sums.split_external_list_usd, reported_usd: null, unpriced_tokens: 0, unpriced_calls: 0, unpriced_models: [],
      first_at: H18, last_at: H19 }],
    cells: [],
    meter: { ...sums, hours, credentials: [cred] },
  };
}

/** The cost cells of one seat over three hours, with the meter's steps; the
 *  outside sessions reported 18:00 and 19:00, not 20:00. covey's list is
 *  $303 and the outside sessions' $455 over the split hours, the proportions
 *  of the day #120 measured. */
function cells(ext: ExternalUsage | undefined, points = [4, 2, 3]): CostCells {
  const periods = [H18, H19, H20];
  const step = (period: string, p: number) => ({ period, secret_key: SEAT, secret_slot: 0, credential_kind: "subscription",
    source: "usage_cmd", window_kind: "seven_day", points: p, readings: 2, resets: 0, longest_gap_secs: 900, usd: p * PP, unpriced_points: 0 });
  const cell = (period: string, agent: string, output: number) => ({ period, agent_id: agent, secret_key: SEAT, secret_slot: 0,
    credential_kind: "subscription", reported_usd: output, measured: { output }, engine_usd: output, tokens: { output: output * 50_000 } });
  const total = points.reduce((a, p) => a + p, 0);
  return {
    periods,
    agents: [{ id: "a1", label: "Coordinator", reported_usd: 0, measured_usd: 0 }, { id: "a2", label: "Developer", reported_usd: 0, measured_usd: 0 }],
    types: ["cache_read", "cache_write", "input", "output", "web_search", "unpriced", "unattributed"],
    cells: [cell(H18, "a1", 101), cell(H18, "a2", 50), cell(H19, "a2", 152), cell(H20, "a1", 40)],
    subscription: periods.map((p, i) => step(p, points[i])),
    meter: [{
      secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd", window_kind: "seven_day",
      baseline_at: H18, baseline_percent: 10, baseline_before_window: true, last_at: "2026-11-17T20:40:00Z",
      last_percent: 10 + total, readings: 6, points: total, points_low: total - 1, points_high: total + 1, longest_gap_secs: 900,
      segments: null, measured_usd: 343,
    }],
    plans: [{
      runtime_id: null, ord: 0, secret_key: SEAT, secret_slot: 0, plan_name: "Max", amount: 200, currency: "USD", period: "month",
      tax_basis: "net", fx_to_usd: 1, fx_date: null, valid_from: "2026-11-01T00:00:00Z", valid_to: null,
      weekly_usd: 200 * 12 / 52, usd_per_point: PP,
    }],
    bookings: [],
    external: ext,
  };
}

const HOURS = [hour(H18, 4, true, 300, 151), hour(H19, 2, true, 155, 152), hour(H20, 3, false, 0, 40)];

function serve(data: CostCells) {
  mockFetch({
    "/api/v1/cost/cells": data,
    "/api/v1/cost/billed": { bucket: "hour", points: [], sync: { configured: false, last_attempt_at: null, last_success_at: null,
      last_daily_at: null, data_refreshed_at: null, last_status: "", last_error: "" } },
    "/api/v1/cost/runs": [],
    "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
    "/api/v1/cost/usage-readings": [],
    "/api/v1/cost/indicators": null,
  });
  return renderWithProviders(<Costs />);
}

describe("subscriptionDent", () => {
  it("splits the dent into covey, the outside sessions and the unreported hours, and they add up", () => {
    const d = subscriptionDent(external(HOURS))!;
    expect(d.real).toBeCloseTo(9 * PP, 9);
    expect(d.unsplit.real).toBeCloseTo(3 * PP, 9);
    expect(d.unsplit.hours).toBe(1);
    expect(d.covey.real + d.outside.real + d.unsplit.real).toBeCloseTo(d.real, 9);
    // The unreported hour is nobody's: covey's part is its share of the split hours only.
    const out18 = 4 * PP * 300 / 451, out19 = 2 * PP * 155 / 307;
    expect(d.outside.real).toBeCloseTo(out18 + out19, 9);
    expect(d.covey.real).toBeCloseTo(6 * PP - out18 - out19, 9);
    expect(d.covey.list).toBe(303);
    expect(d.outside.list).toBe(455);
    expect(d.covey.share! + d.outside.share! + d.unsplit.share!).toBeCloseTo(1, 9);
  });

  it("gives three multiples over the split hours, each with the range the whole percents leave", () => {
    const d = subscriptionDent(external(HOURS))!;
    const split = 6 * PP;
    expect(d.together.multiple).toEqual({ kind: "estimate", value: 758 / split, low: 758 / (split + PP), high: 758 / (split - PP) });
    expect(d.covey.multiple!.kind === "estimate" && d.covey.multiple!.value).toBeCloseTo(303 / d.covey.real, 9);
    expect(d.outside.multiple!.kind === "estimate" && d.outside.multiple!.value).toBeCloseTo(455 / d.outside.real, 9);
    // A part's range is its share of the whole's: the same relative width.
    const m = d.covey.multiple!;
    expect(m.low / (m.kind === "estimate" ? m.value : 1)).toBeCloseTo(split / (split + PP), 9);
  });

  it("gives no multiple below 3 points of split movement, nor without a split hour", () => {
    const small = subscriptionDent(external([hour(H18, 2, true, 10, 10), hour(H19, 5, false, 0, 4)]))!;
    expect(small.noMultiple).toBe("movement");
    expect(small.together.multiple).toBeNull();
    const none = subscriptionDent(external([hour(H18, 5, false, 3, 10)]))!;
    expect(none.noMultiple).toBe("split");
    expect(none.covey.real).toBe(0);
    expect(none.unsplit.real).toBeCloseTo(5 * PP, 9);
  });

  it("is absent until a source names a meter", () => {
    expect(subscriptionDent(undefined)).toBeNull();
    const e = external(HOURS);
    expect(subscriptionDent({ ...e, meter: { ...e.meter, hours: [], credentials: [] } })).toBeNull();
  });
});

describe("one rule for the headline, the chart and the agents", () => {
  it("sums the hours into any bucket, and the periods add up to the dent", () => {
    const ext = external(HOURS);
    const d = subscriptionDent(ext)!;
    const hourly = splitPerPeriod(ext, SEAT, 0, "hour");
    expect(hourly.get(Date.parse(H20))).toEqual({ covey: 0, outside: 0, unsplit: 3 * PP, unsplitHours: 1 });
    const daily = [...splitPerPeriod(ext, SEAT, 0, "day").values()];
    expect(daily).toHaveLength(1);
    expect(daily[0].covey).toBeCloseTo(d.covey.real, 9);
    expect(daily[0].outside).toBeCloseTo(d.outside.real, 9);
    expect(daily[0].unsplit).toBeCloseTo(d.unsplit.real, 9);
    // The bars' total is the meter's movement the steps carry: 9 points.
    expect(daily[0].covey + daily[0].outside + daily[0].unsplit).toBeCloseTo(9 * PP, 9);
  });

  it("gives the agents a share of covey's part of the meter only, and no whole-meter multiple", () => {
    const data = cells(external(HOURS));
    const [seat] = credentialFigures({ cells: data, bucketMs: 3600_000, windowEnd: new Date("2026-11-17T21:00:00Z") });
    const d = subscriptionDent(data.external)!;
    expect(seat.split).not.toBeNull();
    expect(seat.multiple).toBeNull();
    expect(seat.noMultiple).toBe("outside");
    const shares = seat.agents.reduce((a, x) => a + x.realShare!, 0);
    expect(shares).toBeCloseTo(d.covey.real, 9);
    expect(shares).toBeLessThan(seat.real!.value);
    // Without outside sessions the page is as it was: the whole movement, and the multiple.
    const [before] = credentialFigures({ cells: cells(undefined), bucketMs: 3600_000, windowEnd: new Date("2026-11-17T21:00:00Z") });
    expect(before.split).toBeNull();
    expect(before.multiple).not.toBeNull();
    expect(before.agents.reduce((a, x) => a + x.realShare!, 0)).toBeCloseTo(before.real!.value, 9);
    // The per-model card's real-cost row sets no list price beside a meter
    // the outside sessions move too: that would be covey's multiple over it.
    expect(perCredentialTotals([seat]).real!.recomputed).toBeNull();
    expect(perCredentialTotals([before]).real!.recomputed).toBe(343);
  });
});

describe("Costs — the headline", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("shows the dent, its split, the plain sentence and the three multiples, as the API's figures give them", async () => {
    const data = cells(external(HOURS));
    const d = subscriptionDent(data.external)!;
    serve(data);
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    const value = (name: string) => within(within(row).getByRole("group", { name })).getByText(/^≈ /).textContent;
    expect(value("Real cost of the meters")).toBe(`≈ ${fmtUSD(d.real)}`);
    expect(value("covey's agents")).toBe(`≈ ${fmtUSD(d.covey.real)}`);
    expect(value("Sessions outside covey")).toBe(`≈ ${fmtUSD(d.outside.real)}`);
    expect(value("Not split")).toBe(`≈ ${fmtUSD(d.unsplit.real)}`);
    expect(within(row).getByRole("group", { name: "Not split" }).textContent).toMatch(/outside sessions not reported for 1 h/);

    expect(row.querySelector("[data-dent-sentence]")!.textContent).toBe(
      `Your subscription paid ≈ ${fmtUSD(d.together.real)} real for ${fmtUSD(758)} of list-price work: ` +
      `${fmtUSD(303)} by covey's agents, ${fmtUSD(455)} by the sessions outside covey that drive them.`);
    const m = (x: typeof d.covey) => (x.multiple?.kind === "estimate" ? times(x.multiple.value) : "");
    expect(row.querySelector("[data-dent-multiples]")!.textContent).toBe(
      `List price per real dollar: ≈ ${m(d.covey)} for covey's agents, ≈ ${m(d.outside)} for the sessions outside covey ` +
      `and ≈ ${m(d.together)} for both together (range ${times(d.together.multiple!.low)} to ${times(d.together.multiple!.high)}).`);
    expect(row.querySelector("[data-dent-unsplit]")!.textContent).toMatch(/not booked to covey's agents/);
  });

  it("is not on the page while no outside source names a meter", async () => {
    serve(cells(undefined));
    expect(await screen.findByRole("group", { name: "Real cost (estimate)" })).toBeInTheDocument();
    expect(document.querySelector("[data-dent-row]")).toBeNull();
    expect(document.querySelector("[data-split-note]")).toBeNull();
  });

  it("stays with an agent filter, marked as organisation-wide: the meter is not the agents'", async () => {
    serve(cells(external(HOURS)));
    const row = await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(row.querySelector("[data-org-wide]")).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: /Coordinator/ }));
    expect(row.querySelector("[data-org-wide]")).not.toBeNull();
  });

  it("splits the chart's real bar, says which hour was not reported, and moves the multiple to the headline", async () => {
    serve(cells(external(HOURS)));
    await screen.findByRole("region", { name: "The whole dent in the subscription" });
    expect(document.querySelector("[data-split-note]")!.textContent).toMatch(/≈ .* in 1 h the outside sessions did not report is split to nobody/);
    expect(document.querySelector("[data-no-multiple]")!.textContent).toMatch(/The split multiples are in the headline/);
    const said = await screen.findAllByText(/^No multiple here: sessions outside covey share this meter/);
    expect(said.some((e) => e.closest("[data-meter]") != null)).toBe(true);
    // The 20:00 bar: its real cost is the unreported hour's, split to nobody.
    const bar = document.querySelector(`[data-period="${H20}"]`)!;
    expect(bar.getAttribute("aria-label")).toMatch(/Real cost \(estimate\) /);
    fireEvent.focus(bar);
    const tip = await screen.findByRole("tooltip");
    expect(tip.textContent).not.toMatch(/covey's agents/);
    fireEvent.blur(bar);
    const bar18 = document.querySelector(`[data-period="${H18}"]`)!;
    fireEvent.focus(bar18);
    const tip18 = await screen.findByRole("tooltip");
    expect(tip18.textContent).toMatch(/covey's agents/);
    expect(tip18.textContent).toMatch(/Sessions outside covey/);
  });
});
