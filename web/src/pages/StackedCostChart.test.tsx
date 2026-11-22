import { describe, it, expect, beforeEach } from "vitest";
import { render } from "@testing-library/react";
import { fmtCountAxis, fmtUSD, fmtUSDAxis } from "../format";
import { useGerman } from "../test/render";
import { BarChart, niceAxis, type Bar } from "./StackedCostChart";

// The axis of the cost charts: its ticks must read as distinct, exact
// numbers at any scale (review C6).

const bars = (v: number): Bar[][] => [[{ key: "x", label: "x", color: "red", segs: [{ key: "x", label: "x", color: "red", value: v }] }]];

function ticks(max: number, fmtAxis: (v: number, step: number) => string) {
  const { container } = render(
    <BarChart title="t" periods={["2026-11-17T18:00:00Z"]} groups={bars(max)} fmtPeriod={(p) => p}
      fmtValue={fmtUSD} fmtAxis={fmtAxis} />,
  );
  return [...container.querySelectorAll("svg > g[aria-hidden] text")].map((t) => t.textContent);
}

describe("axis ticks", () => {
  beforeEach(() => useGerman());

  it("writes every tick with its step's precision, never four times 0 $", () => {
    expect(ticks(0.0003, fmtUSDAxis)).toEqual(["0 $", "0,0001 $", "0,0002 $", "0,0003 $"]);
    expect(niceAxis(0.009).step).toBeCloseTo(0.0025, 12);
    expect(ticks(0.009, fmtUSDAxis)).toEqual(["0 $", "0,0025 $", "0,0050 $", "0,0075 $", "0,0100 $"]);
  });

  it("does not round a 250 k step to 1,3 M", () => {
    expect(niceAxis(1_100_000).step).toBe(500_000);
    expect(fmtCountAxis(1_250_000, 250_000)).toBe("1,25 M");
    expect(fmtCountAxis(750_000, 250_000)).toBe("750 k");
    expect(ticks(1_000_000, fmtCountAxis)).toEqual(["0", "250 k", "500 k", "750 k", "1,00 M"]);
  });
});

// Review 5: an hourly label is wider than a slot, so only every n-th is
// drawn; the spacing follows the label's width, not a fixed 84 px.
describe("x axis", () => {
  it("draws only every third hourly label where a slot is 55 px and a label 126", () => {
    const periods = Array.from({ length: 24 }, (_, i) => new Date(Date.UTC(2026, 10, 17, i)).toISOString());
    const { container } = render(
      <BarChart title="t" periods={periods} groups={periods.map(() => bars(1)[0])} fmtPeriod={() => "11/17, 04:00 PM UTC"}
        fmtValue={fmtUSD} fmtAxis={fmtUSDAxis} />,
    );
    // labels 0, 3, ... 18, and the last one: 21 is too close to it
    expect(container.querySelectorAll("svg > text[aria-hidden]")).toHaveLength(8);
  });
});

// A period without a bar says why over its slot, in the drawn chart and not only in the table (#117 review 3, finding 11).
describe("notes over a period's slot", () => {
  it("draws a period's note in the plot, over the slot of a period with no bar", () => {
    const periods = ["2026-11-17T18:00:00Z", "2026-11-17T19:00:00Z"];
    const { container } = render(
      <BarChart title="t" periods={periods} groups={[bars(3)[0], []]} fmtPeriod={(p) => p} fmtValue={fmtUSD} fmtAxis={fmtUSDAxis}
        notes={new Map([[1, "no list price"]])} notesLabel="Note" />,
    );
    const plot = container.querySelector("svg[role=group]")!;
    const notes = [...plot.querySelectorAll("text[data-note]")];
    expect(notes.map((n) => n.textContent)).toEqual(["no list price"]);
    // over the second slot, not the first
    const x = Number(notes[0].getAttribute("x"));
    const slots = plot.querySelectorAll("g[data-period]");
    expect(slots).toHaveLength(2);
    expect(x).toBeGreaterThan(Number(plot.querySelector("g[data-period] rect")!.getAttribute("x")) + Number(plot.querySelector("g[data-period] rect")!.getAttribute("width")));
  });
});
