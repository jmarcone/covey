import { useLayoutEffect, useRef, useState } from "react";
import { readFileSync } from "node:fs";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useNavigate } from "react-router";
import type { CostCells, MeterSegment, UsageReading } from "../api";
import i18n from "../i18n";
import { fmtBucket, fmtUSD, fmtUSDAxis, setDisplayTz } from "../format";
import { mockFetch, renderWithProviders } from "../test/render";
import { droppedReadings, meterPoints, meterY, nextBucket, usableReadings } from "./costFigures";
import Costs from "./Costs";
import { customWindow, windowQuery } from "./costRange";
import { BarChart, CostExplorer, type Bar } from "./StackedCostChart";

// The weekly meter as a line over the cost bars (#117): a vertex per reading
// the server counted, whichever reader it came from (the merged meter, #153),
// a line only between readings whose buckets touch (a gap where a bucket has
// none), a fall where the server's weeks change between two
// readings, its own axis from 0 to 100 %. The green crossing labels are gone
// (the human's decision); each reading's value and time is in the tooltip,
// the period's label and the table.

const SEAT = "claude_code_oauth_token";
const KEY = "anthropic_api_key";
const iso = (day: number, h: number, m = 0) => new Date(Date.UTC(2026, 10, day, h, m)).toISOString();

// A segment's top and the coverage's count decide which readings are drawn: where a test is not about that rule they
// are out of the way (1000 %, 1000 readings), where it is, the test gives the server's real ones.
const seg = (start_at: string, end_at: string, over: Partial<MeterSegment> = {}): MeterSegment =>
  ({ start_at, end_at, from_percent: 0, to_percent: 1000, reset: false, points: 0, ...over });

const reading = (at: string, pct: number | null, over: Partial<UsageReading> = {}): UsageReading => ({
  runtime_id: "rt", credential_ord: 0, secret_key: SEAT, secret_slot: 0, credential_kind: "subscription", source: "usage_cmd",
  window_kind: "seven_day", supported: true, window_percent: null, week_percent: pct, resets_at: null, status: "", reason: "",
  stale: false, fetched_at: at, received_at: at, cost_usd: null, ...over,
});

/** One credential's cells in the given periods (one line of $1 each). */
function cellsOf(periods: string[], segments: MeterSegment[] | null, key = SEAT, kind = "subscription", steps: { period: string; points: number }[] = []): CostCells {
  return {
    periods,
    agents: [{ id: "a1", label: "Coordinator", reported_usd: 1, measured_usd: periods.length }],
    types: ["output"],
    cells: periods.map((period) => ({
      period, agent_id: "a1", secret_key: key, secret_slot: 0, credential_kind: kind,
      reported_usd: 1, measured: { output: 1 }, engine_usd: 1, tokens: { output: 50_000 },
    })),
    subscription: steps.map((s) => ({
      period: s.period, secret_key: key, secret_slot: 0, credential_kind: kind, source: "merged", window_kind: "seven_day",
      points: s.points, readings: 2, resets: 0, longest_gap_secs: 900, usd: null, unpriced_points: 0,
    })),
    meter: kind === "subscription" ? [{
      secret_key: key, secret_slot: 0, credential_kind: kind, source: "merged", window_kind: "seven_day",
      baseline_at: periods[0], baseline_percent: 9, baseline_before_window: false,
      last_at: periods[periods.length - 1], last_percent: 10, readings: 1000, points: 1, points_low: 0, points_high: 2,
      longest_gap_secs: 900, segments,
    }] : [],
    plans: [],
    bookings: [],
  };
}

function chart(data: CostCells, readings: UsageReading[], bucket: string, opts: { route?: string; selected?: string[] } = {}) {
  return renderWithProviders(
    <CostExplorer data={data} readings={readings} selected={new Set(opts.selected)} bucket={bucket} fmtPeriod={(p) => fmtBucket(p, bucket)} />,
    { route: opts.route },
  );
}

/** The scroller of a chart: the box around the plot's row. */
const scrollerOf = (c: Element) => c.querySelector("svg[role=group]")!.parentElement!.parentElement as HTMLElement;
/** Readings of one part, a minute apart, with the reset at index `reset`. */
const run = (n: number, reset = -1) => Array.from({ length: n }, (_, i) =>
  reading(iso(18, 1, i), reset >= 0 && i >= reset ? i - reset : 40 + Math.floor(i / 10), { source: ["inband", "manual", "usage_cmd"][i % 3] }));
const weeksOf = (rs: UsageReading[], reset: number): MeterSegment[] => [
  seg(rs[0].fetched_at, rs[reset - 1].fetched_at),
  seg(rs[reset].fetched_at, rs[rs.length - 1].fetched_at, { reset: true }),
];

const parts = (c: HTMLElement) => [...c.querySelectorAll("g[data-meter-part]")];
const dots = (p: Element) => [...p.querySelectorAll<SVGCircleElement>("circle[data-meter-point]")];

