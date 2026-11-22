import { describe, it, expect, beforeEach, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { onlineManager } from "@tanstack/react-query";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import type { ReactElement } from "react";
import type { CostCells, CostHealthReport } from "../api";
import i18n from "../i18n";
import { fmtUSD } from "../format";
import { renderWithProviders, testPrincipal } from "../test/render";
import Costs from "./Costs";
import CostHome from "./CostHome";
import CostModels from "./CostModels";
import CostWork from "./CostWork";
import OutsideCosts from "./OutsideCosts";
import CostHealthPage from "./CostHealth";
import { CostLoading, held } from "./CostLoading";

/* Waiting on the cost pages: cost/cells takes seconds over the default range. A block that has no answer yet says
 * that it loads; "no data", a dash and $0.00 are answers and stand only once the query has answered. A failure is
 * said as one, and an answer stays through its own refetch. */

const t = (k: string, o: Record<string, unknown> = {}) => i18n.t(k, o) as string;
const LOADING = () => t("costs.home.loading");

const SEAT = "seat_token", PP = 0.5;
const H18 = "2026-11-17T18:00:00Z";
const SOURCE = "example tool";

const line = { secret_key: SEAT, secret_slot: 0, credential_kind: "subscription" };
// The seat's meter, all of it covey's: what the answer table and the home's equation need.
const SUMS = {
  real_usd: 4 * PP, external_real_usd: 0, covey_real_usd: 4 * PP, unsplit_real_usd: 0, share: 0, unattributed_real_usd: 0, unattributed_points: 0,
  unattributed_steps: 0, unattributed_no_call_real_usd: 0, unattributed_no_call_points: 0, incomplete_chunks: 0, split_external_list_usd: 0,
  split_covey_list_usd: 8, points: 4, split_points: 4, split_external_points: 0, split_covey_points: 4, split_runs: 1, split_slack_points: 1,
  split_slack_usd: PP, split_external_slack_points: 0, split_external_slack_usd: 0, split_covey_slack_points: 1, split_covey_slack_usd: PP,
  point_rate: PP, point_rate_mixed: false, step_count: 1, split_steps: 1, unsplit_steps: 0, outside_unpriced_steps: 0,
  outside_unpriced_real_usd: 0, unpriced_steps: 0, unpriced_points: 0, unpriced_calls: 0, unpriced_tokens: 0, unmetered_list_usd: 0,
};
const METER = { ...SUMS, hours: [], source_parts: [],
  steps: [{ at: "2026-11-17T20:00:00Z", from: H18, secret_key: SEAT, secret_slot: 0, points: 4, reset: false, real_usd: 4 * PP, external_list_usd: 0,
    covey_list_usd: 8, reported: true, split: true, external_real_usd: 0, covey_real_usd: 4 * PP, unattributed_real_usd: 0, unattributed_points: 0,
    unsplit_real_usd: 0, source_parts: [] }],
  credentials: [{ ...SUMS, secret_key: SEAT, secret_slot: 0, meter_source: "usage_cmd", sources: [], hours_with_reading: 1, points_per_list_usd: null,
    plan_rates: [], source_parts: [], agents: [] }],
};
const BASE: CostCells = {
  periods: [H18],
  agents: [{ id: "a1", label: "Builder", reported_usd: 8, measured_usd: 8 }],
  types: ["output"],
  cells: [{ ...line, period: H18, agent_id: "a1", reported_usd: 8, engine_usd: 8, measured: { output: 8 }, tokens: { output: 1000 } }],
  subscription: [{ ...line, period: H18, source: "usage_cmd", window_kind: "seven_day", points: 4, readings: 2, resets: 0,
    longest_gap_secs: 900, usd: 4 * PP, unpriced_points: 0 }],
  meter: [{ ...line, source: "usage_cmd", window_kind: "seven_day", baseline_at: H18, baseline_percent: 10, baseline_before_window: false,
    last_at: "2026-11-17T20:00:00Z", last_percent: 14, readings: 2, points: 4, points_low: 3, points_high: 5, longest_gap_secs: 900,
    segments: null, measured_usd: 8 }],
  plans: [{ runtime_id: null, ord: 0, secret_key: SEAT, secret_slot: 0, plan_name: "Max", amount: 200, currency: "USD", period: "month",
    tax_basis: "net", fx_to_usd: 1, fx_date: null, valid_from: "2026-11-01T00:00:00Z", valid_to: null, weekly_usd: 50, usd_per_point: PP }],
  bookings: [],
};
/** A window without a subscription meter, its work on an API key. */
const NO_METER = { ...BASE, subscription: [], meter: [], plans: [],
  cells: BASE.cells.map((c) => ({ ...c, secret_key: "example_key", credential_kind: "api_key" })) } as unknown as CostCells;
const TWO_SEATS = { ...BASE, ...Object.fromEntries((["cells", "subscription", "meter", "plans"] as const).map((k) => [k,
  [...(BASE[k] ?? []), ...(BASE[k] ?? []).map((x) => ({ ...x, secret_slot: 1 }))]])) };
const CELLS = { ...BASE, external: { sources: [], cells: [], meter: METER } } as unknown as CostCells;

/** The same with a source outside covey, billed per use. */
const source = { source: SOURCE, meter_credential: null, calls: 3, list_usd: 1.25, reported_usd: 1.25, unpriced_tokens: 0, unpriced_calls: 0,
  unpriced_models: [], first_at: H18, last_at: H18, hosts: [] };
const WITH_SOURCE = { ...CELLS, external: {
  sources: [source], meter: METER,
  cells: [{ period: H18, source: SOURCE, origin: "main", calls: 3, list_usd: 1.25, measured: { output: 1.25 }, reported_usd: 1.25,
    reported_calls: 3, tokens: { output: 10 }, unpriced_tokens: 0, unpriced_calls: 0 }],
} } as unknown as CostCells;

const ROUTES: Record<string, unknown> = {
  "/cost/cells": CELLS,
  "/cost/billed": { bucket: "hour", points: [], sync: { configured: false, last_attempt_at: null, last_success_at: null,
    last_daily_at: null, data_refreshed_at: null, last_status: "", last_error: "" } },
  "/cost/usage-readings": [],
  "/cost/runs": [{ task_id: "r1", agent_id: "a1", slug: "builder", title: "Example run", state: "done", origin: "manual", total_usd: 8,
    measured_usd: 8, engine_usd: 8, entries: 1, actions: 2, started_at: H18, ended_at: H18, input_tokens: 0, output_tokens: 0,
    cache_read_tokens: 0, cache_creation_tokens: 0 }],
  "/cost/models": { runs_without_breakdown: 0, usd_without_breakdown: 0, rows: [{ agent_id: "a1", agent_slug: "builder", agent_name: "Builder",
    model: "example-model", credential_kind: "subscription", runs: 2, input_tokens: 1, output_tokens: 1, thinking_tokens: 0,
    cache_read_tokens: 1, cache_write_5m_tokens: 0, cache_write_1h_tokens: 0, web_search_requests: 0, engine_usd: 8, list_usd: 8 }] },
  "/cost/indicators": { indicators: [{ key: "k", title: "Example indicator", count: 3 }], failed: 0, total_usd: 8, quality: { decided: 0, denied: 0 } },
  "/cost/external-usage": { bucket: "day", since: H18, source: null, sources: [source], models: [{ model: "outside-model", priced: true }], meter: METER,
    cells: [{ period: H18, source: SOURCE, origin: "main", model: "outside-model", calls: 3, list_usd: 1.25, measured: {}, type_usd: {},
      reported_usd: 1.25, reported_calls: 3, tokens: { output: 10 }, unpriced_tokens: 0, unpriced_calls: 0 }] },
  "/cost/health": { status: "pass", checked_at: H18, checks: [{ id: "c1", status: "pass", reason: "fresh", checked_at: H18,
    facts: { last_push_minutes: 3 } }] } as CostHealthReport,
  "/runtime-instances": [{ id: "rt", engine: "claude", display_name: "Claude", model: "", bindings: [], can_carry_blocking: false,
    creds: [{ ord: 0, kind: "subscription", secret_key: SEAT, secret_slot: 0, label: "" }] }],
};

/** The server: `hold` never answers what it matches, `fail` answers it with an error. Both can change during a test. */
function server(over: Record<string, unknown> = {}) {
  const s = { hold: null as RegExp | null, fail: null as RegExp | null, status: 500 };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (s.hold?.test(url)) return new Promise<Response>(() => {});
    const json = (b: unknown, status: number) => new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });
    if (s.fail?.test(url)) return json({ error: "failed", code: "range_order" }, s.status);
    const hit = Object.entries({ ...ROUTES, ...over }).find(([path]) => url.split("?")[0].endsWith(path));
    return hit ? json(hit[1], 200) : json([], 200);
  }));
  return s;
}

