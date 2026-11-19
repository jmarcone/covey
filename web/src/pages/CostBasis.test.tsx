import { describe, it, expect, beforeEach, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Costs from "./Costs";
import type { CostCells, CredentialBasis, WindowCharge } from "../api";
import { mockFetch, renderWithProviders, testPrincipal, useGerman } from "../test/render";
import { basisAt, credentialFigures, perCredentialTotals, usageFigure } from "./costFigures";

// How a credential's real money is formed (0151, #120): a seat billed by
// usage shows its usage charges, each in its own currency, and a dollar figure
// and a multiple only where a rate with its date was given.

const SEAT = "claude_code_oauth_token";
const PP = 200 * 12 / 52 / 100;

function charge(over: Partial<WindowCharge> = {}): WindowCharge {
  return {
    id: "c1", secret_key: SEAT, secret_slot: 0, amount: 180, currency: "EUR",
    from: "2026-11-17T08:00:00Z", to: "2026-11-18T08:00:00Z", fx_to_usd: null, fx_date: null, note: "gemessen",
    window_amount: 180, counted_amount: 180, counted_usd: null, ...over,
  };
}

const usageBasis: CredentialBasis = { secret_key: SEAT, secret_slot: 0, basis: "usage", valid_from: "2026-11-01T00:00:00Z", valid_to: null };

function cells(over: Partial<CostCells> = {}): CostCells {
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
        window_kind: "seven_day", points: 4, readings: 2, resets: 0, longest_gap_secs: 900, usd: 4 * PP, unpriced_points: 0 },
    ],
    meter: [{
      secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd", window_kind: "seven_day",
      baseline_at: "2026-11-17T18:00:00Z", baseline_percent: 9, baseline_before_window: false,
      last_at: "2026-11-17T20:00:00Z", last_percent: 13, readings: 2, points: 4,
      points_low: 3, points_high: 5, longest_gap_secs: 900, segments: null, measured_usd: 16,
    }],
    plans: [{
      runtime_id: null, ord: 0, secret_key: SEAT, secret_slot: 0, plan_name: "Max", amount: 200, currency: "USD", period: "month",
      tax_basis: "net", fx_to_usd: 1, fx_date: null, valid_from: "2026-11-01T00:00:00Z", valid_to: null,
      weekly_usd: 200 * 12 / 52, usd_per_point: PP,
    }],
    bookings: [],
    ...over,
  };
}

/** Under the usage basis the server prices none of the meter's points. */
function onUsage(charges: WindowCharge[]): CostCells {
  const c = cells({ cost_bases: [usageBasis], usage_charges: charges });
  c.subscription = c.subscription!.map((s) => ({ ...s, usd: null, unpriced_points: s.points }));
  return c;
}

const NO_BILL = { bucket: "hour", points: [], sync: { configured: false, last_attempt_at: null, last_success_at: null,
  last_daily_at: null, data_refreshed_at: null, last_status: "", last_error: "" } };

function serve(data: CostCells) {
  mockFetch({
    "/api/v1/cost/cells": data,
    "/api/v1/cost/billed": NO_BILL,
    "/api/v1/cost/runs": [],
    "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
    "/api/v1/cost/usage-readings": [],
  });
  return renderWithProviders(<Costs />);
}

async function block(container: HTMLElement) {
  return waitFor(() => {
    const b = container.querySelector<HTMLElement>(`[data-credential="${SEAT}#0"]`);
    if (!b) throw new Error("no block yet");
    return b;
  });
}

const END = new Date("2026-11-18T12:00:00Z");