describe("weekly meter line", () => {
  beforeEach(() => { void i18n.changeLanguage("en"); });

  // 1
  it("draws the readings of every reader of a merged meter, in order, and none stale, unsupported or of another window", () => {
    const hours = [iso(18, 18), iso(18, 19)];
    const rs = [
      reading(iso(18, 18, 5), 9, { source: "usage_cmd" }), reading(iso(18, 18, 20), 9, { source: "manual", stale: true }),
      reading(iso(18, 18, 25), 12, { supported: false }), reading(iso(18, 18, 30), 10, { source: "inband" }),
      reading(iso(18, 18, 40), 77, { window_kind: "five_hour", source: "inband" }),
      reading(iso(18, 18, 45), 78, { window_kind: "seven_day_opus", source: "inband" }),
      reading(iso(18, 18, 50), 11, { source: "manual" }), reading(iso(18, 19, 5), null),
      reading(iso(18, 19, 10), 12, { source: "inband" }),
    ];
    const { container } = chart(cellsOf(hours, [seg(iso(18, 18, 5), iso(18, 19, 10), { to_percent: 12 })]), rs, "hour");
    const all = parts(container).flatMap(dots);
    expect(all.map((d) => [d.getAttribute("data-at"), d.getAttribute("data-pct")])).toEqual([
      [iso(18, 18, 5), "9"], [iso(18, 18, 30), "10"], [iso(18, 18, 50), "11"], [iso(18, 19, 10), "12"],
    ]);
  });

  it("takes only its own readings for a meter read per source (an older server)", () => {
    const rs = [reading(iso(18, 18, 5), 9, { source: "inband" }), reading(iso(18, 18, 30), 40, { source: "manual" }), reading(iso(18, 18, 50), 10, { source: "inband" })];
    const cov = { ...cellsOf([iso(18, 18)], [seg(iso(18, 18, 5), iso(18, 18, 50))]).meter![0], source: "inband" };
    expect(meterPoints(rs, cov, [iso(18, 18)], "hour").map((p) => p.pct)).toEqual([9, 10]);
  });

  // 2
  it("leaves a gap where the axis skips hours, and joins the same readings at a day", () => {
    const rs = [reading(iso(18, 1, 50), 20), reading(iso(18, 9, 10), 22)];
    const segs = [seg(iso(18, 1, 50), iso(18, 9, 10))];
    const hourly = chart(cellsOf([iso(18, 1), iso(18, 9)], segs), rs, "hour");
    expect(parts(hourly.container)).toHaveLength(2);
    expect(parts(hourly.container).map((p) => dots(p).length)).toEqual([1, 1]);
    // nothing is drawn across the missing readings: no line, no fall
    expect(hourly.container.querySelector("g[data-meter-line] polyline")).toBeNull();
    hourly.unmount();
    const daily = chart(cellsOf([iso(18, 0)], segs), rs, "day");
    expect(parts(daily.container)).toHaveLength(1);
    expect(dots(parts(daily.container)[0])).toHaveLength(2);
    expect(parts(daily.container)[0].querySelector("polyline")).not.toBeNull();
  });

  it("draws no line at 10 minutes: #194 (real cost and meter at 10 minutes) owns it", () => {
    const rs = [reading(iso(18, 10, 5), 10), reading(iso(18, 10, 15), 10), reading(iso(18, 10, 25), 11)];
    const segs = [seg(iso(18, 10, 5), iso(18, 10, 25))];
    const ten = chart(cellsOf([iso(18, 10, 0), iso(18, 10, 10), iso(18, 10, 20)], segs), rs, "10min");
    expect(ten.container.querySelector("[data-meter-line]")).toBeNull();
    expect(ten.container.querySelector("[data-meter-axis]")).toBeNull();
    ten.unmount();
    const hour = chart(cellsOf([iso(18, 10, 0)], segs), rs, "hour");
    expect(hour.container.querySelector("[data-meter-line]")).not.toBeNull();
  });

  it("names the day of a reading in a 10-hour bucket, which can cross midnight", () => {
    // 11/18 20:00 to 11/19 06:00 UTC is one 10-hour bucket; a reading at 02:00 is on the 19th
    const b = 1_794_996_000_000 + 36_000_000;
    expect(new Date(b).toISOString()).toBe(iso(18, 20));
    const at = new Date(b + 6 * 3600_000).toISOString();
    const rs = [reading(at, 10)];
    const { container } = chart(cellsOf([new Date(b).toISOString()], [seg(at, at)]), rs, "10hour");
    fireEvent.focusIn(container.querySelector<SVGGElement>("g[data-period]")!);
    expect(screen.getByRole("tooltip").querySelector("[data-meter-readings]")!.textContent).toContain(fmtBucket(at, "hour"));
  });

  // 3
  it("draws a weekly reset as a fall from the old week's last value to the new week's first", () => {
    const rs = [reading(iso(18, 18, 5), 49), reading(iso(18, 18, 20), 51, { source: "manual" }),
      reading(iso(18, 18, 40), 2, { source: "inband" }), reading(iso(18, 18, 55), 3, { source: "inband" })];
    const segs = [seg(iso(18, 18, 5), iso(18, 18, 20)), seg(iso(18, 18, 40), iso(18, 18, 55), { reset: true })];
    const { container } = chart(cellsOf([iso(18, 18)], segs), rs, "hour");
    // no reading is missing: one part, the line of the old week, the fall, the line of the new week
    const ps = parts(container);
    expect(ps).toHaveLength(1);
    expect(dots(ps[0]).map((d) => d.getAttribute("data-pct"))).toEqual(["49", "51", "2", "3"]);
    const xy = (d: SVGCircleElement) => `${Number(d.getAttribute("cx")).toFixed(1)},${Number(d.getAttribute("cy")).toFixed(1)}`;
    const [a, b, c, d] = dots(ps[0]);
    const drop = ps[0].querySelectorAll("polyline[data-meter-drop]");
    expect(drop).toHaveLength(1);
    // readings this far apart: straight down at the new one's x, from the old week's last value, not a slope
    expect(drop[0].getAttribute("points")).toBe(`${xy(c).split(",")[0]},${xy(b).split(",")[1]} ${xy(c)}`);
    expect(Number(c.getAttribute("cy"))).toBeGreaterThan(Number(b.getAttribute("cy")));
    // the line is solid throughout; the fall is told apart by data-meter-drop, not by its dash
    expect(ps[0].querySelector("polyline[stroke-dasharray]")).toBeNull();
    const lines = [...ps[0].querySelectorAll("polyline[stroke='var(--cost-meter)']:not([data-meter-drop])")].map((l) => l.getAttribute("points"));
    expect(lines).toEqual([`${xy(a)} ${xy(b)}`, `${xy(c)} ${xy(d)}`]);
    // the tooltip says where the new week begins
    fireEvent.focusIn(container.querySelector<SVGGElement>("g[data-period]")!);
    expect(screen.getByRole("tooltip").querySelector("[data-meter-readings]")!.textContent).toContain("18:40 UTC 2 % (new week)");
  });

  it("stands upright across a long silence between adjacent days, where a slope would read as a slow decline", () => {
    const rs = [reading(iso(18, 1), 80), reading(iso(19, 22), 3)];
    const segs = [seg(iso(18, 1), iso(18, 1)), seg(iso(19, 22), iso(19, 22), { reset: true })];
    const { container } = chart(cellsOf([iso(18, 0), iso(19, 0)], segs), rs, "day");
    const [from, to] = container.querySelector("[data-meter-drop]")!.getAttribute("points")!.split(" ").map((p) => p.split(",").map(Number));
    expect(from[0]).toBe(to[0]);
    expect(from[1]).toBeLessThan(to[1]);
  });

  it("draws no fall across a reset that has missing readings on either side: that is a gap", () => {
    const rs = [reading(iso(18, 1, 50), 80), reading(iso(18, 9, 10), 3)];
    const segs = [seg(iso(18, 1, 50), iso(18, 1, 50)), seg(iso(18, 9, 10), iso(18, 9, 10), { reset: true })];
    const { container } = chart(cellsOf([iso(18, 1), iso(18, 9)], segs), rs, "hour");
    expect(parts(container)).toHaveLength(2);
    expect(container.querySelector("[data-meter-drop]")).toBeNull();
    // the week did change, and the tooltip of that period says so
    fireEvent.focusIn(container.querySelectorAll<SVGGElement>("g[data-period]")[1]);
    expect(screen.getByRole("tooltip").querySelector("[data-meter-readings]")!.textContent).toContain("09:10 UTC 3 % (new week)");
  });

  // 4
  it("reads a one-point lag as one week, not a reset", () => {
    const rs = [reading(iso(18, 18, 5), 40), reading(iso(18, 18, 20), 39), reading(iso(18, 18, 40), 41)];
    const { container } = chart(cellsOf([iso(18, 18)], [seg(iso(18, 18, 5), iso(18, 18, 40))]), rs, "hour");
    expect(parts(container)).toHaveLength(1);
    expect(dots(parts(container)[0])).toHaveLength(3);
  });

  it("draws two readers a point apart as they read: no running top, no fall, no new week", () => {
    const rs = [reading(iso(18, 18, 5), 96, { source: "inband" }), reading(iso(18, 18, 20), 97),
      reading(iso(18, 18, 35), 96, { source: "inband" }), reading(iso(18, 18, 50), 97, { source: "manual" })];
    const { container } = chart(cellsOf([iso(18, 18)], [seg(iso(18, 18, 5), iso(18, 18, 50), { to_percent: 97 })]), rs, "hour");
    expect(dots(parts(container)[0]).map((d) => d.getAttribute("data-pct"))).toEqual(["96", "97", "96", "97"]);
    expect(container.querySelector("[data-meter-drop]")).toBeNull();
    expect(container.querySelector("g[data-period]")!.getAttribute("aria-label")).not.toContain("new week");
  });

  // 5
  it("has no crossing label: not above the bars, not in the period's label, not in the tooltip; the tooltip lists each reading with its time", () => {
    const hours = [iso(18, 18)];
    const rs = [reading(iso(18, 18, 5), 9), reading(iso(18, 18, 50), 10)];
    // a step of one point in the hour: this is what wrote "9 % → 10 %" above the bar
    const data = cellsOf(hours, [seg(iso(18, 18, 5), iso(18, 18, 50))], SEAT, "subscription", [{ period: hours[0], points: 1 }]);
    const { container } = chart(data, rs, "hour");
    expect([...container.querySelectorAll("svg text")].filter((t) => /→/.test(t.textContent ?? ""))).toEqual([]);
    expect(container.querySelector("svg text[fill='var(--cost-real)']")).toBeNull();
    const period = container.querySelector<SVGGElement>("g[data-period]")!;
    expect(period.getAttribute("aria-label")).not.toMatch(/→/);
    expect(period.getAttribute("aria-label")).toContain("Weekly meter: 18:05 UTC 9 % · 18:50 UTC 10 %");
    fireEvent.focusIn(period);
    const tip = screen.getByRole("tooltip");
    expect(tip.textContent).not.toMatch(/→/);
    expect(tip.querySelector("[data-meter-readings]")!.textContent).toBe("Weekly meter: 18:05 UTC 9 % · 18:50 UTC 10 %");
  });

  // 6
  it("leaves an API key's chart without line, right axis and legend entry, beside a seat's with them", () => {
    const hours = [iso(18, 18), iso(18, 19)];
    const data = cellsOf(hours, [seg(iso(18, 18, 5), iso(18, 19, 5))], KEY, "api_key");
    data.meter = [{ ...cellsOf(hours, [seg(iso(18, 18, 5), iso(18, 19, 5))]).meter![0], secret_key: KEY, credential_kind: "api_key" }];
    const rs = [reading(iso(18, 18, 5), 9, { secret_key: KEY, credential_kind: "api_key" }), reading(iso(18, 19, 5), 10, { secret_key: KEY, credential_kind: "api_key" })];
    const seat = cellsOf(hours, [seg(iso(18, 18, 5), iso(18, 19, 5))]);
    data.cells.push(...seat.cells);
    data.meter!.push(...seat.meter!);
    rs.push(reading(iso(18, 18, 5), 21), reading(iso(18, 19, 5), 22));
    const { container } = chart(data, rs, "hour");
    const of = (key: string) => container.querySelector(`[data-credential="${key}#0"]`)!;
    expect(of(KEY).querySelector("svg[role=group]")).not.toBeNull();
    expect(of(KEY).querySelector("[data-meter-line], [data-meter-axis], [data-meter-legend]")).toBeNull();
    for (const q of ["[data-meter-line]", "[data-meter-axis]", "[data-meter-legend]"]) expect(of(SEAT).querySelector(q)).not.toBeNull();
  });

  // 9
  it("names the line once, in the legend, and hides it from assistive technology", () => {
    const rs = [reading(iso(18, 18, 5), 9), reading(iso(18, 18, 50), 10)];
    const { container } = chart(cellsOf([iso(18, 18)], [seg(iso(18, 18, 5), iso(18, 18, 50))]), rs, "hour");
    expect([...container.querySelectorAll("[data-meter-tick]")].map((t) => t.textContent)).toEqual(["0 %", "25 %", "50 %", "75 %", "100 %"]);
    expect(new Set([...container.querySelectorAll("[data-meter-tick]")].map((t) => t.getAttribute("fill")))).toEqual(new Set(["var(--text-muted)"]));
    expect(container.querySelectorAll("[data-meter-legend]")).toHaveLength(1);
    expect(container.querySelector("[data-meter-legend]")!.textContent!.trim()).toBe("Weekly meter (%, right axis)");
    // no title along the axis and no sentence under the chart
    expect(container.querySelector("[data-meter-title], [data-meter-note], svg[data-meter-axis] text[transform]")).toBeNull();
    expect(container.querySelector("[data-meter-line]")!.getAttribute("aria-hidden")).toBe("true");
    expect(container.querySelector("[data-meter-axis]")!.getAttribute("aria-hidden")).toBe("true");
  });

  it("has its own ink in both themes, and a legend in the page's language", () => {
    expect(readFileSync(`${process.cwd()}/src/styles.css`, "utf8")).toContain("--cost-meter: light-dark(#1f1f24, #ececef);");
    void i18n.changeLanguage("de");
    const rs = [reading(iso(18, 18, 5), 9)];
    const { container } = chart(cellsOf([iso(18, 18)], [seg(iso(18, 18, 5), iso(18, 18, 5))]), rs, "hour");
    expect(container.querySelector("[data-meter-legend]")!.textContent!.trim()).toBe("Wochenzähler (%, rechte Achse)");
  });

  it.each([false, true])("keeps the legend's swatch beside its text, and the organization's mark outside that entry (selection: %s)", (selected) => {
    const { container } = chart(cellsOf([iso(18, 1)], [seg(iso(18, 1, 5), iso(18, 1, 5))]), [reading(iso(18, 1, 5), 4)], "hour", { selected: selected ? ["a1"] : [] });
    const legend = container.querySelector<HTMLElement>("[data-meter-legend]")!;
    const entry = legend.firstElementChild as HTMLElement;
    // one item, which shrinks with the row: the text wraps inside it
    expect(entry.querySelector("svg")).not.toBeNull();
    expect(entry.style.display).toBe("inline-flex");
    expect(entry.style.maxWidth).toBe("100%");
    const text = entry.querySelector("span")!;
    expect(text.textContent).toBe(i18n.t("costs.fig.meterLine"));
    expect(text.style.minWidth).toBe("0px");
    expect(entry.querySelector("[data-org-wide]")).toBeNull();
    expect(legend.querySelector("[data-org-wide]") != null).toBe(selected);
  });

  it("hides the legend under \"Show as table\"", () => {
    const rs = [reading(iso(18, 18, 5), 9), reading(iso(18, 18, 50), 10)];
    const { container } = chart(cellsOf([iso(18, 18)], [seg(iso(18, 18, 5), iso(18, 18, 50))]), rs, "hour");
    expect(container.querySelector("[data-meter-legend]")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Show as table" }));
    expect(container.querySelector("table")).not.toBeNull();
    expect(container.querySelector("[data-meter-legend]")).toBeNull();
  });

  it("says \"nothing\" for a period without cost, but not when it has readings", () => {
    const days = [iso(18, 0), iso(19, 0)];
    const data = cellsOf(days, [seg(iso(18, 12), iso(19, 12))]);
    // no cost on either day; only the first has a reading
    data.cells = data.cells.map((c) => ({ ...c, reported_usd: 0, measured: { output: 0 }, engine_usd: 0, tokens: { output: 0 } }));
    const { container } = chart(data, [reading(iso(18, 12), 9)], "day");
    const [withReading, without] = [...container.querySelectorAll<SVGGElement>("g[data-period]")];
    fireEvent.focusIn(withReading);
    expect(within(screen.getByRole("tooltip")).getByText(/9 %/)).toBeInTheDocument();
    expect(screen.getByRole("tooltip").textContent).not.toContain("nothing");
    fireEvent.focusIn(without);
    expect(screen.getByRole("tooltip").textContent).toContain("nothing");
  });

  it("is drawn in the real-vs-list view too, and not in the per-agent charts", () => {
    const rs = [reading(iso(18, 18, 5), 9), reading(iso(18, 18, 50), 10)];
    const { container } = chart(cellsOf([iso(18, 18)], [seg(iso(18, 18, 5), iso(18, 18, 50))]), rs, "hour");
    fireEvent.click(screen.getByRole("button", { name: "Real vs list" }));
    expect(container.querySelector("[data-meter-line]")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Per agent" }));
    expect(container.querySelector("[data-meter-line]")).toBeNull();
    expect(container.querySelector("[data-meter-axis]")).toBeNull();
  });

  // One rule for the line, its axis, its legend and its column, under main's Basis switch (#222).
  const full = { line: true, axis: true, legend: true, column: false };
  const none = { line: false, axis: false, legend: false, column: false };
  it.each([
    ["token", "", full],
    ["capacity, priced", "?basis=capacity", full],
    ["billed on a seat", "?basis=billed", none],
    ["billed, token price also shown", "?basis=billed&also=reported", full],
    ["capacity, unpriced", "?basis=capacity&unpriced", none],
    ["capacity, unpriced, token price also shown", "?basis=capacity&also=reported&unpriced", full],
    ["the real series hidden", "?hide=real", full],
    ["real vs list", "?view=vs", full],
    ["as a table", "?table=1", { ...none, column: true }],
    ["billed on a seat, as a table", "?basis=billed&table=1", none],
    ["per agent", "?layout=agents", none],
    ["per agent, as a table", "?layout=agents&table=1", none],
  ])("shows line, axis, legend and column together, only where the chart is drawn: %s", (_, route, want) => {
    const rs = [reading(iso(18, 18, 5), 9), reading(iso(18, 18, 50), 10)];
    const data = cellsOf([iso(18, 18)], [seg(iso(18, 18, 5), iso(18, 18, 50), { to_percent: 10, points: 1 })], SEAT, "subscription",
      [{ period: iso(18, 18), points: 1 }]);
    if (!route.includes("unpriced")) {
      data.subscription![0].usd = 0.5;
      data.plans = [{
        runtime_id: null, ord: 0, secret_key: SEAT, secret_slot: 0, plan_name: "Max", amount: 200, currency: "USD", period: "month",
        tax_basis: "net", fx_to_usd: 1, fx_date: null, valid_from: iso(1, 0), valid_to: null, weekly_usd: 50, usd_per_point: 0.5,
      }];
    }
    const { container } = chart(data, rs, "hour", { route: `/${route}` });
    const has = (q: string) => container.querySelector(q) != null;
    expect({ line: has("[data-meter-line]"), axis: has("[data-meter-axis]"), legend: has("[data-meter-legend]"), column: has("[data-meter-col]") }).toEqual(want);
    expect(container.querySelector("[data-meter-note]")).toBeNull();
  });

  it("keeps the account's line under an agent selection and marks it as the whole organization's", () => {
    const rs = [reading(iso(18, 18, 5), 9), reading(iso(18, 18, 50), 10)];
    const data = () => cellsOf([iso(18, 18)], [seg(iso(18, 18, 5), iso(18, 18, 50))]);
    const all = chart(data(), rs, "hour");
    expect(parts(all.container).flatMap(dots)).toHaveLength(2);
    expect(all.container.querySelector("[data-meter-legend] [data-org-wide]")).toBeNull();
    all.unmount();
    const one = chart(data(), rs, "hour", { selected: ["a1"] });
    expect(parts(one.container).flatMap(dots)).toHaveLength(2);
    const mark = one.container.querySelector<HTMLElement>("[data-meter-legend] [data-org-wide]")!;
    expect(mark).not.toBeNull();
    // it is wider than a phone's card: it breaks into lines there, not past the edge
    expect(mark.style.whiteSpace).not.toBe("nowrap");
    one.unmount();
    const table = chart(data(), rs, "hour", { selected: ["a1"], route: "/?table=1" });
    expect(table.container.querySelector("[data-meter-col] [data-org-wide]")).not.toBeNull();
  });
});

// Design review round 1 of #119.
describe("weekly meter line: legibility", () => {
  beforeEach(() => { void i18n.changeLanguage("en"); });
  const bar = (v: number): Bar[] => [{ key: "x", label: "x", color: "red", valueLabel: true, segs: [{ key: "x", label: "x", color: "red", value: v }] }];
  const point = (pct: number, slot: number, frac: number, at: string, part = 0) => ({ at, pct, slot, frac, part, reset: false, newWeek: false });
  const draw = (points: ReturnType<typeof point>[], n = 2) => {
    const periods = Array.from({ length: n }, (_, i) => iso(18, i));
    return renderWithProviders(
      <BarChart title="t" periods={periods} groups={periods.map(() => bar(1))} fmtPeriod={(p) => p} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis}
        meter={{ points, name: "n", fmtAt: (s) => s }} />,
    );
  };

  it("casts a casing in the card's colour under each part's solid line", () => {
    const { container } = draw([point(10, 0, 0.2, "a"), point(20, 0, 0.8, "b"), point(30, 1, 0.5, "c")]);
    const lines = [...container.querySelectorAll("g[data-meter-part] polyline")];
    expect(lines).toHaveLength(2);
    const [casing, line] = lines;
    expect(casing.getAttribute("stroke")).toBe("var(--surface-2)");
    expect(casing.getAttribute("stroke-width")).toBe("3.75");
    expect(casing.getAttribute("points")).toBe(line.getAttribute("points"));
    expect(line.getAttribute("stroke")).toBe("var(--cost-meter)");
    // dashed is the edge of a bar's "not split" part: the meter's line and its legend swatch are never dashed
    for (const l of container.querySelectorAll("g[data-meter-part] polyline, [data-meter-legend] svg line")) expect(l.getAttribute("stroke-dasharray")).toBeNull();
  });

  it("draws the value labels after the line, so it never strikes one out", () => {
    const { container } = draw([point(10, 0, 0.2, "a"), point(20, 0, 0.8, "b")]);
    const line = container.querySelector("[data-meter-line]")!;
    const labels = [...container.querySelectorAll("[data-value]")];
    expect(labels.length).toBeGreaterThan(0);
    for (const l of labels) expect(line.compareDocumentPosition(l) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    for (const bar of container.querySelectorAll("g[data-period] rect[rx]")) expect(bar.compareDocumentPosition(line) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("keeps every reading a vertex and drops the dots that would run into beads", () => {
    const { container } = draw(Array.from({ length: 60 }, (_, k) => point(k, 0, k / 60, `p${k}`)), 1);
    const part = parts(container as HTMLElement)[0];
    expect(part.querySelector("polyline:not([data-meter-casing])")!.getAttribute("points")!.split(" ")).toHaveLength(60);
    expect(dots(part).map((d) => [d.getAttribute("data-at"), d.getAttribute("r"), d.getAttribute("stroke")]))
      .toEqual([["p0", "2.25", "var(--surface-2)"], ["p59", "2.25", "var(--surface-2)"]]);
  });

  it("keeps every dot where the readings are apart, and both dots beside a stretch without one", () => {
    const apart = draw([0.1, 0.3, 0.5, 0.7, 0.9].map((f, k) => point(k, 0, f, `p${k}`)), 1);
    expect(dots(parts(apart.container as HTMLElement)[0])).toHaveLength(5);
    apart.unmount();
    // two tight runs with most of the slot between them
    const runs = [...Array.from({ length: 20 }, (_, k) => k / 400), ...Array.from({ length: 20 }, (_, k) => 0.9 + k / 400)];
    const two = draw(runs.map((f, k) => point(k, 0, f, `p${k}`)), 1);
    expect(dots(parts(two.container as HTMLElement)[0]).map((d) => d.getAttribute("data-at"))).toEqual(["p0", "p19", "p20", "p39"]);
    two.unmount();
    expect(dots(parts(draw([point(7, 0, 0.5, "only")], 1).container as HTMLElement)[0])).toHaveLength(1);
  });

  it("keeps both ends of a fall dotted in a dense line", () => {
    const ps = Array.from({ length: 300 }, (_, k) => ({ ...point(k < 100 ? 90 : 2, 0, k / 300, `p${k}`), reset: k === 100, newWeek: k === 100 }));
    const { container } = draw(ps, 1);
    expect(dots(parts(container as HTMLElement)[0]).map((d) => d.getAttribute("data-at"))).toEqual(["p0", "p99", "p100", "p299"]);
    const lines = [...container.querySelectorAll("g[data-meter-part] polyline:not([data-meter-casing]):not([data-meter-drop])")];
    expect(lines.map((l) => l.getAttribute("points")!.split(" ").length)).toEqual([100, 200]);
    // less than a dot's room apart, the fall joins the two readings
    const ends = [lines[0].getAttribute("points")!.split(" ").at(-1), lines[1].getAttribute("points")!.split(" ")[0]];
    expect(container.querySelector("[data-meter-drop]")!.getAttribute("points")).toBe(ends.join(" "));
  });

  it("scrolls to the newest period, and sets no rule along the axes", () => {
    const sw = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollWidth");
    Object.defineProperty(HTMLElement.prototype, "scrollWidth", { configurable: true, get: () => 777 });
    try {
      const { container } = draw([point(10, 0, 0.2, "a")], 10);
      expect(scrollerOf(container).scrollLeft).toBe(777);
      for (const q of ["svg[data-meter-axis]", "svg[data-cost-axis]"]) {
        const strip = container.querySelector<SVGElement>(q)!;
        expect([strip.style.borderLeft, strip.style.borderRight]).toEqual(["", ""]);
        expect(strip.style.background).toBe("var(--surface-2)");
      }
      expect(container.querySelector("svg[data-meter-axis]")!.getAttribute("width")).toBe("44");
      // numbers only: a tick mark before one reads as a minus
      expect(container.querySelector("svg[data-meter-axis] line")).toBeNull();
    } finally {
      if (sw) Object.defineProperty(HTMLElement.prototype, "scrollWidth", sw); else delete (HTMLElement.prototype as unknown as Record<string, unknown>).scrollWidth;
    }
  });
});

// 7: the formula, on the chart's own geometry.
describe("weekly meter line: position", () => {
  beforeEach(() => { void i18n.changeLanguage("en"); });
  const bar = (v: number): Bar[] => [{ key: "x", label: "x", color: "red", segs: [{ key: "x", label: "x", color: "red", value: v }] }];

  it("puts 0 % on the baseline, 100 % at the top, and a reading at its fraction of its bucket's slot", () => {
    const periods = [iso(18, 18), iso(18, 19)];
    const point = (pct: number, slot: number, frac: number, part = 0) => ({ at: iso(18, 19, 30), pct, slot, frac, part, reset: false, newWeek: false });
    const { container } = renderWithProviders(
      <BarChart title="t" periods={periods} groups={periods.map(() => bar(1))} fmtPeriod={(p) => p} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis}
        meter={{ points: [point(0, 1, 0.5), point(100, 1, 0.5, 1), point(50, 0, 0.25, 2)], name: "n", fmtAt: (s) => s }} />,
    );
    const svg = container.querySelector("svg[role=group]")!;
    const height = Number(svg.getAttribute("height"));
    // padT 18, padB 34, padL 64, two periods of a 120 px slot
    const padT = 18, plotH = height - 18 - 34, padL = 64, slot = 120;
    const at = (n: number) => dots(parts(container as HTMLElement)[n])[0];
    expect(Number(at(0).getAttribute("cy"))).toBeCloseTo(padT + plotH, 6);
    expect(Number(at(1).getAttribute("cy"))).toBeCloseTo(padT, 6);
    expect(Number(at(2).getAttribute("cy"))).toBeCloseTo(padT + plotH - 0.5 * plotH, 6);
    expect(Number(at(0).getAttribute("cx"))).toBeCloseTo(padL + 1 * slot + 0.5 * slot, 6);
    expect(Number(at(2).getAttribute("cx"))).toBeCloseTo(padL + 0 * slot + 0.25 * slot, 6);
    // the right axis has its own strip: 0 % on the baseline, 100 % at the top, in 25 % steps
    const ticks = [...container.querySelectorAll("svg[data-meter-axis] [data-meter-tick]")];
    expect(ticks.map((t) => Number(t.getAttribute("y")) - 4)).toEqual([0, 25, 50, 75, 100].map((v) => padT + plotH - (v / 100) * plotH));
  });

  it("draws a reading outside 0 to 100 % at the plot's edge, and keeps its true value in the label and the tooltip", () => {
    expect(meterY(1000, 18, 200)).toBe(18);
    expect(meterY(-5, 18, 200)).toBe(218);
    expect(meterY(50, 18, 200)).toBe(118);
    const periods = [iso(18, 18)];
    const point = (pct: number, frac: number, part: number) => ({ at: iso(18, 18, 10 * part), pct, slot: 0, frac, part, reset: false, newWeek: false });
    const { container } = renderWithProviders(
      <BarChart title="t" periods={periods} groups={[bar(1)]} fmtPeriod={(p) => p} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis}
        meter={{ points: [point(1000, 0.25, 0), point(250, 0.75, 1)], name: "n", fmtAt: (s) => s }} />,
    );
    const padT = 18;
    const cys = parts(container as HTMLElement).flatMap(dots).map((d) => Number(d.getAttribute("cy")));
    expect(cys).toEqual([padT, padT]);
    expect(parts(container as HTMLElement).flatMap(dots).map((d) => d.getAttribute("data-pct"))).toEqual(["1000", "250"]);
    expect(container.querySelector("g[data-period]")!.getAttribute("aria-label")).toContain("1,000 % · 2026-11-18T18:10:00.000Z 250 %");
  });
});

describe("meterPoints", () => {
  const cov = (segments: MeterSegment[] | null) => cellsOf([iso(18, 18)], segments).meter![0];

  it("gives each reading its slot and its fraction of the bucket", () => {
    const ps = meterPoints([reading(iso(18, 18, 45), 10)], cov([seg(iso(18, 18, 45), iso(18, 18, 45))]), [iso(18, 17), iso(18, 18)], "hour");
    expect(ps).toEqual([{ at: iso(18, 18, 45), pct: 10, slot: 1, frac: 0.75, part: 0, reset: false, newWeek: false }]);
  });

  it("draws nothing for a reading in no segment, in no period, or without segments", () => {
    const rs = [reading(iso(18, 17, 5), 5), reading(iso(18, 18, 5), 9), reading(iso(18, 20, 5), 11)];
    const segs = [seg(iso(18, 18, 5), iso(18, 20, 5))];
    // 17:05 lies in a period of the chart but in no segment, 20:05 is in no period of the chart
    expect(meterPoints(rs, cov(segs), [iso(18, 17), iso(18, 18)], "hour").map((p) => p.pct)).toEqual([9]);
    expect(meterPoints(rs, cov(null), [iso(18, 18)], "hour")).toEqual([]);
  });

  it("marks the first reading of a new week as a reset when no reading is missing before it, and starts a new part when one is", () => {
    const segs = [seg(iso(18, 18, 5), iso(18, 18, 20)), seg(iso(18, 18, 40), iso(18, 19, 5), { reset: true }), seg(iso(18, 22, 5), iso(18, 22, 5))];
    const rs = [reading(iso(18, 18, 5), 49), reading(iso(18, 18, 20), 51), reading(iso(18, 18, 40), 2), reading(iso(18, 19, 5), 3), reading(iso(18, 22, 5), 4)];
    const ps = meterPoints(rs, cov(segs), [iso(18, 18), iso(18, 19), iso(18, 22)], "hour");
    expect(ps.map((p) => p.reset)).toEqual([false, false, true, false, false]);
    // 11/18 19:05 and 22:05 have the hours 20 and 21 between them: a gap, in the same week or not
    expect(ps.map((p) => p.part)).toEqual([0, 0, 0, 0, 1]);
  });

  it("joins readings of adjacent buckets and splits them where a bucket lies between", () => {
    const segs = [seg(iso(16, 12), iso(23, 12))];
    const days = [reading(iso(16, 12), 5), reading(iso(17, 12), 6), reading(iso(19, 12), 7)];
    expect(meterPoints(days, cov(segs), [iso(16, 0), iso(17, 0), iso(19, 0)], "day").map((p) => p.part)).toEqual([0, 0, 1]);
    // Monday 11/16 and Monday 11/23 are adjacent weeks; 11/22 and 11/23 are adjacent days, 11/16 and 11/22 are not
    const weeks = [reading(iso(16, 12), 5), reading(iso(22, 12), 6), reading(iso(23, 12), 7)];
    expect(meterPoints(weeks, cov(segs), [iso(16, 0), iso(23, 0)], "week").map((p) => p.part)).toEqual([0, 0, 0]);
    expect(meterPoints(weeks, cov(segs), [iso(16, 0), iso(22, 0), iso(23, 0)], "day").map((p) => p.part)).toEqual([0, 1, 1]);
    expect(meterPoints(weeks, cov(segs), [iso(16, 0), iso(23, 0)], "day").map((p) => p.part)).toEqual([0, 1]);
  });

  it("knows the width of every bucket the Costs page offers", () => {
    const t0 = Date.UTC(2026, 10, 18, 10);
    expect(nextBucket(t0, "10min") - t0).toBe(600_000);
    expect(nextBucket(t0, "hour") - t0).toBe(3600_000);
    expect(nextBucket(t0, "10hour") - t0).toBe(36_000_000);
    expect(nextBucket(Date.UTC(2026, 10, 18), "day") - Date.UTC(2026, 10, 18)).toBe(86400_000);
    expect(nextBucket(Date.UTC(2026, 10, 16), "week") - Date.UTC(2026, 10, 16)).toBe(7 * 86400_000);
    // a month is as long as its calendar month: November has 30 days, December 31, February 28
    expect(nextBucket(Date.UTC(2026, 10, 1), "month")).toBe(Date.UTC(2026, 11, 1));
    expect(nextBucket(Date.UTC(2026, 11, 1), "month")).toBe(Date.UTC(2027, 0, 1));
    expect(nextBucket(Date.UTC(2027, 1, 1), "month")).toBe(Date.UTC(2027, 2, 1));
  });

  it("places a reading by the width of a 10-minute bucket, and joins readings of adjacent buckets", () => {
    const segs = [seg(iso(18, 10, 5), iso(18, 10, 25))];
    const rs = [reading(iso(18, 10, 5), 10), reading(iso(18, 10, 15), 10), reading(iso(18, 10, 25), 11)];
    const ps = meterPoints(rs, cov(segs), [iso(18, 10, 0), iso(18, 10, 10), iso(18, 10, 20)], "10min");
    expect(ps.map((p) => p.part)).toEqual([0, 0, 0]);
    expect(ps.map((p) => p.frac)).toEqual([0.5, 0.5, 0.5]);
  });

  it("places a reading by the width of a 10-hour bucket, and joins readings of adjacent buckets", () => {
    // 10-hour buckets start at multiples of 10 h from the epoch: 11/18 10:00 and 20:00 UTC are two of them
    const b1 = 1_794_996_000_000, b2 = b1 + 36_000_000;
    expect(new Date(b1).toISOString()).toBe(iso(18, 10));
    const at = (ms: number) => new Date(ms).toISOString();
    const segs = [seg(at(b1 + 5 * 3600_000), at(b2 + 3600_000))];
    const rs = [reading(at(b1 + 5 * 3600_000), 10), reading(at(b1 + 9 * 3600_000), 11), reading(at(b2 + 3600_000), 12)];
    const ps = meterPoints(rs, cov(segs), [at(b1), at(b2)], "10hour");
    expect(ps.map((p) => p.frac)).toEqual([0.5, 0.9, 0.1]);
    // adjacent buckets: one part, no gap where no reading is missing
    expect(ps.map((p) => p.part)).toEqual([0, 0, 0]);
  });

  it("joins readings of adjacent months, and splits them at a month between", () => {
    const segs = [seg(iso(1, 0), "2027-03-31T00:00:00Z")];
    const m = (y: number, mo: number) => new Date(Date.UTC(y, mo, 1)).toISOString();
    const rs = [reading(iso(20, 12), 5), reading("2026-12-15T12:00:00Z", 6), reading("2027-02-15T12:00:00Z", 7)];
    const ps = meterPoints(rs, cov(segs), [m(2026, 10), m(2026, 11), m(2027, 1)], "month");
    expect(ps.map((p) => p.part)).toEqual([0, 0, 1]);
    expect(ps[0].frac).toBeCloseTo(19.5 / 30, 6);
    expect(ps[1].frac).toBeCloseTo(14.5 / 31, 6);
  });

  it("drops a reading that lags behind into an old week, by the server's rule, and takes only the coverage's window kind", () => {
    const week1 = "2026-11-25T09:00:00Z", week2 = "2026-12-02T09:00:00Z";
    const rs = [
      reading(iso(18, 9), 38, { resets_at: week1 }), reading(iso(18, 10), 39, { resets_at: week1 }),
      reading(iso(18, 10, 35), 1, { resets_at: week2 }),
      reading(iso(18, 10, 45), 39, { resets_at: week1, runtime_id: "rt-lagging" }),
      reading(iso(18, 11), 2, { resets_at: week2 }),
      // another window kind of the same credential: no week of these segments
      reading(iso(18, 10, 50), 77, { window_kind: "" }),
    ];
    const segs = [seg(iso(18, 9), iso(18, 10), { from_percent: 38, to_percent: 39 }), seg(iso(18, 10, 35), iso(18, 11), { reset: true, from_percent: 1, to_percent: 2 })];
    const ps = meterPoints(rs, cov(segs), [iso(18, 9), iso(18, 10), iso(18, 11)], "hour");
    expect(ps.map((p) => p.pct)).toEqual([38, 39, 1, 2]);
    // the line's readings are the server's: each week's first and last drawn reading are the segment's ends and values
    for (const g of segs) {
      const inSeg = ps.filter((p) => Date.parse(p.at) >= Date.parse(g.start_at) && Date.parse(p.at) <= Date.parse(g.end_at));
      expect([inSeg[0].at, inSeg.at(-1)!.at]).toEqual([g.start_at, g.end_at]);
      expect([inSeg[0].pct, inSeg.at(-1)!.pct]).toEqual([g.from_percent, g.to_percent]);
    }
    expect(ps.map((p) => p.newWeek)).toEqual([false, false, true, false]);
  });

  it("keeps a late reading when a reset time is within the hour, or missing", () => {
    const rs = [
      reading(iso(18, 9), 10, { resets_at: "2026-11-25T09:30:00Z" }), reading(iso(18, 9, 10), 11, { resets_at: "2026-11-25T09:00:00Z" }),
      reading(iso(18, 9, 20), 12, { resets_at: null }),
    ];
    expect(usableReadings(rs, SEAT, 0, "usage_cmd", "seven_day").map((r) => r.week_percent)).toEqual([10, 11, 12]);
    // the meter card's call, without a window kind, takes the old "" window as well
    const old = [reading(iso(18, 9), 10, { window_kind: "" }), reading(iso(18, 10), 11)];
    expect(usableReadings(old, SEAT, 0, "usage_cmd")).toHaveLength(2);
    expect(usableReadings(old, SEAT, 0, "usage_cmd", "seven_day")).toHaveLength(1);
  });

  it("keeps a reading whose resets_at drifts by under an hour a step inside one segment, as the server does", () => {
    // A manual weekly reading may carry any resets_at (httpapi/costinsights.go:286-292); stacked.go:656, :664-665 keep this
    // run as one week. Judged against the segment's latest (10:40), the first (09:00) would be 100 minutes late.
    const rs = [
      reading(iso(18, 10, 5), 1, { source: "manual", resets_at: "2026-11-25T09:00:00Z" }),
      reading(iso(18, 10, 20), 2, { source: "manual", resets_at: "2026-11-25T09:50:00Z" }),
      reading(iso(18, 10, 40), 3, { source: "manual", resets_at: "2026-11-25T10:40:00Z" }),
    ];
    const c = { ...cov([seg(iso(18, 10, 5), iso(18, 10, 40))]), source: "manual" };
    expect(meterPoints(rs, c, [iso(18, 10)], "hour").map((p) => p.pct)).toEqual([1, 2, 3]);
  });

  it("drops, in usableReadings, a reading more than an hour before the latest reset an earlier reading gave", () => {
    const rs = [
      reading(iso(18, 10, 5), 1, { resets_at: "2026-11-25T09:00:00Z" }), reading(iso(18, 10, 20), 2, { resets_at: "2026-11-25T09:35:00Z" }),
      reading(iso(18, 10, 30), 39, { resets_at: "2026-11-18T12:00:00Z" }), reading(iso(18, 10, 40), 3, { resets_at: null }),
    ];
    expect(usableReadings(rs, SEAT, 0, "merged", "seven_day").map((r) => r.week_percent)).toEqual([1, 2, 3]);
  });

  // The server's late mark knows the days before the range; the page does not (#117 review 2, finding 10).
  it("drops a late reading at the range's start: it lies above its week's top", () => {
    const rs = [
      reading(iso(18, 10, 45), 39, { resets_at: "2026-11-25T09:00:00Z", runtime_id: "rt-lagging" }),
      reading(iso(18, 11), 2, { resets_at: "2026-12-02T09:00:00Z" }),
    ];
    // the segment began at the baseline, before the range
    const c = cov([seg(iso(18, 10, 35), iso(18, 11), { to_percent: 2 })]);
    expect(meterPoints(rs, c, [iso(18, 10), iso(18, 11)], "hour").map((p) => p.pct)).toEqual([2]);
  });

  it("drops late readings at the range's start that lie within the week's range, by the server's count", () => {
    const old = "2026-11-25T09:00:00Z", now = "2026-12-02T09:00:00Z";
    const rs = [
      reading(iso(18, 10, 45), 61, { resets_at: old }), reading(iso(18, 10, 47), 64, { resets_at: null }),
      reading(iso(18, 10, 50), 64, { resets_at: old, source: "manual" }), reading(iso(18, 11), 67, { resets_at: now, source: "inband" }),
      reading(iso(18, 11, 5), 67, { resets_at: null }),
    ];
    const c = { ...cov([seg(iso(18, 10, 35), iso(18, 11, 5), { to_percent: 67 })]), readings: 3, baseline_before_window: true, baseline_at: iso(18, 10, 35) };
    // the two of the old week go; the one without a reset time, which the server never calls late, stays
    expect(meterPoints(rs, c, [iso(18, 10), iso(18, 11)], "hour").map((p) => p.at)).toEqual([iso(18, 10, 47), iso(18, 11), iso(18, 11, 5)]);
  });

  it("drops nothing the server counted when the readings are a fetch ahead of or behind the coverage", () => {
    const resets = { resets_at: "2026-12-02T09:00:00Z" };
    const base = reading(iso(18, 10), 1, resets), a = reading(iso(18, 10, 20), 2, resets), b = reading(iso(18, 10, 40), 3, resets);
    const newer = reading(iso(18, 10, 55), 4, resets);
    const c = { ...cov([seg(iso(18, 10), iso(18, 10, 40), { to_percent: 3 })]), readings: 2, baseline_before_window: true, baseline_at: iso(18, 10) };
    const of = (rs: UsageReading[]) => meterPoints(rs, c, [iso(18, 10)], "hour").map((p) => p.pct);
    // the baseline still in the readings' window is not in the count; a newer reading is in no segment yet
    expect(of([base, a, b, newer])).toEqual([1, 2, 3]);
    expect(of([a, b])).toEqual([2, 3]);
    expect(of([a])).toEqual([2]);
  });

  it("drops no counted reading of the old week when a backdated one of the new week is a fetch ahead of the coverage", () => {
    const old = { resets_at: iso(18, 16) }, now = { resets_at: iso(25, 16) };
    const rs = [reading(iso(18, 15, 50), 77, old), reading(iso(18, 16, 5), 4, now), reading(iso(18, 16, 20), 5, now), reading(iso(18, 16, 30), 6, now)];
    const segs = [seg(iso(18, 15, 40), iso(18, 15, 50), { to_percent: 77 }), seg(iso(18, 16, 5), iso(18, 16, 30), { reset: true, to_percent: 6 })];
    // the coverage has not counted 16:20 yet
    const c = { ...cov(segs), readings: 3, baseline_before_window: true, baseline_at: iso(18, 15, 40) };
    const ps = meterPoints(rs, c, [iso(18, 15), iso(18, 16)], "hour");
    expect(ps.map((p) => [p.pct, p.reset])).toEqual([[77, false], [4, true], [5, false], [6, false]]);
  });

  // Its week may be over, or be one the provider ended early: the count alone knows it is late.
  it.each([iso(18, 9), iso(25, 9)])("still drops, by the count, a late reading at the range's start that resets at %s", (resets_at) => {
    const rs = [reading(iso(18, 10, 45), 1, { resets_at }), reading(iso(18, 11), 2)];
    const c = { ...cov([seg(iso(18, 10, 35), iso(18, 11), { to_percent: 2 })]), readings: 1, baseline_before_window: true, baseline_at: iso(18, 10, 35) };
    expect(meterPoints(rs, c, [iso(18, 10), iso(18, 11)], "hour").map((p) => p.pct)).toEqual([2]);
  });

  it("drops no counted reading for a backdated one ahead where the reset times differ by seconds", () => {
    const at = (s: number) => ({ resets_at: `2026-11-25T09:00:0${s}Z` });
    const rs = [reading(iso(18, 10, 5), 5, at(0)), reading(iso(18, 11, 5), 6, at(0)), reading(iso(18, 11, 30), 7, { ...at(1), source: "manual" }), reading(iso(18, 12, 5), 8, at(1))];
    const c = { ...cov([seg(iso(17, 8), iso(18, 12, 5), { to_percent: 8 })]), readings: 3, baseline_before_window: true, baseline_at: iso(17, 8) };
    expect(meterPoints(rs, c, [iso(18, 10), iso(18, 11), iso(18, 12)], "hour").map((p) => p.pct)).toEqual([5, 6, 7, 8]);
  });

  it("takes readings out only in a period that has more than its step counted", () => {
    const hours = [iso(18, 10), iso(18, 11), iso(18, 12)];
    const stepsOf = (ns: number[]) => cellsOf(hours, null, SEAT, "subscription", hours.map((period) => ({ period, points: 0 }))).subscription!
      .map((s, i) => ({ ...s, readings: ns[i] })).filter((s) => s.readings > 0);
    // no surely counted reading has a reset time: the one ahead at 11:30 can cost no other hour a reading
    const rs = [reading(iso(18, 10, 5), 5, { resets_at: "2026-11-25T09:00:00Z" }), reading(iso(18, 11, 30), 7, { source: "manual" }), reading(iso(18, 12, 5), 8)];
    const c = { ...cov([seg(iso(17, 8), iso(18, 12, 5), { to_percent: 8 })]), readings: 2, baseline_before_window: true, baseline_at: iso(17, 8) };
    expect(meterPoints(rs, c, hours, "hour", stepsOf([1, 0, 1])).map((p) => p.pct)).toEqual([5, 7, 8]);
    // in step, a late reading still goes: its hour has one more than the step counted
    expect(meterPoints(rs, c, hours, "hour", stepsOf([0, 1, 1])).map((p) => p.pct)).toEqual([7, 8]);
  });

  it("gives the chart's line the meter's own steps", () => {
    const hours = [iso(18, 10), iso(18, 11), iso(18, 12)];
    const data = cellsOf(hours, [seg(iso(17, 8), iso(18, 12, 5), { to_percent: 8 })], SEAT, "subscription", [0, 2].map((i) => ({ period: hours[i], points: 0 })));
    Object.assign(data.meter![0], { readings: 2, baseline_before_window: true, baseline_at: iso(17, 8) });
    for (const s of data.subscription!) s.readings = 1;
    const rs = [reading(iso(18, 10, 5), 5, { resets_at: "2026-11-25T09:00:00Z" }), reading(iso(18, 11, 30), 7, { source: "manual" }), reading(iso(18, 12, 5), 8)];
    const { container } = chart(data, rs, "hour");
    expect(parts(container).flatMap(dots).map((d) => d.getAttribute("data-pct"))).toEqual(["5", "7", "8"]);
  });

  it.each([1, 2])("falls at the reset where a new week of %i readings begins in the old one's last millisecond", (count) => {
    const at = iso(18, 13, 20), now = { resets_at: iso(25, 13) };
    const fresh = [reading(at, 3, now), reading(iso(18, 13, 40), 5, now)].slice(0, count);
    const rs = [reading(iso(18, 12), 70), reading(at, 77), ...fresh];
    const segs = [seg(iso(18, 12), at, { to_percent: 77 }), seg(at, fresh.at(-1)!.fetched_at, { reset: true, to_percent: 5 })];
    const ps = meterPoints(rs, cov(segs), [iso(18, 0)], "day");
    expect(ps.filter((p) => p.reset || p.newWeek).map((p) => [p.at, p.pct, p.reset, p.newWeek])).toEqual([[at, 3, true, true]]);
  });

  it("marks one new week where two readers report the reset at the same instant, in the API's order", () => {
    const at = iso(18, 10, 30);
    const rs = [reading(iso(18, 10), 77), reading(at, 3, { source: "manual" }), reading(at, 4, { source: "inband" })];
    const ps = meterPoints(rs, cov([seg(iso(18, 10), iso(18, 10)), seg(at, at, { reset: true })]), [iso(18, 10)], "hour");
    expect(ps.map((p) => [p.pct, p.newWeek, p.reset])).toEqual([[77, false, false], [3, true, true], [4, false, false]]);
  });

  it.each(["with", "without"])("falls on the new week's reading where two of the old week share its instant (%s reset times)", (kind) => {
    const at = iso(18, 9), week = (day: number) => ({ resets_at: kind === "with" ? iso(day, 12) : null });
    const rs = [reading(iso(18, 8), 1, week(20)), reading(at, 3, { ...week(20), source: "inband" }), reading(at, 2, { ...week(20), source: "manual" }),
      reading(at, 0, week(27)), reading(iso(18, 9, 30), 4, week(27))];
    const segs = [seg(iso(18, 8), at, { to_percent: 3 }), seg(at, iso(18, 9, 30), { reset: true, to_percent: 4 })];
    const ps = meterPoints(rs, { ...cov(segs), readings: 5 }, [iso(18, 8), iso(18, 9)], "hour");
    expect(ps.map((p) => [p.pct, p.newWeek])).toEqual([[1, false], [3, false], [2, false], [0, true], [4, false]]);
  });

  it("marks both of two resets in one instant", () => {
    const at = iso(18, 9), week = (day: number) => ({ resets_at: iso(day, 12) });
    const rs = [reading(iso(18, 8), 60, week(20)), reading(at, 70, week(20)), reading(at, 2, week(27)), reading(at, 1, week(29))];
    const segs = [seg(iso(18, 8), at, { to_percent: 70 }), seg(at, at, { reset: true, to_percent: 2 }), seg(at, at, { reset: true, to_percent: 1 })];
    const ps = meterPoints(rs, { ...cov(segs), readings: 4 }, [iso(18, 8), iso(18, 9)], "hour");
    expect(ps.map((p) => [p.pct, p.newWeek])).toEqual([[60, false], [70, false], [2, true], [1, true]]);
  });

  it("keeps both readings of one instant that ends one week and begins the next", () => {
    const at = iso(18, 10, 30);
    const rs = [reading(iso(18, 10), 1), reading(at, 2), reading(at, 5, { source: "manual" })];
    const segs = [seg(iso(18, 10), at, { to_percent: 2 }), seg(at, at, { reset: true, to_percent: 5 })];
    expect(meterPoints(rs, cov(segs), [iso(18, 10)], "hour").map((p) => [p.pct, p.newWeek])).toEqual([[1, false], [2, false], [5, true]]);
  });

  it("falls into a new week also where the readings lack that week's exact first", () => {
    const rs = [reading(iso(18, 10), 77), reading(iso(18, 10, 40), 2)];
    const ps = meterPoints(rs, cov([seg(iso(18, 10), iso(18, 10)), seg(iso(18, 10, 30), iso(18, 10, 40), { reset: true })]), [iso(18, 10)], "hour");
    expect(ps.map((p) => [p.newWeek, p.reset])).toEqual([[false, false], [true, true]]);
  });

  it("marks the first drawn reading as a new week when the range starts at a reset, from the segment's Reset flag", () => {
    // the first segment of the window began with a reset (Reset = true) and starts at this reading: nothing drawn before it
    const rs = [reading(iso(18, 10, 5), 1), reading(iso(18, 10, 20), 2), reading(iso(18, 11, 5), 3)];
    const segs = [seg(iso(18, 10, 5), iso(18, 11, 5), { reset: true })];
    const ps = meterPoints(rs, cov(segs), [iso(18, 10), iso(18, 11)], "hour");
    expect(ps.map((p) => [p.pct, p.newWeek, p.reset])).toEqual([[1, true, false], [2, false, false], [3, false, false]]);
    // a first segment that is no reset (the baseline's week), or a range that starts inside a week, marks nothing
    expect(meterPoints(rs, cov([seg(iso(18, 10, 5), iso(18, 11, 5))]), [iso(18, 10), iso(18, 11)], "hour").some((p) => p.newWeek)).toBe(false);
    expect(meterPoints(rs.slice(1), cov(segs), [iso(18, 10), iso(18, 11)], "hour").some((p) => p.newWeek)).toBe(false);
  });

  it("counts the meter card's dropped readings per window kind, as the server partitions them", () => {
    // seven_day: week 2 (resets 12/02) then a late reading of week 1; "" (older daemon): only week 1's reset time, all fine
    // on its own. Run together, the "" readings after 12/02 would be late; apart they are not.
    const rs = [
      reading(iso(18, 9), 38, { resets_at: "2026-11-25T09:00:00Z" }),
      reading(iso(18, 9, 10), 1, { resets_at: "2026-12-02T09:00:00Z", source: "inband" }),
      reading(iso(18, 9, 20), 39, { resets_at: "2026-11-25T09:00:00Z", window_kind: "" }),
      reading(iso(18, 9, 30), 2, { resets_at: "2026-12-02T09:00:00Z", source: "manual" }),
      reading(iso(18, 9, 40), 40, { resets_at: "2026-11-25T09:00:00Z", window_kind: "", source: "manual" }),
      reading(iso(18, 9, 45), 39, { resets_at: "2026-11-25T09:00:00Z", source: "inband" }),
      reading(iso(18, 9, 50), 41, { stale: true, source: "manual" }), reading(iso(18, 9, 55), 5, { window_kind: "five_hour" }),
    ];
    expect(droppedReadings(rs, SEAT, 0, "merged")).toBe(2);
    // a meter read per source counts its own only: no late one among usage_cmd's
    expect(droppedReadings(rs, SEAT, 0, "usage_cmd")).toBe(0);
  });

  it("marks a new week after a gap as such without drawing a fall", () => {
    const segs = [seg(iso(18, 1, 50), iso(18, 1, 50)), seg(iso(18, 9, 10), iso(18, 9, 10), { reset: true })];
    const ps = meterPoints([reading(iso(18, 1, 50), 39), reading(iso(18, 9, 10), 1)], cov(segs), [iso(18, 1), iso(18, 9)], "hour");
    expect(ps.map((p) => [p.reset, p.newWeek])).toEqual([[false, false], [false, true]]);
  });
});

// Design review round 2 of #170.
describe("weekly meter: names, the $ axis and the tooltip in the scrolled view", () => {
  beforeEach(() => { void i18n.changeLanguage("en"); });
  const bar = (v: number): Bar[] => [{ key: "x", label: "x", color: "red", segs: [{ key: "x", label: "x", color: "red", value: v }] }];
  const periods = Array.from({ length: 10 }, (_, i) => iso(18, i));
  const draw = (asTable = false) => renderWithProviders(
    <BarChart title="t" periods={periods} asTable={asTable} groups={periods.map(() => bar(1))} fmtPeriod={(p) => p} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis}
      meter={{ points: [{ at: "a", pct: 10, slot: 0, frac: 0.5, part: 0, reset: false, newWeek: false }, { at: "b", pct: 20, slot: 9, frac: 0.5, part: 0, reset: false, newWeek: false }],
        name: "m", fmtAt: (x) => x }} />,
  );

  it("names the meter without a unit in the details, the label and the table; the unit stays on the axis", () => {
    const { container } = draw();
    const period = container.querySelector<SVGGElement>("g[data-period]")!;
    expect(period.getAttribute("aria-label")).toContain(" · m: a 10 %");
    fireEvent.focusIn(period);
    expect(screen.getByRole("tooltip").querySelector("[data-meter-readings]")!.textContent).toBe("m: a 10 %");
    const table = draw(true).container;
    expect(table.querySelector("[data-meter-col]")!.textContent).toBe("m");
  });

  it("draws the $ tick labels in a sticky strip of their own, first in the scroller's row, and not in the plot", () => {
    const { container } = draw();
    const strip = container.querySelector<SVGSVGElement>("svg[data-cost-axis]")!;
    expect(strip.style.position).toBe("sticky");
    expect(strip.style.left).toBe("0px");
    expect(strip.style.background).toBe("var(--surface-2)");
    expect(strip.getAttribute("width")).toBe("60");
    expect(strip.parentElement!.firstElementChild).toBe(strip);
    const labels = [...strip.querySelectorAll("[data-cost-tick]")].map((t) => t.textContent);
    expect(labels.length).toBeGreaterThan(1);
    expect(labels[0]).toBe(fmtUSDAxis(0, 1));
    // the plot keeps its gridlines and has no tick label of its own
    const plot = container.querySelector<SVGSVGElement>("svg[role='group']")!;
    expect(plot.querySelectorAll("[data-cost-tick]")).toHaveLength(0);
    expect([...plot.querySelectorAll(":scope > g > text")]).toEqual([]);
    expect(plot.querySelectorAll("line").length).toBeGreaterThanOrEqual(labels.length);
  });

  /** The scroller of a chart: a window `view` wide, scrolled `left`. jsdom measures nothing. */
  const scrolled = (container: HTMLElement, view: number, left: number) => {
    const sc = scrollerOf(container);
    Object.defineProperty(sc, "clientWidth", { configurable: true, value: view });
    Object.defineProperty(sc, "scrollLeft", { configurable: true, writable: true, value: left });
    return sc;
  };
  const tipOf = (container: HTMLElement, i: number) => {
    fireEvent.focusIn(container.querySelectorAll<SVGGElement>("g[data-period]")[i]);
    const tip = screen.getByRole("tooltip");
    return { left: parseFloat(tip.style.left), width: parseFloat(tip.style.width) };
  };

  it("puts the tooltip beside the scroll box, not in it, where it would be cut off below the plot", () => {
    const { container } = draw();
    fireEvent.focusIn(container.querySelector<SVGGElement>("g[data-period]")!);
    const sc = scrollerOf(container), tip = screen.getByRole("tooltip");
    expect(sc.style.overflowX).toBe("auto");
    expect(sc.contains(tip)).toBe(false);
    expect(tip.parentElement).toBe(sc.parentElement);
  });

  it("scrolls a period reached with Tab clear of the two strips, and not one that is pressed", () => {
    const { container } = draw();
    const sc = scrolled(container, 390, 300);
    const period = (i: number) => container.querySelectorAll<SVGGElement>("g[data-period]")[i];
    const slot = Number(period(0).querySelector("rect")!.getAttribute("width"));
    // under the $ strip: its left edge comes to the strip's end
    fireEvent.keyUp(period(1), { key: "Tab" });
    expect(sc.scrollLeft).toBe(slot);
    // under the meter's axis, then in view already
    fireEvent.keyUp(period(8), { key: "Tab", shiftKey: true });
    expect(sc.scrollLeft).toBe(64 + 9 * slot - (390 - 44));
    const at = sc.scrollLeft;
    fireEvent.keyUp(period(8), { key: "Tab" });
    expect(sc.scrollLeft).toBe(at);
    fireEvent.mouseDown(period(1));
    fireEvent.focusIn(period(1));
    expect(sc.scrollLeft).toBe(at);
  });

  it("keeps the tooltip inside the visible window, less the meter's axis, on the newest and the oldest period", () => {
    // 390 px phone, 44 px meter axis: 346 px visible. The chart is 980 px wide.
    const { container } = draw();
    scrolled(container, 390, 634);
    let t = tipOf(container, 9);
    expect(t.left).toBeGreaterThanOrEqual(4);
    expect(t.left + t.width).toBeLessThanOrEqual(346 - 4);
    expect(t.width).toBe(250);
    fireEvent.blur(container.querySelectorAll<SVGGElement>("g[data-period]")[9]);
    scrolled(container, 390, 0);
    t = tipOf(container, 0);
    expect(t.left).toBeGreaterThanOrEqual(4);
    expect(t.left + t.width).toBeLessThanOrEqual(346 - 4);
  });

  it("narrows the tooltip to a window narrower than it", () => {
    const { container } = draw();
    scrolled(container, 200, 300);
    const t = tipOf(container, 3);
    expect(t.width).toBe(156 - 8);
    expect(t.left).toBeGreaterThanOrEqual(4);
    expect(t.left + t.width).toBeLessThanOrEqual(156 - 4);
  });

  it("re-places an open tooltip on a scroll", () => {
    const { container } = draw();
    const sc = scrolled(container, 900, 0);
    const before = tipOf(container, 4).left;
    sc.scrollLeft = 100;
    fireEvent.scroll(sc);
    expect(parseFloat(screen.getByRole("tooltip").style.left)).toBe(before - 100);
  });

  it("re-places an open tooltip when the window is resized, and stops watching when it closes", () => {
    const seen: { cb: () => void; disconnected: boolean }[] = [];
    vi.stubGlobal("ResizeObserver", class {
      rec: { cb: () => void; disconnected: boolean };
      constructor(cb: () => void) { this.rec = { cb, disconnected: false }; seen.push(this.rec); }
      observe() {}
      unobserve() {}
      disconnect() { this.rec.disconnected = true; }
    });
    try {
      const { container } = draw();
      const sc = scrolled(container, 390, 0);
      const wide = tipOf(container, 1);
      // an orientation change: the window gets narrower
      Object.defineProperty(sc, "clientWidth", { configurable: true, value: 280 });
      act(() => seen.filter((r) => !r.disconnected).forEach((r) => r.cb()));
      const narrow = screen.getByRole("tooltip");
      expect(parseFloat(narrow.style.left) + parseFloat(narrow.style.width)).toBeLessThanOrEqual(280 - 44 - 4);
      expect(parseFloat(narrow.style.width)).toBeLessThan(wide.width);
      fireEvent.blur(container.querySelectorAll<SVGGElement>("g[data-period]")[1]);
      // the one left is the scroll's, which follows the chart's width
      expect(seen.filter((r) => !r.disconnected)).toHaveLength(1);
    } finally {
      vi.unstubAllGlobals();
    }
  });
});

describe("weekly meter: the scroll to the newest period", () => {
  beforeEach(() => { void i18n.changeLanguage("en"); });
  const bar = (v: number): Bar[] => [{ key: "x", label: "x", color: "red", segs: [{ key: "x", label: "x", color: "red", value: v }] }];
  const periodsOf = (hours: number[]) => hours.map((h) => new Date(Date.UTC(2026, 10, 18) + h * 3600_000).toISOString());
  const pt = (slot: number) => ({ at: "a", pct: 10, slot, frac: 0.5, part: 0, reset: false, newWeek: false });
  /** The hours of the periods are the test's to change: the chart gets what the caller says, like a refetch. */
  function Harness({ first, scrollKey = "1:hour" }: { first: number[]; scrollKey?: string }) {
    const [hours, setHours] = useState(first);
    const [key, setKey] = useState(scrollKey);
    const ps = periodsOf(hours);
    return (
      <>
        <button onClick={() => setHours([...hours, hours.at(-1)! + 1])}>more</button>
        <button onClick={() => setHours(hours.slice(1))}>drop first</button>
        <button onClick={() => { setHours(Array.from({ length: first.length + 30 }, (_, i) => i)); setKey("7:hour"); }}>range</button>
        <button onClick={() => setKey("1:10min")}>bucket</button>
        <BarChart title="t" periods={ps} groups={ps.map(() => bar(1))} fmtPeriod={(p) => p} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis} scrollKey={key}
          meter={{ points: [pt(0)], name: "n", fmtAt: (x) => x }} />
      </>
    );
  }
  /** A scroller 400 px wide whose content grows 120 px with every period; jsdom measures nothing. */
  function measured() {
    const w = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollWidth");
    const c = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientWidth");
    Object.defineProperty(HTMLElement.prototype, "scrollWidth", { configurable: true, get() { return this.querySelectorAll("g[data-period]").length * 120 + 64; } });
    Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get() { return 400; } });
    return () => {
      if (w) Object.defineProperty(HTMLElement.prototype, "scrollWidth", w); else delete (HTMLElement.prototype as unknown as Record<string, unknown>).scrollWidth;
      if (c) Object.defineProperty(HTMLElement.prototype, "clientWidth", c); else delete (HTMLElement.prototype as unknown as Record<string, unknown>).clientWidth;
    };
  }
  const dense = (n: number) => Array.from({ length: n }, (_, i) => i);

  it("follows a new period when the view is at the end, and stays where a user who scrolled back is", () => {
    const restore = measured();
    try {
      const { container } = renderWithProviders(<Harness first={dense(10)} />);
      const sc = scrollerOf(container);
      expect(sc.scrollLeft).toBe(10 * 120 + 64);
      // at the end: a new period scrolls on
      fireEvent.click(screen.getByRole("button", { name: "more" }));
      expect(sc.scrollLeft).toBe(11 * 120 + 64);
      // scrolled back to an older period: the view stays
      sc.scrollLeft = 200;
      fireEvent.click(screen.getByRole("button", { name: "more" }));
      expect(sc.scrollLeft).toBe(200);
      // a change of range (the key, with many periods) goes to the end again
      fireEvent.click(screen.getByRole("button", { name: "range" }));
      expect(sc.scrollLeft).toBe(40 * 120 + 64);
    } finally {
      restore();
    }
  });

  it("goes to the end when the caller's key changes, even with as many periods as before", () => {
    const restore = measured();
    try {
      const { container } = renderWithProviders(<Harness first={dense(10)} />);
      const sc = scrollerOf(container);
      sc.scrollLeft = 200;
      // the same ten periods, another bucket: only the key says so
      fireEvent.click(screen.getByRole("button", { name: "bucket" }));
      expect(sc.scrollLeft).toBe(10 * 120 + 64);
    } finally {
      restore();
    }
  });

  it("keeps a scrolled-back view on a refetch of sparse periods, which a gap at the start or the end does not change", () => {
    const restore = measured();
    try {
      // hours 0, 1 and 8 to 20: the distance of the first two periods is not the bucket's width
      const { container } = renderWithProviders(<Harness first={[0, 1, ...Array.from({ length: 13 }, (_, i) => 8 + i)]} />);
      const sc = scrollerOf(container);
      sc.scrollLeft = 200;
      fireEvent.click(screen.getByRole("button", { name: "drop first" }));
      expect(sc.scrollLeft).toBe(200);
      fireEvent.click(screen.getByRole("button", { name: "more" }));
      expect(sc.scrollLeft).toBe(200);
    } finally {
      restore();
    }
  });
});