const el = (sel: string) => document.querySelector<HTMLElement>(sel);
/** What a reader sees: the text less what is kept mounted out of sight. */
function seen(root: Element | null = document.body): string {
  if (!root) return "";
  const c = root.cloneNode(true) as HTMLElement;
  c.querySelectorAll("*").forEach((e) => { if ((e as HTMLElement).style?.visibility === "hidden") e.remove(); });
  return c.textContent ?? "";
}
const card = (title: string) => el(title)?.closest(".card") ?? null;
const titled = (text: string) => [...document.querySelectorAll(".card > div:first-child")].find((e) => e.textContent?.startsWith(text))?.parentElement ?? null;
const says = (text: () => string, root: () => Element | null = () => document.body) => waitFor(() => expect(seen(root())).toContain(text()));
const show = (ui: ReactElement, route: string) => renderWithProviders(ui, { route });
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 30)); });

const details = (query = "") => show(<Costs me={testPrincipal("org_admin")} />, `/costs/details${query}`);
const chartCard = () => card("[data-chart-title]");
const modelsCard = () => card("[data-models-title]");
const meterCard = () => card("[data-meter-title]");

beforeEach(async () => {
  await i18n.changeLanguage("en");
  try { localStorage.clear(); } catch { /* none */ }
});

describe("Details while it loads", () => {
  it("says loading where the cells are not in: no $0.00, no 'no cost data', and the bar stays usable", async () => {
    server().hold = /cost\/cells/;
    details();
    await says(() => "Example run");
    await settle();
    expect(seen()).not.toContain(fmtUSD(0));
    expect(seen()).not.toContain(t("costs.empty"));
    expect(seen()).not.toContain(t("costs.insights.noReadings"));
    for (const block of [chartCard(), modelsCard(), meterCard(), el("[data-tokens]")]) {
      expect(seen(block)).toContain(LOADING());
      expect(block!.querySelector('[aria-busy="true"]')).not.toBeNull();
    }
    expect(screen.getAllByRole("status").some((s) => s.textContent === LOADING())).toBe(true);
    const bar = el("[data-filterbar]")!;
    expect(bar.querySelector('button[aria-pressed="true"]')).not.toBeNull();
    expect([...bar.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("costs.range.custom"))!.disabled).toBe(false);
    // What has a query of its own does not wait for the cells.
    expect(seen()).toContain("Example indicator");
  });

  it("the price list says loading, never that there are no indicators", async () => {
    server().hold = /indicators/;
    details();
    await says(() => fmtUSD(8));
    expect(seen()).not.toContain(t("costs.indicators.none"));
    expect(seen(titled(t("costs.indicators.title")))).toContain(LOADING());
  });

  it("a price list that failed is an error, never 'no indicators'", async () => {
    server().fail = /indicators/;
    details();
    await says(() => t("costs.home.loadError"), () => titled(t("costs.indicators.title")));
    expect(seen()).not.toContain(t("costs.indicators.none"));
  });

  it("the meter card waits for the readings instead of saying the 5-hour window was not reported", async () => {
    server().hold = /usage-readings/;
    details();
    await waitFor(() => expect(el("[data-answer-total]")).not.toBeNull());
    expect(seen(meterCard())).toContain(LOADING());
    expect(seen()).not.toContain(t("costs.insights.fiveHourNotReported"));
  });

  it("readings that failed are said, and nothing is said of what they hold", async () => {
    server().fail = /usage-readings/;
    details();
    await says(() => t("costs.home.readingsError"), meterCard);
    await says(() => t("costs.home.readingsError"), chartCard);
    expect(seen()).not.toContain(t("costs.home.loadError"));
    expect(seen()).not.toContain(t("costs.insights.fiveHourNotReported"));
  });

  it.each([
    ["a seat", "", {}],
    ["a seat as a table", "?table=1", {}],
    ["two seats", "", { "/cost/cells": TWO_SEATS }],
    ["a seat at 10 minutes", "?days=1&bucket=10min", {}],
    ["a seat per agent", "?layout=agents", {}],
    ["an API key", "", { "/cost/cells": NO_METER }],
  ])("the chart card says once that the readings failed, also while it asks again: %s", async (_, query, over) => {
    const s = server(over);
    s.fail = /usage-readings/;
    const { qc } = details(query);
    const said = () => [...chartCard()!.querySelectorAll("[data-cost-load-error], [data-meter-error]")].map((e) => e.textContent);
    await says(() => t("costs.home.readingsError"), chartCard);
    expect(said()).toEqual([t("costs.home.readingsError")]);
    s.hold = /usage-readings/;
    act(() => { void qc.refetchQueries({ queryKey: ["cost", "usage-readings"] }); });
    await settle();
    expect(said()).toEqual([t("costs.home.readingsError")]);
    expect(el("[data-chart-body]")!.style.visibility).toBe("");
  });

  it("the chart waits for the bill instead of calling it unreadable", async () => {
    server().hold = /cost\/billed/;
    details();
    await waitFor(() => expect(el("[data-answer-total]")).not.toBeNull());
    expect(seen(chartCard())).toContain(LOADING());
  });

  it("the model table waits for the outside sessions its total holds", async () => {
    server({ "/cost/cells": WITH_SOURCE }).hold = /external-usage/;
    details();
    await waitFor(() => expect(el("[data-answer-total]")).not.toBeNull());
    await says(LOADING, modelsCard);
    expect(modelsCard()!.querySelector("table")).toBeNull();
  });

  it("outside sessions that failed are said beside the model table", async () => {
    server({ "/cost/cells": WITH_SOURCE }).fail = /external-usage/;
    details();
    await says(() => t("costs.outside.loadError"), modelsCard);
  });

  it("a new range shows the wait, never the old figures or another window's outside rows", async () => {
    const s = server({ "/cost/cells": WITH_SOURCE });
    details();
    await waitFor(() => expect(modelsCard()!.querySelector("[data-outside-row]")).not.toBeNull());
    s.hold = /(cells|external-usage).*days=7|days=7.*(cells|external-usage)/;
    fireEvent.click(screen.getByRole("button", { name: t("costs.rangeDays", { count: 7 }) }));
    await says(LOADING, chartCard);
    expect(el("[data-answer-total]")).toBeNull();
    expect(modelsCard()!.querySelector("[data-outside-row]")).toBeNull();
    expect(seen()).not.toContain(fmtUSD(0));
    expect(seen()).not.toContain(t("costs.empty"));
    // The bar keeps its "covey only" chip, so it neither jumps nor loses the focus.
    expect(el("[data-inclusion]")).not.toBeNull();
  });

  it("a range change does not carry the previous window's outside rows while only they load", async () => {
    const s = server({ "/cost/cells": WITH_SOURCE });
    details();
    await waitFor(() => expect(modelsCard()!.querySelector("[data-outside-row]")).not.toBeNull());
    s.hold = /external-usage.*days=7/;
    fireEvent.click(screen.getByRole("button", { name: t("costs.rangeDays", { count: 7 }) }));
    await waitFor(() => expect(el("[data-answer-total]")).not.toBeNull());
    await says(LOADING, modelsCard);
    expect(modelsCard()!.querySelector("[data-outside-row]")).toBeNull();
  });

  it("a new bucket shows the wait in the chart, never 'no cost data'", async () => {
    const s = server();
    details();
    await waitFor(() => expect(el("[data-answer-total]")).not.toBeNull());
    s.hold = /cells.*bucket=week/;
    fireEvent.click(screen.getByRole("button", { name: t("costs.bWeek") }));
    await says(LOADING, chartCard);
    expect(seen()).not.toContain(t("costs.empty"));
  });

  it("a new Who keeps what is ready and says the runs and the price list load", async () => {
    const s = server();
    details();
    const agent = await screen.findByRole("button", { name: /^Builder/ });
    s.hold = /agents\/a1\//;
    fireEvent.click(agent);
    await says(LOADING, () => titled(t("costs.indicators.title")));
    expect(seen(titled(t("costs.runs.title")))).toContain(LOADING());
    expect(seen()).not.toContain(t("costs.indicators.none"));
    expect(el("[data-answer-total]")).not.toBeNull();
  });

  it("cells that failed are an error in each block, never $0.00, and not a wait", async () => {
    server().fail = /cost\/cells/;
    details();
    await says(() => t("costs.home.loadError"), chartCard);
    await settle();
    expect(seen(chartCard())).not.toContain(LOADING());
    expect(seen()).not.toContain(fmtUSD(0));
    expect(seen()).not.toContain(t("costs.empty"));
  });

  it("a failure stays one while the interval tries again", async () => {
    const s = server();
    s.fail = /cost\/cells/;
    const { qc } = details();
    await says(() => t("costs.home.loadError"), chartCard);
    s.hold = /cost\/cells/;
    act(() => { void qc.refetchQueries({ queryKey: ["cost", "cells"] }); });
    await settle();
    expect(seen(chartCard())).toContain(t("costs.home.loadError"));
    expect(seen(chartCard())).not.toContain(LOADING());
  });

  it("a refused range is not asked again every 30 s", async () => {
    const s = server();
    s.fail = /cost\/cells/;
    s.status = 400;
    const { qc } = details();
    await waitFor(() => expect(el("[data-range-refused]")).not.toBeNull());
    const q = qc.getQueryCache().find({ queryKey: ["cost", "cells"], exact: false })!;
    const every = q.observers[0].options.refetchInterval;
    expect(typeof every === "function" ? every(q as never) : every).toBe(false);
  });

  it("a background refetch keeps the figures, and says so when it failed", async () => {
    const s = server();
    const { qc } = details();
    await waitFor(() => expect(el("[data-answer-total]")).not.toBeNull());
    await settle();
    s.hold = /cost\//;
    act(() => { void qc.refetchQueries({ queryKey: ["cost", "cells"] }); });
    await settle();
    expect(el("[data-answer-total]")).not.toBeNull();
    expect(seen(chartCard())).not.toContain(LOADING());

    s.hold = null;
    s.fail = /cost\/cells/;
    await act(async () => { await qc.cancelQueries({ queryKey: ["cost", "cells"] }); await qc.refetchQueries({ queryKey: ["cost", "cells"] }); });
    await says(() => t("costs.home.staleError"));
    expect(el("[data-answer-total]")).not.toBeNull();
  });

  it("the custom range's helpers wait for the readings instead of saying there are none", async () => {
    server().hold = /usage-readings/;
    details("?from=2026-11-17T00:00:00Z&to=2026-11-18T00:00:00Z&tz=utc");
    await screen.findByRole("form", { name: t("costs.range.title") }, { timeout: 15_000 });
    expect(seen()).not.toContain(t("costs.range.noReadings"));
    expect(seen()).not.toContain(t("costs.range.sinceResetNone"));
  });

  it("the list of every charge says it loads", async () => {
    server().hold = /usage-charges/;
    details();
    fireEvent.click(await screen.findByRole("button", { name: new RegExp(t("costs.basis.allCharges")) }));
    await says(LOADING, () => el("[data-all-charges]"));
  });
});

