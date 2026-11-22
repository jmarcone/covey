import { describe, it, expect, beforeEach } from "vitest";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Costs from "./Costs";
import type { CostCells } from "../api";
import i18n from "../i18n";
import { mockFetch, renderWithProviders, useGerman } from "../test/render";
import { multipleOf, realCost } from "./costFigures";

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
function serve(data: CostCells, over: Record<string, unknown> = {}, route = "/?days=1&tz=utc") {
  mockFetch({
    ...over,
    "/api/v1/cost/cells": data,
    "/api/v1/cost/billed": NO_BILL,
    "/api/v1/cost/runs": [],
    "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
    "/api/v1/cost/usage-readings": [],
    ...over,
  });
  return renderWithProviders(<Costs />, { route });
}

function render(points: [number, number]) {
  return serve(cells(points));
}

/** The full view of covey's price table (#120): Reported and Recomputed apart, as before. */
const FULL = "/?prices=recomputed&tz=utc";
function renderFull(points: [number, number]) {
  return serve(cells(points), {}, FULL);
}

/** The lead figure's name: one list-price figure leads (#120). */
const LEAD = "Zu Tokenpreisen (wie von Claude Code gemeldet)";

const MULTIPLE = "Listen-$ je echtem $";


// The chart's own source row. Since #79 the Usage per model card has a
// "Quelle" row too (a labelled group); the chart's row is the one that isn't.
const chartSources = () =>
  screen.getAllByText("Quelle").map((e) => e.parentElement!).find((p) => p.getAttribute("role") !== "group")!;

