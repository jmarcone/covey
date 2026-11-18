import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CostInsights } from "./CostInsights";
import type { CredentialFigures } from "./costFigures";
import { mockFetch, renderWithProviders, useGerman } from "../test/render";

// The per-model table offers the cost sources as columns that can be switched
// off, as the chart does. What is pinned: a column and its total go with the
// chip, the last chip stays, the choice outlives the page, and a source that
// is per credential stands in the totals row only.

const row = (model: string, engine: number, list: number | null) => ({
  agent_id: "a1", agent_slug: "coordinator", agent_name: "Coordinator", model, credential_kind: "subscription", runs: 1,
  input_tokens: 0, output_tokens: 0, thinking_tokens: 0, cache_read_tokens: 0, cache_write_5m_tokens: 0, cache_write_1h_tokens: 0,
  web_search_requests: 0, engine_usd: engine, list_usd: list,
});

const MODELS = { rows: [row("claude-opus-5-5", 10, 10.5), row("claude-mystery-1", 2, null)], runs_without_breakdown: 0, usd_without_breakdown: 0 };

const seat = {
  id: "seat#0", secretKey: "seat", slot: 0, kind: "subscription",
  real: { value: 7, low: 6, high: 8, points: 4, below: false, unpricedPoints: 0, weeks: 1, priced: true },
  billed: null, meter: null,
} as unknown as CredentialFigures;
const key = {
  id: "key#1", secretKey: "key", slot: 1, kind: "api_key", real: null, meter: null,
  billed: { state: "measured", usd: 80.5, status: { state: "complete", completeTo: null } },
} as unknown as CredentialFigures;

const KEY = "covey.cost.modelColumns";

function show(figures: CredentialFigures[] = []) {
  mockFetch({ "/api/v1/cost/models": MODELS, "/api/v1/cost/usage-readings": [] });
  const r = renderWithProviders(<CostInsights days={7} figures={figures} />);
  return r;
}
const totals = async () => {
  const tr = await waitFor(() => {
    const t = document.querySelector<HTMLElement>("tr[data-total]");
    if (!t) throw new Error("no table yet");
    return t;
  });
  return [...tr.querySelectorAll("td")].map((td) => td.textContent);
};
const chip = (name: RegExp) => screen.getByRole("button", { name });
const headers = () => [...document.querySelectorAll("th[data-col]")].map((th) => th.getAttribute("data-col"));

