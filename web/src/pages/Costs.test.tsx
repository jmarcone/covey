import { describe, it, expect, beforeEach } from "vitest";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Costs from "./Costs";
import type { CostCells } from "../api";
import { mockFetch, renderWithProviders, useGerman } from "../test/render";

// The Costs page is argued with, so what it may and may not show is pinned:
// the multiple only from three points of meter movement, never the booked
// figure in a tile, every period reachable by keyboard, and a table that
// says the same as the chart.

const SEAT = "claude_code_oauth_token";
const BOOKED = 77.77; // what covey booked per cell: never a figure on the page
const PP = 200 * 12 / 52 / 100; // $200 a month: $0.4615 per point

function cells(points: [number, number]): CostCells {
  const cell = (period: string, output: number, cacheRead: number) => ({
    period, agent_id: "a1", secret_key: SEAT, secret_slot: 0, credential_kind: "subscription",
    reported_usd: BOOKED, measured: { output, cache_read: cacheRead }, engine_usd: output + cacheRead,
    tokens: { output: output * 50_000, cache_read: cacheRead * 5_000_000 },
  });
  const total = points[0] + points[1];
  return {
    periods: ["2026-11-17T18:00:00Z", "2026-11-17T19:00:00Z"],
    agents: [{ id: "a1", label: "Coordinator", reported_usd: 2 * BOOKED, measured_usd: 16 }],
    types: ["cache_read", "cache_write", "input", "output", "web_search", "unpriced", "unattributed"],
    cells: [cell("2026-11-17T18:00:00Z", 6, 2), cell("2026-11-17T19:00:00Z", 5, 3)],
    subscription: [
      { period: "2026-11-17T18:00:00Z", secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd",
        window_kind: "seven_day", points: points[0], readings: 2, resets: 0, longest_gap_secs: 900, usd: points[0] * PP, unpriced_points: 0 },
      { period: "2026-11-17T19:00:00Z", secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd",
        window_kind: "seven_day", points: points[1], readings: 2, resets: 0, longest_gap_secs: 900, usd: points[1] * PP, unpriced_points: 0 },
    ],
    meter: [{
      secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd", window_kind: "seven_day",
      baseline_at: "2026-11-17T18:00:00Z", baseline_percent: 9, baseline_before_window: false,
      last_at: "2026-11-17T20:00:00Z", last_percent: 9 + total, readings: 4, points: total,
      points_low: Math.max(total - 1, 0), points_high: total + 1, longest_gap_secs: 900, segments: null,
      measured_usd: 16,
    }],
    plans: [{
      runtime_id: null, ord: 0, secret_key: SEAT, secret_slot: 0, plan_name: "Max", amount: 200, currency: "USD", period: "month",
      tax_basis: "net", fx_to_usd: 1, fx_date: null, valid_from: "2026-11-01T00:00:00Z", valid_to: null,
      weekly_usd: 200 * 12 / 52, usd_per_point: 200 * 12 / 52 / 100,
    }],
    bookings: [{ secret_key: SEAT, secret_slot: 0, basis: "invocation", entries: 2, usd: 2 * BOOKED }],
  };
}

const NO_BILL = { bucket: "hour", points: [], sync: { configured: false, last_attempt_at: null, last_success_at: null,
  last_daily_at: null, data_refreshed_at: null, last_status: "", last_error: "" } };

/** The page's endpoints with these cells; `over` replaces or adds routes
 *  (a pattern with a query goes first, so it wins over the plain one). */
function serve(data: CostCells, over: Record<string, unknown> = {}) {
  mockFetch({
    ...over,
    "/api/v1/cost/cells": data,
    "/api/v1/cost/billed": NO_BILL,
    "/api/v1/cost/runs": [],
    "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
    "/api/v1/cost/usage-readings": [],
    ...over,
  });
  return renderWithProviders(<Costs />);
}

function render(points: [number, number]) {
  return serve(cells(points));
}

const MULTIPLE = "Listen-$ je echtem $";

