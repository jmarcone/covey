import { describe, it, expect, beforeEach, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Costs from "./Costs";
import type { CostCells, CredentialBasis, MeterStep, WindowCharge } from "../api";
import i18n from "../i18n";
import { mockFetch, renderWithProviders, testPrincipal, useGerman } from "../test/render";
import { basisAt, credentialFigures, perCredentialTotals, usageFigure, type CredentialFigures } from "./costFigures";
import { chargeLine, CostBasisPanel } from "./CostBasis";

// How a credential's real money is formed (0151, #120): a seat billed by
// usage shows its usage charges, each in its own currency, and a dollar figure
// and a multiple only where a rate with its date was given.

const SEAT = "claude_code_oauth_token";
const PP = 200 * 12 / 52 / 100;
const SINCE = "2026-11-11T00:00:00Z";
const LATER = "2099-01-01T00:00:00Z";

function charge(over: Partial<WindowCharge> = {}): WindowCharge {
  return {
    id: "c1", secret_key: SEAT, secret_slot: 0, amount: 180, currency: "EUR",
    from: "2026-11-17T08:00:00Z", to: "2026-11-18T08:00:00Z", fx_to_usd: null, fx_date: null,
    window_amount: 180, counted_amount: 180, counted_usd: null, ...over,
  };
}

const usageBasis: CredentialBasis = { secret_key: SEAT, secret_slot: 0, basis: "usage", valid_from: "2026-11-01T00:00:00Z", valid_to: null };

function step(period: string, points: number, over: Partial<MeterStep> = {}): MeterStep {
  return { period, secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd",
    window_kind: "seven_day", points, readings: 2, resets: 0, longest_gap_secs: 900, usd: points * PP, unpriced_points: 0, ...over };
}

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
    subscription: [step("2026-11-17T18:00:00Z", 4)],
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

/** Under the usage basis the server prices none of the meter's points: they
 *  are its usage points. */
function onUsage(charges: WindowCharge[]): CostCells {
  const c = cells({ cost_bases: [usageBasis], usage_charges: charges, since: SINCE });
  c.subscription = c.subscription!.map((s) => ({ ...s, usd: null, unpriced_points: 0, usage_points: s.points }));
  return c;
}

/** Seat until 19:00, usage from then: the 18:00 step priced by the plan, the
 *  19:00 step the charges'. */
function seatThenUsage(charges: WindowCharge[] = []): CostCells {
  const change = "2026-11-17T19:00:00Z";
  return cells({
    cost_bases: [
      { ...usageBasis, basis: "seat", valid_to: change },
      { ...usageBasis, basis: "usage", valid_from: change },
    ],
    usage_charges: charges, since: SINCE,
    subscription: [step("2026-11-17T18:00:00Z", 4), step("2026-11-17T19:00:00Z", 3, { usd: null, usage_points: 3 })],
    meter: [{ ...cells().meter![0], points: 7, last_percent: 16 }],
  });
}

/** Usage until 19:00, seat from then: the other way round. */
function usageThenSeat(charges: WindowCharge[]): CostCells {
  const change = "2026-11-17T19:00:00Z";
  return cells({
    cost_bases: [
      { ...usageBasis, basis: "usage", valid_to: change },
      { ...usageBasis, basis: "seat", valid_from: change },
    ],
    usage_charges: charges, since: SINCE,
    subscription: [step("2026-11-17T18:00:00Z", 4, { usd: null, usage_points: 4 }), step("2026-11-17T19:00:00Z", 2)],
    meter: [{ ...cells().meter![0], points: 6, last_percent: 15 }],
  });
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
const fig = (data: CostCells) => credentialFigures({ cells: data, bucketMs: 3600_000, windowEnd: END })[0];

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
    const f = fig(cells());
    expect(f.basis).toBeNull();
    expect(f.usage).toBeNull();
    expect(f.real?.value).toBeCloseTo(4 * PP, 9);
    expect(f.multiple?.kind).toBe("estimate");
    const tot = perCredentialTotals([f]);
    expect(tot.real).toMatchObject({ with: 1, of: 1 });
    expect(tot.usage).toBeNull();
  });

  // Review major 1: the page took one basis for the whole window.
  it("behält bei Sitz → Nutzung im Zeitraum den Sitzanteil, wie ihn der Server bepreist hat", () => {
    const f = fig(seatThenUsage());
    expect(f.basis).toBe("usage");
    expect(f.usage!.changed).toBe(true);
    expect(f.real).not.toBeNull();
    expect(f.real!.priced).toBe(true);
    expect(f.real!.value).toBeCloseTo(4 * PP, 9);
    expect(f.real!.points).toBe(4);
    expect(f.real!.usagePoints).toBe(3);
    expect(f.real!.unpricedPoints).toBe(0);
    expect(f.multiple).toBeNull();
    expect(f.noMultiple).toBe("basisChanged");
    // ... and the seat stays in the headline with its seat part.
    const tot = perCredentialTotals([f]);
    expect(tot.real).toMatchObject({ with: 1, of: 1 });
    expect(tot.real!.usd).toBeCloseTo(4 * PP, 9);
    expect(tot.real!.recomputed).toBeNull();
  });

  it("zeigt bei Nutzung → Sitz einen gezählten Betrag, obwohl am Ende der Sitz gilt", () => {
    const f = fig(usageThenSeat([charge({ amount: 10, counted_amount: 10, window_amount: 10, to: "2026-11-17T18:00:00Z" })]));
    expect(f.basis).toBe("seat");
    expect(f.usage!.changed).toBe(true);
    expect(f.usage!.currencies).toEqual([{ currency: "EUR", counted: 10, usd: null, charges: 1 }]);
    expect(f.real!.value).toBeCloseTo(2 * PP, 9);
    expect(f.real!.usagePoints).toBe(4);
    expect(f.noMultiple).toBe("basisChanged");
  });

  // Review major 2: a basis entered to start later was shown as current.
  it("nimmt eine erst später beginnende Grundlage nicht als die geltende", () => {
    const only = usageFigure({ cost_bases: [{ ...usageBasis, valid_from: LATER }], since: SINCE }, SEAT, 0, END)!;
    expect(only.basis).toBeNull();
    expect(only.upcoming.map((b) => b.basis)).toEqual(["usage"]);
    expect(only.changed).toBe(false);
    expect(only.regimes).toEqual(["seat"]);
    const next = usageFigure({ cost_bases: [
      { ...usageBasis, basis: "seat", valid_to: LATER }, { ...usageBasis, valid_from: LATER },
    ], since: SINCE }, SEAT, 0, END)!;
    expect(next.basis).toBe("seat");
    expect(next.changed).toBe(false);
    expect(next.upcoming).toHaveLength(1);
    // The seat is priced as without a basis: the later one changes nothing yet.
    const f = fig(cells({ cost_bases: [{ ...usageBasis, valid_from: LATER }], since: SINCE }));
    expect(f.basis).toBeNull();
    expect(f.multiple?.kind).toBe("estimate");
    expect(f.real!.value).toBeCloseTo(4 * PP, 9);
  });

  // Review major 3: the headline dropped seats billed by usage and still
  // looked complete.
  it("zählt einen nach Nutzung abgerechneten Sitz in der Summe mit: ohne Zahl, mit seinen Beträgen daneben", () => {
    const usageSeat = fig(onUsage([charge()]));
    expect(usageSeat.real).toBeNull();
    expect(usageSeat.noMultiple).toBe("rate");
    const other = { ...fig(cells()), id: "other#0", secretKey: "other" };
    const tot = perCredentialTotals([usageSeat, other]);
    expect(tot.real).toMatchObject({ with: 1, of: 2 });
    expect(tot.usage).toEqual([{ currency: "EUR", counted: 180, usd: null, charges: 1 }]);
    expect(perCredentialTotals([usageSeat]).real).toMatchObject({ with: 0, of: 1 });
  });

  // Review major 4: the multiple divided the whole window's list by charges
  // covering part of it.
  it("rechnet das Vielfache nur über die Zeiträume der gezählten Beträge", () => {
    const usd = (over: Partial<WindowCharge>) => charge({ currency: "USD", ...over });
    // 18:00–19:00 only: that hour's list (6 + 2), not the window's 16.
    let f = fig(onUsage([usd({ amount: 4, counted_amount: 4, counted_usd: 4, from: "2026-11-17T18:00:00Z", to: "2026-11-17T19:00:00Z" })]));
    expect(f.usageList).toBeCloseTo(8, 9);
    expect(f.usageMultiple).toBeCloseTo(2, 9);
    // Half an hour: half of that hour's list, pro rata by time.
    f = fig(onUsage([usd({ amount: 2, counted_amount: 2, counted_usd: 2, from: "2026-11-17T18:30:00Z", to: "2026-11-17T19:00:00Z" })]));
    expect(f.usageList).toBeCloseTo(4, 9);
    expect(f.usageMultiple).toBeCloseTo(2, 9);
    // Over the whole window both hours: 16.
    f = fig(onUsage([usd({ amount: 8, counted_amount: 8, counted_usd: 8, from: "2026-11-17T00:00:00Z", to: "2026-11-18T00:00:00Z" })]));
    expect(f.usageList).toBeCloseTo(16, 9);
    expect(f.usageMultiple).toBeCloseTo(2, 9);
    // Where covey ran nothing: no multiple, and why.
    f = fig(onUsage([usd({ amount: 8, counted_amount: 8, counted_usd: 8, from: "2026-11-12T00:00:00Z", to: "2026-11-13T00:00:00Z" })]));
    expect(f.usageMultiple).toBeNull();
    expect(f.noMultiple).toBe("spanList");
  });

  it("schreibt für einen nicht gezählten Betrag mit Kurs „–“, nie 0,00 $", () => {
    useGerman();
    const line = chargeLine(i18n.t.bind(i18n), charge({ fx_to_usd: 1.08, fx_date: "2026-11-18", counted_amount: 0, counted_usd: 0 }));
    expect(line).toMatch(/nicht gezählt/);
    expect(line).toMatch(/\(2026-11-18\): –$/);
    expect(line).not.toMatch(/\$/);
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

  it("zeigt mit Kurs und Datum den USD-Betrag und das Vielfache über die Zeiträume der Beträge", async () => {
    const { container } = serve(onUsage([charge({ fx_to_usd: 1.08, fx_date: "2026-11-18", counted_usd: 194.4 })]));
    const b = await block(container);
    const tile = within(b).getByRole("group", { name: "Nutzungsbeträge" });
    expect(within(tile).getByText("180,00 EUR")).toBeInTheDocument();
    expect(within(tile).getByText(/^194,40\s\$ zu den angegebenen Kursen$/)).toBeInTheDocument();
    const mult = within(b).getByRole("group", { name: "Listen-$ je echtem $" });
    expect(within(mult).getByText(/×$/)).toBeInTheDocument();
    expect(mult.textContent).toMatch(/über die Zeiträume der gezählten Nutzungsbeträge \(16,00\s\$/);
    expect(b.querySelector("[data-no-multiple]")).toBeNull();
    expect(b.querySelector(`[data-charge="c1"]`)!.textContent).toMatch(/zu 1,0800 USD je EUR \(2026-11-18\): 194,40\s\$/);
  });

  it("sagt bei Sitz und Nutzung, dass beides zählt, und nennt einen Betrag zu einer Zeit ohne Nutzungsgrundlage", async () => {
    const data = cells({
      cost_bases: [{ ...usageBasis, basis: "seat_and_usage" }], since: SINCE,
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

  // Review major 1, on the page: usage → seat within the window still shows
  // the charge counted while usage held, and the seat's real figure.
  it("zeigt bei Nutzung → Sitz die gezählten Beträge und den Sitzanteil", async () => {
    const { container } = serve(usageThenSeat([charge({ amount: 10, counted_amount: 10, window_amount: 10, to: "2026-11-17T18:00:00Z" })]));
    const b = await block(container);
    const line = b.querySelector("[data-basis-line]")!.textContent!;
    expect(line).toMatch(/^Abgerechnet nach: Sitzpreis\./);
    expect(line).toMatch(/Die Grundlage hat sich in diesem Zeitraum geändert/);
    expect(within(within(b).getByRole("group", { name: "Nutzungsbeträge" })).getByText("10,00 EUR")).toBeInTheDocument();
    const real = within(b).getByRole("group", { name: "Echte Kosten (Schätzung)" });
    expect(real.textContent).toMatch(/nur der Sitzanteil: 4 Punkte/);
    expect(b.querySelector("[data-no-multiple]")!.textContent).toBe("Kein Vielfaches: Die Grundlage hat sich in diesem Zeitraum geändert.");
  });

  // Review major 2, on the page: a later basis is listed from its start.
  it("nennt eine später beginnende Grundlage „ab …“ und nicht als die geltende", async () => {
    const { container } = serve(cells({ cost_bases: [{ ...usageBasis, valid_from: LATER }], since: SINCE }));
    const b = await block(container);
    const line = b.querySelector("[data-basis-line]")!.textContent!;
    expect(line).toMatch(/^Abgerechnet nach: Sitzpreis \(keine Grundlage gesetzt/);
    expect(line).toMatch(/Ab .*2099.*: Nutzung \(eingetragen, gilt noch nicht\)\./);
    expect(line).not.toMatch(/geändert/);
    expect(within(b).getByRole("group", { name: "Echte Kosten (Schätzung)" })).toBeInTheDocument();
    expect(within(b).queryByRole("group", { name: "Nutzungsbeträge" })).toBeNull();
  });

  // Review major 3, on the page: the usage charges stand beside the totals.
  it("nennt die Nutzungsbeträge neben den Summen, in keiner Zahl enthalten", async () => {
    serve(onUsage([charge()]));
    await waitFor(() => expect(document.body.textContent).toMatch(
      /Nutzungsbeträge, in keiner dieser Zahlen enthalten: 180,00 EUR \(kein USD-Betrag: kein Kurs angegeben\)\./));
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

  // Charges outside the window, and the notes the cells do not carry: the
  // credential's own list, for the roles that may read it.
  it("listet Admins alle Beträge mit Notiz, auch außerhalb des Zeitraums", async () => {
    const urls: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input), path = url.split("?")[0];
      urls.push(path);
      const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });
      if (path.endsWith("/runtime-instances")) {
        return json([{ id: "rt-1", engine: "claude", display_name: "Claude", model: "", bindings: [], can_carry_blocking: false,
          creds: [{ ord: 2, kind: "subscription", secret_key: SEAT, secret_slot: 0, label: "" }] }]);
      }
      if (path.endsWith("/credentials/2/usage-charges")) {
        return json([
          { id: "c1", runtime_id: "rt-1", ord: 2, secret_key: SEAT, secret_slot: 0, amount: 180, currency: "EUR",
            from: "2026-11-17T08:00:00Z", to: "2026-11-18T08:00:00Z", fx_to_usd: null, fx_date: null, note: "gemessen", created_at: "2026-11-18T09:00:00Z" },
          { id: "old", runtime_id: "rt-1", ord: 2, secret_key: SEAT, secret_slot: 0, amount: 90, currency: "EUR",
            from: "2026-09-01T00:00:00Z", to: "2026-09-02T00:00:00Z", fx_to_usd: 1.1, fx_date: "2026-09-02", note: "Rechnung Sept.", created_at: "2026-09-03T09:00:00Z" },
        ]);
      }
      if (path.endsWith("/cost/cells")) return json(onUsage([charge()]));
      if (path.endsWith("/cost/billed")) return json(NO_BILL);
      if (path.endsWith("/cost/runs")) return json([]);
      if (path.endsWith("/cost/models")) return json({ rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 });
      if (path.endsWith("/cost/usage-readings")) return json([]);
      return json({ error: "not mocked" }, 404);
    }));
    const { container } = renderWithProviders(<Costs me={testPrincipal("org_admin")} />);
    const b = await block(container);
    // The window's list has no note: the cells do not carry one.
    expect(b.querySelector(`[data-charge="c1"] [data-charge-note]`)).toBeNull();
    const all = await waitFor(() => {
      const d = b.querySelector<HTMLElement>("[data-all-charges]");
      if (!d) throw new Error("no disclosure yet");
      return d;
    });
    expect(urls.some((u) => u.endsWith("/usage-charges"))).toBe(false);
    await userEvent.click(within(all).getByRole("button", { name: /Alle Nutzungsbeträge dieses Zugangs/ }));
    const old = await waitFor(() => {
      const li = all.querySelector<HTMLElement>(`[data-stored-charge="old"]`);
      if (!li) throw new Error("not listed yet");
      return li;
    });
    expect(old.textContent).toMatch(/90,00 EUR · .* · zu 1,1000 USD je EUR \(2026-09-02\) · Rechnung Sept\./);
    expect(within(old).getByRole("button", { name: "Löschen" })).toBeInTheDocument();
    expect(all.querySelector(`[data-stored-charge="c1"]`)!.textContent).toMatch(/gemessen/);
  });

  it("warnt bei einem API-Schlüssel mit abgeglichener Rechnung, dass die Beträge daneben stehen", async () => {
    mockFetch({});
    const u = usageFigure({ cost_bases: [usageBasis], usage_charges: [charge()], since: SINCE }, SEAT, 0, END)!;
    const f = { id: `${SEAT}#0`, secretKey: SEAT, slot: 0, kind: "api_key", basis: "usage", usage: u,
      billed: { state: "measured" }, meter: null } as unknown as CredentialFigures;
    const { container } = renderWithProviders(<CostBasisPanel f={f} name="Key" editable={false} />);
    expect(container.querySelector("[data-beside-bill]")!.textContent).toMatch(/nicht dieselbe Rechnung sein, sonst zählt sie doppelt/);
    // Without a bill covey reads, there is nothing to warn about.
    const unmapped = { ...f, billed: { state: "unmapped" } } as unknown as CredentialFigures;
    const other = renderWithProviders(<CostBasisPanel f={unmapped} name="Key" editable={false} />);
    expect(other.container.querySelector("[data-beside-bill]")).toBeNull();
  });

  it("zeigt Lesenden ohne gesetzte Grundlage nichts Neues", async () => {
    const { container } = serve(cells());
    const b = await block(container);
    await screen.findAllByRole("group", { name: "Echte Kosten (Schätzung)" });
    expect(b.querySelector("[data-cost-basis]")).toBeNull();
  });
});