describe("the cost home while it loads", () => {
  const door = (id: string) => el(`[data-home-door="${id}"]`);

  it("shows the doors that are ready while the answer loads", async () => {
    server().hold = /cost\/cells/;
    show(<CostHome />, "/costs");
    await says(() => "example-model");
    await says(() => "Example run");
    expect(seen(el(".cost-home-hero"))).toContain(LOADING());
    expect(seen(door("details"))).toContain(LOADING());
    expect(seen(door("outside"))).toContain(LOADING());
  });

  it("a door whose query has not answered is no dash and no 'no runs'", async () => {
    server().hold = /cost\/(models|runs|health)/;
    show(<CostHome />, "/costs");
    await waitFor(() => expect(el("[data-home-metered]")).not.toBeNull());
    for (const id of ["model", "run", "health"]) {
      expect(seen(door(id))).toContain(LOADING());
      expect(door(id)!.querySelector(".cost-home-door-value")!.textContent).not.toBe("–");
    }
    expect(seen()).not.toContain(t("costs.home.noRuns"));
    expect(seen(el("[data-home-trust]"))).toContain(LOADING());
  });

  it("a door whose query failed says it was not loaded", async () => {
    server().fail = /cost\/(models|runs)/;
    show(<CostHome />, "/costs");
    await says(() => t("costs.noFigure.short.queryFailed"), () => door("model"));
    await says(() => t("costs.noFigure.short.queryFailed"), () => door("run"));
    // The reason is the failed request's own, not the bill's.
    expect(door("model")).toHaveAccessibleName(new RegExp(t("costs.noFigure.long.queryFailed")));
    expect(seen()).not.toContain(t("costs.noFigure.long.loadError"));
    expect(seen()).not.toContain(t("costs.home.noRuns"));
  });

  it("cells that failed are an error, never a blank page", async () => {
    server().fail = /cost\/cells/;
    show(<CostHome />, "/costs");
    await says(() => t("costs.home.loadError"));
  });

  it("the billed basis waits in words", async () => {
    server().hold = /cost\/billed/;
    show(<CostHome />, "/costs?basis=billed");
    await says(LOADING, () => el('[data-home-billed="loading"]'));
  });
});