describe("Kostengrundlage — Zahlen", () => {
  it("nimmt die Grundlage, die zum Zeitpunkt galt", () => {
    const bases: CredentialBasis[] = [
      { ...usageBasis, basis: "seat", valid_to: "2026-11-10T00:00:00Z" },
      { ...usageBasis, basis: "usage", valid_from: "2026-11-10T00:00:00Z" },
    ];
    expect(basisAt(bases, SEAT, 0, new Date("2026-11-05T00:00:00Z"))).toBe("seat");
    expect(basisAt(bases, SEAT, 0, new Date("2026-11-10T00:00:00Z"))).toBe("usage");
    expect(basisAt(bases, SEAT, 1, new Date("2026-11-10T00:00:00Z"))).toBeNull();
    expect(basisAt(bases, SEAT, 0, new Date("2026-10-01T00:00:00Z"))).toBeNull();
  });

  it("zählt je Währung, rechnet nie um und hat nur mit Kurs einen USD-Betrag", () => {
    const u = usageFigure({ cost_bases: [usageBasis], usage_charges: [
      charge(), charge({ id: "c2", amount: 100, counted_amount: 100, fx_to_usd: 1.08, fx_date: "2026-11-18", counted_usd: 108 }),
      charge({ id: "c3", currency: "USD", amount: 5, counted_amount: 5, counted_usd: 5 }),
    ] }, SEAT, 0, END)!;
    expect(u.currencies).toEqual([
      { currency: "EUR", counted: 280, usd: null, charges: 2 },
      { currency: "USD", counted: 5, usd: 5, charges: 1 },
    ]);
    expect(u.usd).toBeNull();
    expect(usageFigure({}, SEAT, 0, END)).toBeNull();
  });

  it("lässt alles beim Alten, solange weder Grundlage noch Betrag gesetzt ist", () => {
    const [f] = credentialFigures({ cells: cells(), bucketMs: 3600_000, windowEnd: END });
    expect(f.basis).toBeNull();
    expect(f.usage).toBeNull();
    expect(f.real?.value).toBeCloseTo(4 * PP, 9);
    expect(f.multiple?.kind).toBe("estimate");
  });

  it("zieht einen nach Nutzung abgerechneten Sitz nicht in die Summe der Zähler", () => {
    const figs = credentialFigures({ cells: onUsage([charge()]), bucketMs: 3600_000, windowEnd: END });
    expect(figs[0].real).toBeNull();
    expect(figs[0].noMultiple).toBe("rate");
    expect(perCredentialTotals(figs).real).toBeNull();
  });
});

