import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import Costs, { BasisKpi } from "./Costs";
import { fmtUSD, fmtUSDBelow } from "../format";
import { CostInsights } from "./CostInsights";
import type { CostCells } from "../api";
import type { CredentialFigures } from "./costFigures";
import i18n from "../i18n";
import { mockFetch, renderWithProviders, useGerman } from "../test/render";

// The Basis switch on the Details page (cost home PR 9): the chart leads with the basis's series, set by the basis and
// not switchable off; at 10 minutes there is no figure; per agent the token price stays and says why; the runs, the
// price list and the per-model columns keep the token price with a lock chip; CostInsights puts the basis's row on
// without touching the remembered columns. Assertions use data-* attributes: the new strings are not in the catalogue yet.

const SEAT = "claude_code_oauth_token";
const PP = 200 * 12 / 52 / 100;

function cells(): CostCells {
  const cell = (period: string, output: number, cacheRead: number) => ({
    period, agent_id: "a1", secret_key: SEAT, secret_slot: 0, credential_kind: "subscription",
    reported_usd: 1, measured: { output, cache_read: cacheRead }, engine_usd: output + cacheRead,
    tokens: { output: output * 50_000, cache_read: cacheRead * 5_000_000 },
  });
  return {
    periods: ["2026-11-17T18:00:00Z", "2026-11-17T19:00:00Z"],
    agents: [{ id: "a1", label: "Coordinator", reported_usd: 2, measured_usd: 16 }],
    types: ["cache_read", "cache_write", "input", "output", "web_search", "unpriced", "unattributed"],
    cells: [cell("2026-11-17T18:00:00Z", 6, 2), cell("2026-11-17T19:00:00Z", 5, 3)],
    subscription: [
      { period: "2026-11-17T18:00:00Z", secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd",
        window_kind: "seven_day", points: 2, readings: 2, resets: 0, longest_gap_secs: 900, usd: 2 * PP, unpriced_points: 0 },
      { period: "2026-11-17T19:00:00Z", secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd",
        window_kind: "seven_day", points: 2, readings: 2, resets: 0, longest_gap_secs: 900, usd: 2 * PP, unpriced_points: 0 },
    ],
    meter: [{
      secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd", window_kind: "seven_day",
      baseline_at: "2026-11-17T18:00:00Z", baseline_percent: 9, baseline_before_window: false,
      last_at: "2026-11-17T20:00:00Z", last_percent: 13, readings: 4, points: 4,
      points_low: 3, points_high: 5, longest_gap_secs: 900, segments: null, measured_usd: 16,
    }],
    plans: [{
      runtime_id: null, ord: 0, secret_key: SEAT, secret_slot: 0, plan_name: "Max", amount: 200, currency: "USD", period: "month",
      tax_basis: "net", fx_to_usd: 1, fx_date: null, valid_from: "2026-11-01T00:00:00Z", valid_to: null,
      weekly_usd: 200 * 12 / 52, usd_per_point: PP,
    }],
    bookings: [],
  };
}

const NO_BILL = { bucket: "hour", points: [], sync: { configured: false, last_attempt_at: null, last_success_at: null,
  last_daily_at: null, data_refreshed_at: null, last_status: "", last_error: "" } };

function serve(query: string) {
  mockFetch({
    "/api/v1/cost/cells": cells(),
    "/api/v1/cost/billed": NO_BILL,
    "/api/v1/cost/runs": [],
    "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
    "/api/v1/cost/usage-readings": [],
  });
  return renderWithProviders(<Costs />, { route: `/?days=1&tz=utc${query}` });
}

const block = () => waitFor(() => {
  const b = document.querySelector<HTMLElement>(`[data-credential="${SEAT}#0"]`);
  if (!b) throw new Error("no block yet");
  return b;
});
const locks = () => [...document.querySelectorAll("[data-basis-lock]")].map((e) => e.getAttribute("data-basis-lock"));
const segs = (b: HTMLElement, key: string) => b.querySelectorAll(`rect[data-seg="${key}"]`).length;