describe("weekly meter: the plot's width and the table", () => {
  const hours = Array.from({ length: 30 }, (_, i) => new Date(Date.UTC(2026, 10, 18) + i * 3600_000).toISOString());
  function Harness({ table = false }: { table?: boolean }) {
    const [asTable, setAsTable] = useState(table);
    const [n, setN] = useState(1);
    const [wide, setWide] = useState(false);
    const bars = Array.from({ length: n }, (_, k): Bar => ({ key: `b${k}`, label: `b${k}`, color: "red", segs: [{ key: "x", label: "x", color: "red", value: 1 }] }));
    return (
      <>
        <button onClick={() => setAsTable((v) => !v)}>table</button>
        <button onClick={() => setN(3)}>bars</button>
        <button onClick={() => setWide(true)}>wide</button>
        <BarChart title="t" periods={hours} groups={hours.map(() => bars)} fmtPeriod={(p) => p} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis}
          asTable={asTable} wide={wide}
          meter={{ points: [{ at: "a", pct: 10, slot: 0, frac: 0.5, part: 0, reset: false, newWeek: false }], name: "n", fmtAt: (x) => x }} />
      </>
    );
  }
  // jsdom measures nothing: the scroller is as wide as the plot in it, its window 400 px
  beforeEach(() => {
    vi.spyOn(HTMLElement.prototype, "scrollWidth", "get").mockImplementation(function (this: HTMLElement) {
      return Number(this.querySelector("svg[role=group]")?.getAttribute("width") ?? 0);
    });
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(400);
  });
  afterEach(() => { vi.restoreAllMocks(); });
  const end = (c: HTMLElement) => Number(c.querySelector("svg[role=group]")!.getAttribute("width"));

  it("opens at the newest period when the table is switched back to the chart", () => {
    const { container } = renderWithProviders(<Harness table />);
    expect(container.querySelector("svg[role=group]")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "table" }));
    expect(scrollerOf(container).scrollLeft).toBe(end(container));
  });

  it.each(["bars", "wide"])("stays at the end when the plot gets wider (%s)", (what) => {
    const { container } = renderWithProviders(<Harness />);
    const before = end(container);
    fireEvent.click(screen.getByRole("button", { name: what }));
    expect(end(container)).toBeGreaterThan(before);
    expect(scrollerOf(container).scrollLeft).toBe(end(container));
  });

  it("leaves a scrolled-back view where it is when the plot gets wider", () => {
    const { container } = renderWithProviders(<Harness />);
    scrollerOf(container).scrollLeft = 200;
    fireEvent.click(screen.getByRole("button", { name: "bars" }));
    expect(scrollerOf(container).scrollLeft).toBe(200);
  });
});