describe("Nutzung je Modell: Quellen", () => {
  beforeEach(() => { useGerman(); window.localStorage.removeItem(KEY); });
  afterEach(() => { vi.restoreAllMocks(); window.localStorage.removeItem(KEY); });

  it("zeigt anfangs die beiden Quellen je Modell, mit Summe je Spalte", async () => {
    show();
    expect(await totals()).toEqual(["Summe (Liste, Modelle ohne Listenpreis zu Claude Codes Preis)", "12,00 $", "12,50 $"]);
    expect(headers()).toEqual(["reported", "recomputed"]);
    // no meter, no bill: nothing else is offered
    expect(screen.getAllByRole("button", { pressed: true })).toHaveLength(2);
    expect(screen.queryByRole("button", { name: /Echte Kosten/ })).toBeNull();
  });

  it("blendet eine Spalte samt Summe aus und wieder ein", async () => {
    show();
    await totals();
    await userEvent.click(chip(/^Aus Tokens nachgerechnet/));
    expect(headers()).toEqual(["reported"]);
    expect(await totals()).toEqual(["Summe", "12,00 $"]);
    expect(chip(/^Aus Tokens nachgerechnet/)).toHaveAttribute("aria-pressed", "false");
    await userEvent.click(chip(/^Aus Tokens nachgerechnet/));
    expect(headers()).toEqual(["reported", "recomputed"]);
    expect((await totals())[2]).toBe("12,50 $");
  });

  it("lässt die letzte Quelle nicht abschalten", async () => {
    show();
    await totals();
    await userEvent.click(chip(/^Aus Tokens nachgerechnet/));
    await userEvent.click(chip(/^Von Claude Code gemeldet/));
    expect(headers()).toEqual(["reported"]);
    expect(chip(/^Von Claude Code gemeldet/)).toHaveAttribute("aria-pressed", "true");
    expect(JSON.parse(window.localStorage.getItem(KEY)!)).toEqual(["reported"]);
  });

  it("merkt sich die Auswahl im Browser und stellt sie wieder her", async () => {
    const first = show();
    await totals();
    await userEvent.click(chip(/^Von Claude Code gemeldet/));
    expect(JSON.parse(window.localStorage.getItem(KEY)!)).toEqual(["recomputed"]);
    first.unmount();
    show();
    await totals();
    expect(headers()).toEqual(["recomputed"]);
    expect(chip(/^Von Claude Code gemeldet/)).toHaveAttribute("aria-pressed", "false");
  });

  it("fällt auf den Standard zurück, wenn der Speicher Unbrauchbares enthält oder fehlt", async () => {
    window.localStorage.setItem(KEY, "{kaputt");
    const first = show();
    await totals();
    expect(headers()).toEqual(["reported", "recomputed"]);
    first.unmount();
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("denied"); });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("denied"); });
    show();
    await totals();
    await userEvent.click(chip(/^Aus Tokens nachgerechnet/));
    expect(headers()).toEqual(["reported"]); // still switches, only not remembered
  });

  it("zeigt Echte Kosten und Rechnung nur in der Summenzeile, mit Hinweis in der Kopfzeile", async () => {
    show([seat, key]);
    await totals();
    await userEvent.click(chip(/^Echte Kosten/));
    await userEvent.click(chip(/^Von Anthropic berechnet/));
    expect(headers()).toEqual(["reported", "recomputed", "real", "billed"]);
    expect(await totals()).toEqual(["Summe (Liste, Modelle ohne Listenpreis zu Claude Codes Preis)", "12,00 $", "12,50 $", "≈ 7,00 $", "80,50 $"]);
    // one row per model, "–" where the source is not per model
    const body = document.querySelectorAll("tbody tr:not([data-total]):not([data-unpriced])");
    expect(body).toHaveLength(2);
    for (const tr of body) expect([...tr.querySelectorAll("td")].slice(-2).map((td) => td.textContent)).not.toContain("80,50 $");
    expect(within(body[0] as HTMLElement).getAllByText("–")).toHaveLength(2);
    expect(document.querySelector("th[data-col=real]")!.getAttribute("title")).toMatch(/Pro Zugang, nicht pro Modell/);
    expect(document.querySelector("th[data-col=billed]")!.getAttribute("title")).toMatch(/Pro Zugang, nicht pro Modell/);
    expect(document.querySelector("th[data-col=reported]")!.getAttribute("title")).not.toMatch(/Pro Zugang/);
  });

  it("zeigt keine Summe, wo nicht alle Zugänge eine Zahl haben, und sagt warum", async () => {
    const unpriced = { ...seat, id: "seat#1", real: { ...seat.real!, priced: false, unpricedPoints: 4 } } as CredentialFigures;
    show([seat, unpriced]);
    await totals();
    await userEvent.click(chip(/^Echte Kosten/));
    const cell = document.querySelector<HTMLElement>("tr[data-total] td[data-col=real]")!;
    expect(cell.textContent).toBe("–");
    expect(cell.getAttribute("title")).toBe("1 von 2 Zugängen ohne Zahl");
  });

  it("ignoriert eine gemerkte Spalte, die dieses Fenster nicht anbietet", async () => {
    window.localStorage.setItem(KEY, JSON.stringify(["billed"]));
    show();
    await totals();
    expect(headers()).toEqual(["reported", "recomputed"]);
  });

  it("stellt eine gemerkte Zusatzspalte wieder her, sobald die Zahlen da sind", async () => {
    const first = show([seat, key]);
    await totals();
    await userEvent.click(chip(/^Echte Kosten/));
    expect(JSON.parse(window.localStorage.getItem(KEY)!)).toEqual(["reported", "recomputed", "real"]);
    first.unmount();
    show([seat, key]);
    await totals();
    expect(headers()).toEqual(["reported", "recomputed", "real"]);
    await userEvent.click(chip(/^Echte Kosten/));
    expect(headers()).toEqual(["reported", "recomputed"]);
    await userEvent.click(chip(/^Echte Kosten/));
    expect(headers()).toEqual(["reported", "recomputed", "real"]);
  });
});