describe("Kostengrundlage — Seite", () => {
  beforeEach(() => useGerman());

  it("zeigt einen EUR-Betrag ohne Kurs in EUR, ohne USD-Betrag und ohne Vielfaches", async () => {
    const { container } = serve(onUsage([charge()]));
    const b = await block(container);
    const line = b.querySelector("[data-basis-line]")!.textContent!;
    expect(line).toMatch(/^Abgerechnet nach: Nutzung\./);
    expect(line).toMatch(/Dieser Sitz wird nach Nutzung abgerechnet, nicht nach seinem Plan/);
    const tile = within(b).getByRole("group", { name: "Nutzungsbeträge" });
    expect(within(tile).getByText("180,00 EUR")).toBeInTheDocument();
    expect(within(tile).getByText("Kein USD-Betrag: Es wurde kein Kurs mit Datum angegeben.")).toBeInTheDocument();
    expect(tile.textContent).not.toMatch(/\$/);
    expect(within(b).queryByRole("group", { name: "Echte Kosten (Schätzung)" })).toBeNull();
    expect(within(b).queryByRole("group", { name: "Listen-$ je echtem $" })).toBeNull();
    expect(b.querySelector("[data-no-multiple]")!.textContent).toBe(
      "Kein Vielfaches: Ohne Kurs mit Datum haben die Nutzungsbeträge keinen USD-Betrag.");
    expect(b.querySelector(`[data-charge="c1"]`)!.textContent).toMatch(/180,00 EUR · .* · voll gezählt · kein USD-Betrag: kein Kurs angegeben/);
  });

  it("zeigt mit Kurs und Datum den USD-Betrag und das Vielfache", async () => {
    const { container } = serve(onUsage([charge({ fx_to_usd: 1.08, fx_date: "2026-11-18", counted_usd: 194.4 })]));
    const b = await block(container);
    const tile = within(b).getByRole("group", { name: "Nutzungsbeträge" });
    expect(within(tile).getByText("180,00 EUR")).toBeInTheDocument();
    expect(within(tile).getByText(/^194,40\s\$ zu den angegebenen Kursen$/)).toBeInTheDocument();
    const mult = within(b).getByRole("group", { name: "Listen-$ je echtem $" });
    expect(within(mult).getByText(/×$/)).toBeInTheDocument();
    expect(b.querySelector("[data-no-multiple]")).toBeNull();
    expect(b.querySelector(`[data-charge="c1"]`)!.textContent).toMatch(/zu 1,0800 USD je EUR \(2026-11-18\): 194,40\s\$/);
  });

  it("sagt bei Sitz und Nutzung, dass beides zählt, und nennt einen Betrag zu einer Zeit ohne Nutzungsgrundlage", async () => {
    const data = cells({
      cost_bases: [{ ...usageBasis, basis: "seat_and_usage" }],
      usage_charges: [charge(), charge({ id: "c2", counted_amount: 0, from: "2026-10-01T00:00:00Z", to: "2026-10-02T00:00:00Z" })],
    });
    const { container } = serve(data);
    const b = await block(container);
    expect(b.querySelector("[data-basis-line]")!.textContent).toMatch(
      /^Abgerechnet nach: Sitzpreis und Nutzung\. Dieser Sitz wird nach Plan und nach Nutzung abgerechnet/);
    expect(within(b).getByRole("group", { name: "Echte Kosten (Schätzung)" })).toBeInTheDocument();
    expect(within(within(b).getByRole("group", { name: "Nutzungsbeträge" })).getByText("180,00 EUR")).toBeInTheDocument();
    // EUR without a rate: no seat + usage total in dollars.
    expect(within(b).queryByRole("group", { name: "Echte Kosten, Sitz + Nutzung" })).toBeNull();
    expect(b.querySelector("[data-uncounted]")!.textContent).toMatch(/nirgends zählen: 1/);
    expect(b.querySelector("[data-no-multiple]")!.textContent).toMatch(/^Kein Vielfaches: Sitz und Nutzung/);
  });

  it("speichert einen Betrag in seiner Währung, ohne Kurs keinen", async () => {
    const posts: { url: string; body: Record<string, unknown> }[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input), path = url.split("?")[0];
      const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });
      if ((init?.method ?? "GET").toUpperCase() === "POST") {
        posts.push({ url, body: JSON.parse(String(init?.body)) });
        return json({ id: "new" }, 201);
      }
      if (path.endsWith("/runtime-instances")) {
        return json([{ id: "rt-1", engine: "claude", display_name: "Claude", model: "", bindings: [], can_carry_blocking: false,
          creds: [{ ord: 2, kind: "subscription", secret_key: SEAT, secret_slot: 0, label: "" }] }]);
      }
      if (path.endsWith("/cost/cells")) return json(cells());
      if (path.endsWith("/cost/billed")) return json(NO_BILL);
      if (path.endsWith("/cost/runs")) return json([]);
      if (path.endsWith("/cost/models")) return json({ rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 });
      if (path.endsWith("/cost/usage-readings")) return json([]);
      return json({ error: "not mocked" }, 404);
    }));
    const { container } = renderWithProviders(<Costs me={testPrincipal("org_admin")} />);
    const b = await block(container);
    expect(b.querySelector("[data-basis-line]")!.textContent).toMatch(/^Abgerechnet nach: Sitzpreis \(keine Grundlage gesetzt/);
    await userEvent.click(await within(b).findByRole("button", { name: "Nutzungsbetrag hinzufügen" }));
    const form = within(b).getByRole("form", { name: `Nutzungsbetrag für ${SEAT} #0 hinzufügen` });
    await userEvent.type(within(form).getByLabelText("Betrag"), "180");
    await userEvent.type(within(form).getByLabelText("Notiz"), "gemessen");
    await userEvent.click(within(form).getByRole("button", { name: "Speichern" }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].url).toBe("/api/v1/runtime-instances/rt-1/credentials/2/usage-charges");
    const body = posts[0].body;
    expect(body.amount).toBe(180);
    expect(body.currency).toBe("EUR");
    expect(Date.parse(String(body.to)) - Date.parse(String(body.from))).toBe(86400_000);
    expect(body.note).toBe("gemessen");
    expect(body).not.toHaveProperty("fx_to_usd");
    expect(body).not.toHaveProperty("fx_date");
  });

  it("zeigt Lesenden ohne gesetzte Grundlage nichts Neues", async () => {
    const { container } = serve(cells());
    const b = await block(container);
    await screen.findAllByRole("group", { name: "Echte Kosten (Schätzung)" });
    expect(b.querySelector("[data-cost-basis]")).toBeNull();
  });
});