describe("Kosten — Zahlen je Zugangsdaten", () => {
  beforeEach(() => useGerman());

  it("blendet das Vielfache unter drei Punkten Bewegung aus und nennt den Grund", async () => {
    render([1, 1]);
    expect(await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" })).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: MULTIPLE })).toBeNull();
    expect(document.querySelector("[data-no-multiple]")!.textContent).toMatch(/Kein Vielfaches: Der Zähler bewegte sich um 2 Punkt/);
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
        window: { start: "2026-11-17T18:00:00Z", end: "2026-11-17T23:00:00Z", compared_until: "2026-11-17T23:00:00Z", width: "1h", totals: [] },
        sync: { configured: false, last_attempt_at: null, last_success_at: null, last_daily_at: null, data_refreshed_at: null,
          last_status: "", last_error: "" } },
      "/api/v1/cost/runs": [],
      "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
      "/api/v1/cost/usage-readings": [],
    });
    const { container } = renderWithProviders(<Costs />, { route: "/?days=1&tz=utc" });
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

  // #83 outside review: "compared until 20:00 UTC" with no date reads as today,
  // so a mark on another day than the window's end carries its date.
  it("nennt bei „verglichen bis“ den Tag, wenn er nicht der des Fensterendes ist", async () => {
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
        points: [point("2026-11-17T18:00:00Z", "3.50"), point("2026-11-17T19:00:00Z", "0.50")],
        window: { start: "2026-11-17T18:00:00Z", end: "2026-11-18T03:00:00Z", compared_until: "2026-11-17T20:00:00Z", width: "1h",
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
    expect(within(tile).getByText(/verglichen bis 17\.11\., 20:00 UTC$/)).toBeInTheDocument();
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
        window: { start: "2026-11-17T18:00:00Z", end: "2026-11-17T23:00:00Z", compared_until: "2026-11-17T20:00:00Z", width: "1h",
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
    // #83: the lines stop where the bill is complete, not at "now"
    expect(within(tile).getByText(/gemeldet 4,00 \$ und nachgerechnet 4,00 \$, verglichen bis 20:00 UTC$/)).toBeInTheDocument();
    await act(async () => {
      await userEvent.click(screen.getByRole("button", { name: "Als Tabelle zeigen" }));
    });
    const totals = [...block.querySelectorAll("tr[data-totals] td[data-total]")].map((td) => td.textContent);
    // at token prices (the lead figure), billed
    expect(totals).toEqual(["4,00 $", "4,00 $"]);
    // the periods 18:00 and 19:00 only: nothing from before the window
    expect(block.querySelector("tr[data-totals]")!.closest("table")!.querySelectorAll("tbody tr")).toHaveLength(2);
  });

  it("zeigt die gebuchte Zahl in keiner Kachel", async () => {
    const { container } = render([2, 2]);
    await screen.findByRole("group", { name: MULTIPLE });
    const tiles = [...container.querySelectorAll("[data-tile]")];
    // the lead figure, the real cost, the multiple
    expect(tiles.length).toBeGreaterThanOrEqual(3);
    for (const tile of tiles) {
      expect(tile.textContent).not.toMatch(/77,77|155,54/);
    }
    // what Claude Code reported for its lines: 6+2 + 5+3
    expect(within(screen.getAllByRole("group", { name: LEAD })[0]).getByText("16,00 $")).toBeInTheDocument();
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
    expect(document.querySelector("[data-no-multiple]")!.textContent).toMatch(/kein Vielfaches: kein Tarifpreis für 2 von 4 Punkten/);
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

  // #83: an upper bound under a cent was written "< <$0.01".
  it("schreibt die Obergrenze unter einem Cent als „< $0.01“", async () => {
    await i18n.changeLanguage("en");
    const data = cells([0, 0]);
    for (const s of data.subscription!) s.usd = 0;
    data.plans![0] = { ...data.plans![0], amount: 1.73, weekly_usd: 0.4, usd_per_point: 0.004 }; // a point costs 0.4 cents
    serve(data);
    const tile = await screen.findByRole("group", { name: "Real cost (estimate)" });
    expect(within(tile).getByText("< $0.01")).toBeInTheDocument();
    expect(tile.textContent).not.toMatch(/<\s*</);
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
    const { container } = renderFull([2, 2]);
    const tile = await screen.findByRole("group", { name: MULTIPLE });
    // 16 $ over 4 points × 0,4615 $
    expect(within(tile).getByText("≈ 8,7×")).toBeInTheDocument();
    expect(within(tile).getByText("Spanne 6,9× – 11,6×")).toBeInTheDocument();
    // #83: the point price carries four decimals
    expect(screen.getByRole("group", { name: "Echte Kosten (Schätzung)" }).textContent).toMatch(/4 Punkte × 0,4615 \$/);
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

  // Review C10: an agent selected in one range stayed selected in a range
  // without it, invisible, and narrowed every figure to nothing.
  it("verwirft gewählte Agenten, die es im neuen Zeitraum nicht gibt", async () => {
    const week = cells([2, 2]);
    week.agents = [{ id: "a2", label: "QA", reported_usd: 0, measured_usd: 16 }];
    for (const c of week.cells) c.agent_id = "a2";
    serve(cells([2, 2]), { "/api/v1/cost/cells?days=7": week });
    await screen.findByRole("group", { name: MULTIPLE });
    await act(async () => { await userEvent.click(screen.getByRole("button", { name: /^Coordinator/ })); });
    expect(screen.getByRole("button", { name: "Alle" })).toHaveAttribute("aria-pressed", "false");
    await act(async () => { await userEvent.click(screen.getByRole("button", { name: "7 Tage" })); });
    await screen.findByRole("button", { name: /^QA/ });
    await waitFor(() => expect(screen.getByRole("button", { name: "Alle" })).toHaveAttribute("aria-pressed", "true"));
    expect(screen.getByText("Gesamte Organisation")).toBeInTheDocument();
  });

  // Review C12: the card shows shares of TOKENS under the title "cost types".
  it("nennt die Karte der Token-Anteile nicht Kostenarten", async () => {
    render([2, 2]);
    await screen.findByRole("group", { name: MULTIPLE });
    expect(screen.getByText("Token-Arten")).toBeInTheDocument();
    expect(screen.getByText(/^Anteile an den Token, nicht an den Dollar\./)).toBeInTheDocument();
    expect(screen.queryByText("Kostenarten")).toBeNull();
  });

  // Review C8: the per-model list total left out the models without a list
  // price, which the recomputed headline counts at Claude Code's price.
  it("zeigt den Anteil der Modelle ohne Listenpreis und zählt ihn in die Summe", async () => {
    const row = (model: string, engine: number, list: number | null) => ({
      agent_id: "a1", agent_slug: "coordinator", agent_name: "Coordinator", model, credential_kind: "subscription", runs: 1,
      input_tokens: 0, output_tokens: 0, thinking_tokens: 0, cache_read_tokens: 0, cache_write_5m_tokens: 0, cache_write_1h_tokens: 0,
      web_search_requests: 0, engine_usd: engine, list_usd: list,
    });
    const { container } = serve(cells([2, 2]), {
      "/api/v1/cost/models": { rows: [row("claude-opus-5-5", 10, 10.5), row("claude-mystery-1", 2, null)], runs_without_breakdown: 0, usd_without_breakdown: 0 },
    });
    const unpriced = await waitFor(() => {
      const r = container.querySelector<HTMLElement>("tr[data-unpriced]");
      if (!r) throw new Error("no unpriced row yet");
      return r;
    });
    expect(unpriced.textContent).toMatch(/davon Modelle ohne Listenpreis \(1\).*2,00 \$2,00 \$$/);
    const total = unpriced.parentElement!.querySelector("tr[data-total]")!;
    expect([...total.querySelectorAll("td")].map((td) => td.textContent)).toEqual([
      "Summe (nachgerechnet, Modelle ohne Listenpreis zum gemeldeten Preis)", "12,00 $", "12,50 $",
    ]);
  });

  // Review C7: the meter card divided Claude Code's own (engine) dollars,
  // labelled list price, by the meter without a baseline, on a chart of two
  // independent scales — and so contradicted the tiles.
  it("zeigt im Zähler-Kärtchen dieselben Zahlen wie die Kacheln, ohne Zwei-Achsen-Diagramm", async () => {
    const reading = (at: string, pct: number) => ({
      runtime_id: "rt", credential_ord: 0, secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd",
      window_kind: "seven_day", supported: true, window_percent: null, week_percent: pct, resets_at: null, status: "", reason: "",
      stale: false, fetched_at: at, received_at: at, cost_usd: 999,
    });
    const { container } = serve(cells([2, 2]), {
      "/api/v1/cost/usage-readings": [reading("2026-11-17T18:00:00Z", 9), reading("2026-11-17T20:00:00Z", 13)],
    });
    const tile = await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
    const mult = screen.getByRole("group", { name: MULTIPLE });
    const card = await waitFor(() => {
      const c = container.querySelector<HTMLElement>(`[data-meter="${SEAT}#0"]`);
      if (!c) throw new Error("no meter card yet");
      return c;
    });
    const real = within(tile).getByText(/^≈ /).textContent!;
    expect(card.textContent).toContain(real);
    expect(card.textContent).toContain(within(mult).getByText(/^≈ /).textContent!);
    // the list $ of the same span as the multiple, not Claude Code's figure on the readings
    expect(card.textContent).toContain("16,00 $");
    expect(card.textContent).not.toMatch(/999/);
    expect(card.textContent).toMatch(/9 % \(17\.11\., 18:00 UTC\) → 13 % \(17\.11\., 20:00 UTC\): 4 Punkte/);
    expect(card.closest(".card")!.querySelector("svg:not([data-swatch])")).toBeNull();
  });

  // Review C9: under an agent filter the per-model and meter cards vanished,
  // and the cards that stay org-wide did not say so.
  it("behält die Karten unter dem Agentenfilter und sagt, welche ihm nicht folgen", async () => {
    const data = cells([2, 2]);
    data.agents.push({ id: "a2", label: "QA", reported_usd: 0, measured_usd: 1 });
    const { container } = serve(data);
    await screen.findByRole("group", { name: MULTIPLE });
    expect(container.querySelectorAll("[data-org-wide]")).toHaveLength(0);
    await act(async () => { await userEvent.click(screen.getByRole("button", { name: /^Coordinator/ })); });
    expect(screen.getByText(/^Verbrauch je Modell/)).toBeInTheDocument();
    expect(screen.getByText(/^Abo-Zähler je Zugangsdaten/)).toBeInTheDocument();
    // one agent: the price list, the runs and the model table follow it, the meter card cannot
    expect(container.querySelectorAll("[data-org-wide]")).toHaveLength(1);
    await act(async () => { await userEvent.click(screen.getByRole("button", { name: /^QA/ })); });
    // two agents: the price list is the whole organisation's too; the runs are each agent's own, merged (cost home PR 7)
    expect(container.querySelectorAll("[data-org-wide]")).toHaveLength(2);
    expect(container.querySelector("[data-org-wide]")!.textContent).toBe("ganze Organisation: folgt dem Agentenfilter nicht");
  });

  // Review C4: a cost type switched off under "split by cost type" stayed
  // off, invisibly, after the split was switched off again.
  it("lässt einen ausgeblendeten Kostentyp nicht weiterwirken, wenn die Aufteilung aus ist", async () => {
    // the split by cost type belongs to covey's price table: its full view
    const { container } = renderFull([2, 2]);
    await screen.findByRole("group", { name: MULTIPLE });
    const click = async (name: string) => act(async () => { await userEvent.click(screen.getByRole("button", { name })); });
    await click("Kostenart");
    await click("Ausgabe");
    await click("Keine");
    await click("Als Tabelle zeigen");
    const values = [...container.querySelectorAll("tr[data-totals] td[data-total]")].map((td) => td.textContent);
    // reported 16, recomputed 16: output 11 + cache read 5, not 5; real 4 points
    expect(values).toEqual(["16,00 $", "16,00 $", "1,85 $"]);
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
    // at token prices, real: one list-price column (#120)
    expect(values).toEqual(["16,00 $", "1,85 $"]);
    for (const v of values) expect(desc).toContain(v);
  });

  // #77: at an hour the bars are the meter's whole points × the plan's price,
  // as the server priced them, and add up to the real tile.
  describe("echte Kosten je Stunde und im Vergleich zur Liste", () => {
    const click = async (name: string | RegExp) => act(async () => { await userEvent.click(screen.getByRole("button", { name })); });
    const realCol = (container: HTMLElement) => {
      // the chart's table, not the agents'
      const table = container.querySelector("tr[data-totals]")!.closest("table")!;
      const heads = [...table.querySelectorAll("thead th")].map((th) => th.textContent);
      const col = heads.indexOf("Echte Kosten (Schätzung)");
      const rows = [...table.querySelectorAll("tbody tr")].map((tr) => tr.querySelectorAll("td")[col - 1]?.textContent);
      const total = table.querySelector("tr[data-totals]")!.querySelectorAll("td[data-total]")[col - 1]?.textContent;
      return { col, rows, total };
    };

    it("zeichnet je Stunde echte Balken, die sich zur Kachel summieren, und ersetzt den Hinweis", async () => {
      const { container } = render([2, 3]);
      const tile = await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
      await click("Als Tabelle zeigen");
      const { col, rows, total } = realCol(container);
      expect(col).toBeGreaterThan(0);
      expect(rows).toEqual(["0,92 $", "1,38 $"]);
      // the tile: 5 points × $0.4615
      expect(total).toBe("2,31 $");
      expect(within(tile).getByText("≈ 2,31 $")).toBeInTheDocument();
      const note = container.querySelector("[data-hour-note]")!.textContent!;
      expect(note).toMatch(/ein Punkt 0,46 \$ wert/);
      // a bar is what was read in the hour, since the previous reading — not what was worked in it
      expect(note).toMatch(/die in dieser Stunde seit der vorigen Ablesung abgelesen wurden/);
      expect(note).toMatch(/Arbeit aus früheren Stunden kann später erscheinen/);
      expect(note).toMatch(/Stunden ohne Ablesung zeigen 0/);
      expect(note).not.toMatch(/Überschreitung/);
      expect(note).not.toMatch(/nicht in Dollar/);
    });

    it("zeigt für eine Stunde ohne Ablesung 0 und keinen Balken", async () => {
      const { container } = render([2, 0]);
      await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
      expect(container.querySelectorAll("g[data-period] rect[fill^='url(']").length).toBe(1);
      await click("Als Tabelle zeigen");
      expect(realCol(container).rows).toEqual(["0,92 $", "0,00 $"]);
    });

    it("zeichnet für Punkte ohne Tarifpreis keinen echten Balken", async () => {
      const data = cells([2, 2]);
      data.subscription![1] = { ...data.subscription![1], usd: null, unpriced_points: 2 };
      const { container } = serve(data);
      await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
      expect(container.querySelector("g[data-period] rect[fill^='url(']")).toBeNull();
      expect(container.querySelector("[data-hour-note]")).toBeNull();
      await click("Als Tabelle zeigen");
      expect(realCol(container).col).toBe(-1);
    });

    const tags = (container: HTMLElement) => [...container.querySelectorAll("[data-tag]")].map((e) => e.textContent);

    it("zeigt in „Echt gegen Liste“ den echten Wert am Balken und das Vielfache der Periode", async () => {
      const { container } = render([4, 0]);
      await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
      expect(container.querySelector("[data-value]")).toBeNull();
      await click("Echt gegen Liste");
      // the list bar and the real bar of each hour; the gap hour has no real value, hence no multiple
      expect(container.querySelectorAll("g[data-period]")[0].querySelectorAll("rect").length).toBe(3);
      expect([...container.querySelectorAll("[data-value]")].map((e) => e.textContent)).toEqual(["1,85 $"]);
      // 8 $ list over 4 points × 0,4615 $, ±1 point in the details as the tile's range
      expect(tags(container)).toEqual(["≈ 4,3×"]);
      fireEvent.focus(container.querySelectorAll("g[data-period]")[0]);
      expect(container.querySelector("[data-tag-detail]")!.textContent).toBe("≈ 4,3× · Spanne 3,5× – 5,8×");
      expect(container.querySelector("[data-vs-note]")).not.toBeNull();
      // the table keeps the note: it is the only explanation of the comparison
      await click("Als Tabelle zeigen");
      expect(container.querySelector("tr[data-totals]")).not.toBeNull();
      expect(container.querySelector("[data-vs-note]")).not.toBeNull();
      await click("Als Tabelle zeigen");
      await click("Beträge");
      expect(container.querySelector("[data-tag]")).toBeNull();
      expect(container.querySelector("[data-vs-note]")).toBeNull();
    });

    // PR #80 review: an hour is 1–3 points of ±1 each; the tile shows no
    // multiple below 3 points, and neither does a period.
    it("zeigt für eine Stunde unter drei Punkten kein Vielfaches, auch bei hohem Listenpreis", async () => {
      const data = cells([1, 2]);
      data.cells[0] = { ...data.cells[0], measured: { output: 148, cache_read: 2 }, engine_usd: 150 };
      const { container } = serve(data);
      await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
      await click("Echt gegen Liste");
      expect([...container.querySelectorAll("[data-value]")].map((e) => e.textContent)).toEqual(["0,46 $", "0,92 $"]);
      expect(tags(container)).toEqual([]);
      expect(container.textContent).not.toMatch(/325,0×/);
    });

    it("zeigt für eine Periode mit Listenpreis von wenigen Cent kein Vielfaches", async () => {
      const data = cells([4, 4]);
      data.cells[0] = { ...data.cells[0], measured: { output: 0.02 }, engine_usd: 0.02 };
      data.cells[1] = { ...data.cells[1], measured: { output: 0.004 }, engine_usd: 0.004 };
      const { container } = serve(data);
      await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
      await click("Echt gegen Liste");
      expect([...container.querySelectorAll("[data-value]")].map((e) => e.textContent)).toEqual(["1,85 $", "1,85 $"]);
      expect(tags(container)).toEqual([]);
      expect(container.textContent).not.toContain("0,0×");
    });

    it("zeigt für einen Tag mit vier Punkten das Vielfache, das multipleOf nennt", async () => {
      const data = cells([4, 2]);
      const day = (p: string) => p.replace(/^2026-11-17T18:00:00Z$/, "2026-11-16T00:00:00Z").replace(/^2026-11-17T19:00:00Z$/, "2026-11-17T00:00:00Z");
      data.periods = data.periods.map(day);
      data.cells = data.cells.map((c) => ({ ...c, period: day(c.period) }));
      data.subscription = data.subscription!.map((s) => ({ ...s, period: day(s.period) }));
      data.meter![0] = { ...data.meter![0], baseline_at: "2026-11-16T00:00:00Z", last_at: "2026-11-17T20:00:00Z" };
      const { container } = serve(data);
      await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
      await act(async () => { await userEvent.click(screen.getByRole("button", { name: "7 Tage" })); });
      await screen.findByRole("button", { name: "Echt gegen Liste" }); // the chart waits for the new range
      await click("Echt gegen Liste");
      // the day's whole list (6 + 2 $) over its 4 points, by the tile's rule
      const m = multipleOf(8, realCost(4, PP))!;
      expect(m.kind).toBe("estimate");
      const fmt = (v: number) => `${v.toFixed(1).replace(".", ",")}×`;
      expect(tags(container)).toEqual([`≈ ${fmt((m as { value: number }).value)}`]);
      fireEvent.focus(container.querySelectorAll("g[data-period]")[0]);
      expect(container.querySelector("[data-tag-detail]")!.textContent).toContain(`Spanne ${fmt(m.low)} – ${fmt(m.high)}`);
    });

    // Review 1: the multiple is the whole list over the real cost, as the
    // tile's; a cost type switched off only shortens the list bar.
    it("lässt das Vielfache der Periode bei einem ausgeblendeten Kostentyp unverändert", async () => {
      const { container } = serve(cells([4, 4]), {}, FULL);
      await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
      await click("Echt gegen Liste");
      expect(tags(container)).toEqual(["≈ 4,3×", "≈ 4,3×"]);
      await click("Kostenart");
      await click("Ausgabe");
      expect(tags(container)).toEqual(["≈ 4,3×", "≈ 4,3×"]);
    });

    // Review 3: meter moved, but no list costs in the period (other use of the account).
    it("zeigt für eine Periode mit Zählerbewegung, aber ohne Listenkosten kein Vielfaches", async () => {
      const data = cells([4, 4]);
      data.cells = [data.cells[0]];
      const { container } = serve(data);
      await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
      await click("Echt gegen Liste");
      expect([...container.querySelectorAll("g[data-period]")]).toHaveLength(2);
      expect([...container.querySelectorAll("[data-value]")].map((e) => e.textContent)).toEqual(["1,85 $", "1,85 $"]);
      expect(tags(container)).toEqual(["≈ 4,3×"]);
      expect(container.textContent).not.toContain("0,0×");
    });

    // Review 2: the span's average belongs to no bar when two plans priced it.
    it("nennt in der Stundenauflösung den Preis je Punkt nicht, wenn zwei Tarife die Spanne bepreist haben", async () => {
      const data = cells([2, 2]);
      data.subscription![1] = { ...data.subscription![1], usd: 2 * 2 * PP };
      const { container } = serve(data);
      await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
      const note = container.querySelector("[data-hour-note]")!.textContent!;
      expect(note).not.toMatch(/\d,\d\d \$/);
      expect(note).toMatch(/Preis je Punkt des Tarifs wert, der bei der jeweiligen Ablesung galt/);
      expect(note).toMatch(/seit der vorigen Ablesung abgelesen wurden/);
      expect(note).toMatch(/Stunden ohne Ablesung zeigen 0/);
      await click("Als Tabelle zeigen");
      expect(realCol(container).rows).toEqual(["0,92 $", "1,85 $"]);
    });

    it("nennt den Preis je Punkt, wenn alle bepreisten Schritte ihn teilen", async () => {
      const { container } = render([2, 3]);
      await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
      expect(container.querySelector("[data-hour-note]")!.textContent).toMatch(/ein Punkt 0,46 \$ wert/);
    });

    // Review 4: the chip stays pressed where the comparison does not apply; the page says why.
    it("sagt, warum „Echt gegen Liste“ nicht gezeichnet wird, wenn die echten Kosten fehlen", async () => {
      const { container } = render([2, 2]);
      await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
      expect(container.querySelector("[data-vs-absent]")).toBeNull();
      await click("Echt gegen Liste");
      expect(container.querySelector("[data-vs-absent]")).toBeNull();
      expect(container.querySelector("[data-vs-note]")).not.toBeNull();
      await act(async () => { await userEvent.click(within(chartSources()).getByRole("button", { name: /Echte Kosten/ })); });
      expect(screen.getByRole("button", { name: "Echt gegen Liste" })).toHaveAttribute("aria-pressed", "true");
      expect(container.querySelector("[data-vs-note]")).toBeNull();
      expect(container.querySelector("[data-vs-absent]")!.textContent).toMatch(/„Echt gegen Liste“ wird hier nicht gezeichnet/);
    });

    // PR #80 review: an API key or a run without a known credential met the
    // three listed conditions and still got no comparison; the first
    // condition is a subscription credential.
    it("nennt bei einem API-Schlüssel und ohne bekannte Zugangsdaten die Abo-Zugangsdaten als Bedingung", async () => {
      const KEY = "anthropic_api_key";
      const data = cells([2, 2]);
      data.cells.push(
        { period: "2026-11-17T18:00:00Z", agent_id: "a1", secret_key: KEY, secret_slot: 0, credential_kind: "api_key",
          reported_usd: BOOKED, measured: { output: 4 }, engine_usd: 4, tokens: { output: 200_000 } },
        { period: "2026-11-17T18:00:00Z", agent_id: "a1", secret_key: null, secret_slot: null, credential_kind: "",
          reported_usd: BOOKED, measured: { output: 1 }, engine_usd: 1, tokens: { output: 50_000 } },
      );
      const { container } = serve(data);
      const block = (id: string) => container.querySelector<HTMLElement>(`[data-credential="${id}"]`)!;
      await waitFor(() => { if (!block(`${KEY}#0`) || !block("")) throw new Error("blocks not there yet"); });
      await click("Echt gegen Liste");
      for (const id of [`${KEY}#0`, ""]) {
        const why = block(id).querySelector("[data-vs-absent]")!.textContent!;
        expect(why).toMatch(/„Echt gegen Liste“ wird hier nicht gezeichnet: Dafür braucht es Abonnement-Zugangsdaten mit Zählerablesungen,/);
      }
      // the subscription draws it
      expect(block(`${SEAT}#0`).querySelector("[data-vs-absent]")).toBeNull();
    });

    it("sagt es auch bei Punkten ohne Tarifpreis", async () => {
      const data = cells([2, 2]);
      data.subscription![1] = { ...data.subscription![1], usd: null, unpriced_points: 2 };
      const { container } = serve(data);
      await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" });
      await click("Echt gegen Liste");
      expect(container.querySelector("[data-vs-absent]")).not.toBeNull();
    });

    it("behält beim Wechsel der Ansicht Quellen- und Agentenfilter", async () => {
      const { container } = render([2, 2]);
      await screen.findByRole("group", { name: MULTIPLE });
      const chip = (name: string) => screen.getByRole("button", { name });
      await act(async () => { await userEvent.click(within(chartSources()).getByRole("button", { name: LEAD })); });
      await click(/^Coordinator/);
      await click("Echt gegen Liste");
      expect(within(chartSources()).getByRole("button", { name: LEAD })).toHaveAttribute("aria-pressed", "false");
      expect(chip("Echt gegen Liste")).toHaveAttribute("aria-pressed", "true");
      expect(screen.getByRole("button", { name: /^Coordinator/ })).toHaveAttribute("aria-pressed", "true");
      // an agent filter has no real bar to set against the list
      expect(container.querySelector("[data-tag]")).toBeNull();
      await click("Beträge");
      expect(within(chartSources()).getByRole("button", { name: LEAD })).toHaveAttribute("aria-pressed", "false");
      expect(screen.getByRole("button", { name: /^Coordinator/ })).toHaveAttribute("aria-pressed", "true");
    });
  });
});