describe("Models and Work while they load", () => {
  it("Models waits for the outside sessions instead of showing a total without them", async () => {
    server().hold = /external-usage/;
    show(<CostModels />, "/costs/models");
    await settle();
    await says(LOADING);
    expect(el("[data-models-tiles]")).toBeNull();
  });

  it("Models under a selection of agents does not wait for what it leaves out", async () => {
    server().hold = /external-usage/;
    show(<CostModels />, "/costs/models?agents=a1");
    await waitFor(() => expect(el("[data-models-tiles]")).not.toBeNull());
  });

  it("Models keeps its rows through a failed refetch, and says it", async () => {
    const s = server();
    const { qc } = show(<CostModels />, "/costs/models");
    await waitFor(() => expect(el("[data-models-tiles]")).not.toBeNull());
    s.fail = /cost\/models/;
    await act(async () => { await qc.refetchQueries({ queryKey: ["cost", "models"] }); });
    await says(() => t("costs.home.staleError"));
    expect(el("[data-models-tiles]")).not.toBeNull();
  });

  it("Work shows its price list while the runs load", async () => {
    server().hold = /cost\/runs/;
    show(<CostWork />, "/costs/work");
    await says(() => "Example indicator");
    expect(seen()).toContain(LOADING());
  });

  it("Work's price list says loading, never that there are no indicators", async () => {
    server().hold = /indicators/;
    show(<CostWork />, "/costs/work");
    await waitFor(() => expect(el("[data-work-tiles]")).not.toBeNull());
    expect(seen()).not.toContain(t("costs.indicators.none"));
    expect(seen()).toContain(LOADING());
  });

  it("Work keeps its runs through a failed refetch, and says it", async () => {
    const s = server();
    const { qc } = show(<CostWork />, "/costs/work");
    await waitFor(() => expect(el("[data-work-tiles]")).not.toBeNull());
    s.fail = /cost\/runs/;
    await act(async () => { await qc.refetchQueries({ queryKey: ["cost", "runs"] }); });
    await says(() => t("costs.home.staleError"));
    expect(el("[data-work-tiles]")).not.toBeNull();
  });
});