describe("weekly meter: a period's details", () => {
  beforeEach(() => { void i18n.changeLanguage("en"); setDisplayTz("utc"); });
  afterEach(() => { setDisplayTz("utc"); });

  it("never leaves out a reset: the first, the old week's last, the new one's first and the last, in all three surfaces", () => {
    const rs = run(50, 20);
    const want = "Weekly meter: 01:00 UTC 40 % · … · 01:19 UTC 41 % · 01:20 UTC 0 % (new week) · … · 01:49 UTC 29 %";
    const drawn = chart(cellsOf([iso(18, 0)], weeksOf(rs, 20)), rs, "day");
    const period = drawn.container.querySelector<SVGGElement>("g[data-period]")!;
    expect(period.getAttribute("aria-label")).toContain(want);
    fireEvent.focusIn(period);
    expect(screen.getByRole("tooltip").querySelector("[data-meter-readings]")!.textContent).toBe(want);
    drawn.unmount();
    const table = chart(cellsOf([iso(18, 0)], weeksOf(rs, 20)), rs, "day", { route: "/?table=1" });
    expect(`Weekly meter: ${table.container.querySelector("[data-meter-cell]")!.textContent}`).toBe(want);
  });

  it("lists all of three readings, and marks a reset at a period's first reading once", () => {
    const three = run(3);
    const a = chart(cellsOf([iso(18, 0)], [seg(three[0].fetched_at, three[2].fetched_at)]), three, "day");
    expect(a.container.querySelector("g[data-period]")!.getAttribute("aria-label")).toMatch(/01:00 UTC 40 % · 01:01 UTC 40 % · 01:02 UTC 40 %$/);
    a.unmount();
    const many = run(20);
    const b = chart(cellsOf([iso(18, 0)], [seg(many[0].fetched_at, many[19].fetched_at, { reset: true })]), many, "day");
    expect(b.container.querySelector("g[data-period]")!.getAttribute("aria-label")).toMatch(/01:00 UTC 40 % \(new week\) · … · 01:19 UTC 41 %$/);
  });

  it("lets a reading that alone is wider than the tooltip wrap inside it", () => {
    const { container } = renderWithProviders(
      <BarChart title="t" periods={[iso(18, 0)]} groups={[[]]} fmtPeriod={(p) => p} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis}
        meter={{ points: [{ at: "a", pct: 1, slot: 0, frac: 0.5, part: 0, reset: false, newWeek: true }], name: "m", fmtAt: () => "10/28, 18:52 GMT+1" }} />,
    );
    fireEvent.focusIn(container.querySelector("g[data-period]")!);
    const item = screen.getByRole("tooltip").querySelector<HTMLElement>("[data-meter-readings] span")!;
    expect(item.style.whiteSpace).not.toBe("nowrap");
    // a box of its own: a short one still moves to the next line as a whole
    expect(item.style.display).toBe("inline-block");
    expect(item.style.maxWidth).toBe("100%");
  });

  it("says a dash in the table for a period without a reading", () => {
    const { container } = chart(cellsOf([iso(18, 0), iso(19, 0)], [seg(iso(18, 5), iso(18, 5))]), [reading(iso(18, 5), 9)], "day", { route: "/?table=1" });
    expect([...container.querySelectorAll("[data-meter-cell]")].map((c) => c.textContent)).toEqual(["05:00 UTC 9 %", "–"]);
  });

  // The tests run in Asia/Tokyo (UTC+9, vite.config.ts).
  it.each([
    ["hour", iso(18, 18), "18:05 UTC", "03:05 GMT+9"],
    ["day", iso(18, 0), "18:05 UTC", "11/19, 03:05 GMT+9"],
    ["week", iso(16, 0), "11/18, 18:05 UTC", "11/19, 03:05 GMT+9"],
    ["month", iso(1, 0), "11/18, 18:05 UTC", "11/19, 03:05 GMT+9"],
  ])("reads a reading's time on the page's clock (%s)", (bucket, period, utc, local) => {
    const rs = [reading(iso(18, 18, 5), 9)];
    for (const [tz, want] of [["utc", utc], ["local", local]] as const) {
      setDisplayTz(tz);
      const r = chart(cellsOf([period], [seg(rs[0].fetched_at, rs[0].fetched_at)]), rs, bucket);
      expect(r.container.querySelector("g[data-period]")!.getAttribute("aria-label")).toContain(`Weekly meter: ${want} 9 %`);
      r.unmount();
    }
  });

  it("keeps a fractional percentage's digits, at most two", () => {
    const rs = [reading(iso(18, 18, 5), 9.5), reading(iso(18, 18, 25), 10.1234), reading(iso(18, 18, 45), 11)];
    const { container } = chart(cellsOf([iso(18, 18)], [seg(rs[0].fetched_at, rs[2].fetched_at)]), rs, "hour");
    expect(container.querySelector("g[data-period]")!.getAttribute("aria-label")).toMatch(/18:05 UTC 9\.5 % · 18:25 UTC 10\.12 % · 18:45 UTC 11 %$/);
  });

  it("names the day of a reading past the reader's midnight in an hour", () => {
    const zone = process.env.TZ;
    process.env.TZ = "Asia/Kolkata";
    try {
      setDisplayTz("local");
      // 18:00 UTC is 23:30 there
      const rs = [reading(iso(18, 18, 10), 5), reading(iso(18, 18, 45), 6)];
      const { container } = chart(cellsOf([iso(18, 18)], [seg(rs[0].fetched_at, rs[1].fetched_at)]), rs, "hour");
      expect(container.querySelector("g[data-period]")!.getAttribute("aria-label")).toMatch(/Weekly meter: 23:40 GMT\+5:30 5 % · 11\/19, 00:15 GMT\+5:30 6 %$/);
    } finally {
      process.env.TZ = zone;
    }
  });

  it("writes the times once, not again on every hover", () => {
    const periods = Array.from({ length: 5 }, (_, h) => iso(18, h));
    const points = Array.from({ length: 500 }, (_, k) => ({ at: `p${k}`, pct: 10, slot: Math.floor(k / 100), frac: (k % 100) / 100, part: 0, reset: false, newWeek: false }));
    const meter = { points, name: "n", fmtAt: vi.fn((x: string) => x) };
    const { container } = renderWithProviders(
      <BarChart title="t" periods={periods} groups={periods.map(() => [])} fmtPeriod={(p) => p} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis} meter={meter} />,
    );
    expect(meter.fmtAt).toHaveBeenCalledTimes(10);
    fireEvent.focusIn(container.querySelectorAll<SVGGElement>("g[data-period]")[1]);
    fireEvent.focusIn(container.querySelectorAll<SVGGElement>("g[data-period]")[2]);
    expect(meter.fmtAt).toHaveBeenCalledTimes(10);
  });

  it("draws two readers' readings of one instant as two dots and one fall", () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    const at = iso(18, 18, 30);
    const rs = [reading(iso(18, 18, 5), 77), reading(at, 3, { source: "manual" }), reading(at, 4, { source: "inband" })];
    const { container } = chart(cellsOf([iso(18, 18)], [seg(rs[0].fetched_at, rs[0].fetched_at), seg(at, at, { reset: true })]), rs, "hour");
    expect(container.querySelectorAll(`circle[data-at="${at}"]`)).toHaveLength(2);
    expect(container.querySelectorAll("[data-meter-drop]")).toHaveLength(1);
    expect(container.querySelector("g[data-period]")!.getAttribute("aria-label")!.match(/new week/g)).toHaveLength(1);
    expect(errors.mock.calls.filter((c) => String(c[0]).includes("same key"))).toEqual([]);
  });
});

