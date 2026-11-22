import { describe, it, expect } from "vitest";
import { act, render } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router";
import { useCostRange, type CostRange } from "./costRangeState";

// The range state of the cost pages: what applying a custom range and freezing a quick one write to the address.

const H = 3_600_000, D = 24 * H;
const NOW = Date.UTC(2026, 10, 20, 17, 30);

function mount(route: string) {
  const out: { r: CostRange | null; search: string } = { r: null, search: "" };
  function Probe() {
    out.r = useCostRange();
    out.search = useLocation().search;
    return null;
  }
  render(<MemoryRouter initialEntries={[route]}><Probe /></MemoryRouter>);
  return out;
}

describe("useCostRange", () => {
  it("freezes a quick range keeping the bucket the reader reads where it fits the frozen span", () => {
    const s = mount("/costs?days=7&bucket=hour");
    expect(s.r!.bucket).toBe("hour");
    act(() => s.r!.freezeCustom(NOW - 7 * D, NOW));
    const p = new URLSearchParams(s.search);
    expect(p.get("from")).toBe("2026-11-13T17:30:00Z");
    expect(p.get("bucket")).toBe("hour");
    expect(s.r!.custom).toBe(true);
    expect(s.r!.bucket).toBe("hour");
  });

  it("keeps a quick range's own bucket on freeze, not only one the address names", () => {
    const s = mount("/costs?days=1");
    const before = s.r!.bucket;
    act(() => s.r!.freezeCustom(NOW - D, NOW));
    expect(s.r!.bucket).toBe(before);
  });

  it("applies a custom range at its span's own bucket, and a frozen one too where the old bucket does not fit", () => {
    const s = mount("/costs?days=7&bucket=hour");
    act(() => s.r!.applyCustom(NOW - 7 * D, NOW));
    expect(new URLSearchParams(s.search).get("bucket")).toBeNull();
    expect(s.r!.bucket).toBe("day");
    const t = mount("/costs?days=2&bucket=10min");
    act(() => t.r!.freezeCustom(NOW - 3 * D, NOW)); // 10-minute buckets hold at most 2 days
    expect(new URLSearchParams(t.search).get("bucket")).toBeNull();
  });
});