describe("Details: der Chart unter der Basis", () => {
  beforeEach(() => useGerman());
  afterEach(() => vi.restoreAllMocks());

  it("token: unverändert, ohne Sperrchip und ohne gesetzte Leitreihe", async () => {
    serve("");
    const b = await block();
    expect(segs(b, "r")).toBeGreaterThan(0);
    expect(document.querySelector("[data-basis-lead-chip]")).toBeNull();
    expect(document.querySelector("[data-also-show]")).toBeNull();
    expect(locks()).toEqual([]);
    expect(document.querySelector("[data-basis-lead]")).toBeNull();
  });

  it("capacity: nur die Reihe real führt, von der Basis gesetzt und nicht abschaltbar", async () => {
    serve("&basis=capacity");
    const b = await block();
    await waitFor(() => expect(segs(b, "real")).toBeGreaterThan(0));
    expect(segs(b, "r")).toBe(0);
    const lead = document.querySelector<HTMLElement>('[data-basis-lead-chip="real"]')!;
    const btn = lead.querySelector("button")!;
    expect(btn.getAttribute("aria-pressed")).toBe("true");
    expect(btn.getAttribute("aria-disabled")).toBe("true");
    expect(lead.querySelector("[data-basis-set]")).not.toBeNull();
    fireEvent.click(btn);
    expect(segs(b, "real")).toBeGreaterThan(0);
    expect(document.querySelector("[data-also-show]")).not.toBeNull();
    // the token series is "also shown" when asked for, beside the lead
    const tokenChip = document.querySelector<HTMLElement>('[data-source-chip="reported"] button')!;
    expect(tokenChip.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(tokenChip);
    await waitFor(() => expect(segs(b, "r")).toBeGreaterThan(0));
    expect(segs(b, "real")).toBeGreaterThan(0);
  });

  it("capacity: ein alter ?hide=real-Link verbirgt die Leitreihe nicht", async () => {
    serve("&basis=capacity&hide=real");
    const b = await block();
    await waitFor(() => expect(segs(b, "real")).toBeGreaterThan(0));
    expect(document.querySelector('[data-basis-lead-chip="real"] button')!.getAttribute("aria-pressed")).toBe("true");
  });

  it("capacity: die Kachel der Plankapazität führt, in der Seitenzeile und im Block", async () => {
    serve("&basis=capacity");
    const b = await block();
    const tiles = [...b.querySelectorAll<HTMLElement>("[data-tile]")];
    expect(tiles[0].getAttribute("data-basis-lead")).toBe("capacity");
    expect(tiles[0].textContent).toMatch(/≈/);
    const kpis = [...document.querySelectorAll<HTMLElement>("[data-kpi]")];
    expect(kpis[0].getAttribute("data-basis-lead")).toBe("capacity");
    expect(kpis[0].querySelector("[data-kpi-value]")!.textContent).toMatch(/^≈ /);
    // the token tile says it is one
    expect(kpis[1].querySelector('[data-basis-tag="token"]')).not.toBeNull();
  });

  it("billed: ein Sitz hat keine Rechnung, gesagt statt $0, und die Seitenkachel nennt den Grund", async () => {
    serve("&basis=billed");
    const b = await block();
    await waitFor(() => expect(b.querySelector('[data-basis-absent="billed"] [data-no-figure]')).not.toBeNull());
    expect(segs(b, "r")).toBe(0);
    const kpi = document.querySelector<HTMLElement>('[data-kpi][data-basis-lead="billed"]')!;
    expect(kpi.querySelector('[data-no-figure="noBilledKeys"]')).not.toBeNull();
    expect(kpi.querySelector("[data-whole-key]")).not.toBeNull();
    expect(kpi.textContent).not.toMatch(/0[.,]00/);
  });

  it("10 Minuten unter capacity: keine Zahl, mit dem Weg zur Stunde", async () => {
    serve("&basis=capacity&bucket=10min");
    const b = await block();
    const note = await waitFor(() => {
      const n = document.querySelector<HTMLElement>("[data-bucket-too-fine]");
      if (!n) throw new Error("no note yet");
      return n;
    });
    expect(note.querySelector('[data-no-figure="bucketTooFine"]')).not.toBeNull();
    expect(b.querySelector("[data-bucket-too-fine-block]")).not.toBeNull();
    expect(segs(b, "r")).toBe(0);
    fireEvent.click(note.querySelector("[data-use-hourly]")!);
    await waitFor(() => expect(document.querySelector("[data-bucket-too-fine]")).toBeNull());
    await waitFor(async () => expect(segs(await block(), "real")).toBeGreaterThan(0));
  });

  it("10 Minuten unter billed: ebenso keine Zahl", async () => {
    serve("&basis=billed&bucket=10min");
    await block();
    await waitFor(() => expect(document.querySelector('[data-bucket-too-fine] [data-no-figure="bucketTooFine"]')).not.toBeNull());
  });

  it("je Agent: Tokenpreis mit Sperrchip perPeriodAgent, der Hinweis bleibt", async () => {
    serve("&basis=capacity&agents=a1");
    const b = await block();
    await waitFor(() => expect(document.querySelector('[data-chart-lock] [data-basis-lock="perPeriodAgent"]')).not.toBeNull());
    expect(b.textContent).toContain(i18n.t("costs.fig.filteredRealNote") as string);
    expect(segs(b, "r")).toBeGreaterThan(0);
    expect(document.querySelector("[data-basis-lead-chip]")).toBeNull();
    // the page tile keeps the token price and says why, once
    expect(document.querySelector('[data-kpi-lock] [data-basis-lock="perPeriodAgent"]')).not.toBeNull();
    expect(document.querySelector("[data-basis-lead]")).toBeNull();
  });

  it("Layout je Agent: ebenso gesperrt", async () => {
    serve("&basis=billed&layout=agents");
    await block();
    await waitFor(() => expect(document.querySelector('[data-chart-lock] [data-basis-lock="perPeriodAgent"]')).not.toBeNull());
  });

  it("Läufe und Preisliste: Sperrchip unter capacity und billed", async () => {
    serve("&basis=capacity");
    await block();
    await waitFor(() => expect(locks().filter((l) => l === "notPerRun")).toHaveLength(2));
  });

  it("Läufe und Preisliste unter billed: keine Zugangsdaten am Lauf", async () => {
    serve("&basis=billed");
    await block();
    await waitFor(() => expect(locks().filter((l) => l === "noCredentialOnRun")).toHaveLength(2));
  });
});

// --- CostInsights --------------------------------------------------------------------------------------------------

const row = (model: string, engine: number, list: number | null) => ({
  agent_id: "a1", agent_slug: "coordinator", agent_name: "Coordinator", model, credential_kind: "subscription", runs: 1,
  input_tokens: 0, output_tokens: 0, thinking_tokens: 0, cache_read_tokens: 0, cache_write_5m_tokens: 0, cache_write_1h_tokens: 0,
  web_search_requests: 0, engine_usd: engine, list_usd: list,
});
const MODELS = { rows: [row("claude-opus-5-5", 10, 10.5)], runs_without_breakdown: 0, usd_without_breakdown: 0 };
const span = { secret_key: "seat", secret_slot: 0, source: "usage_cmd", baseline_at: "2026-11-17T18:00:00Z", last_at: "2026-11-17T21:00:00Z",
  baseline_percent: 9, last_percent: 13, points: 4, readings: 4 };
const real = { value: 7, low: 6, high: 8, points: 4, below: false, unpricedPoints: 0, usagePoints: 0, weeks: 1, priced: true };
const seat = { id: "seat#0", secretKey: "seat", slot: 0, kind: "subscription", real, aligned: 6.5, billed: null, meter: span } as unknown as CredentialFigures;
const key = {
  id: "key#1", secretKey: "key", slot: 1, kind: "api_key", real: null, aligned: null, meter: null,
  billed: {
    state: "measured", usd: 80.5, reported: 95, recomputed: 99, status: { state: "complete", completeTo: null },
    window: { start: new Date("2026-11-17T18:00:00Z"), end: new Date("2026-11-17T23:00:00Z"), comparedUntil: new Date("2026-11-17T20:00:00Z"), width: "1h" },
  },
} as unknown as CredentialFigures;

const COLS = "covey.cost.modelColumns";

function insights(basis: string) {
  mockFetch({ "/api/v1/cost/models": MODELS, "/api/v1/cost/usage-readings": [] });
  return renderWithProviders(<CostInsights days={7} figures={[seat, key]} />, { route: basis ? `/?basis=${basis}` : "/" });
}
const cols = () => [...document.querySelectorAll("th[data-col]")].map((th) => th.getAttribute("data-col"));

describe("CostInsights unter der Basis", () => {
  beforeEach(() => { useGerman(); window.localStorage.removeItem(COLS); });
  afterEach(() => { vi.restoreAllMocks(); window.localStorage.removeItem(COLS); });

  it("token: die gemerkten Spalten, kein Sperrchip", async () => {
    insights("");
    await waitFor(() => expect(cols()).toEqual(["reported", "recomputed"]));
    expect(locks()).toEqual([]);
  });

  it("capacity: die Sitzzeile ist an, die Modellspalten gesperrt, der Zähler gilt", async () => {
    insights("capacity");
    await waitFor(() => expect(cols()).toEqual(["reported", "recomputed", "real"]));
    expect(document.querySelector('[data-credential-row="real"]')).not.toBeNull();
    expect(locks()).toEqual(["notPerModel"]);
    expect(document.querySelector('[data-meter-title] [data-basis-tag="capacity"]')).not.toBeNull();
    // set by the basis, not switchable off, and not remembered
    const forced = document.querySelector<HTMLElement>('[data-insights-chip="real"][data-basis-forced] button')!;
    expect(forced.getAttribute("aria-disabled")).toBe("true");
    fireEvent.click(forced);
    expect(cols()).toContain("real");
    expect(window.localStorage.getItem(COLS)).toBeNull();
  });

  it("billed: die Schlüsselzeile ist an, ganze Schlüssel, Modelle und Zähler gesperrt", async () => {
    insights("billed");
    await waitFor(() => expect(cols()).toEqual(["reported", "recomputed", "billed"]));
    const keys = document.querySelector<HTMLElement>('[data-credential-row="billed"]')!;
    expect(keys.querySelector("[data-whole-key]")).not.toBeNull();
    // the models keep the token price (per key); the meter card shows capacity per seat and says so (D)
    expect(locks()).toEqual(["billedPerKey", "meterPerSeat"]);
  });

  it("die gemerkte Wahl unter token bleibt, was die Basis erzwingt", async () => {
    window.localStorage.setItem(COLS, JSON.stringify(["reported"]));
    insights("capacity");
    await waitFor(() => expect(cols()).toEqual(["reported", "real"]));
    // switching a token column still writes the token choice only
    fireEvent.click(screen.getByRole("button", { name: new RegExp(i18n.t("costs.fig.recomputed") as string) }));
    await waitFor(() => expect(cols()).toEqual(["reported", "recomputed", "real"]));
    expect(JSON.parse(window.localStorage.getItem(COLS)!)).toEqual(["reported", "recomputed"]);
  });
});

// --- The lead tile under capacity and billed (review H, I) -----------------------------------------------------------

const seatFig = (over: Partial<typeof real>) => ({ ...seat, id: `${Math.random()}`, real: { ...real, ...over } }) as unknown as CredentialFigures;
const keyFig = (state: string) => ({ ...key, id: `k-${state}`, billed: { ...(key.billed as object), state, usd: 0, error: "" } }) as unknown as CredentialFigures;
const kpiValue = () => document.querySelector<HTMLElement>("[data-kpi] [data-kpi-value]")!;

describe("Details: die Leitkachel unter capacity und billed", () => {
  beforeEach(() => useGerman());

  it("I: ein Sitz ohne Bewegung zählt als bepreist (0, sein oberer Rand im Bereich), kein Teilhinweis", () => {
    renderWithProviders(<BasisKpi basis="capacity" figures={[seatFig({}), seatFig({ value: 0, low: 0, high: 1.2, points: 0, below: true })]} />);
    expect(kpiValue().textContent).toMatch(/^≈ /);
    const sub = document.querySelector("[data-kpi]")!.textContent!;
    expect(sub).toContain(fmtUSD(6));
    expect(sub).toContain(fmtUSD(9.2));
    expect(sub).not.toContain("1/2");
  });

  it("I: nur Sitze ohne Planpreis fehlen und werden genannt", () => {
    renderWithProviders(<BasisKpi basis="capacity" figures={[seatFig({}), seatFig({ priced: false })]} />);
    expect(kpiValue().textContent).toBe(`≈ ${fmtUSD(7)}`);
    expect(document.querySelector("[data-kpi]")!.textContent).toContain(i18n.t("costs.basisDetails.kpi.capacityPartial", { n: 1, m: 2 }) as string);
  });

  it("I: jeder bepreiste Sitz ohne Bewegung: < $high wie CostInsights, keine NoFigure", () => {
    renderWithProviders(<BasisKpi basis="capacity" figures={[seatFig({ value: 0, low: 0, high: 1.2, points: 0, below: true })]} />);
    expect(document.querySelector("[data-kpi-below]")!.textContent).toBe(fmtUSDBelow(1.2));
    expect(document.querySelector("[data-kpi] [data-no-figure]")).toBeNull();
  });

  it("H: Schlüssel ohne gemessene Rechnung: notLanded mit dem Zustand der Schlüssel; keine Schlüssel: noBilledKeys", () => {
    const { unmount } = renderWithProviders(<BasisKpi basis="billed" figures={[keyFig("not_landed")]} />);
    expect(document.querySelector("[data-kpi] [data-no-figure]")!.getAttribute("data-no-figure")).toBe("notLanded");
    expect(document.querySelector("[data-kpi]")!.textContent).toContain(i18n.t("costs.fig.billedNotLanded") as string);
    expect(document.querySelector("[data-kpi]")!.textContent).not.toMatch(/0[.,]00/);
    unmount();
    const second = renderWithProviders(<BasisKpi basis="billed" figures={[keyFig("failed")]} />);
    expect(document.querySelector("[data-kpi] [data-no-figure]")!.getAttribute("data-no-figure")).toBe("notLanded");
    second.unmount();
    // A key not read yet is not a load error: only the bill's own failed query is (#review H).
    const third = renderWithProviders(<BasisKpi basis="billed" figures={[keyFig("unavailable")]} />);
    expect(document.querySelector("[data-kpi] [data-no-figure]")!.getAttribute("data-no-figure")).toBe("notLanded");
    third.unmount();
    const loading = renderWithProviders(<BasisKpi basis="billed" figures={[keyFig("unavailable")]} billedQuery={{ loading: true, error: false }} />);
    expect(document.querySelector("[data-kpi] [data-no-figure]")).toBeNull();
    expect(document.querySelector("[data-kpi]")!.textContent).toContain("…");
    loading.unmount();
    const failed = renderWithProviders(<BasisKpi basis="billed" figures={[keyFig("unavailable")]} billedQuery={{ loading: false, error: true }} />);
    expect(document.querySelector("[data-kpi] [data-no-figure]")!.getAttribute("data-no-figure")).toBe("loadError");
    failed.unmount();
    renderWithProviders(<BasisKpi basis="billed" figures={[seat]} />);
    expect(document.querySelector("[data-kpi] [data-no-figure]")!.getAttribute("data-no-figure")).toBe("noBilledKeys");
  });
});
