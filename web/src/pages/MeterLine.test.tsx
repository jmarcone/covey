import { useState } from "react";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { act, fireEvent, screen, within } from "@testing-library/react";
import type { CostCells, MeterSegment, UsageReading } from "../api";
import i18n from "../i18n";
import { fmtBucket, fmtUSD, fmtUSDAxis } from "../format";
import { renderWithProviders } from "../test/render";
import { droppedReadings, meterPoints, meterY, nextBucket, usableReadings } from "./costFigures";
import { BarChart, CostExplorer, type Bar } from "./StackedCostChart";

// The weekly meter as a line over the cost bars (#117): one dot per usable
// reading, a line only between readings whose buckets touch (a gap where
// readings are missing), a fall where the server's weeks change between two
// readings, its own axis from 0 to 100 %. The green crossing labels are gone
// (the human's decision); each reading's value and time is in the tooltip,
// the period's label and the table.

const SEAT = "claude_code_oauth_token";
const KEY = "anthropic_api_key";
const iso = (day: number, h: number, m = 0) => new Date(Date.UTC(2026, 10, day, h, m)).toISOString();

const seg = (start_at: string, end_at: string, over: Partial<MeterSegment> = {}): MeterSegment =>
  ({ start_at, end_at, from_percent: 0, to_percent: 0, reset: false, points: 0, ...over });

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
      period: s.period, secret_key: key, secret_slot: 0, credential_kind: kind, source: "usage_cmd", window_kind: "seven_day",
      points: s.points, readings: 2, resets: 0, longest_gap_secs: 900, usd: null, unpriced_points: 0,
    })),
    meter: kind === "subscription" ? [{
      secret_key: key, secret_slot: 0, credential_kind: kind, source: "usage_cmd", window_kind: "seven_day",
      baseline_at: periods[0], baseline_percent: 9, baseline_before_window: false,
      last_at: periods[periods.length - 1], last_percent: 10, readings: 2, points: 1, points_low: 0, points_high: 2,
      longest_gap_secs: 900, segments,
    }] : [],
    plans: [],
    bookings: [],
  };
}

function chart(data: CostCells, readings: UsageReading[], bucket: string) {
  return renderWithProviders(
    <CostExplorer data={data} readings={readings} selected={new Set()} bucket={bucket} fmtPeriod={(p) => fmtBucket(p, bucket)} />,
  );
}

const parts = (c: HTMLElement) => [...c.querySelectorAll("g[data-meter-part]")];
const dots = (p: Element) => [...p.querySelectorAll<SVGCircleElement>("circle[data-meter-point]")];

