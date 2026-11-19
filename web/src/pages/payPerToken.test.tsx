import { describe, it, expect, beforeEach, vi } from "vitest";
import { waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { BilledReport, CostCells, CredentialBasis, ExternalUsage, MeterCoverage, WindowCharge } from "../api";
import i18n from "../i18n";
import { mockFetch, renderWithProviders } from "../test/render";
import {
  chargeDeletable, chargeKey, credentialFigures, monthlyMeters, payPerTokenHeadline, pickMeter, usageCurrencies, usageFigure,
  type CredentialFigures,
} from "./costFigures";
import { chargeLine, CostBasisPanel, monthlyLine, usageSub } from "./CostBasis";
import { PayPerTokenRow } from "./SubscriptionDent";

// Pay-per-token credentials (0152): usage exports beside the charges, the
// money a monthly usage limit makes of its meter, and the headline of an
// organisation paid per token (an API key's bill, or usage charges).

const SEAT = "claude_code_oauth_token";
const KEY = "anthropic_api_key";
const SINCE = "2026-11-17T00:00:00Z";
const END = new Date("2026-11-18T00:00:00Z");
const HOUR = 3600_000;
const ZERO = "00000000-0000-0000-0000-000000000000";

const usageBasis: CredentialBasis = { secret_key: SEAT, secret_slot: 0, basis: "usage", valid_from: "2026-11-01T00:00:00Z", valid_to: null };

function charge(over: Partial<WindowCharge> = {}): WindowCharge {
  return {
    id: "c1", secret_key: SEAT, secret_slot: 0, amount: 100, currency: "EUR",
    from: "2026-11-17T10:00:00Z", to: "2026-11-17T20:00:00Z", fx_to_usd: null, fx_date: null,
    window_amount: 100, counted_amount: 100, counted_usd: null, ...over,
  };
}

/** A meter charge as the server sends it: the zero id, its window, limit, points and range. */
function meterCharge(over: Partial<WindowCharge> = {}): WindowCharge {
  return charge({
    id: ZERO, kind: "meter", amount: 50, window_amount: 50, counted_amount: 50, window_kind: "overage", limit_amount: 1000,
    points: 5, counted_points: 5, counted_low: 40, counted_high: 60, ...over,
  });
}

const exportOf = (over: Partial<WindowCharge> = {}) =>
  charge({ id: "e1", kind: "export", amount: 120, window_amount: 120, counted_amount: 0, counted_usd: null, ...over });

function cell(period: string, key: string, kind: string, engine: number) {
  return { period, agent_id: "a1", secret_key: key, secret_slot: 0, credential_kind: kind, reported_usd: engine,
    measured: { output: engine }, engine_usd: engine, tokens: { output: 1000 } };
}

function cells(over: Partial<CostCells> = {}): CostCells {
  return {
    since: SINCE, periods: ["2026-11-17T18:00:00Z", "2026-11-17T19:00:00Z"],
    agents: [{ id: "a1", label: "Coordinator", reported_usd: 0 }],
    types: ["output"], cells: [], subscription: [], meter: [], plans: [], bookings: [], ...over,
  };
}

const overage: MeterCoverage = {
  secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "inband", window_kind: "overage",
  baseline_at: "2026-11-17T10:00:00Z", baseline_percent: 78, baseline_before_window: false,
  last_at: "2026-11-17T20:00:00Z", last_percent: 83, readings: 4, points: 5, points_low: 4, points_high: 6,
  longest_gap_secs: 3600, segments: null,
};

beforeEach(() => {
  void i18n.changeLanguage("en");
});

describe("usage exports (0152)", () => {
  it("are not in the uncounted warning nor the currencies, but checked against the counted figure", () => {
    const u = usageFigure({ cost_bases: [usageBasis], usage_charges: [charge(), exportOf()], since: SINCE }, SEAT, 0, END)!;
    expect(u.uncounted).toEqual([]);
    expect(u.currencies).toEqual([{ currency: "EUR", counted: 100, usd: null, charges: 1 }]);
    expect(u.exports).toHaveLength(1);
    expect(u.exports[0].counted).toBeCloseTo(100);
    expect(u.exports[0].diff).toBeCloseTo(-20);
  });

  it("compares only within its currency", () => {
    const u = usageFigure({ cost_bases: [usageBasis], usage_charges: [charge({ currency: "USD" }), exportOf()], since: SINCE }, SEAT, 0, END)!;
    expect(u.exports[0]).toMatchObject({ counted: null, diff: null, otherCurrency: true });
  });

  it("show as a check beside the counted figure, with the difference", () => {
    const { container } = renderWithProviders(<CostBasisPanel f={figure([charge(), exportOf()])} name="Seat" editable={false} />);
    expect(container.querySelector("[data-uncounted]")).toBeNull();
    const li = container.querySelector<HTMLElement>(`[data-charge="e1"]`)!;
    expect(li.textContent).toMatch(/^Usage export \(a check, not counted\) · 120\.00 EUR/);
    expect(li.textContent).toMatch(/counted over the same span: 100\.00 EUR \(−20\.00 EUR against the export\)/);
  });
});

/** A credential's figures with only what the panel reads. */
function figure(charges: WindowCharge[], basis = usageBasis): CredentialFigures {
  const usage = usageFigure({ cost_bases: [basis], usage_charges: charges, since: SINCE }, SEAT, 0, END);
  return { id: `${SEAT}#0`, secretKey: SEAT, slot: 0, kind: "subscription", basis: usage?.basis ?? null, usage, billed: null, meter: null,
    monthly: [] } as unknown as CredentialFigures;
}

describe("meter charges (0152)", () => {
  const a = meterCharge();
  const b = meterCharge({ from: "2026-11-17T20:00:00Z", to: "2026-11-17T22:00:00Z", limit_amount: 1200 });

  it("get keys that do not collide although they share the zero id, and cannot be deleted", () => {
    expect(chargeKey(a)).not.toBe(chargeKey(b));
    expect(chargeKey(charge())).toBe("c1");
    expect(chargeDeletable(a)).toBe(false);
    expect(chargeDeletable(charge())).toBe(true);
    expect(chargeDeletable(exportOf())).toBe(true);
  });

  it("are labelled as the monthly meter's, with their limit, points and range, and have no Delete button", async () => {
    mockFetch({
      "/runtime-instances": [{ id: "rt-1", engine: "claude", display_name: "Claude", model: "", bindings: [], can_carry_blocking: false,
        creds: [{ ord: 2, kind: "subscription", secret_key: SEAT, secret_slot: 0, label: "" }] }],
    });
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    const { container } = renderWithProviders(<CostBasisPanel f={figure([a, b, charge({ id: "h1", from: "2026-11-16T00:00:00Z", to: "2026-11-16T10:00:00Z" })])}
      name="Seat" editable />);
    // The Delete buttons appear once the credential's place is known.
    await waitFor(() => expect(within(container.querySelector<HTMLElement>(`[data-charge="h1"]`)!).getByRole("button", { name: "Delete" })).toBeInTheDocument());
    const meters = container.querySelectorAll<HTMLElement>(`[data-charge-kind="meter"]`);
    expect(meters).toHaveLength(2);
    for (const m of meters) expect(within(m).queryByRole("button", { name: "Delete" })).toBeNull();
    expect(meters[0].textContent).toMatch(/^From the monthly meter \(overage\), limit 1,000\.00 EUR per month · .* · 5\.0 points moved, 5\.0 counted · counted: 50\.00 EUR \(between 40\.00 EUR and 60\.00 EUR/);
    expect(errors.mock.calls.some((c) => String(c[0]).includes("same key"))).toBe(false);
    errors.mockRestore();
  });

  it("count apart from the charges entered by hand: \"Charges: n\" counts only those", () => {
    const [eur] = usageCurrencies([meterCharge({ currency: "USD", counted_usd: 50 }), charge({ currency: "USD", counted_usd: 100 })]);
    expect(eur).toEqual({ currency: "USD", counted: 150, usd: 150, charges: 1, meter: 50 });
    expect(usageSub(i18n.t, eur)).toBe("Charges: 1, in USD · from the monthly meter: $50.00");
  });
});

describe("superseded by the monthly meter (0152)", () => {
  it("is shown on the charge, and such a charge is not in the uncounted warning", () => {
    const c = charge({ counted_amount: 0, superseded_amount: 100 });
    expect(chargeLine(i18n.t, c)).toMatch(/^100\.00 EUR · .* · 100\.00 EUR superseded by the monthly meter \(not counted twice\)/);
    expect(chargeLine(i18n.t, c)).not.toMatch(/no usage basis/);
    const u = usageFigure({ cost_bases: [usageBasis], usage_charges: [c], since: SINCE }, SEAT, 0, END)!;
    expect(u.uncounted).toEqual([]);
  });
});

describe("the monthly meter (0152)", () => {
  const limit = { secret_key: SEAT, secret_slot: 0, window_kind: "overage", amount: 1000, currency: "EUR", fx_to_usd: null, fx_date: null,
    valid_from: "2026-11-01T00:00:00Z", valid_to: null };

  it("is a line of its own, never the weekly seat meter", () => {
    expect(pickMeter([overage], SEAT, 0)).toBeNull();
    const [m] = monthlyMeters([overage], [limit], [meterCharge()], SEAT, 0);
    expect(m.coverage.window_kind).toBe("overage");
    expect(m.limit?.amount).toBe(1000);
    expect(monthlyLine(i18n.t, m, "2026-12-01T00:00:00Z"))
      .toMatch(/^Monthly usage limit \(overage\): 78 % → 83 % \(5 points from 4 readings\) · resets .*12.* · limit 1,000\.00 EUR per month \(overage\) · from the meter: 50\.00 EUR$/);
    const f = credentialFigures({ cells: cells({ meter: [overage], usage_limits: [limit], usage_charges: [meterCharge()], cost_bases: [usageBasis] }),
      bucketMs: HOUR, windowEnd: END });
    expect(f[0].meter).toBeNull();
    expect(f[0].monthly).toHaveLength(1);
  });
});

describe("the pay-per-token headline (0152)", () => {
  it("is there for an organisation with only an API key: the lead figure against the bill, ≈ 1× at list price", () => {
    const billed: BilledReport = {
      bucket: "hour", points: [],
      window: { start: "2026-11-17T18:00:00Z", end: "2026-11-18T00:00:00Z", compared_until: "2026-11-17T20:00:00Z", width: "1h",
        totals: [{ secret_key: KEY, secret_slot: 0, has_billed: true, billed_usd: "4.00", billed_list_usd: "4.00", reported_usd: 4, list_usd: 4 }] },
      sync: { configured: true, last_attempt_at: null, last_success_at: null, last_daily_at: null,
        data_refreshed_at: "2026-11-18T00:00:00Z", last_status: "ok", last_error: "" },
    } as unknown as BilledReport;
    const data = cells({ cells: [cell("2026-11-17T18:00:00Z", KEY, "api_key", 3), cell("2026-11-17T19:00:00Z", KEY, "api_key", 1)] });
    const figures = credentialFigures({ cells: data, billed, bucketMs: HOUR, windowEnd: END });
    const h = payPerTokenHeadline(data, figures, HOUR, END)!;
    expect(h).not.toBeNull();
    expect(h.billed).toBe(4);
    expect(h.list).toBe(4);
    expect(h.realUsd).toBe(4);
    expect(h.multiple).toBeCloseTo(1, 6);
    expect(h.why).toBeNull();
    expect(h.credentials.map((c) => c.money)).toEqual(["bill"]);
    const { container } = renderWithProviders(<PayPerTokenRow h={h} filtered={false} />);
    expect(container.querySelector("[data-ppt-sentence]")!.textContent)
      .toBe("At token prices (as reported by Claude Code): $4.00. Real money: $4.00. That is ≈ 1.00× list price per real dollar.");
  });

  const seat = (charges: WindowCharge[]) => cells({
    cells: [cell("2026-11-17T18:00:00Z", SEAT, "subscription", 30), cell("2026-11-17T19:00:00Z", SEAT, "subscription", 25)],
    cost_bases: [usageBasis], usage_charges: charges, meter: [overage],
  });

  it("gives a usage seat whose EUR meter charge has a rate one multiple, in USD", () => {
    const data = seat([meterCharge({ fx_to_usd: 1.1, fx_date: "2026-11-17", counted_usd: 55 })]);
    const h = payPerTokenHeadline(data, credentialFigures({ cells: data, bucketMs: HOUR, windowEnd: END }), HOUR, END)!;
    expect(h.charges).toEqual([{ currency: "EUR", counted: 50, usd: 55, charges: 0, meter: 50 }]);
    expect(h.realUsd).toBeCloseTo(55);
    expect(h.list).toBeCloseTo(55);
    expect(h.multiple).toBeCloseTo(1, 6);
    expect(h.why).toBeNull();
    // Converted only at the rate a person entered, and named beside the multiple.
    expect(h.rates).toEqual([{ currency: "EUR", rate: 1.1, date: "2026-11-17" }]);
    const { container } = renderWithProviders(<PayPerTokenRow h={h} filtered={false} />);
    expect(container.querySelector("[data-ppt-rates]")!.textContent)
      .toBe("In USD only at the rates a person entered: at 1.1000 USD per EUR (2026-11-17).");
  });

  it("leaves out a usage seat the subscription headline already shows (external.meter)", () => {
    const data = seat([meterCharge({ fx_to_usd: 1.1, fx_date: "2026-11-17", counted_usd: 55 })]);
    data.external = {
      sources: [], cells: [],
      meter: { credentials: [{ secret_key: SEAT, secret_slot: 0, sources: [], step_count: 2, usage_steps: 2, split_steps: 0 }], steps: [] },
    } as unknown as ExternalUsage;
    const figures = credentialFigures({ cells: data, bucketMs: HOUR, windowEnd: END });
    expect(payPerTokenHeadline(data, figures, HOUR, END)).toBeNull();
  });

  it("gives a usage seat whose EUR meter charge has no rate no multiple, and says why", () => {
    const data = seat([meterCharge()]);
    const h = payPerTokenHeadline(data, credentialFigures({ cells: data, bucketMs: HOUR, windowEnd: END }), HOUR, END)!;
    expect(h.realUsd).toBeNull();
    expect(h.multiple).toBeNull();
    expect(h.why).toBe("rate");
    expect(h.noRate).toEqual(["EUR"]);
    expect(h.charges[0].counted).toBe(50);
    const { container } = renderWithProviders(<PayPerTokenRow h={h} filtered={false} />);
    expect(container.querySelector("[data-ppt-sentence]")!.textContent)
      .toBe("At token prices (as reported by Claude Code): $55.00. Real money: usage charges 50.00 EUR (from the monthly meter: 50.00 EUR; no USD figure: no rate given).");
    expect(container.querySelector("[data-ppt-no-multiple]")!.textContent).toMatch(/^No multiple: EUR has no USD figure/);
  });

  it("is none for a seat priced by its plan only", () => {
    const data = cells({ cells: [cell("2026-11-17T18:00:00Z", SEAT, "subscription", 30)] });
    expect(payPerTokenHeadline(data, credentialFigures({ cells: data, bucketMs: HOUR, windowEnd: END }), HOUR, END)).toBeNull();
  });
});

describe("the forms (0152)", () => {
  it("sets a monthly usage limit and sends the kind of a usage export", async () => {
    const calls = mockFetch({
      "/runtime-instances": [{ id: "rt-1", engine: "claude", display_name: "Claude", model: "", bindings: [], can_carry_blocking: false,
        creds: [{ ord: 2, kind: "subscription", secret_key: SEAT, secret_slot: 0, label: "" }] }],
      "/credentials/2/usage-limit": [],
      "PUT /credentials/2/usage-limit": {},
      "POST /credentials/2/usage-charges": { id: "new" },
    });
    const fetchSpy = vi.mocked(globalThis.fetch);
    const { container } = renderWithProviders(<CostBasisPanel f={figure([])} name="Seat" editable />);
    const root = within(container);
    await userEvent.click(await root.findByRole("button", { name: "Set monthly usage limit for Seat" }));
    const form = root.getByRole("form", { name: "Set monthly usage limit for Seat" });
    // The history stands before Save; the hint describes the amount.
    const history = await within(form).findByText("No usage limit entered.");
    const save = within(form).getByRole("button", { name: "Save" });
    expect(history.compareDocumentPosition(save) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(within(form).getByLabelText("Limit per month")).toHaveAccessibleDescription(/^The money limit the monthly meter/);
    await userEvent.type(within(form).getByLabelText("Limit per month"), "1000");
    await userEvent.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetchSpy.mock.calls.some((c) => (c[1]?.method ?? "").toUpperCase() === "PUT")).toBe(true));
    const put = fetchSpy.mock.calls.find((c) => (c[1]?.method ?? "").toUpperCase() === "PUT")!;
    expect(String(put[0])).toBe("/api/v1/runtime-instances/rt-1/credentials/2/usage-limit");
    expect(JSON.parse(String(put[1]!.body))).toMatchObject({ amount: 1000, currency: "EUR" });

    await userEvent.click(await root.findByRole("button", { name: "Add usage charge" }));
    const cf = root.getByRole("form", { name: "Add usage charge for Seat" });
    await userEvent.selectOptions(within(cf).getByLabelText("Kind"), "export");
    await userEvent.type(within(cf).getByLabelText("Amount"), "120");
    await userEvent.click(within(cf).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetchSpy.mock.calls.some((c) => (c[1]?.method ?? "").toUpperCase() === "POST")).toBe(true));
    const post = fetchSpy.mock.calls.find((c) => (c[1]?.method ?? "").toUpperCase() === "POST")!;
    expect(JSON.parse(String(post[1]!.body))).toMatchObject({ amount: 120, kind: "export" });
    void calls;
  });

  it("takes a reading with a fractional percent", async () => {
    mockFetch({
      "/runtime-instances": [{ id: "rt-1", engine: "claude", display_name: "Claude", model: "", bindings: [], can_carry_blocking: false,
        creds: [{ ord: 2, kind: "subscription", secret_key: SEAT, secret_slot: 0, label: "" }] }],
      "POST /cost/usage-readings": {},
    });
    const fetchSpy = vi.mocked(globalThis.fetch);
    const { container } = renderWithProviders(<CostBasisPanel f={figure([])} name="Seat" editable />);
    const root = within(container);
    await userEvent.click(await root.findByRole("button", { name: "Enter a meter reading for Seat" }));
    const form = root.getByRole("form", { name: "Enter a meter reading for Seat" });
    const pct = within(form).getByLabelText("Used (%)");
    expect(pct).toHaveAttribute("step", "any");
    expect(pct).toHaveAccessibleDescription(/^A reading you read off the provider/);
    await userEvent.type(pct, "83.4");
    await userEvent.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetchSpy.mock.calls.some((c) => (c[1]?.method ?? "").toUpperCase() === "POST")).toBe(true));
    const post = fetchSpy.mock.calls.find((c) => (c[1]?.method ?? "").toUpperCase() === "POST")!;
    expect(String(post[0])).toBe("/api/v1/cost/usage-readings");
    expect(JSON.parse(String(post[1]!.body))).toMatchObject({ runtime_id: "rt-1", ord: 2, window_kind: "overage", percent: 83.4 });
  });
});
