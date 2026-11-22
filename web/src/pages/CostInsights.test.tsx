import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
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

const span = { baseline_at: "2026-11-17T18:00:00Z", last_at: "2026-11-17T21:00:00Z" };
const real = { value: 7, low: 6, high: 8, points: 4, below: false, unpricedPoints: 0, weeks: 1, priced: true };
const seatOf = (id: string, over: Record<string, unknown> = {}) => ({
  id, secretKey: id.split("#")[0], slot: 0, kind: "subscription", real, aligned: 6.5, billed: null, meter: span, ...over,
}) as unknown as CredentialFigures;
const keyOf = (id: string, billed: Record<string, unknown> = {}) => ({
  id, secretKey: id.split("#")[0], slot: 1, kind: "api_key", real: null, aligned: null, meter: null,
  billed: {
    state: "measured", usd: 80.5, reported: 95, recomputed: 99, status: { state: "complete", completeTo: null },
    window: {
      start: new Date("2026-11-17T18:00:00Z"), end: new Date("2026-11-17T23:00:00Z"), comparedUntil: new Date("2026-11-17T20:00:00Z"), width: "1h",
    }, ...billed,
  },
}) as unknown as CredentialFigures;
const seat = seatOf("seat#0");
const key = keyOf("key#1");

const KEY = "covey.cost.modelColumns";
const TOTAL = "Summe (nachgerechnet, Modelle ohne Listenpreis zum gemeldeten Preis)";

