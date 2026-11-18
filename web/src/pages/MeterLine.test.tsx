import { describe, it, expect, beforeEach } from "vitest";
import { fireEvent, screen, within } from "@testing-library/react";
import type { CostCells, MeterSegment, UsageReading } from "../api";
import i18n from "../i18n";
import { fmtBucket, fmtUSD, fmtUSDAxis } from "../format";
import { renderWithProviders } from "../test/render";
import { meterPoints } from "./costFigures";
import { BarChart, CostExplorer, type Bar } from "./StackedCostChart";

// The weekly meter as a line over the cost bars (#117): one dot per usable
// reading, a line only between readings of one week whose buckets touch, the
// server's weeks, its own axis from 0 to 100 %. What the green labels said
// stays in the tooltip, the period's label and the table.

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
    hourly.unmount();
    const daily = chart(cellsOf([iso(18, 0)], segs), rs, "day");
    expect(parts(daily.container)).toHaveLength(1);
    expect(dots(parts(daily.container)[0])).toHaveLength(2);
    expect(parts(daily.container)[0].querySelector("polyline")).not.toBeNull();
  });

  // 3
  it("does not connect a week's last reading to the next week's first: a reset is a drop", () => {
    const rs = [reading(iso(18, 18, 5), 49), reading(iso(18, 18, 20), 51), reading(iso(18, 18, 40), 2)];
    const segs = [seg(iso(18, 18, 5), iso(18, 18, 20)), seg(iso(18, 18, 40), iso(18, 18, 40), { reset: true })];
    const { container } = chart(cellsOf([iso(18, 18)], segs), rs, "hour");
    const ps = parts(container);
    expect(ps).toHaveLength(2);
    expect(dots(ps[0]).map((d) => d.getAttribute("data-pct"))).toEqual(["49", "51"]);
    expect(dots(ps[1]).map((d) => d.getAttribute("data-pct"))).toEqual(["2"]);
    // the first part's line ends at 51 %, the second begins at 2 %
    const ys = dots(ps[0]).map((d) => Number(d.getAttribute("cy")));
    expect(ys[0]).toBeGreaterThan(ys[1]);
    expect(ps[0].querySelector("polyline")!.getAttribute("points")!.split(" ")).toHaveLength(2);
    expect(ps[1].querySelector("polyline")).toBeNull();
  });

  // 4
  it("reads a one-point lag as one week, not a reset", () => {
    const rs = [reading(iso(18, 18, 5), 40), reading(iso(18, 18, 20), 39), reading(iso(18, 18, 40), 41)];
    const { container } = chart(cellsOf([iso(18, 18)], [seg(iso(18, 18, 5), iso(18, 18, 40))]), rs, "hour");
    expect(parts(container)).toHaveLength(1);
    expect(dots(parts(container)[0])).toHaveLength(3);
  });

  // 5
  it("has no crossing label above the bars; the tooltip carries the crossing, the points and their times", () => {
    const hours = [iso(18, 18)];
    const rs = [reading(iso(18, 18, 5), 9), reading(iso(18, 18, 50), 10)];
    const data = cellsOf(hours, [seg(iso(18, 18, 5), iso(18, 18, 50))], SEAT, "subscription", [{ period: hours[0], points: 1 }]);
    const { container } = chart(data, rs, "hour");
    expect([...container.querySelectorAll("svg text")].filter((t) => /→/.test(t.textContent ?? ""))).toEqual([]);
    expect(container.querySelector("svg text[fill='var(--cost-real)']")).toBeNull();
    const period = container.querySelector<SVGGElement>("g[data-period]")!;
    expect(period.getAttribute("aria-label")).toMatch(/9 % → 10 %/);
    fireEvent.focusIn(period);
    const tip = screen.getByRole("tooltip");
    expect(within(tip).getByText(/9 % → 10 %/)).toBeInTheDocument();
    const list = tip.querySelector("[data-meter-readings]")!.textContent!;
    expect(list).toContain("Weekly meter: 9 % → 10 % · 18:05 UTC 9 % · 18:50 UTC 10 %");
    // one name for the meter in the tooltip, and no second line for the crossing
    expect(tip.textContent).not.toMatch(/Meter: /);
    expect(list).not.toContain("(%)");
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

  it("is drawn in the real-vs-list view too, and not in the per-agent charts", () => {
    const rs = [reading(iso(18, 18, 5), 9), reading(iso(18, 18, 50), 10)];
    const { container } = chart(cellsOf([iso(18, 18)], [seg(iso(18, 18, 5), iso(18, 18, 50))]), rs, "hour");
    fireEvent.click(screen.getByRole("button", { name: "Real vs list" }));
    expect(container.querySelector("[data-meter-line]")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Per agent" }));
    expect(container.querySelector("[data-meter-line]")).toBeNull();
    expect(container.querySelector("[data-meter-axis]")).toBeNull();
  });
});

// Design review round 1 of #119.
describe("weekly meter line: legibility", () => {
  beforeEach(() => { void i18n.changeLanguage("en"); });
  const bar = (v: number): Bar[] => [{ key: "x", label: "x", color: "red", valueLabel: true, segs: [{ key: "x", label: "x", color: "red", value: v }] }];
  const point = (pct: number, slot: number, frac: number, at: string, part = 0) => ({ at, pct, slot, frac, part });
  const draw = (points: ReturnType<typeof point>[], n = 2) => {
    const periods = Array.from({ length: n }, (_, i) => iso(18, i));
    return renderWithProviders(
      <BarChart title="t" periods={periods} groups={periods.map(() => bar(1))} fmtPeriod={(p) => p} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis}
        meter={{ points, label: "m", fmtAt: (s) => s }} />,
    );
  };

  it("casts a solid casing in the card's colour under each part's dashed line", () => {
    const { container } = draw([point(10, 0, 0.2, "a"), point(20, 0, 0.8, "b"), point(30, 1, 0.5, "c")]);
    const lines = [...container.querySelectorAll("g[data-meter-part] polyline")];
    expect(lines).toHaveLength(2);
    const [casing, dashed] = lines;
    expect(casing.getAttribute("stroke")).toBe("var(--surface-2)");
    expect(casing.getAttribute("stroke-width")).toBe("3.75");
    expect(casing.getAttribute("stroke-dasharray")).toBeNull();
    expect(casing.getAttribute("points")).toBe(dashed.getAttribute("points"));
    expect(dashed.getAttribute("stroke")).toBe("var(--cost-meter)");
    expect(dashed.getAttribute("stroke-dasharray")).not.toBeNull();
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
    const point = (pct: number, slot: number, frac: number, part = 0) => ({ at: iso(18, 19, 30), pct, slot, frac, part });
    const { container } = renderWithProviders(
      <BarChart title="t" periods={periods} groups={periods.map(() => bar(1))} fmtPeriod={(p) => p} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis}
        meter={{ points: [point(0, 1, 0.5), point(100, 1, 0.5, 1), point(50, 0, 0.25, 2)], label: "m", fmtAt: (s) => s }} />,
    );
    const svg = container.querySelector("svg")!;
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
});

describe("meterPoints", () => {
  const cov = (segments: MeterSegment[] | null) => cellsOf([iso(18, 18)], segments).meter![0];

  it("gives each reading its slot and its fraction of the bucket", () => {
    const ps = meterPoints([reading(iso(18, 18, 45), 10)], cov([seg(iso(18, 18, 45), iso(18, 18, 45))]), [iso(18, 17), iso(18, 18)], "hour");
    expect(ps).toEqual([{ at: iso(18, 18, 45), pct: 10, slot: 1, frac: 0.75, part: 0 }]);
  });

  it("draws nothing for a reading in no segment, in no period, or without segments", () => {
    const rs = [reading(iso(18, 17, 5), 5), reading(iso(18, 18, 5), 9), reading(iso(18, 20, 5), 11)];
    const segs = [seg(iso(18, 18, 5), iso(18, 20, 5))];
    // 17:05 is in no segment, 20:05 is in no period of the chart
    expect(meterPoints(rs, cov(segs), [iso(18, 18)], "hour").map((p) => p.pct)).toEqual([9]);
    expect(meterPoints(rs, cov(null), [iso(18, 18)], "hour")).toEqual([]);
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
});
