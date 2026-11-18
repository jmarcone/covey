import { describe, it, expect, beforeEach, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Costs from "./Costs";
import { canSetPlan, defaultValidFrom, planPlace, toLocalInput } from "./PlanPrice";
import type { CostCells, CredentialPlan, Principal, UsageReading } from "../api";
import type { CredentialFigures } from "./costFigures";
import { renderWithProviders, testPrincipal, useGerman } from "../test/render";

// The plan price is what turns the meter's points into dollars. The form that
// stores it is for the roles the endpoint lets through, posts exactly what
// the endpoint reads, presets the start of the current meter week from the
// credential's own readings, and says what the server said when it refuses.

const SEAT = "claude_code_oauth_token";
const RT = "0b0e0000-0000-4000-8000-000000000001";
const RESET = "2026-11-20T16:00:00Z"; // the weekly reset the readings name
const WEEK_START = "2026-11-13T16:00:00.000Z"; // RESET − 7 days

const reading = (over: Partial<UsageReading> = {}): UsageReading => ({
  runtime_id: RT, credential_ord: 1, secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd",
  window_kind: "seven_day", supported: true, window_percent: null, week_percent: 12, resets_at: RESET, status: "", reason: "",
  stale: false, fetched_at: "2026-11-18T10:00:00Z", received_at: "2026-11-18T10:00:00Z", cost_usd: null, ...over,
});

const cells = (plans: CredentialPlan[] = []): CostCells => ({
  periods: ["2026-11-18T10:00:00Z"],
  agents: [{ id: "a1", label: "Coordinator", reported_usd: 1, measured_usd: 1 }],
  types: ["output"],
  cells: [{
    period: "2026-11-18T10:00:00Z", agent_id: "a1", secret_key: SEAT, secret_slot: 0, credential_kind: "subscription",
    reported_usd: 1, measured: { output: 1 }, engine_usd: 1, tokens: { output: 1000 },
  }],
  subscription: [],
  meter: [],
  plans,
  bookings: [],
});

const NO_BILL = { bucket: "hour", points: [], sync: { configured: false, last_attempt_at: null, last_success_at: null,
  last_daily_at: null, data_refreshed_at: null, last_status: "", last_error: "" } };

type Put = { url: string; body: Record<string, unknown> };

/** The page's endpoints, with the plan endpoint answered by `plan` and its
 *  requests recorded. Cells are served from `state`, so a saved plan can show
 *  up in the refetch. */
function serve(opts: { readings?: UsageReading[]; plan?: (body: Record<string, unknown>) => Response; state?: { cells: CostCells } } = {}) {
  const puts: Put[] = [];
  const state = opts.state ?? { cells: cells() };
  let cellFetches = 0;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    const json = (b: unknown) => new Response(JSON.stringify(b), { status: 200, headers: { "Content-Type": "application/json" } });
    if (method === "PUT" && url.includes("/plan")) {
      const body = JSON.parse(String(init?.body));
      puts.push({ url, body });
      return opts.plan ? opts.plan(body) : json({});
    }
    const path = url.split("?")[0];
    if (path.endsWith("/cost/cells")) { cellFetches++; return json(state.cells); }
    if (path.endsWith("/cost/billed")) return json(NO_BILL);
    if (path.endsWith("/cost/runs")) return json([]);
    if (path.endsWith("/cost/models")) return json({ rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 });
    if (path.endsWith("/cost/usage-readings")) return json(opts.readings ?? []);
    return new Response(JSON.stringify({ error: "not mocked" }), { status: 404 });
  }));
  return { puts, state, cellFetches: () => cellFetches };
}

const page = (me: Principal) => renderWithProviders(<Costs me={me} />);
const block = async (c: HTMLElement) => waitFor(() => {
  const b = c.querySelector<HTMLElement>(`[data-credential="${SEAT}#0"]`);
  if (!b) throw new Error("no credential block yet");
  return b;
});