describe("weekly meter line", () => {
  beforeEach(() => { void i18n.changeLanguage("en"); });

  // 1
  it("draws one point per usable reading, in order, and none for stale, unsupported or other-source ones", () => {
    const hours = [iso(18, 18), iso(18, 19)];
    const rs = [
      reading(iso(18, 18, 5), 9), reading(iso(18, 18, 20), 9, { stale: true }), reading(iso(18, 18, 25), 12, { supported: false }),
      reading(iso(18, 18, 30), 40, { source: "inband" }), reading(iso(18, 18, 50), 10), reading(iso(18, 19, 5), null),
      reading(iso(18, 19, 10), 11),
    ];
    const { container } = chart(cellsOf(hours, [seg(iso(18, 18, 5), iso(18, 19, 10))]), rs, "hour");
    const all = parts(container).flatMap(dots);
    expect(all.map((d) => [d.getAttribute("data-at"), d.getAttribute("data-pct")])).toEqual([
      [iso(18, 18, 5), "9"], [iso(18, 18, 50), "10"], [iso(18, 19, 10), "11"],
    ]);
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

  it("draws no line at 10 minutes: the meter is read per hour there, as Real and Billed are not drawn", () => {
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
    const rs = [reading(iso(18, 18, 5), 49), reading(iso(18, 18, 20), 51), reading(iso(18, 18, 40), 2), reading(iso(18, 18, 55), 3)];
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
    // from the last value of the old week to the first of the new one, falling
    expect(drop[0].getAttribute("points")).toBe(`${xy(b)} ${xy(c)}`);
    expect(Number(c.getAttribute("cy"))).toBeGreaterThan(Number(b.getAttribute("cy")));
    // the line is solid throughout; the fall is told apart by data-meter-drop, not by its dash
    expect(ps[0].querySelector("polyline[stroke-dasharray]")).toBeNull();
    const lines = [...ps[0].querySelectorAll("polyline[stroke='var(--cost-meter)']:not([data-meter-drop])")].map((l) => l.getAttribute("points"));
    expect(lines).toEqual([`${xy(a)} ${xy(b)}`, `${xy(c)} ${xy(d)}`]);
    // the tooltip says where the new week begins
    fireEvent.focusIn(container.querySelector<SVGGElement>("g[data-period]")!);
    expect(screen.getByRole("tooltip").querySelector("[data-meter-readings]")!.textContent).toContain("18:40 UTC 2 % (new week)");
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
  it("leaves an API key's chart without line, right axis and legend entry", () => {
    const hours = [iso(18, 18), iso(18, 19)];
    const data = cellsOf(hours, [seg(iso(18, 18, 5), iso(18, 19, 5))], KEY, "api_key");
    data.meter = [{ ...cellsOf(hours, [seg(iso(18, 18, 5), iso(18, 19, 5))]).meter![0], secret_key: KEY, credential_kind: "api_key" }];
    const rs = [reading(iso(18, 18, 5), 9, { secret_key: KEY, credential_kind: "api_key" }), reading(iso(18, 19, 5), 10, { secret_key: KEY, credential_kind: "api_key" })];
    const { container } = chart(data, rs, "hour");
    expect(container.querySelector("svg")).not.toBeNull();
    expect(container.querySelector("[data-meter-line], [data-meter-axis], [data-meter-legend]")).toBeNull();
  });

  // 9
  it("names the axis and the legend entry, and hides the line from assistive technology", () => {
    const rs = [reading(iso(18, 18, 5), 9), reading(iso(18, 18, 50), 10)];
    const { container } = chart(cellsOf([iso(18, 18)], [seg(iso(18, 18, 5), iso(18, 18, 50))]), rs, "hour");
    expect([...container.querySelectorAll("[data-meter-tick]")].map((t) => t.textContent)).toEqual(["0 %", "25 %", "50 %", "75 %", "100 %"]);
    expect(container.querySelector("[data-meter-title]")!.textContent).toBe("Weekly meter (%)");
    expect(container.querySelector("[data-meter-legend]")!.textContent).toContain("Weekly meter (%)");
    expect(container.querySelector("[data-meter-line]")!.getAttribute("aria-hidden")).toBe("true");
    expect(container.querySelector("[data-meter-axis]")!.getAttribute("aria-hidden")).toBe("true");
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

  it("explains the line in a sentence of its own, only where the line is drawn", () => {
    const rs = [reading(iso(18, 18, 5), 9), reading(iso(18, 18, 50), 10)];
    const { container } = chart(cellsOf([iso(18, 18)], [seg(iso(18, 18, 5), iso(18, 18, 50))]), rs, "hour");
    const sentence = i18n.t("costs.fig.meterNote");
    expect(sentence).not.toBe("costs.fig.meterNote");
    expect(container.querySelector("[data-meter-note]")!.textContent).toBe(sentence);
    // the hour note no longer carries it
    expect(i18n.t("costs.fig.hourRealNote", { usd: "$1" })).not.toContain(sentence);
    expect(i18n.t("costs.fig.hourRealNoteMixed")).not.toContain(sentence);
    fireEvent.click(screen.getByRole("button", { name: "Show as table" }));
    expect(container.querySelector("[data-meter-note]")).toBeNull();
    expect(container.textContent).not.toContain(sentence);
    fireEvent.click(screen.getByRole("button", { name: "Show as table" }));
    expect(container.querySelector("[data-meter-note]")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Per agent" }));
    expect(container.querySelector("[data-meter-note]")).toBeNull();
    expect(container.textContent).not.toContain(sentence);
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
        meter={{ points, label: "m", name: "n", fmtAt: (s) => s }} />,
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

  it("keeps the full dot and its halo on the ends of each part when the line is dense", () => {
    const many = Array.from({ length: 60 }, (_, k) => point(k, 0, k / 60, `p${k}`));
    const { container } = draw(many, 1);
    const r = dots(parts(container as HTMLElement)[0]).map((d) => d.getAttribute("r"));
    expect(r[0]).toBe("2.25");
    expect(r.at(-1)).toBe("2.25");
    expect(new Set(r.slice(1, -1))).toEqual(new Set(["1.25"]));
    expect(dots(parts(container as HTMLElement)[0])[0].getAttribute("stroke")).toBe("var(--surface-2)");
    expect(dots(parts(container as HTMLElement)[0])[30].getAttribute("stroke")).toBeNull();
  });

  it("scrolls to the newest period, and sets the axis strip off with a border", () => {
    const sw = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollWidth");
    Object.defineProperty(HTMLElement.prototype, "scrollWidth", { configurable: true, get: () => 777 });
    try {
      const { container } = draw([point(10, 0, 0.2, "a")]);
      const scroller = container.querySelector("svg")!.parentElement!.parentElement as HTMLElement;
      expect(scroller.scrollLeft).toBe(777);
      expect((container.querySelector("svg[data-meter-axis]") as SVGElement).style.borderLeft).toBe("1px solid var(--border)");
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
        meter={{ points: [point(0, 1, 0.5), point(100, 1, 0.5, 1), point(50, 0, 0.25, 2)], label: "m", name: "n", fmtAt: (s) => s }} />,
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
        meter={{ points: [point(1000, 0.25, 0), point(250, 0.75, 1)], label: "m", name: "n", fmtAt: (s) => s }} />,
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

  it("still drops a reading more than an hour before the latest reset an earlier reading of its segment gave", () => {
    const rs = [
      reading(iso(18, 10, 5), 1, { resets_at: "2026-11-25T09:00:00Z" }), reading(iso(18, 10, 20), 2, { resets_at: "2026-11-25T09:35:00Z" }),
      reading(iso(18, 10, 30), 39, { resets_at: "2026-11-18T12:00:00Z" }), reading(iso(18, 10, 40), 3, { resets_at: null }),
    ];
    expect(meterPoints(rs, cov([seg(iso(18, 10, 5), iso(18, 10, 40))]), [iso(18, 10)], "hour").map((p) => p.pct)).toEqual([1, 2, 3]);
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
      reading(iso(18, 9), 38, { resets_at: "2026-11-25T09:00:00Z" }), reading(iso(18, 9, 10), 1, { resets_at: "2026-12-02T09:00:00Z" }),
      reading(iso(18, 9, 20), 39, { resets_at: "2026-11-25T09:00:00Z", window_kind: "" }),
      reading(iso(18, 9, 30), 2, { resets_at: "2026-12-02T09:00:00Z" }), reading(iso(18, 9, 40), 40, { resets_at: "2026-11-25T09:00:00Z", window_kind: "" }),
      reading(iso(18, 9, 50), 41, { stale: true }), reading(iso(18, 9, 55), 5, { window_kind: "five_hour" }),
    ];
    expect(droppedReadings(rs, SEAT, 0, "usage_cmd")).toBe(1);
    // the old call, over both kinds as one run, would have counted the "" readings late too
    expect(rs.filter((r) => r.window_kind !== "five_hour").length - usableReadings(rs, SEAT, 0, "usage_cmd").length).toBeGreaterThan(1);
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
        label: "m (%)", name: "m", fmtAt: (x) => x }} />,
  );

  it("names the meter without a unit in the details, the label and the table; the unit stays on the axis", () => {
    const { container } = draw();
    expect(container.querySelector("[data-meter-title]")!.textContent).toBe("m (%)");
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
    expect(strip.style.borderRight).toBe("1px solid var(--border)");
    expect(strip.getAttribute("width")).toBe("64");
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
    const sc = container.firstElementChild as HTMLElement;
    Object.defineProperty(sc, "clientWidth", { configurable: true, value: view });
    Object.defineProperty(sc, "scrollLeft", { configurable: true, writable: true, value: left });
    return sc;
  };
  const tipOf = (container: HTMLElement, i: number) => {
    fireEvent.focusIn(container.querySelectorAll<SVGGElement>("g[data-period]")[i]);
    const tip = screen.getByRole("tooltip");
    return { left: parseFloat(tip.style.left), width: parseFloat(tip.style.width) };
  };

  it("keeps the tooltip inside the visible window, less the meter's axis, on the newest and the oldest period", () => {
    // 390 px phone, 60 px meter axis: 330 px visible. The chart is 980 px wide.
    const { container } = draw();
    scrolled(container, 390, 500);
    let t = tipOf(container, 9);
    expect(t.left).toBeGreaterThanOrEqual(504);
    expect(t.left + t.width).toBeLessThanOrEqual(500 + 330 - 4);
    expect(t.width).toBe(250);
    fireEvent.blur(container.querySelectorAll<SVGGElement>("g[data-period]")[9]);
    scrolled(container, 390, 0);
    t = tipOf(container, 0);
    expect(t.left).toBeGreaterThanOrEqual(4);
    expect(t.left + t.width).toBeLessThanOrEqual(330 - 4);
  });

  it("narrows the tooltip to a window narrower than it", () => {
    const { container } = draw();
    scrolled(container, 200, 300);
    const t = tipOf(container, 5);
    expect(t.width).toBe(140 - 8);
    expect(t.left).toBeGreaterThanOrEqual(304);
    expect(t.left + t.width).toBeLessThanOrEqual(300 + 140 - 4);
  });

  it("re-places an open tooltip on a scroll", () => {
    const { container } = draw();
    const sc = scrolled(container, 390, 0);
    const before = tipOf(container, 9).left;
    sc.scrollLeft = 500;
    fireEvent.scroll(sc);
    const left = parseFloat(screen.getByRole("tooltip").style.left);
    expect(left).toBeGreaterThanOrEqual(504);
    expect(left).not.toBe(before);
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
      const sc = scrolled(container, 390, 500);
      const wide = tipOf(container, 9);
      // an orientation change: the window gets narrower
      Object.defineProperty(sc, "clientWidth", { configurable: true, value: 300 });
      act(() => seen.filter((r) => !r.disconnected).forEach((r) => r.cb()));
      const narrow = screen.getByRole("tooltip");
      expect(parseFloat(narrow.style.left) + parseFloat(narrow.style.width)).toBeLessThanOrEqual(500 + 300 - 60 - 4);
      expect(parseFloat(narrow.style.width)).toBeLessThan(wide.width);
      fireEvent.blur(container.querySelectorAll<SVGGElement>("g[data-period]")[9]);
      expect(seen.every((r) => r.disconnected)).toBe(true);
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
          meter={{ points: [pt(0)], label: "m", name: "n", fmtAt: (x) => x }} />
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
  const scrollerOf = (c: HTMLElement) => c.querySelector("svg[role=group]")!.parentElement!.parentElement as HTMLElement;

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
