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
