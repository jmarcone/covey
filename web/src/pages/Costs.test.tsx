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
        window_kind: "seven_day", points: points[0], readings: 2, resets: 0, longest_gap_secs: 900, usd: null, unpriced_points: 0 },
      { period: "2026-11-17T19:00:00Z", secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd",
        window_kind: "seven_day", points: points[1], readings: 2, resets: 0, longest_gap_secs: 900, usd: null, unpriced_points: 0 },
    ],
    meter: [{
      secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd", window_kind: "seven_day",
      baseline_at: "2026-11-17T18:00:00Z", baseline_percent: 9, baseline_before_window: false,
      last_at: "2026-11-17T20:00:00Z", last_percent: 9 + total, readings: 4, points: total,
      points_low: Math.max(total - 1, 0), points_high: total + 1, longest_gap_secs: 900, segments: null,
    }],
    plans: [{
      runtime_id: null, ord: 0, secret_key: SEAT, secret_slot: 0, plan_name: "Max", amount: 200, currency: "USD", period: "month",
      tax_basis: "net", fx_to_usd: 1, fx_date: null, valid_from: "2026-11-01T00:00:00Z", valid_to: null,
      weekly_usd: 200 * 12 / 52, usd_per_point: 200 * 12 / 52 / 100,
    }],
    bookings: [{ secret_key: SEAT, secret_slot: 0, basis: "invocation", entries: 2, usd: 2 * BOOKED }],
  };
}

function render(points: [number, number]) {
  mockFetch({
    "/api/v1/cost/cells": cells(points),
    "/api/v1/cost/billed": { bucket: "hour", points: [], sync: { configured: false, last_attempt_at: null, last_success_at: null,
      last_daily_at: null, data_refreshed_at: null, last_status: "", last_error: "" } },
    "/api/v1/cost/runs": [],
    "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
    "/api/v1/cost/usage-readings": [],
  });
  return renderWithProviders(<Costs />);
}

const MULTIPLE = "Listen-$ je echtem $ (Untergrenze)";

describe("Kosten — Zahlen je Zugangsdaten", () => {
  beforeEach(() => useGerman());

  it("blendet das Vielfache unter drei Punkten Bewegung aus und nennt den Grund", async () => {
    render([1, 1]);
    expect(await screen.findByRole("group", { name: "Echte Kosten (Schätzung)" })).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: MULTIPLE })).toBeNull();
    expect(screen.getByText(/Kein Vielfaches: Der Zähler bewegte sich um 2 Punkt/)).toBeInTheDocument();
  });

  it("zeigt das Vielfache ab drei Punkten, mit Spanne und als Untergrenze", async () => {
    render([2, 2]);
    const tile = await screen.findByRole("group", { name: MULTIPLE });
    expect(within(tile).getByText(/^≈ .*×$/)).toBeInTheDocument();
    expect(within(tile).getByText(/^Spanne .*× – .*×$/)).toBeInTheDocument();
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