describe("a chart without the meter", () => {
  it("keeps its $ labels in the plot, has no strip over it, and draws its first period's note", () => {
    const { container } = renderWithProviders(
      <BarChart title="t" periods={[iso(18, 0), iso(18, 1)]} groups={[[], []]} fmtPeriod={(p) => p} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis}
        notes={new Map([[0, "incomplete"]])} notesLabel="Note" />,
    );
    expect(container.querySelector("[data-cost-axis], [data-meter-axis]")).toBeNull();
    expect(container.querySelectorAll("svg[role=group] > g[aria-hidden] > text").length).toBeGreaterThan(1);
    expect(container.querySelector("text[data-note]")!.textContent).toBe("incomplete");
    const sc = scrollerOf(container);
    Object.defineProperty(sc, "clientWidth", { configurable: true, value: 100 });
    fireEvent.keyUp(container.querySelectorAll("g[data-period]")[1], { key: "Tab" });
    expect(sc.scrollLeft).toBe(0);
  });
});

// The page: a merged meter, as the server has sent since #214.
describe("weekly meter: on the Costs page", () => {
  beforeEach(() => { void i18n.changeLanguage("en"); });
  afterEach(() => { vi.restoreAllMocks(); setDisplayTz("utc"); });
  const rs = [
    reading(iso(18, 1, 5), 9, { source: "inband" }), reading(iso(18, 2, 20), 10, { source: "manual" }),
    reading(iso(18, 3, 35), 11, { source: "usage_cmd" }),
  ];
  function serve() {
    const hours = [1, 2, 3].map((h) => iso(18, h));
    const data = cellsOf(hours, [seg(rs[0].fetched_at, rs[2].fetched_at, { from_percent: 9, to_percent: 11, points: 2 })], SEAT, "subscription",
      hours.map((period) => ({ period, points: 0 })));
    data.meter![0].readings = 3;
    mockFetch({
      "/api/v1/cost/cells": { ...data, since: iso(18, 0), until: iso(18, 4) },
      "/api/v1/cost/billed": { bucket: "hour", points: [], sync: { configured: false, last_attempt_at: null, last_success_at: null,
        last_daily_at: null, data_refreshed_at: null, last_status: "", last_error: "" } },
      "/api/v1/cost/runs": [],
      "/api/v1/cost/models": { rows: [], runs_without_breakdown: 0, usd_without_breakdown: 0 },
      "/api/v1/cost/usage-readings": rs,
    });
  }
  // the address takes minute-exact times, without milliseconds
  const range = (from: string) => `?from=${from.replace(".000Z", "Z")}&to=${iso(18, 4).replace(".000Z", "Z")}&bucket=hour&tz=utc`;
  const line = () => waitFor(() => {
    const l = document.querySelector<HTMLElement>(`[data-credential="${SEAT}#0"] [data-meter-line]`);
    if (!l) throw new Error("no line yet");
    return l;
  });

  it("draws the line of a merged meter from the readings of every source", async () => {
    serve();
    renderWithProviders(<Costs />, { route: `/${range(iso(18, 0))}` });
    const l = await line();
    expect([...l.querySelectorAll("circle")].map((d) => d.getAttribute("data-pct"))).toEqual(["9", "10", "11"]);
  });

  it("goes to the end on another custom range of the same bucket and stays put on a refetch or a change of clock", async () => {
    serve();
    vi.spyOn(HTMLElement.prototype, "scrollWidth", "get").mockReturnValue(900);
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(400);
    const a = range(iso(18, 0)), b = range(iso(17, 23));
    let go: (to: string) => void = () => {};
    const Go = () => { go = useNavigate(); return null; };
    // kept between the ranges, so that the second visit to a range changes the key alone and mounts no new chart
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    renderWithProviders(<QueryClientProvider client={qc}><Costs /><Go /></QueryClientProvider>, { route: `/${a}` });
    const scroller = async () => scrollerOf((await line()).closest("svg")!.parentElement!.parentElement!);
    // three periods end 120 px before the plot's 560: at the last one, not in that padding
    expect((await scroller()).scrollLeft).toBe(380);
    act(() => go(b));
    await waitFor(() => expect(qc.getQueryData(["cost", "usage-readings", windowQuery(customWindow(Date.parse(iso(17, 23)), Date.parse(iso(18, 4))))])).toBeDefined());
    const sc = await scroller();
    sc.scrollLeft = 200;
    await act(async () => { await qc.refetchQueries({ queryKey: ["cost"] }); });
    expect((await scroller()).scrollLeft).toBe(200);
    const label = () => document.querySelector(`[data-credential="${SEAT}#0"] g[data-period]`)!.getAttribute("aria-label");
    expect(label()).toContain("Weekly meter: 01:05 UTC 9 %");
    act(() => go(b.replace("tz=utc", "tz=local")));
    expect((await scroller()).scrollLeft).toBe(200);
    expect(label()).toContain("Weekly meter: 10:05 GMT+9 9 %");
    act(() => go(a));
    await waitFor(() => expect(scrollerOf(document.querySelector(`[data-credential="${SEAT}#0"]`)!).scrollLeft).toBe(380));
    // the bucket alone is a new window too
    const cells = ["cost", "cells", windowQuery(customWindow(Date.parse(iso(18, 0)), Date.parse(iso(18, 4))))];
    const hourly = qc.getQueryData<CostCells>([...cells, "hour"])!, period = iso(18, 0);
    qc.setQueryData([...cells, "10hour"], { ...hourly, periods: [period],
      cells: [{ ...hourly.cells[0], period }], subscription: [{ ...hourly.subscription![0], period }] });
    (await scroller()).scrollLeft = 200;
    act(() => go(a.replace("bucket=hour", "bucket=10hour")));
    // one period, so 320 px of padding
    await waitFor(async () => expect((await scroller()).scrollLeft).toBe(180));
  });

  // A failed first load is the chart card's one alert.
  const chartError = () => document.querySelector("[data-chart-title]")?.closest(".card")?.querySelector("[data-cost-load-error]") ?? null;

  it("says so when the readings could not be read, in the chart and in its table", async () => {
    serve();
    const ok = globalThis.fetch;
    vi.stubGlobal("fetch", (url: RequestInfo | URL, init?: RequestInit) => (String(url).includes("/cost/usage-readings")
      ? Promise.resolve(new Response('{"error":"boom"}', { status: 500, headers: { "Content-Type": "application/json" } })) : ok(url, init)));
    try {
      renderWithProviders(<Costs />, { route: `/${range(iso(18, 0))}` });
      await waitFor(() => expect(chartError()).not.toBeNull());
      expect(chartError()!.textContent).toBe(i18n.t("costs.home.readingsError"));
      expect(document.querySelector("[data-meter-line], [data-meter-legend]")).toBeNull();
      fireEvent.click(screen.getByRole("button", { name: "Show as table" }));
      expect(document.querySelector("table")).not.toBeNull();
      expect(chartError()).not.toBeNull();
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("keeps saying so while it asks again for readings that could not be loaded", async () => {
    serve();
    const ok = globalThis.fetch;
    let pending = false;
    vi.stubGlobal("fetch", (url: RequestInfo | URL, init?: RequestInit) => (!String(url).includes("/cost/usage-readings") ? ok(url, init)
      : pending ? new Promise<Response>(() => {})
      : Promise.resolve(new Response('{"error":"boom"}', { status: 500, headers: { "Content-Type": "application/json" } }))));
    try {
      const { qc } = renderWithProviders(<Costs />, { route: `/${range(iso(18, 0))}` });
      await waitFor(() => expect(chartError()).not.toBeNull());
      pending = true;
      await act(async () => {
        void qc.refetchQueries({ queryKey: ["cost", "usage-readings"] });
        await new Promise((r) => setTimeout(r, 20));
      });
      expect(qc.isFetching({ queryKey: ["cost", "usage-readings"] })).toBeGreaterThan(0);
      expect(chartError()?.textContent).toBe(i18n.t("costs.home.readingsError"));
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("says so when a refetch of the readings fails, keeps the line it had, and takes it back when one succeeds", async () => {
    serve();
    const { qc } = renderWithProviders(<Costs />, { route: `/${range(iso(18, 0))}` });
    await line();
    const ok = globalThis.fetch;
    const refetch = () => act(async () => { await qc.refetchQueries({ queryKey: ["cost", "usage-readings"] }); });
    vi.stubGlobal("fetch", (url: RequestInfo | URL, init?: RequestInit) => (String(url).includes("/cost/usage-readings")
      ? Promise.resolve(new Response('{"error":"boom"}', { status: 500, headers: { "Content-Type": "application/json" } })) : ok(url, init)));
    try {
      await refetch();
      await waitFor(() => expect(document.querySelector("[data-meter-error]")).not.toBeNull());
      const said = document.querySelector("[data-meter-error]")!;
      expect(said.getAttribute("role")).toBe("status");
      expect(said.textContent).toBe(i18n.t("costs.fig.meterBehind"));
      expect(said.textContent).toContain("may be behind");
      expect((await line()).querySelectorAll("circle")).toHaveLength(3);
      vi.stubGlobal("fetch", ok);
      await refetch();
      await waitFor(() => expect(document.querySelector("[data-meter-error]")).toBeNull());
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("says nothing of it when the readings came", async () => {
    serve();
    renderWithProviders(<Costs />, { route: `/${range(iso(18, 0))}` });
    await line();
    expect(document.querySelector("[data-meter-error]")).toBeNull();
  });
});

// The chart in a scroller with a browser's measures (the outside review of #197's port).
describe("weekly meter: in a scroller that measures", () => {
  beforeEach(() => { void i18n.changeLanguage("en"); });
  afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });
  const hoursOf = (n: number) => Array.from({ length: n }, (_, i) => new Date(Date.UTC(2026, 10, 18, i)).toISOString());
  const el = (n: number, over: Partial<Bar> = {}, meter = true) => (
    <BarChart title="t" periods={hoursOf(n)} fmtPeriod={(p) => `${p.slice(5, 16)} UTC`} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis} scrollKey="k"
      groups={hoursOf(n).map(() => [{ key: "b", label: "B", color: "red", segs: [{ key: "s", label: "S", color: "red", value: 7 }], ...over }])}
      meter={meter ? { points: [], name: "m", fmtAt: (x) => x } : undefined} />
  );
  /** A scroller `view.w` wide around the plot and the meter's 44 px, its scroll clamped as a browser's is. */
  function browser(view: { w: number }) {
    const content = (e: Element) => Number(e.querySelector("svg[role=group]")?.getAttribute("width") ?? 0) + 44;
    const max = (e: Element) => Math.max(0, content(e) - view.w);
    const at = new WeakMap<Element, number>();
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(() => view.w);
    vi.spyOn(HTMLElement.prototype, "scrollWidth", "get").mockImplementation(function (this: HTMLElement) { return Math.max(view.w, content(this)); });
    vi.spyOn(Element.prototype, "scrollLeft", "get").mockImplementation(function (this: Element) { return Math.min(at.get(this) ?? 0, max(this)); });
    vi.spyOn(Element.prototype, "scrollLeft", "set").mockImplementation(function (this: Element, v: number) { at.set(this, Math.min(Math.max(0, v), max(this))); });
  }

  it.each([1, 2, 3])("opens with the newest of %i periods between the axes on a phone", (n) => {
    browser({ w: 243 });
    const { container } = renderWithProviders(el(n));
    const last = [...container.querySelectorAll("rect[data-seg]")].at(-1)!;
    const x = Number(last.getAttribute("x")) - scrollerOf(container).scrollLeft;
    expect(x).toBeGreaterThanOrEqual(60);
    expect(x + Number(last.getAttribute("width"))).toBeLessThanOrEqual(243 - 44);
  });

  it("does not stay in the padding when the plot shrinks to two periods", () => {
    browser({ w: 243 });
    let set: (n: number) => void = () => {};
    function Shrinks() { const [n, setN] = useState(10); set = setN; return el(n); }
    const { container } = renderWithProviders(<Shrinks />);
    act(() => set(2));
    expect(scrollerOf(container).scrollLeft).toBe(64 + 2 * 120 + 16 + 44 - 243);
  });

  it("is at the newest period before the first paint", () => {
    browser({ w: 243 });
    let seen = -1;
    function Parent() {
      const box = useRef<HTMLDivElement>(null);
      // after the chart's layout effects, before any effect that waits for the paint
      useLayoutEffect(() => { seen = scrollerOf(box.current!).scrollLeft; }, []);
      return <div ref={box}>{el(20)}</div>;
    }
    renderWithProviders(<Parent />);
    expect(seen).toBeGreaterThan(0);
  });

  it.each([20, 70, 169])("keeps %i periods' x labels a label apart, and the last one whole", (n) => {
    const { container } = renderWithProviders(el(n));
    const svg = container.querySelector<SVGSVGElement>("svg[role=group]")!;
    const xs = [...svg.querySelectorAll(":scope > text[aria-hidden]")].map((e) => Number(e.getAttribute("x")));
    expect(xs.at(-1)).toBeGreaterThan(Number(svg.getAttribute("width")) - 16 - 120);
    for (let i = 1; i < xs.length; i++) expect(xs[i] - xs[i - 1]).toBeGreaterThanOrEqual("11-18T00:00 UTC".length * 6);
    expect(svg.style.overflow).toBe("visible");
  });

  const day = (p: string) => `${p.slice(5, 7)}/${p.slice(8, 10)} UTC`;
  const cjk = (p: string) => `第${p.slice(5, 7)}月${p.slice(8, 10)}日`;
  it.each([
    ["11/29 UTC has room", 2, day, [27, 29, 30]],
    ["11/29 UTC would touch", 1, day, [25, 27, 30]],
    ["第11月29日 has room", 2, cjk, [27, 29, 30]],
    ["第11月29日 would touch", 1, cjk, [25, 27, 30]],
  ] as const)("decides on the label before the last by the labels' own width: %s", (_, bars, fmt, want) => {
    const days = Array.from({ length: 30 }, (_, i) => new Date(Date.UTC(2026, 10, 1 + i)).toISOString());
    const group = Array.from({ length: bars }, (_, k): Bar => ({ key: `b${k}`, label: "B", color: "red", segs: [{ key: "s", label: "S", color: "red", value: 7 }] }));
    const { container } = renderWithProviders(
      <BarChart title="t" periods={days} groups={days.map(() => group)} fmtPeriod={fmt} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis} />,
    );
    const shown = [...container.querySelectorAll("svg[role=group] > text[aria-hidden]")].slice(-3).map((e) => e.textContent);
    expect(shown).toEqual(want.map((d) => fmt(days[d - 1])));
  });

  it("follows a scroller that gets narrower, and leaves one that was scrolled back", () => {
    const view = { w: 900 };
    browser(view);
    const observers: (() => void)[] = [];
    vi.stubGlobal("ResizeObserver", class { constructor(cb: () => void) { observers.push(cb); } observe() {} disconnect() {} });
    const { container } = renderWithProviders(el(20));
    const sc = scrollerOf(container), resize = (w: number) => { view.w = w; act(() => observers.forEach((f) => f())); };
    resize(400);
    expect(sc.scrollLeft).toBe(sc.scrollWidth - 400);
    // a rotation passes a wider layout before the narrower one: the browser clamps there, and only then tells
    view.w = 500;
    sc.scrollLeft = sc.scrollLeft + 0;
    resize(350);
    expect(sc.scrollLeft).toBe(sc.scrollWidth - 350);
    sc.scrollLeft = 10;
    fireEvent.scroll(sc);
    resize(300);
    expect(sc.scrollLeft).toBe(10);
  });

  it("starts a chart that loses the meter at its first period, where its $ labels are", () => {
    browser({ w: 400 });
    let lose: () => void = () => {};
    function Loses() {
      const [meter, setMeter] = useState(true);
      lose = () => setMeter(false);
      return el(20, {}, meter);
    }
    const { container } = renderWithProviders(<Loses />);
    const sc = scrollerOf(container);
    expect(sc.scrollLeft).toBeGreaterThan(0);
    act(() => lose());
    expect(container.querySelector("[data-cost-axis]")).toBeNull();
    expect(scrollerOf(container)).toBe(sc);
    expect(sc.scrollLeft).toBe(0);
  });

  it.each([true, false])("hides a focused, unpinned tooltip while its period is out of view (with the meter: %s)", (meter) => {
    browser({ w: 400 });
    const { container } = renderWithProviders(el(20, {}, meter));
    const sc = scrollerOf(container), period = [...container.querySelectorAll("g[data-period]")].at(-1)!;
    sc.scrollLeft = 99999;
    fireEvent.focusIn(period);
    const tip = screen.getByRole("tooltip");
    fireEvent.scroll(sc);
    expect(tip.style.visibility).not.toBe("hidden");
    sc.scrollLeft = 0;
    fireEvent.scroll(sc);
    expect(tip.style.visibility).toBe("hidden");
    expect(period.getAttribute("aria-pressed")).toBe("false");
    sc.scrollLeft = 99999;
    fireEvent.scroll(sc);
    expect(tip.style.visibility).not.toBe("hidden");
  });

  it("hides a pinned tooltip while its period is out of view, and keeps the pin", () => {
    browser({ w: 400 });
    const { container } = renderWithProviders(el(20));
    const sc = scrollerOf(container), period = [...container.querySelectorAll("g[data-period]")].at(-1)!;
    fireEvent.click(period);
    const tip = screen.getByRole("tooltip");
    expect(tip.style.visibility).not.toBe("hidden");
    sc.scrollLeft = 0;
    fireEvent.scroll(sc);
    expect(tip.style.visibility).toBe("hidden");
    expect(period.getAttribute("aria-pressed")).toBe("true");
    sc.scrollLeft = 99999;
    fireEvent.scroll(sc);
    expect(tip.style.visibility).not.toBe("hidden");
  });

  it("cuts the line out under every value and multiple, also an open period's", () => {
    const marked = renderWithProviders(el(3, { valueLabel: true, tag: "≈ 7.5×" }));
    const svg = marked.container.querySelector("svg[role=group]")!;
    const mask = svg.querySelector("mask")!;
    expect(mask).not.toBeNull();
    expect(svg.querySelector("[data-meter-line]")!.parentElement!.getAttribute("mask")).toBe(`url(#${mask.id})`);
    const holes = [...mask.querySelectorAll("rect[data-cut]")].map((r) => ["x", "y", "width", "height"].map((a) => Number(r.getAttribute(a))));
    const texts = [...svg.querySelectorAll("text[data-value], text[data-tag]")];
    expect(texts).toHaveLength(6);
    for (const text of texts) {
      const x = Number(text.getAttribute("x")), y = Number(text.getAttribute("y")) - 3;
      expect(holes.some(([l, t, w, h]) => l < x - 10 && l + w > x + 10 && t < y && t + h > y + 3)).toBe(true);
    }
    marked.unmount();
    const plain = renderWithProviders(el(3));
    expect(plain.container.querySelector("mask")).toBeNull();
    fireEvent.focusIn(plain.container.querySelector("g[data-period]")!);
    expect(plain.container.querySelectorAll("mask rect[data-cut]")).toHaveLength(1);
  });
});

describe("a period's hit area", () => {
  it("reaches up to a note or a label above the plot", () => {
    const { container } = renderWithProviders(
      <BarChart title="t" periods={[iso(18, 0)]} groups={[[]]} fmtPeriod={(p) => p} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis}
        notes={new Map([[0, "no list price"]])} notesLabel="Note" />,
    );
    const note = container.querySelector("text[data-note]")!;
    const hit = container.querySelector("g[data-period] path[data-hit]");
    expect(hit).not.toBeNull();
    const [left, top, width, down] = hit!.getAttribute("d")!.match(/[\d.]+/g)!.map(Number);
    expect(top).toBe(0);
    expect(Number(note.getAttribute("y"))).toBeLessThan(top + down);
    expect(Number(note.getAttribute("x"))).toBeGreaterThan(left);
    expect(Number(note.getAttribute("x"))).toBeLessThan(left + width);
  });
});
