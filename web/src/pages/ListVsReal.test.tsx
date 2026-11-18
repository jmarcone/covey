import { describe, it, expect, beforeEach } from "vitest";
import { act, fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { CostCells } from "../api";
import i18n from "../i18n";
import { fmtBucket } from "../format";
import { renderWithProviders } from "../test/render";
import { CostExplorer } from "./StackedCostChart";

// The list-vs-real multiple in a period's details and on the tile, and
// Recomputed as a comparison asked for, not a second series (#118): what a
// seat's tooltip says per period, when covey's price table is named, and what
// is drawn by default.

const SEAT = "claude_code_oauth_token";
const PP = 0.46; // a point is worth $0.46 on this plan
const iso = (h: number) => new Date(Date.UTC(2026, 10, 17, h)).toISOString();
const H18 = iso(18), H19 = iso(19), H20 = iso(20);

type Hour = {
  period: string;
  /** The period's list price (recomputed, all types). */
  list: number;
  /** What Claude Code reported. */
  reported?: number;
  /** The meter's points in the period; undefined: no reading at all. */
  points?: number;
  /** The server's price of those points; PP each when not given. */
  usd?: number | null;
  unpriced?: number;
};

function seat(hours: Hour[], kind = "subscription", key = SEAT): CostCells {
  const steps = hours.filter((h) => h.points !== undefined);
  return {
    periods: hours.map((h) => h.period),
    agents: [{ id: "a1", label: "Coordinator", reported_usd: 1, measured_usd: 1 }],
    types: ["output"],
    cells: hours.map((h) => ({
      period: h.period, agent_id: "a1", secret_key: key, secret_slot: 0, credential_kind: kind,
      reported_usd: 1, measured: { output: h.list }, engine_usd: h.reported ?? h.list, tokens: { output: 50_000 },
    })),
    subscription: steps.map((h) => ({
      period: h.period, secret_key: key, secret_slot: 0, credential_kind: kind, source: "usage_cmd", window_kind: "seven_day",
      points: h.points!, readings: 2, resets: 0, longest_gap_secs: 900,
      usd: h.usd === undefined ? h.points! * PP : h.usd, unpriced_points: h.unpriced ?? 0,
    })),
    meter: kind === "subscription" ? [{
      secret_key: key, secret_slot: 0, credential_kind: kind, source: "usage_cmd", window_kind: "seven_day",
      baseline_at: hours[0].period, baseline_percent: 9, baseline_before_window: false,
      last_at: hours[hours.length - 1].period, last_percent: 9 + steps.reduce((a, h) => a + h.points!, 0), readings: 4,
      points: steps.reduce((a, h) => a + h.points!, 0), points_low: 0, points_high: 9, longest_gap_secs: 900, segments: null,
      measured_usd: hours.reduce((a, h) => a + h.list, 0),
    }] : [],
    plans: kind === "subscription" ? [{
      runtime_id: null, ord: 0, secret_key: key, secret_slot: 0, plan_name: "Max", amount: 200, currency: "USD", period: "month",
      tax_basis: "net", fx_to_usd: 1, fx_date: null, valid_from: "2026-11-01T00:00:00Z", valid_to: null,
      weekly_usd: PP * 100, usd_per_point: PP,
    }] : [],
    bookings: [],
  };
}

function chart(data: CostCells, selected: Set<string> = new Set()) {
  return renderWithProviders(
    <CostExplorer data={data} selected={selected} bucket="hour" fmtPeriod={(p) => fmtBucket(p, "hour")} />,
  );
}

/** The period's tooltip text, opened by focus. */
function tip(root: HTMLElement, period: string): string {
  fireEvent.focus(root.querySelector(`g[data-period="${period}"]`)!);
  return root.querySelector("[role=tooltip]")!.textContent!;
}
const multipleLine = (container: HTMLElement) => container.querySelector("[data-multiple-line]")?.textContent ?? null;
const diffNote = (container: HTMLElement) => container.querySelector("[data-diff-note]")?.textContent ?? null;
const sources = (container: HTMLElement) => within(container.querySelector<HTMLElement>("[data-credential]")!.parentElement!)
  .getAllByText("Source")[0].parentElement!;
const click = async (el: HTMLElement) => act(async () => { await userEvent.click(el); });

describe("list-vs-real multiple in a period's details", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });

  // 1
  it("says 'about N×' from three points: 14.29 ÷ (3 × 0.46) = 10.36, rounded to the nearest whole number", () => {
    const { container } = chart(seat([{ period: H18, list: 14.29, points: 3 }]));
    expect(tip(container, H18)).toContain("List price is about 10× what you really paid");
    expect(multipleLine(container)).toBe("List price is about 10× what you really paid");
  });

  it("writes a multiple under 10× with one decimal", () => {
    const { container } = chart(seat([{ period: H18, list: 8, points: 4 }]));
    // 8 ÷ (4 × 0.46) = 4.35
    expect(tip(container, H18)).toContain("List price is about 4.3× what you really paid");
  });

  // 2
  it("gives a range below three points: 'at least' at one, 'between' at two, never 'about'", () => {
    const one = chart(seat([{ period: H18, list: 14.29, points: 1 }]));
    // 14.29 ÷ 0.92 = 15.53, rounded down
    expect(tip(one.container, H18)).toContain("List price is at least 15× what you really paid");
    expect(tip(one.container, H18)).not.toMatch(/about/);
    one.unmount();
    const two = chart(seat([{ period: H18, list: 14.29, points: 2 }]));
    // low 14.29 ÷ 1.38 = 10.35 down, high 14.29 ÷ 0.46 = 31.07 up
    expect(tip(two.container, H18)).toContain("List price is between 10× and 32× what you really paid");
    expect(tip(two.container, H18)).not.toMatch(/about/);
  });

  // 3
  it("says the meter did not move for an hour with spend and a reading of 0 points, with no ×, ∞ or 0×", () => {
    const { container } = chart(seat([{ period: H18, list: 14.29, points: 0 }, { period: H19, list: 1, points: 2 }]));
    const text = tip(container, H18);
    expect(multipleLine(container)).toBe("The meter didn't move");
    expect(text).not.toMatch(/×|∞|0×/);
  });

  it("says there is no meter reading for an hour without one, and nothing for an hour without spend", () => {
    const { container } = chart(seat([
      { period: H18, list: 14.29 }, { period: H19, list: 1, points: 2 }, { period: H20, list: 0.004, points: 0 },
    ]));
    expect(tip(container, H18)).toContain("No meter reading");
    expect(tip(container, H18)).not.toMatch(/×|∞/);
    expect(multipleLine(container)).toBe("No meter reading");
    tip(container, H20);
    expect(multipleLine(container)).toBeNull();
  });

  // 4
  describe("where it does not belong", () => {
    const hours: Hour[] = [{ period: H18, list: 14.29, points: 3 }];

    it("has none for an API key, beside the seat's", () => {
      const KEY = "anthropic_api_key";
      const data = seat(hours);
      data.cells.push(...seat([{ period: H18, list: 14.29 }], "api_key", KEY).cells);
      const { container } = chart(data);
      const of = (id: string) => container.querySelector<HTMLElement>(`[data-credential="${id}#0"]`)!;
      expect(tip(of(SEAT), H18)).toContain("about 10×");
      tip(of(KEY), H18);
      expect(multipleLine(of(KEY))).toBeNull();
    });

    it("has none with an agent filter", () => {
      const open = chart(seat(hours));
      expect(tip(open.container, H18)).toContain("about 10×");
      open.unmount();
      const { container } = chart(seat(hours), new Set(["a1"]));
      tip(container, H18);
      expect(multipleLine(container)).toBeNull();
    });

    it("has none with Real switched off", async () => {
      const { container } = chart(seat(hours));
      expect(tip(container, H18)).toContain("about 10×");
      await click(within(sources(container)).getByRole("button", { name: /^Real cost/ }));
      tip(container, H18);
      expect(multipleLine(container)).toBeNull();
    });

    it("has none with both Reported and Recomputed off", async () => {
      const { container } = chart(seat(hours));
      await click(within(sources(container)).getByRole("button", { name: "Reported by Claude Code" }));
      tip(container, H18);
      expect(multipleLine(container)).toBeNull();
      // Recomputed back on: the list bar is drawn again, and so is the line
      await click(screen.getByRole("checkbox", { name: "Compare with covey's price table" }));
      tip(container, H18);
      expect(multipleLine(container)).toBe("List price is about 10× what you really paid");
    });

    it("has none for points without a plan price (the real figure is not drawn either)", () => {
      const { container } = chart(seat([{ period: H18, list: 14.29, points: 3, usd: null, unpriced: 3 }]));
      expect(container.querySelector("g[data-period]")!.getAttribute("aria-label")).not.toMatch(/Real cost/);
      tip(container, H18);
      expect(multipleLine(container)).toBeNull();
    });
  });

  it("is on the list-vs-real view too", async () => {
    const { container } = chart(seat([{ period: H18, list: 14.29, points: 1 }]));
    await click(screen.getByRole("button", { name: "Real vs list" }));
    expect(tip(container, H18)).toContain("List price is at least 15× what you really paid");
  });
});