function show(figures: CredentialFigures[] = [], agents?: ReadonlySet<string>, models: unknown = MODELS) {
  mockFetch({ "/api/v1/cost/models": models, "/api/v1/cost/usage-readings": [] });
  const r = renderWithProviders(<CostInsights days={7} figures={figures} agents={agents} />);
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
const rowCells = async (selector: string) => {
  const tr = await waitFor(() => {
    const t = document.querySelector<HTMLElement>(selector);
    if (!t) throw new Error("no row yet");
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
    expect(await totals()).toEqual([TOTAL, "12,00 $", "12,50 $"]);
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

  it("setzt Echte Kosten und Rechnung in eigene Zeilen unter die Summe, je mit den Listenzahlen derselben Zugänge", async () => {
    show([seat, key]);
    await totals();
    await userEvent.click(chip(/^Echte Kosten/));
    await userEvent.click(chip(/^Von Anthropic berechnet/));
    expect(headers()).toEqual(["reported", "recomputed", "real", "billed"]);
    // the total holds the two per-model sources only
    expect(await totals()).toEqual([TOTAL, "12,00 $", "12,50 $", "", ""]);
    // the seats: the meter's span, the real cost beside the list price of the same span
    expect(await rowCells("tr[data-credential-row=real]")).toEqual([expect.stringMatching(/^Abo-Zugänge, Zählerzeitraum .*18:00 UTC – .*21:00 UTC$/), "–", "6,50 $", "≈ 7,00 $", ""]);
    // the keys: the bill's window, the bill beside Claude Code's and the list figure of the same window
    expect(await rowCells("tr[data-credential-row=billed]")).toEqual([expect.stringMatching(/^API-Schlüssel, Rechnung .*18:00 UTC – .*23:00 UTC$/), "95,00 $", "99,00 $", "", "80,50 $"]);
    // one row per model, "–" where the source is not per model, and the reason on the cell
    const body = document.querySelectorAll("tbody tr:not([data-total]):not([data-unpriced]):not([data-credential-row])");
    expect(body).toHaveLength(2);
    for (const tr of body) {
      const last = [...tr.querySelectorAll("td")].slice(-2);
      expect(last.map((td) => td.textContent)).toEqual(["–", "–"]);
      for (const td of last) expect(td.getAttribute("title")).toMatch(/Pro Zugang, nicht pro Modell/);
    }
    // the models without a list price carry no real cost or bill either
    expect(await rowCells("tr[data-unpriced]")).toEqual([expect.any(String), "2,00 $", "2,00 $", "", ""]);
    expect(document.querySelector("th[data-col=real]")!.getAttribute("title")).toMatch(/Pro Zugang, nicht pro Modell/);
    expect(document.querySelector("th[data-col=billed]")!.getAttribute("title")).toMatch(/Pro Zugang, nicht pro Modell/);
    expect(document.querySelector("th[data-col=reported]")!.getAttribute("title")).not.toMatch(/Pro Zugang/);
  });

  it("zeigt die Zeile eines Zugangstyps nur, solange seine Spalte an ist", async () => {
    show([seat, key]);
    await totals();
    expect(document.querySelector("tr[data-credential-row=real]")).toBeNull();
    expect(document.querySelector("tr[data-credential-row=billed]")).toBeNull();
    await userEvent.click(chip(/^Echte Kosten/));
    expect(document.querySelector("tr[data-credential-row=real]")).not.toBeNull();
    expect(document.querySelector("tr[data-credential-row=billed]")).toBeNull();
  });

  it("zeigt die Zählerzeiträume je Zugang im Titel, wo die Zeile mehrere zusammenfasst", async () => {
    show([seat, seatOf("seat2#0", { meter: { baseline_at: "2026-11-16T18:00:00Z", last_at: "2026-11-17T22:00:00Z" } })]);
    await totals();
    await userEvent.click(chip(/^Echte Kosten/));
    const label = document.querySelector<HTMLElement>("tr[data-credential-row=real] td")!;
    expect(label.textContent).toMatch(/Zählerzeitraum .*16\.11\..*22:00 UTC$/);
    expect(label.getAttribute("title")!.split("\n")).toHaveLength(2);
  });

  it("zeigt keine Summe, wo nicht alle Zugänge eine Zahl haben, und sagt warum", async () => {
    const unpriced = seatOf("seat#1", { real: { ...real, priced: false, unpricedPoints: 4 } });
    show([seat, unpriced]);
    await totals();
    await userEvent.click(chip(/^Echte Kosten/));
    const cell = document.querySelector<HTMLElement>("tr[data-credential-row=real] td[data-col=real]")!;
    expect(cell.textContent).toBe("–");
    expect(cell.getAttribute("title")).toBe("1 von 2 Zugängen ohne Zahl");
    // the list price beside it is of the same seats, so no number either
    expect(document.querySelector("tr[data-credential-row=real] td[data-col=recomputed]")!.textContent).toBe("–");
  });

  // A seat without a meter is one of the seats the figure is about: leaving it
  // out would make the metered seats' sum pass for the total.
  it("zählt einen Zugang ohne Zähler mit: keine Echte-Kosten-Summe, wenn er fehlt", async () => {
    const noMeter = seatOf("seat#1", { real: null, meter: null, aligned: null });
    show([seat, noMeter]);
    await totals();
    await userEvent.click(chip(/^Echte Kosten/));
    const cell = document.querySelector<HTMLElement>("tr[data-credential-row=real] td[data-col=real]")!;
    expect(cell.textContent).toBe("–");
    expect(cell.getAttribute("title")).toBe("1 von 2 Zugängen ohne Zahl");
  });

  it("zählt einen API-Schlüssel ohne Rechnung mit: keine Rechnungssumme, wenn er fehlt", async () => {
    for (const state of ["unmapped", "not_landed"]) {
      const first = show([key, keyOf("key#2", { state, usd: 0, reported: 0, recomputed: 0 })]);
      await totals();
      await userEvent.click(chip(/^Von Anthropic berechnet/));
      const cell = document.querySelector<HTMLElement>("tr[data-credential-row=billed] td[data-col=billed]")!;
      expect(cell.textContent).toBe("–");
      expect(cell.getAttribute("title")).toBe("1 von 2 Zugängen ohne Zahl");
      expect(document.querySelector("tr[data-credential-row=billed] td[data-col=reported]")!.textContent).toBe("–");
      expect(document.querySelector("tr[data-credential-row=billed] td[data-col=recomputed]")!.textContent).toBe("–");
      first.unmount();
      window.localStorage.removeItem(KEY);
    }
  });

  it("zeigt bei Zugängen ganz ohne Bewegung die Obergrenze", async () => {
    const still = (id: string) => seatOf(id, { real: { ...real, value: 0, low: 0, high: 0.46, points: 0, below: true } });
    show([still("seat#0"), still("seat#1")]);
    await totals();
    await userEvent.click(chip(/^Echte Kosten/));
    expect(document.querySelector("tr[data-credential-row=real] td[data-col=real]")!.textContent).toBe("< 0,92 $");
  });

  it("nennt bei gemischten Zugängen die Spanne, in der die Summe liegt", async () => {
    const still = seatOf("seat#1", { real: { ...real, value: 0, low: 0, high: 0.46, points: 0, below: true } });
    show([seat, still]);
    await totals();
    await userEvent.click(chip(/^Echte Kosten/));
    const cell = document.querySelector<HTMLElement>("tr[data-credential-row=real] td[data-col=real]")!;
    expect(cell.textContent).toBe("≈ 7,00 $");
    expect(cell.getAttribute("title")).toBe("Zwischen 6,00 $ und 8,46 $, wie es die ganzen Prozente zulassen.");
  });

  it("sagt am Rechnungsbetrag, dass die Rechnung nur bis zum letzten Abgleich vollständig ist", async () => {
    show([keyOf("key#1", { status: { state: "pending", completeTo: null } })]);
    await totals();
    await userEvent.click(chip(/^Von Anthropic berechnet/));
    expect(document.querySelector("tr[data-credential-row=billed] td[data-col=billed]")!.getAttribute("title")).toMatch(/nur bis zum letzten Abgleich vollständig/);
    // complete: nothing to say
    expect(document.querySelector("tr[data-total] td[data-col=billed]")!.getAttribute("title")).toBeNull();
  });

  it("vergleicht Gemeldet und Nachgerechnet im Fuß nur, solange beide an sind", async () => {
    show();
    await totals();
    const foot = () => document.querySelector("table")!.parentElement!.nextElementSibling!.textContent!;
    expect(foot()).toMatch(/^Preistabellen-Prüfung \(nachgerechnet ggü\. gemeldet, bepreiste Modelle\): \+5,00 %/);
    expect(foot()).not.toMatch(/Claude Code|\bListe\b/);
    await userEvent.click(chip(/^Aus Tokens nachgerechnet/));
    expect(foot()).not.toMatch(/Preistabellen/);
    expect(foot()).toMatch(/ohne Listenpreis/); // the other notes stay
    await userEvent.click(chip(/^Aus Tokens nachgerechnet/));
    await userEvent.click(chip(/^Von Claude Code gemeldet/));
    expect(foot()).not.toMatch(/Preistabellen/);
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

  // The headline follows the agent filter; the table's per-model columns do too,
  // or its totals disagree with it. What is per credential stays org-wide.
  it("folgt dem Agentenfilter: Zeilen und Summen nur des gewählten Agenten, Zugangszeilen als organisationsweit gekennzeichnet", async () => {
    const other = { ...row("claude-haiku-5", 100, 110), agent_id: "a2", agent_slug: "reviewer", agent_name: "Reviewer" };
    const otherUnpriced = { ...row("claude-mystery-2", 30, null), agent_id: "a2", agent_slug: "reviewer", agent_name: "Reviewer" };
    const models = { rows: [...MODELS.rows, other, otherUnpriced], runs_without_breakdown: 3, usd_without_breakdown: 4 };
    show([seat], new Set(["a1"]), models);
    expect(await totals()).toEqual([TOTAL, "12,00 $", "12,50 $"]);
    // the models without a list price are the chosen agent's too: one, at 2 $, not the other's 30 $
    expect(await rowCells("tr[data-unpriced]")).toEqual([expect.stringMatching(/\(1\)/), "2,00 $", "2,00 $"]);
    expect(screen.queryByText("Reviewer")).toBeNull();
    expect(screen.getAllByText("Coordinator", { selector: "a" })).toHaveLength(2);
    // the card's figures are the filter's now: no badge on its title ...
    expect(screen.getByText("Verbrauch je Modell").querySelector("[data-org-wide]")).toBeNull();
    // ... but on what cannot follow it: the runs without a model record and the per-credential rows
    await userEvent.click(chip(/^Echte Kosten/));
    expect(document.querySelector("tr[data-credential-row=real] [data-org-wide]")).not.toBeNull();
    expect(document.querySelector("p.muted [data-org-wide]")).not.toBeNull();
  });

  it("zeigt ohne Filter alle Agenten", async () => {
    const other = { ...row("claude-haiku-5", 100, 110), agent_id: "a2", agent_slug: "reviewer", agent_name: "Reviewer" };
    show([seat], new Set(), { rows: [...MODELS.rows, other], runs_without_breakdown: 0, usd_without_breakdown: 0 });
    expect(await totals()).toEqual([TOTAL, "112,00 $", "122,50 $"]);
  });

  // Real is remembered although a 24 h window has no meter to offer it; a
  // toggle there must not drop it from the choice.
  it("behält eine gemerkte, gerade nicht angebotene Spalte, wenn eine andere umgeschaltet wird", async () => {
    window.localStorage.setItem(KEY, JSON.stringify(["reported", "recomputed", "real"]));
    show(); // no meter: Real is not offered
    await totals();
    expect(headers()).toEqual(["reported", "recomputed"]);
    await userEvent.click(chip(/^Aus Tokens nachgerechnet/));
    expect(JSON.parse(window.localStorage.getItem(KEY)!)).toEqual(["reported", "real"]);
    await userEvent.click(chip(/^Aus Tokens nachgerechnet/));
    expect(JSON.parse(window.localStorage.getItem(KEY)!)).toEqual(["reported", "recomputed", "real"]);
  });

  it("lässt die letzte sichtbare Spalte auch dann nicht abschalten, wenn eine nicht angebotene gemerkt ist", async () => {
    window.localStorage.setItem(KEY, JSON.stringify(["reported", "real"]));
    show();
    await totals();
    expect(headers()).toEqual(["reported"]);
    await userEvent.click(chip(/^Von Claude Code gemeldet/));
    expect(headers()).toEqual(["reported"]);
    expect(JSON.parse(window.localStorage.getItem(KEY)!)).toEqual(["reported", "real"]);
  });

  it("sagt an der letzten eingeschalteten Quelle, dass sie bleibt", async () => {
    show();
    await totals();
    expect(chip(/^Von Claude Code gemeldet/)).not.toHaveAttribute("aria-disabled");
    await userEvent.click(chip(/^Aus Tokens nachgerechnet/));
    const last = chip(/^Von Claude Code gemeldet/);
    expect(last).toHaveAttribute("aria-disabled", "true");
    expect(last).toHaveAttribute("title", "Mindestens eine Quelle bleibt an.");
    expect(chip(/^Aus Tokens nachgerechnet/)).not.toHaveAttribute("aria-disabled");
  });

  it("zeigt Echte Kosten und Rechnung auch dann, wenn das Fenster keine Modellzeilen hat", async () => {
    const first = show([seat, key], undefined, { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 });
    await waitFor(() => expect(document.querySelector("tr[data-empty]")).not.toBeNull());
    await userEvent.click(chip(/^Echte Kosten/));
    expect(await rowCells("tr[data-credential-row=real]")).toContain("≈ 7,00 $");
    first.unmount();
    // without any meter or bill there is nothing but the empty note
    show([], undefined, { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 });
    expect(await screen.findByText("Keine Kostendaten im gewählten Zeitraum.")).toBeInTheDocument();
    expect(document.querySelector("table")).toBeNull();
    expect(screen.queryByRole("group", { name: "Quelle" })).toBeNull();
  });

  it("sagt an der Rechnung, dass der letzte Abgleich fehlschlug", async () => {
    show([keyOf("key#1", { error: "HTTP 401" })]);
    await totals();
    await userEvent.click(chip(/^Von Anthropic berechnet/));
    expect(document.querySelector("tr[data-credential-row=billed] td[data-col=billed]")!.getAttribute("title")).toMatch(/HTTP 401/);
  });

  // The case the hint explains best: a key's bill is missing because the sync failed.
  it("sagt den fehlgeschlagenen Abgleich auch, wo er einem Schlüssel die Rechnung nahm", async () => {
    const failed = keyOf("key#2", { state: "failed", usd: 0, reported: 0, recomputed: 0, error: "HTTP 401" });
    show([keyOf("key#1", { error: "HTTP 401" }), failed]);
    await totals();
    await userEvent.click(chip(/^Von Anthropic berechnet/));
    const cell = document.querySelector<HTMLElement>("tr[data-credential-row=billed] td[data-col=billed]")!;
    expect(cell.textContent).toBe("–");
    expect(cell.getAttribute("title")).toBe("1 von 2 Zugängen ohne Zahl · letzter Abgleich fehlgeschlagen: HTTP 401");
    const foot = document.querySelector("table")!.parentElement!.nextElementSibling!.textContent!;
    expect(foot).toMatch(/letzter Abgleich fehlgeschlagen: HTTP 401/);
  });

  // Titles are not there on a phone or for a keyboard.
  it("setzt Gründe und Spannen auch als Text unter die Tabelle", async () => {
    const still = seatOf("seat#1", { real: { ...real, value: 0, low: 0, high: 0.46, points: 0, below: true } });
    show([seat, still, keyOf("key#1", { status: { state: "pending", completeTo: null } })]);
    await totals();
    await userEvent.click(chip(/^Echte Kosten/));
    await userEvent.click(chip(/^Von Anthropic berechnet/));
    const foot = document.querySelector("table")!.parentElement!.nextElementSibling!.textContent!;
    expect(foot).toMatch(/Zwischen 6,00 \$ und 8,46 \$/);
    expect(foot).toMatch(/nur bis zum letzten Abgleich vollständig/);
    expect(foot).toMatch(/Pro Zugang, nicht pro Modell/);
  });

  it("setzt die Zählerzeiträume je Zugang unter die Tabelle, wo die Zeile mehrere zusammenfasst", async () => {
    show([seat, seatOf("seat2#0", { meter: { baseline_at: "2026-11-16T18:00:00Z", last_at: "2026-11-17T22:00:00Z" } })]);
    await totals();
    await userEvent.click(chip(/^Echte Kosten/));
    const foot = document.querySelector("table")!.parentElement!.nextElementSibling!.textContent!;
    expect(foot).toMatch(/seat#0: .*17\.11\..*18:00 UTC – .*21:00 UTC; seat2#0: .*16\.11\..*18:00 UTC – .*22:00 UTC/);
  });

  // A credential of a kind covey does not know yet has no real cost in the
  // seats row, so the card shows no meter for it either.
  it("zeigt im Zählerkärtchen nur die Zugänge, die die Abo-Zeile zählt", async () => {
    show([seat, seatOf("odd#2", { kind: "mystery" })]);
    await totals();
    await waitFor(() => expect(document.querySelector("[data-meter='seat#0']")).not.toBeNull());
    expect(document.querySelector("[data-meter='odd#2']")).toBeNull();
  });
});

// #83: an upper bound under a cent read "< <0,01 $" (the sum of the seats'
// real cost), and the point price of the meter figures had two decimals next to
// a figure it does not multiply to.
describe("Nutzung je Modell: Beträge unter einem Cent und Punktpreis", () => {
  beforeEach(() => { useGerman(); window.localStorage.removeItem(KEY); });
  afterEach(() => { vi.restoreAllMocks(); window.localStorage.removeItem(KEY); });

  const meter = {
    secret_key: "claude_code_oauth_token", secret_slot: 0, source: "usage_cmd", window_kind: "seven_day",
    baseline_at: "2026-11-17T18:00:00Z", baseline_percent: 9, last_at: "2026-11-17T21:00:00Z", last_percent: 13,
    readings: 4, points: 4,
  };

  it("schreibt die Obergrenze unter einem Cent als „< 0,01 $“, nie als „< <0,01 $“", async () => {
    const below = { ...real, value: 0, low: 0, high: 0.004, points: 0, below: true };
    show([seatOf("seat#0", { real: below, meter })]);
    await totals();
    await userEvent.click(chip(/^Echte Kosten/));
    const cells = await rowCells("tr[data-credential-row=real]");
    expect(cells[3]).toBe("< 0,01 $");
    expect(cells.join("")).not.toMatch(/<\s*</);
    // the meter figures of the card below say the same
    expect(document.querySelector("[data-meter]")!.textContent).toMatch(/< 0,01 \$ · der Zähler/);
  });

  it("zeigt in den Zählerzahlen den Punktpreis mit vier Stellen", async () => {
    const r = { ...real, value: 1.846, low: 1.38, high: 2.31, points: 4 };
    show([seatOf("seat#0", { real: r, meter })]);
    await totals();
    const card = await waitFor(() => {
      const c = document.querySelector<HTMLElement>("[data-meter]");
      if (!c) throw new Error("no meter figures yet");
      return c;
    });
    expect(card.textContent).toMatch(/≈ 1,85 \$ · Spanne 1,38 \$ – 2,31 \$ · 4 Punkte × 0,4615 \$/);
  });
});

describe("Zählerkarte: verworfene Ablesungen (#117)", () => {
  beforeEach(() => useGerman());
  it("zählt über alle Quellen eines zusammengeführten Zählers, je Fensterart", async () => {
    const r = (at: string, source: string, resets: string, over: Record<string, unknown> = {}) => ({
      secret_key: "seat", secret_slot: 0, source, window_kind: "seven_day", supported: true, stale: false, week_percent: 5,
      window_percent: null, resets_at: resets, fetched_at: `2026-11-18T${at}:00Z`, ...over,
    });
    const old = "2026-11-25T09:00:00Z", next = "2026-12-02T09:00:00Z";
    mockFetch({
      "/api/v1/cost/models": MODELS,
      "/api/v1/cost/usage-readings": [r("09:00", "inband", old), r("09:10", "usage_cmd", next), r("09:20", "manual", old),
        // the older daemons' window: judged on its own, not late
        r("09:30", "manual", old, { window_kind: "" }), r("09:40", "inband", next, { stale: true }), r("09:50", "inband", next)],
    });
    const meter = { ...span, secret_key: "seat", secret_slot: 0, source: "merged", window_kind: "seven_day" };
    renderWithProviders(<CostInsights days={7} figures={[seatOf("seat#0", { meter })]} />);
    expect(await screen.findByText(/2 Ablesung\(en\) verworfen/)).toBeTruthy();
  });
});