describe("Outside and Health while they load", () => {
  it("Outside says loading, never an empty page", async () => {
    server().hold = /external-usage/;
    show(<OutsideCosts />, "/costs/outside");
    await says(LOADING);
    expect(seen()).not.toContain(t("costs.outside.empty"));
  });

  it("choosing a source keeps the chips and the focus, without the old figures", async () => {
    const s = server();
    show(<OutsideCosts />, "/costs/outside");
    const chip = await screen.findByRole("button", { name: new RegExp(`^${SOURCE}`) });
    s.hold = /source=example/;
    chip.focus();
    fireEvent.click(chip);
    await says(LOADING);
    expect(document.activeElement).toBe(chip);
    expect(chip.isConnected).toBe(true);
    expect(seen()).not.toContain(fmtUSD(1.25));
  });

  it("Outside keeps its figures through a failed refetch, and says it", async () => {
    const s = server();
    const { qc } = show(<OutsideCosts />, "/costs/outside");
    await waitFor(() => expect(el("[data-outside-tiles]")).not.toBeNull());
    s.fail = /external-usage/;
    await act(async () => { await qc.refetchQueries({ queryKey: ["cost", "external-usage"] }); });
    await says(() => t("costs.home.staleError"));
    expect(el("[data-outside-tiles]")).not.toBeNull();
  });

  it("Health says loading, never an empty page", async () => {
    server().hold = /cost\/health/;
    show(<CostHealthPage />, "/costs/health");
    await says(LOADING);
  });

  it("Health stays failed while it is tried again", async () => {
    const s = server();
    s.fail = /cost\/health/;
    const { qc } = show(<CostHealthPage />, "/costs/health");
    await says(() => t("costs.health.loadError"));
    s.hold = /cost\/health/;
    act(() => { void qc.refetchQueries({ queryKey: ["cost", "health"] }); });
    await settle();
    expect(seen()).toContain(t("costs.health.loadError"));
  });
});