describe("the difference to covey's price table", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });
  const reportedTile = (container: HTMLElement) => within(container.querySelector<HTMLElement>("[data-credential]")!)
    .getByRole("group", { name: "Reported by Claude Code" });

  // 5
  it("names it under Reported, in the tooltip and on the tile, only above 1 % and a cent", () => {
    const { container } = chart(seat([{ period: H18, list: 13.8, reported: 14.29, points: 3 }]));
    const line = "covey's price table comes to $13.80, 3 % less than Claude Code reported";
    expect(tip(container, H18)).toContain(line);
    expect(diffNote(container)).toBe(line);
    expect(reportedTile(container).textContent).toContain(line);
  });

  it("says 'more' where the price table comes to more", () => {
    const { container } = chart(seat([{ period: H18, list: 15, reported: 12, points: 3 }]));
    expect(tip(container, H18)).toContain("covey's price table comes to $15.00, 25 % more than Claude Code reported");
  });

  it.each([
    ["under 1 %", 14.2, 14.29],
    ["1 % but under a cent", 0.505, 0.5],
    ["nothing reported", 3, 0],
  ])("stays silent for a difference %s", (_why, list, reported) => {
    // the second hour differs by 25 %: the line is there, only the first is silent
    const { container } = chart(seat([{ period: H18, list, reported, points: 3 }, { period: H19, list: 15, reported: 12, points: 3 }]));
    tip(container, H18);
    expect(diffNote(container)).toBeNull();
    tip(container, H19);
    expect(diffNote(container)).not.toBeNull();
  });

  it.each([
    ["under 1 %", 14.2, 14.29],
    ["1 % but under a cent", 0.505, 0.5],
    ["nothing reported", 3, 0],
  ])("leaves the Reported tile silent for a difference %s", (_why, list, reported) => {
    const quiet = chart(seat([{ period: H18, list, reported, points: 3 }]));
    expect(reportedTile(quiet.container).textContent).not.toMatch(/price table comes to/);
    quiet.unmount();
    const loud = chart(seat([{ period: H18, list: 15, reported: 12, points: 3 }]));
    expect(reportedTile(loud.container).textContent).toMatch(/price table comes to \$15\.00, 25 % more/);
  });
});