describe("Kosten — Tarifpreis setzen", () => {
  beforeEach(() => useGerman());

  it("schickt den Tarifpreis mit dem Wochenbeginn aus resets_at der Ablesung als valid_from", async () => {
    const s = serve({ readings: [reading()] });
    const { container } = page(testPrincipal("org_admin"));
    const b = await block(container);
    await userEvent.click(await within(b).findByRole("button", { name: "Tarifpreis setzen" }));

    const form = within(b).getByRole("form", { name: "Tarifpreis setzen" });
    // preset: the reset minus seven days, not "now"
    expect(within(form).getByLabelText("Gültig ab")).toHaveValue(toLocalInput(new Date(WEEK_START)));
    await userEvent.type(within(form).getByLabelText("Betrag (USD)"), "200");
    await userEvent.click(within(form).getByRole("button", { name: "Speichern" }));

    await waitFor(() => expect(s.puts).toHaveLength(1));
    expect(s.puts[0].url).toBe(`/api/v1/runtime-instances/${RT}/credentials/1/plan`);
    expect(s.puts[0].body).toEqual({ amount: 200, currency: "USD", period: "month", valid_from: WEEK_START });
  });

  it("schickt Jahr und einen geänderten Beginn", async () => {
    const s = serve({ readings: [reading()] });
    const { container } = page(testPrincipal("security"));
    const b = await block(container);
    await userEvent.click(await within(b).findByRole("button", { name: "Tarifpreis setzen" }));
    const form = within(b).getByRole("form", { name: "Tarifpreis setzen" });
    await userEvent.type(within(form).getByLabelText("Betrag (USD)"), "2000.5");
    await userEvent.selectOptions(within(form).getByLabelText("Zeitraum"), "year");
    fireEvent.change(within(form).getByLabelText("Gültig ab"), { target: { value: "2026-11-15T08:30" } });
    await userEvent.click(within(form).getByRole("button", { name: "Speichern" }));

    await waitFor(() => expect(s.puts).toHaveLength(1));
    expect(s.puts[0].body).toEqual({ amount: 2000.5, currency: "USD", period: "year", valid_from: new Date("2026-11-15T08:30").toISOString() });
  });

  it("holt nach dem Speichern die Kostendaten neu und schließt das Formular", async () => {
    const s = serve({ readings: [reading()] });
    const { container } = page(testPrincipal("org_admin"));
    const b = await block(container);
    expect(within(b).getByText("Kein Tarifpreis")).toBeInTheDocument();
    const before = s.cellFetches();
    await userEvent.click(await within(b).findByRole("button", { name: "Tarifpreis setzen" }));
    await userEvent.type(within(b).getByLabelText("Betrag (USD)"), "200");
    // what the server answers on the refetch: the plan is there now
    s.state.cells = cells([{
      runtime_id: RT, ord: 1, secret_key: SEAT, secret_slot: 0, plan_name: "", amount: 200, currency: "USD", period: "month",
      tax_basis: "net", fx_to_usd: 1, fx_date: null, valid_from: WEEK_START, valid_to: null, weekly_usd: 46.15, usd_per_point: 0.4615,
    }]);
    await userEvent.click(within(b).getByRole("button", { name: "Speichern" }));

    await waitFor(() => expect(s.cellFetches()).toBeGreaterThan(before));
    await waitFor(() => expect(within(b).queryByRole("form")).toBeNull());
    expect(within(b).getByText(/^Tarif: .*200.*\/Monat seit /)).toBeInTheDocument();
  });

  it("zeigt bei einem 400 den Text des Servers als Gefahrenzeile und behält das Formular", async () => {
    const s = serve({
      readings: [reading()],
      plan: () => new Response(JSON.stringify({ error: 'invalid plan: period must be "month" or "year"' }), {
        status: 400, headers: { "Content-Type": "application/json" },
      }),
    });
    const { container } = page(testPrincipal("org_admin"));
    const b = await block(container);
    await userEvent.click(await within(b).findByRole("button", { name: "Tarifpreis setzen" }));
    await userEvent.type(within(b).getByLabelText("Betrag (USD)"), "200");
    await userEvent.click(within(b).getByRole("button", { name: "Speichern" }));

    const alert = await within(b).findByRole("alert");
    expect(alert).toHaveTextContent('invalid plan: period must be "month" or "year"');
    expect(alert.getAttribute("style")).toContain("var(--text-danger)");
    // the form stays, with what was typed
    expect(within(b).getByLabelText("Betrag (USD)")).toHaveValue(200);
    expect(s.puts).toHaveLength(1);
  });

  it("zeigt einem Betrachter den Tarif, aber keine Schaltfläche", async () => {
    serve({ readings: [reading()] });
    const { container } = page(testPrincipal("viewer"));
    const b = await block(container);
    expect(within(b).getByText("Kein Tarifpreis")).toBeInTheDocument();
    expect(within(b).queryByRole("button", { name: "Tarifpreis setzen" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Tarifpreis setzen" })).toBeNull();
  });

  it("ohne Ablesung gibt es keine Stelle für den Preis und damit keine Schaltfläche", async () => {
    serve();
    const { container } = page(testPrincipal("org_admin"));
    const b = await block(container);
    expect(within(b).getByText("Kein Tarifpreis")).toBeInTheDocument();
    expect(within(b).queryByRole("button", { name: "Tarifpreis setzen" })).toBeNull();
  });
});

describe("Tarifpreis — Rollen, Ort und Vorbelegung", () => {
  const fig = (over: Partial<CredentialFigures> = {}) => ({ id: `${SEAT}#0`, secretKey: SEAT, slot: 0, kind: "subscription", plan: null, ...over }) as CredentialFigures;

  it("lässt nur die Rollen durch, die der Endpunkt durchlässt", () => {
    expect(["org_admin", "security"].every(canSetPlan)).toBe(true);
    for (const r of ["auditor", "viewer", "agent_owner", "controlling", ""]) expect(canSetPlan(r)).toBe(false);
  });

  it("nimmt den Ort aus der jüngsten Ablesung der Zugangsdaten, nicht einer fremden", () => {
    const rs = [
      reading({ runtime_id: "old", credential_ord: 0, fetched_at: "2026-11-17T10:00:00Z" }),
      reading({ runtime_id: "new", credential_ord: 2, fetched_at: "2026-11-18T10:00:00Z" }),
      reading({ runtime_id: "other", credential_ord: 5, secret_key: "other_key", fetched_at: "2026-11-19T10:00:00Z" }),
    ];
    expect(planPlace(fig(), rs)).toEqual({ runtimeId: "new", ord: 2 });
    expect(planPlace(fig(), [])).toBeNull();
    expect(planPlace(fig({ plan: { runtime_id: "p", ord: 3 } as CredentialPlan }), [])).toEqual({ runtimeId: "p", ord: 3 });
  });

  it("beginnt die aktuelle Zählerwoche sieben Tage vor dem jüngsten Wochen-Reset", () => {
    const now = new Date("2026-11-18T12:00:00Z");
    const rs = [
      reading({ resets_at: "2026-11-19T16:00:00Z", fetched_at: "2026-11-17T10:00:00Z" }),
      reading({ resets_at: RESET }),
      // the five-hour window and another credential do not say when the week began
      reading({ window_kind: "five_hour", resets_at: "2026-11-18T15:00:00Z" }),
      reading({ secret_key: "other_key", resets_at: "2026-11-30T00:00:00Z" }),
    ];
    expect(defaultValidFrom(fig(), rs, now).toISOString()).toBe(WEEK_START);
  });

  it("steigt bei einer alten Ablesung auf die Woche, die jetzt läuft", () => {
    // reset on 20 Nov, read long ago; it is 29 Nov now: the week began 27 Nov 16:00
    const now = new Date("2026-11-29T12:00:00Z");
    expect(defaultValidFrom(fig(), [reading()], now).toISOString()).toBe("2026-11-27T16:00:00.000Z");
  });

  it("nimmt ohne Ablesung jetzt", () => {
    const now = new Date("2026-11-18T12:00:00Z");
    expect(defaultValidFrom(fig(), [], now)).toEqual(now);
  });
});