describe("a refusal while the request is tried again", () => {
  // A live event invalidates ["cost"], and react-query clears the error of a query without data for that time.
  it.each([
    ["Home", () => <CostHome />, "/costs"],
    ["Details", () => <Costs me={testPrincipal("org_admin")} />, "/costs/details"],
    ["Models", () => <CostModels />, "/costs/models"],
    ["Work", () => <CostWork />, "/costs/work"],
  ] as const)("%s keeps a refused range refused", async (_, page, route) => {
    const s = server();
    s.fail = /cost\/(cells|models|runs)/;
    s.status = 400;
    const { qc } = show(page(), route);
    await waitFor(() => expect(el("[data-range-refused]")).not.toBeNull());
    s.hold = /cost\//;
    act(() => { void qc.invalidateQueries({ queryKey: ["cost"] }); });
    await settle();
    expect(el("[data-range-refused]")).not.toBeNull();
    expect(seen()).not.toContain(t("costs.home.loadError"));
  });

  it("a range once refused does not refuse the next one", async () => {
    const s = server();
    s.fail = /cells.*days=30/;
    s.status = 400;
    details();
    await waitFor(() => expect(el("[data-range-refused]")).not.toBeNull());
    s.hold = /cells.*days=7/;
    fireEvent.click(screen.getByRole("button", { name: t("costs.rangeDays", { count: 7 }) }));
    await says(LOADING, chartCard);
    expect(el("[data-range-refused]")).toBeNull();
  });

  it.each([
    ["Details", () => <Costs me={testPrincipal("org_admin")} />, "/costs/details?agents=a1"],
    ["Work", () => <CostWork />, "/costs/work"],
  ] as const)("%s keeps the reason of a forbidden price list", async (_, page, route) => {
    const s = server();
    s.fail = /indicators/;
    s.status = 403;
    const { qc } = show(page(), route);
    await says(() => t("costs.indicators.restricted"));
    s.hold = /indicators/;
    act(() => { void qc.invalidateQueries({ queryKey: ["cost"] }); });
    await settle();
    expect(seen()).toContain(t("costs.indicators.restricted"));
  });

  it("a forbidden price list is not asked again every 30 s", async () => {
    const s = server();
    s.fail = /indicators/;
    s.status = 403;
    const { qc } = details();
    await says(() => t("costs.indicators.restricted"));
    const q = qc.getQueryCache().find({ queryKey: ["cost", "indicators"], exact: false })!;
    const every = q.observers[0].options.refetchInterval;
    expect(typeof every === "function" ? every(q as never) : every).toBe(false);
  });
});