describe("Recomputed as a comparison", () => {
  beforeEach(async () => { await i18n.changeLanguage("en"); });
  const data = () => seat([{ period: H18, list: 14.29, reported: 14.29, points: 3 }]);
  const block = (c: HTMLElement) => c.querySelector<HTMLElement>("[data-credential]")!;

  // 6
  it("is off by default: no chip, no bar, no tile, no key entry, no split row; Reported, Real and Billed on", async () => {
    const { container } = chart(data());
    const row = sources(container);
    expect(within(row).queryByRole("button", { name: /Recomputed/ })).toBeNull();
    for (const name of ["Reported by Claude Code", /^Real cost/, "Billed by Anthropic"]) {
      expect(within(row).getByRole("button", { name })).toHaveAttribute("aria-pressed", "true");
    }
    expect(screen.getByRole("checkbox", { name: "Compare with covey's price table" })).not.toBeChecked();
    expect(container.querySelector("g[data-period]")!.getAttribute("aria-label")).not.toMatch(/Recomputed/);
    expect(tip(container, H18)).not.toMatch(/Recomputed/);
    expect(within(block(container)).queryByRole("group", { name: "Recomputed from tokens" })).toBeNull();
    expect(container.querySelector("[aria-label='Colour key']")!.textContent).not.toMatch(/recomputed/);
    expect(screen.queryByText("Split by")).toBeNull();
  });

  it("draws the bars, the tile with its price-table check, the key entry and the split row when switched on", async () => {
    const { container } = chart(data());
    await click(screen.getByRole("checkbox", { name: "Compare with covey's price table" }));
    expect(container.querySelector("g[data-period]")!.getAttribute("aria-label")).toMatch(/Recomputed from tokens \$14\.29/);
    const tile = within(block(container)).getByRole("group", { name: "Recomputed from tokens" });
    expect(tile.textContent).toContain("price-table check +0.00 %");
    expect(container.querySelector("[aria-label='Colour key']")!.textContent).toMatch(/blue: recomputed from tokens/);
    expect(screen.getByText("Split by")).toBeInTheDocument();
    // and off again
    await click(screen.getByRole("checkbox", { name: "Compare with covey's price table" }));
    expect(within(block(container)).queryByRole("group", { name: "Recomputed from tokens" })).toBeNull();
  });

  it("keeps the colour key to what is drawn, and lists Recomputed in the list-vs-real view", async () => {
    const { container } = chart(data());
    const key = () => container.querySelector("[aria-label='Colour key']")!.textContent!;
    expect(key()).toMatch(/grey: reported/);
    expect(key()).toMatch(/real/);
    await click(within(sources(container)).getByRole("button", { name: "Reported by Claude Code" }));
    expect(key()).not.toMatch(/grey: reported/);
    await click(screen.getByRole("button", { name: "Real vs list" }));
    expect(key()).toMatch(/blue: recomputed/);
    // the split by cost type stacks the list bar there
    expect(screen.getByText("Split by")).toBeInTheDocument();
  });

  it("counts the comparison in 'keep at least one source'", async () => {
    const { container } = chart(data());
    const row = sources(container);
    await click(screen.getByRole("checkbox", { name: "Compare with covey's price table" }));
    for (const name of ["Reported by Claude Code", /^Real cost/, "Billed by Anthropic"]) await click(within(row).getByRole("button", { name }));
    // only the comparison is left on: it can be switched off no more than a last chip
    await click(screen.getByRole("checkbox", { name: "Compare with covey's price table" }));
    expect(screen.getByRole("checkbox", { name: "Compare with covey's price table" })).toBeChecked();
  });

  it("puts a sentence on the multiple tile from the tile's own figure", async () => {
    const { container } = chart(seat([{ period: H18, list: 14.29, points: 3 }]));
    const tile = within(block(container)).getByRole("group", { name: "List $ per real $" });
    // the tile's figure: 14.29 ÷ 1.38 = 10.36
    expect(tile.textContent).toContain("≈ 10.4×");
    expect(within(tile).getByText("List price is about 10× what you really paid")).toBeInTheDocument();
  });
});