describe("Kosten — Zahlen je Zugangsdaten", () => {
  beforeEach(() => useGerman());

  it("blendet das Vielfache unter drei Punkten Bewegung aus und nennt den Grund", async () => {
    render([1, 1]);
    expect(await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" })).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: MULTIPLE })).toBeNull();
    expect(screen.getByText(/Kein Vielfaches: Der Zähler bewegte sich um 2 Punkt/)).toBeInTheDocument();
  });

  it("zeigt das Vielfache ab drei Punkten, mit Spanne und ohne es eine Untergrenze zu nennen", async () => {
    render([2, 2]);
    const tile = await screen.findByRole("group", { name: MULTIPLE });
    expect(within(tile).getByText(/^≈ .*×$/)).toBeInTheDocument();
    expect(within(tile).getByText(/^Spanne .*× – .*×$/)).toBeInTheDocument();
    // both directions: an unused allowance lowers it, other use of the account raises it
    expect(within(tile).getByText(/keine Schranke: niedriger, .* höher, /)).toBeInTheDocument();
    expect(tile.textContent).not.toMatch(/Untergrenze/);
  });

  it("zeigt bei einem API-Schlüssel ohne Admin-Schlüssel keinen Betrag, sondern den Grund", async () => {
    const KEY = "anthropic_api_key";
    const data = cells([2, 2]);
    data.cells.push({
      period: "2026-11-17T18:00:00Z", agent_id: "a1", secret_key: KEY, secret_slot: 0, credential_kind: "api_key",
      reported_usd: BOOKED, measured: { output: 4 }, engine_usd: 4, tokens: { output: 200_000 },
    });
    mockFetch({
      "/api/v1/cost/cells": data,
      "/api/v1/cost/billed": { bucket: "hour", points: [],
        window: { start: "2026-11-17T18:00:00Z", end: "2026-11-17T23:00:00Z", width: "1h", totals: [] },
        sync: { configured: false, last_attempt_at: null, last_success_at: null, last_daily_at: null, data_refreshed_at: null,
          last_status: "", last_error: "" } },
      "/api/v1/cost/runs": [],
      "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
      "/api/v1/cost/usage-readings": [],
    });
    const { container } = renderWithProviders(<Costs />);
    const block = await waitFor(() => {
      const b = container.querySelector<HTMLElement>(`[data-credential="${KEY}#0"]`);
      if (!b) throw new Error("no block for the API key yet");
      return b;
    });
    const tile = within(block).getByRole("group", { name: "Von Anthropic berechnet" });
    expect(within(tile).getByText("–")).toBeInTheDocument();
    expect(within(tile).getByText(/kein Anthropic-Admin-Schlüssel hinterlegt/)).toBeInTheDocument();
    expect(tile.textContent).not.toMatch(/0,00/);
    expect(within(block).queryByRole("group", { name: "Listenpreis laut Anthropic" })).toBeNull();
  });

  // Review C3: the bill's bars reached back to the midnight before the
  // window, so the table's billed total was not the tile's.
  it("summiert die Rechnung in der Tabelle zu derselben Zahl wie die Kachel", async () => {
    const KEY = "anthropic_api_key";
    const data = cells([2, 2]);
    data.cells.push({
      period: "2026-11-17T18:00:00Z", agent_id: "a1", secret_key: KEY, secret_slot: 0, credential_kind: "api_key",
      reported_usd: BOOKED, measured: { output: 4 }, engine_usd: 4, tokens: { output: 200_000 },
    });
    const point = (bucket: string, usd: string) => ({ bucket, api_key_id: "apikey_01", workspace_id: "", model: "claude-opus-5-5",
      secret_key: KEY, secret_slot: 0, billed_usd: usd, billed_list_usd: usd, complete: true });
    const { container } = serve(data, {
      "/api/v1/cost/billed": { bucket: "hour",
        points: [point("2026-11-17T00:00:00Z", "500.00"), point("2026-11-17T18:00:00Z", "3.50"), point("2026-11-17T19:00:00Z", "0.50")],
        window: { start: "2026-11-17T18:00:00Z", end: "2026-11-17T23:00:00Z", width: "1h",
          totals: [{ secret_key: KEY, secret_slot: 0, has_billed: true, billed_usd: "4.00", billed_list_usd: "4.00", reported_usd: 4, list_usd: 4 }] },
        sync: { configured: true, last_attempt_at: null, last_success_at: null, last_daily_at: null,
          data_refreshed_at: "2026-11-18T00:00:00Z", last_status: "ok", last_error: "" } },
    });
    const block = await waitFor(() => {
      const b = container.querySelector<HTMLElement>(`[data-credential="${KEY}#0"]`);
      if (!b) throw new Error("no block for the API key yet");
      return b;
    });
    const tile = within(block).getByRole("group", { name: "Von Anthropic berechnet" });
    expect(within(tile).getByText("4,00 $")).toBeInTheDocument();
    await act(async () => {
      await userEvent.click(screen.getByRole("button", { name: "Als Tabelle zeigen" }));
    });
    const totals = [...block.querySelectorAll("tr[data-totals] td[data-total]")].map((td) => td.textContent);
    // reported, recomputed, billed
    expect(totals).toEqual(["4,00 $", "4,00 $", "4,00 $"]);
    // the periods 18:00 and 19:00 only: nothing from before the window
    expect(block.querySelector("tr[data-totals]")!.closest("table")!.querySelectorAll("tbody tr")).toHaveLength(2);
  });

  it("zeigt die gebuchte Zahl in keiner Kachel", async () => {
    const { container } = render([2, 2]);
    await screen.findByRole("group", { name: MULTIPLE });
    const tiles = [...container.querySelectorAll("[data-tile]")];
    expect(tiles.length).toBeGreaterThan(3);
    for (const tile of tiles) {
      expect(tile.textContent).not.toMatch(/77,77|155,54/);
    }
    // what Claude Code reported for its lines: 6+2 + 5+3
    expect(within(screen.getAllByRole("group", { name: "Von Claude Code gemeldet" })[0]).getByText("16,00 $")).toBeInTheDocument();
  });

  it("öffnet die Details eines Zeitraums beim Fokus und schließt sie mit Escape", async () => {
    const { container } = render([1, 1]);
    await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
    const period = container.querySelector<SVGGElement>("g[data-period]")!;
    expect(period.getAttribute("tabindex")).toBe("0");
    expect(screen.queryByRole("tooltip")).toBeNull();
    fireEvent.focusIn(period);
    const tip = await screen.findByRole("tooltip");
    expect(period.getAttribute("aria-describedby")).toBe(tip.id);
    // at an hour the meter shows its crossings, not dollars
    expect(within(tip).getByText(/9 % → 10 %/)).toBeInTheDocument();
    fireEvent.keyDown(period, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("tooltip")).toBeNull());
    fireEvent.keyDown(period, { key: "Enter" });
    expect(await screen.findByRole("tooltip")).toBeInTheDocument();
  });

  // Review C2: points without a plan price were left out of the real cost
  // and the multiple divided by the rest; a seat without a plan read "≈ $0.00".
  it("zeigt ohne Tarifpreis für einen Teil der Punkte weder echte Kosten noch ein Vielfaches", async () => {
    const data = cells([2, 2]);
    data.subscription![1] = { ...data.subscription![1], usd: null, unpriced_points: 2 };
    serve(data);
    const tile = await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
    expect(within(tile).getByText("–")).toBeInTheDocument();
    expect(within(tile).getByText("kein Tarifpreis für 2 von 4 Punkten")).toBeInTheDocument();
    expect(tile.textContent).not.toMatch(/≈/);
    expect(screen.queryByRole("group", { name: MULTIPLE })).toBeNull();
    expect(screen.getByText(/kein Vielfaches: kein Tarifpreis für 2 von 4 Punkten/)).toBeInTheDocument();
  });

  it("zeigt bei Zugangsdaten ohne Tarif „kein Tarifpreis“, nie ≈ 0", async () => {
    const data = cells([0, 0]);
    data.plans = [];
    for (const s of data.subscription!) s.usd = null;
    serve(data);
    const tile = await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
    expect(within(tile).getByText("kein Tarifpreis hinterlegt")).toBeInTheDocument();
    expect(tile.textContent).not.toMatch(/≈|0,00|< /);
  });

  // Review C5: the meter's baseline can lie days before the window; the
  // real figure (and every share of it) covers that span, not the window.
  it("sagt, seit wann die echten Kosten und ihr Anteil gelten", async () => {
    const data = cells([2, 2]);
    data.meter![0] = { ...data.meter![0], baseline_at: "2026-11-15T06:00:00Z", baseline_before_window: true };
    const { container } = serve(data);
    const tile = await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
    expect(within(tile).getByText(/^echt seit 15\.11\., 06:00 UTC .*letzte Ablesung 17\.11\., 20:00 UTC$/)).toBeInTheDocument();
    const caption = container.querySelector("caption [data-span]")!;
    expect(caption.textContent).toMatch(/seit 15\.11\., 06:00 UTC .* bis 17\.11\., 20:00 UTC/);
    await act(async () => { await userEvent.click(screen.getByRole("button", { name: /^Coordinator/ })); });
    const share = await screen.findByRole("group", { name: "≈ Anteil an den echten Kosten (Schätzung)" });
    expect(within(share).getByText(/^echt seit 15\.11\., 06:00 UTC/)).toBeInTheDocument();
  });

  // Review C11: multiples, percents and shares used "." on a German page.
  it("schreibt Vielfache, Prozente und Anteile mit dem Dezimaltrenner der Sprache", async () => {
    const { container } = render([2, 2]);
    const tile = await screen.findByRole("group", { name: MULTIPLE });
    // 16 $ over 4 points × 0,4615 $
    expect(within(tile).getByText("≈ 8,7×")).toBeInTheDocument();
    expect(within(tile).getByText("Spanne 6,9× – 11,6×")).toBeInTheDocument();
    expect(screen.getAllByText("Preistabellen-Prüfung +0,00 %").length).toBeGreaterThan(0);
    const share = [...container.querySelectorAll("caption")].find((c) => /Je Agent/.test(c.textContent!))!.closest("table")!;
    expect(within(share).getByText("100,0 %")).toBeInTheDocument();
  });

  // Review C13: 90 days by the hour were 2,160 periods.
  it("bietet Stunden nur bis zu einer Woche an", async () => {
    render([2, 2]);
    await screen.findByRole("group", { name: MULTIPLE });
    expect(screen.getByRole("button", { name: "Stunde" })).toBeEnabled();
    await act(async () => { await userEvent.click(screen.getByRole("button", { name: "7 Tage" })); });
    expect(screen.getByRole("button", { name: "Stunde" })).toBeEnabled();
    await act(async () => { await userEvent.click(screen.getByRole("button", { name: "30 Tage" })); });
    expect(screen.getByRole("button", { name: "Stunde" })).toBeDisabled();
    await act(async () => { await userEvent.click(screen.getByRole("button", { name: "90 Tage" })); });
    expect(screen.getByRole("button", { name: "Stunde" })).toBeDisabled();
  });

  // Review C4: a cost type switched off under "split by cost type" stayed
  // off, invisibly, after the split was switched off again.
  it("lässt einen ausgeblendeten Kostentyp nicht weiterwirken, wenn die Aufteilung aus ist", async () => {
    const { container } = render([2, 2]);
    await screen.findByRole("group", { name: MULTIPLE });
    const click = async (name: string) => act(async () => { await userEvent.click(screen.getByRole("button", { name })); });
    await click("Kostenart");
    await click("Ausgabe");
    await click("Keine");
    await click("Als Tabelle zeigen");
    const values = [...container.querySelectorAll("tr[data-totals] td[data-total]")].map((td) => td.textContent);
    // reported 16, recomputed 16: output 11 + cache read 5, not 5
    expect(values).toEqual(["16,00 $", "16,00 $"]);
  });

  // Review C1: cache writes were counted as hits, so 94 % read the cache
  // as 100 %.
  it("rechnet Cache-Schreibvorgänge nicht als Treffer", async () => {
    const data = cells([2, 2]);
    for (const c of data.cells) c.tokens = { cache_read: 47, cache_write_1h: 3, output: 1 };
    serve(data);
    expect(await screen.findByText("94 % aus dem Prompt-Cache")).toBeInTheDocument();
  });

  it("zeigt als Tabelle dieselben Summen wie das Diagramm", async () => {
    const { container } = render([2, 2]);
    await screen.findByRole("group", { name: MULTIPLE });
    const desc = container.querySelector("desc[data-desc]")!.textContent!;
    await act(async () => {
      await userEvent.click(screen.getByRole("button", { name: "Als Tabelle zeigen" }));
    });
    const totals = container.querySelector("tr[data-totals]")!;
    const values = [...totals.querySelectorAll("td[data-total]")].map((td) => td.textContent!);
    expect(values).toEqual(["16,00 $", "16,00 $"]);
    for (const v of values) expect(desc).toContain(v);
  });
});