describe("a change of the filters on every view", () => {
  it.each([
    ["Home", () => <CostHome />, "/costs", () => [fmtUSD(8), "example-model", "Example run"]],
    ["Models", () => <CostModels />, "/costs/models", () => ["example-model", "outside-model"]],
    // A selection has no outside sessions to wait for: the models alone must not be carried over.
    ["Models of a selection", () => <CostModels />, "/costs/models?agents=a1", () => ["example-model"]],
    ["Work", () => <CostWork />, "/costs/work", () => ["Example run"]],
    ["Outside", () => <OutsideCosts />, "/costs/outside", () => [fmtUSD(1.25), "outside-model"]],
  ] as const)("%s shows the wait under a new range, never the old figures", async (_, page, route, old) => {
    const s = server();
    show(page(), route);
    for (const x of old()) await says(() => x);
    s.hold = /days=7/;
    fireEvent.click(screen.getByRole("button", { name: t("costs.rangeDays", { count: 7 }) }));
    await says(LOADING);
    for (const x of old()) expect(seen()).not.toContain(x);
  });

  it("Work shows the wait under a new Who, never the last selection's runs", async () => {
    const s = server();
    show(<CostWork />, "/costs/work?agents=a1");
    await says(() => "Example run");
    s.hold = /v1\/cost\/runs/;
    fireEvent.click(screen.getByRole("button", { name: t("costs.bar.clear") }));
    await says(LOADING);
    expect(seen()).not.toContain("Example run");
  });

  it("Details keeps its agents through the wait, by name and without their figures", async () => {
    const s = server();
    details("?agents=a1");
    const chip = await screen.findByRole("button", { name: /^Builder/ });
    s.hold = /cells.*days=7/;
    fireEvent.click(screen.getByRole("button", { name: t("costs.rangeDays", { count: 7 }) }));
    await says(LOADING, chartCard);
    expect(chip.isConnected).toBe(true);
    expect(seen(chip)).not.toContain(fmtUSD(8));
    expect(chip.textContent).toContain(fmtUSD(8)); // out of sight, it holds the chip's width
    expect(seen(el("[data-who]"))).toContain("Builder");
  });

  it("a department keeps its heading through the wait, without its figure", async () => {
    const s = server({ "/cost/cells": { ...CELLS, agents: [{ ...CELLS.agents[0], department_id: "d1", department: "Example team" }] } });
    details();
    const heading = await screen.findByRole("button", { name: /Example team/ });
    s.hold = /cells.*days=7/;
    fireEvent.click(screen.getByRole("button", { name: t("costs.rangeDays", { count: 7 }) }));
    await says(LOADING, chartCard);
    expect(heading.isConnected).toBe(true);
    expect(seen(heading)).not.toContain(fmtUSD(8));
  });

  it.each(["hold", "fail"] as const)("Details has its Who control before it has agents (%s)", async (how) => {
    server()[how] = /cost\/cells/;
    details();
    await says(how === "hold" ? LOADING : () => t("costs.home.loadError"), chartCard);
    expect(screen.getByRole("button", { name: t("costs.filter.all") })).toBeEnabled();
  });

  it("the 'covey only' chip has its place from the first wait on, locked until it is known", async () => {
    server().hold = /cost\/cells/;
    details();
    await says(LOADING, chartCard);
    expect(el("[data-inclusion] button")).toHaveAttribute("aria-disabled", "true");
  });

  it("the place of 'covey only' goes once there is no source outside covey", async () => {
    server();
    details();
    await waitFor(() => expect(el("[data-answer-total]")).not.toBeNull());
    expect(el("[data-inclusion]")).toBeNull();
  });

  it("an Outside chip keeps the width of its figure, out of sight", async () => {
    const s = server();
    show(<OutsideCosts />, "/costs/outside");
    const chip = await screen.findByRole("button", { name: new RegExp(`^${SOURCE}`) });
    s.hold = /days=7/;
    fireEvent.click(screen.getByRole("button", { name: t("costs.rangeDays", { count: 7 }) }));
    await says(LOADING);
    const figure = chip.querySelector<HTMLElement>("[data-source-figure]")!;
    expect(figure.style.visibility).toBe("hidden");
    expect(figure.textContent).toBe(fmtUSD(1.25));
  });
});

describe("what a screen reader and a scrolled page get of the wait", () => {
  const waits = () => screen.getAllByRole("status").filter((e) => e.textContent === LOADING());

  it.each([
    ["Home", () => <CostHome />, "/costs"],
    ["Details", () => <Costs me={testPrincipal("org_admin")} />, "/costs/details"],
    ["Models", () => <CostModels />, "/costs/models"],
    ["Work", () => <CostWork />, "/costs/work"],
  ] as const)("%s says it once, in the sticky bar and outside what is busy", async (_, page, route) => {
    server().hold = /cost\//;
    show(page(), route);
    await waitFor(() => expect(waits()).toHaveLength(1));
    expect(waits()[0].closest("[data-filterbar]")).not.toBeNull();
    expect(waits()[0].closest('[aria-busy="true"]')).toBeNull();
  });

  it.each([
    ["Outside", () => <OutsideCosts />, "/costs/outside"],
    ["Health", () => <CostHealthPage />, "/costs/health"],
  ] as const)("%s says it once", async (_, page, route) => {
    server().hold = /cost\//;
    show(page(), route);
    await waitFor(() => expect(waits()).toHaveLength(1));
  });

  it.each([
    ["Home", () => <CostHome />, "/costs"],
    ["Details", () => <Costs me={testPrincipal("org_admin")} />, "/costs/details?from=2026-11-17T00:00:00Z&to=2026-11-18T00:00:00Z"],
    ["Models", () => <CostModels />, "/costs/models?agents=a1"],
    ["Work", () => <CostWork />, "/costs/work?from=2026-11-17T00:00:00Z&to=2026-11-18T00:00:00Z"],
  ] as const)("%s stops saying it once all it asks has answered", async (_, page, route) => {
    server();
    show(page(), route);
    await waitFor(() => expect(el("[data-cost-status]")).not.toBeNull());
    await waitFor(() => expect(el("[data-cost-status]")!.textContent).toBe(""));
  });

  it("a waiting door's name says that it loads", async () => {
    server().hold = /cost\//;
    show(<CostHome />, "/costs");
    await says(LOADING);
    for (const id of ["details", "model", "run", "health"]) {
      expect(el(`[data-home-door="${id}"]`)).toHaveAccessibleName(new RegExp(LOADING()));
      expect(el(`[data-home-door="${id}"]`)!.querySelector('[role="status"]')).toBeNull();
    }
  });
});

