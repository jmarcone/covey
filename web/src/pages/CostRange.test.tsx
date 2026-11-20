import { describe, it, expect, beforeEach, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { useLocation } from "react-router";
import Costs from "./Costs";
import type { CostCells } from "../api";
import { mockFetch, renderWithProviders, useGerman } from "../test/render";

// The custom range of the Costs page: in the address (?from=&to=&tz=), read
// back from it, a zone switch that moves the axis, and a range that cannot be
// used says why next to the fields — not an empty page. The tests run in
// Asia/Tokyo (UTC+9, vite.config.ts); every date is in the past of any run.

const SEAT = "claude_code_oauth_token";
const FROM = "2026-11-20T16:00:00Z";
const TO = "2026-11-20T16:45:00Z";
const AT = ["2026-11-20T16:00:00Z", "2026-11-20T16:10:00Z"];

function cells(meter: boolean): CostCells {
  const cell = (period: string, output: number) => ({
    period, agent_id: "a1", secret_key: SEAT, secret_slot: 0, credential_kind: "subscription",
    reported_usd: 3, measured: { output }, engine_usd: output, tokens: { output: output * 50_000 },
  });
  return {
    since: FROM,
    until: TO,
    periods: AT,
    agents: [{ id: "a1", label: "Coordinator", reported_usd: 6, measured_usd: 11 }],
    types: ["cache_read", "cache_write", "input", "output", "web_search", "unpriced", "unattributed"],
    cells: [cell(AT[0], 6), cell(AT[1], 5)],
    subscription: [],
    meter: meter ? [{
      secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd", window_kind: "seven_day",
      baseline_at: AT[0], baseline_percent: 9, baseline_before_window: false, last_at: AT[1], last_percent: 13,
      readings: 4, points: 4, points_low: 3, points_high: 5, longest_gap_secs: 900, segments: null, measured_usd: 11,
    }] : [],
    plans: [],
    bookings: [],
  };
}

const NO_BILL = { bucket: "hour", points: [], sync: { configured: false, last_attempt_at: null, last_success_at: null,
  last_daily_at: null, data_refreshed_at: null, last_status: "", last_error: "" } };

const routes = (meter = true) => ({
  "/api/v1/cost/cells": cells(meter),
  "/api/v1/cost/billed": NO_BILL,
  "/api/v1/cost/runs": [],
  "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
  "/api/v1/cost/usage-readings": [],
});

function Probe() {
  const l = useLocation();
  return <output data-testid="loc">{l.pathname + l.search}</output>;
}

function serve(route: string, meter = true) {
  const m = mockFetch(routes(meter));
  renderWithProviders(<><Costs /><Probe /></>, { route });
  return m;
}
const search = () => new URLSearchParams(screen.getByTestId("loc").textContent!.split("?")[1] ?? "");
const field = (name: RegExp) => screen.getByLabelText(name) as HTMLInputElement;

describe("Kosten — eigener Zeitraum", () => {
  beforeEach(() => useGerman());

  it("fragt genau das Fenster aus der Adresse ab und füllt die Felder daraus", async () => {
    const m = serve(`/costs?from=${FROM}&to=${TO}&tz=utc`);
    await screen.findByRole("form", { name: "Eigener Zeitraum" });
    expect(field(/^Von/).value).toBe("2026-11-20T16:00");
    expect(field(/^Bis/).value).toBe("2026-11-20T16:45");
    await waitFor(() => expect(m.calls.some((c) => c.includes("/cost/cells"))).toBe(true));
    // Every endpoint of the page gets the same window, as from and to — not as days.
    const asked = m.calls.filter((c) => /\/cost\/(cells|billed|usage-readings|models|runs)\b/.test(c));
    expect(asked.length).toBeGreaterThanOrEqual(4);
    for (const c of asked.filter((x) => !x.includes("days=8"))) {
      expect(c).toContain(`from=${encodeURIComponent(FROM)}`);
      expect(c).toContain(`to=${encodeURIComponent(TO)}`);
      expect(c).not.toContain("days=");
    }
    expect(m.calls.some((c) => c.includes("/cost/indicators"))).toBe(false);
    // The span is 45 minutes: 10-minute buckets.
    expect(m.calls.find((c) => c.includes("/cost/cells"))).toContain("bucket=10min");
  });

  it("schreibt einen übernommenen Zeitraum in die Adresse und liest ihn von dort wieder", async () => {
    serve("/costs?tz=utc");
    fireEvent.click(screen.getByRole("button", { name: "Eigener Zeitraum…" }));
    await screen.findByRole("form", { name: "Eigener Zeitraum" });
    fireEvent.change(field(/^Von/), { target: { value: "2026-11-20T16:00" } });
    fireEvent.change(field(/^Bis/), { target: { value: "2026-11-20T16:45" } });
    fireEvent.click(screen.getByRole("button", { name: "Übernehmen" }));
    await waitFor(() => expect(search().get("from")).toBe(FROM));
    expect(search().get("to")).toBe(TO);
    expect(search().get("tz")).toBe("utc");
    // A quick range leaves the custom one.
    fireEvent.click(screen.getByRole("button", { name: "7 Tage" }));
    await waitFor(() => expect(search().get("from")).toBeNull());
    expect(search().get("days")).toBe("7");
    expect(screen.queryByRole("form", { name: "Eigener Zeitraum" })).toBeNull();
  });

  it("liest die Felder in der gewählten Zeitzone und bewegt beim Umschalten die Achse mit", async () => {
    serve(`/costs?from=${FROM}&to=${TO}&tz=utc`);
    await screen.findAllByText("20.11., 16:00 UTC");
    const panel = within(screen.getByRole("form", { name: "Eigener Zeitraum" }));
    fireEvent.click(panel.getByRole("button", { name: "Lokal" }));
    // The same instants on the reader's clock (UTC+9), the zone named, not UTC.
    await waitFor(() => expect(search().get("tz")).toBe("local"));
    expect(field(/^Von/).value).toBe("2026-11-21T01:00");
    expect(field(/^Bis/).value).toBe("2026-11-21T01:45");
    const labels = await screen.findAllByText(/^21\.11\., 01:00/);
    expect(labels.length).toBeGreaterThan(0);
    expect(labels[0].textContent).not.toContain("UTC");
    expect(screen.queryAllByText("20.11., 16:00 UTC")).toHaveLength(0);
    // The range itself did not move.
    expect(search().get("from")).toBe(FROM);
    expect(search().get("to")).toBe(TO);
    // Back to UTC.
    fireEvent.click(within(screen.getByRole("form", { name: "Eigener Zeitraum" })).getByRole("button", { name: "UTC" }));
    await screen.findAllByText("20.11., 16:00 UTC");
  });

  it("nennt die UTC-Zeit im Tooltip, wenn die Achse lokal läuft", async () => {
    serve(`/costs?from=${FROM}&to=${TO}&tz=local`);
    await screen.findAllByText(/^21\.11\., 01:00/);
    const bar = document.querySelector("[role='tooltip']") ?? null;
    expect(bar).toBeNull();
    const period = document.querySelector("svg [tabindex='0'], svg [role='button']") as SVGElement | null;
    expect(period).not.toBeNull();
    fireEvent.focus(period!);
    const tip = await screen.findByRole("tooltip");
    expect(tip.querySelector("[data-tip-utc]")!.textContent).toBe("20.11., 16:00 UTC");
  });

  it("zeigt bei einem unbrauchbaren Zeitraum in der Adresse den Grund neben den Feldern und fragt nichts ab", async () => {
    const m = serve(`/costs?from=2026-11-20T17:00:00Z&to=2026-11-20T16:00:00Z&tz=utc`);
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toBe("Das Ende muss nach dem Anfang liegen.");
    expect(alert.textContent).not.toMatch(/must be after/); // never the server's words
    expect(field(/^Von/)).toHaveAttribute("aria-invalid", "true");
    // The fields hold what the address says, so the error points at them.
    expect(field(/^Von/)).toHaveValue("2026-11-20T17:00");
    expect(field(/^Bis/)).toHaveValue("2026-11-20T16:00");
    expect(document.querySelector("[data-range-window]")).toBeNull(); // no range is in force
    expect(document.querySelector("[data-range-refused]")).not.toBeNull();
    expect(m.calls.filter((c) => /\/cost\//.test(c)).every((c) => c.includes("days=8"))).toBe(true);
    expect(screen.getByRole("heading", { level: 1 })).toBeInTheDocument(); // the page, not a blank
  });

  it("weist einen zu langen, einen unvollständigen und einen zukünftigen Zeitraum ab", async () => {
    serve(`/costs?from=2026-09-01T00:00:00Z&to=2026-11-01T00:00:00Z&tz=utc`);
    expect((await screen.findByRole("alert")).textContent).toBe("Ein Zeitraum darf höchstens 31 Tage umfassen.");
  });

  it("zeigt beim Übernehmen eines Zeitraums von hinten nach vorn den Fehler und lässt die Adresse stehen", async () => {
    serve(`/costs?from=${FROM}&to=${TO}&tz=utc`);
    await screen.findByRole("form", { name: "Eigener Zeitraum" });
    fireEvent.change(field(/^Bis/), { target: { value: "2026-11-20T15:00" } });
    fireEvent.click(screen.getByRole("button", { name: "Übernehmen" }));
    expect((await screen.findByRole("alert")).textContent).toBe("Das Ende muss nach dem Anfang liegen.");
    expect(search().get("to")).toBe(TO);
    // An empty field is not a time either.
    fireEvent.change(field(/^Von/), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Übernehmen" }));
    expect((await screen.findByRole("alert")).textContent).toBe("Gib Anfang und Ende als Datum und Uhrzeit an.");
  });

  it("zeigt die Ablehnung des Servers mit ihrem Code in eigenen Worten", async () => {
    const fn = vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.includes("/cost/cells")) {
        return new Response(JSON.stringify({ error: "the range may span at most 31 days", code: "range_span" }), { status: 400, headers: { "Content-Type": "application/json" } });
      }
      return new Response(JSON.stringify(url.includes("/cost/billed") ? NO_BILL : url.includes("/cost/models") ? { rows: [] } : []), { status: 200 });
    });
    vi.stubGlobal("fetch", fn);
    renderWithProviders(<><Costs /><Probe /></>, { route: `/costs?from=${FROM}&to=${TO}&tz=utc` });
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toBe("Ein Zeitraum darf höchstens 31 Tage umfassen.");
    expect(alert.textContent).not.toContain("the range may span");
  });

  it("sagt bei Zugangsdaten ohne Zählerstand im Abschnitt, dass es keinen gibt — ohne echten Betrag und ohne Vielfaches", async () => {
    serve(`/costs?from=${FROM}&to=${TO}&tz=utc`, false);
    const note = await screen.findByText("Kein Zählerstand in diesem Abschnitt: keine echten Kosten und kein Vielfaches dafür.");
    expect(note).toHaveAttribute("data-no-meter");
    // The real cost stays a dash with its reason, never an amount, and there is no multiple.
    const real = screen.getByRole("group", { name: "Echte Kosten (Schätzung)" });
    expect(real.textContent).toContain("keine Zählerablesung");
    expect(real.textContent).not.toMatch(/\$|≈/);
    expect(screen.queryByRole("group", { name: "Listen-$ je echtem $" })).toBeNull();
  });

  it("sagt bei den schnellen Zeiträumen nichts davon", async () => {
    serve(`/costs?tz=utc`, false);
    await screen.findAllByText("20.11., 16:00 UTC");
    expect(document.querySelector("[data-no-meter]")).toBeNull();
  });

  it("legt die Preisliste für einen eigenen Zeitraum nicht vor, mit dem Grund", async () => {
    serve(`/costs?from=${FROM}&to=${TO}&tz=utc`);
    expect(await screen.findByText(/Die Preisliste gilt für die Schnellbereiche/)).toBeInTheDocument();
  });
});