describe("what waits beside the cells", () => {
  it("Home without a meter keeps what it showed, and says why there is no answer", async () => {
    server({ "/cost/cells": NO_METER });
    show(<CostHome />, "/costs");
    await waitFor(() => expect(el("[data-home-empty]")).not.toBeNull());
    await says(() => "example-model");
    expect(seen()).toContain("Example run");
    expect(el("[data-home-trust]")).not.toBeNull();
    expect(seen(el('[data-home-door="details"]'))).toContain("–");
    expect(seen(el('[data-home-door="details"]'))).not.toContain(t("costs.noFigure.short.queryFailed"));
  });

  it("Home says that a refetch of the answer failed", async () => {
    const s = server();
    const { qc } = show(<CostHome />, "/costs");
    await waitFor(() => expect(el("[data-home-metered]")).not.toBeNull());
    s.fail = /cost\/cells/;
    await act(async () => { await qc.refetchQueries({ queryKey: ["cost", "cells"] }); });
    await says(() => t("costs.home.staleError"));
  });

  it("the pay-per-token answer has its place while the bill loads", async () => {
    server({ "/cost/cells": NO_METER }).hold = /cost\/billed/;
    details();
    await says(LOADING, () => el("[data-ppt-wait]"));
    expect(seen(el("[data-ppt-wait]"))).toContain(t("costs.ppt.title"));
  });

  it("a seat alone waits for no pay-per-token answer", async () => {
    server().hold = /cost\/billed/;
    details();
    await waitFor(() => expect(el("[data-answer-total]")).not.toBeNull());
    expect(el("[data-ppt-wait]")).toBeNull();
  });

  it("the chart waits for the readings, to draw none as absent", async () => {
    server().hold = /usage-readings/;
    details();
    await waitFor(() => expect(el("[data-answer-total]")).not.toBeNull());
    expect(seen(chartCard())).toContain(LOADING());
  });

  it("the billed tile says that it loads", async () => {
    server({ "/cost/cells": NO_METER }).hold = /cost\/billed/;
    details("?basis=billed");
    await says(LOADING, () => el('[data-cost-loading="line"]'));
  });

  it("the history of a limit says that it loads", async () => {
    server().hold = /usage-limit/;
    details();
    fireEvent.click(await screen.findByRole("button", { name: new RegExp(t("costs.basis.limitForm", { name: ".*" })) }));
    await waitFor(() => expect(el('form [data-cost-loading="line"]')).not.toBeNull());
  });

  it("a request that cannot go out is no endless wait", async () => {
    server();
    onlineManager.setOnline(false);
    try {
      show(<CostModels />, "/costs/models");
      await says(() => t("costs.home.loadError"));
      expect(el("[data-cost-loading]")).toBeNull();
    } finally {
      onlineManager.setOnline(true);
    }
  });
});

describe("a page's placeholder", () => {
  it("goes on counting where it takes over from another, so no blank falls between two bundles", () => {
    let now = 1000;
    const spy = vi.spyOn(performance, "now").mockImplementation(() => now);
    const view = render(<div key="app"><CostLoading shape="page" /></div>);
    expect(el(".cost-loading.page")!.style.animationDelay).toBe("400ms");
    now = 2500;
    // The shell is in, and its route's placeholder takes the app's place.
    view.rerender(<div key="route"><CostLoading shape="page" /></div>);
    expect(el(".cost-loading.page")!.style.animationDelay).toBe("-1100ms");
    view.unmount();
    // The next wait is one of its own.
    render(<CostLoading shape="page" />);
    expect(el(".cost-loading.page")!.style.animationDelay).toBe("400ms");
    spy.mockRestore();
  });

  it.each([[120, 0], [450, 250], [1500, 0]])("after %i ms it holds the page for %i ms", async (took, more) => {
    let now = 1000;
    let done = false;
    const spy = vi.spyOn(performance, "now").mockImplementation(() => now);
    const view = render(<CostLoading shape="page" />);
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      now += took;
      void held(Promise.resolve("bundle")).then(() => { done = true; });
      await vi.advanceTimersByTimeAsync(Math.max(0, more - 10));
      expect(done).toBe(more === 0);
      await vi.advanceTimersByTimeAsync(20);
      expect(done).toBe(true);
    } finally {
      vi.useRealTimers();
      view.unmount();
      spy.mockRestore();
    }
  });

  it("appears after its delay and pulses only where motion is welcome", () => {
    const css = readFileSync(resolve("src/styles.css"), "utf8").replace(/\r\n/g, "\n"); // a checkout may have CRLF
    expect(css).toMatch(/\.cost-loading\.page \{ animation: cost-loading-late 1ms linear 400ms both; \}/);
    expect(css).toMatch(/@keyframes cost-loading-late \{ from \{ opacity: 0; \} to \{ opacity: 1; \} \}/);
    expect(css).toMatch(/@media \(prefers-reduced-motion: no-preference\) \{\n  \.cost-loading-bones \{ animation: pulse-dot/);
    expect(css.match(/\.cost-loading[^{\n]*\{[^}]*pulse-dot/g)).toHaveLength(1);
  });
});
