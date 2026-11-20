import { describe, it, expect, beforeEach } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import i18n from "../i18n";
import type { FixFigure, FixRoundGroup, FixRoundReport } from "../api";
import { FixRounds, FixRoundsView, fixWindow } from "./FixRounds";
import { mockFetch, renderWithProviders } from "../test/render";

// The fix-round figures (#130, AC 8/9): every figure with its n, "no data"
// where n = 0 — never a 0 that reads as a measurement.

const none: FixFigure = { mean: null, total: null, n: 0 };
const fig = (mean: number, n: number): FixFigure => ({ mean, total: mean * n, n });
const empty = (key: string): FixRoundGroup => ({
  key, rounds: 0, usd: none, first_turn_cache_write: none, first_turn_cache_write_5m: none,
  first_turn_cache_write_1h: none, first_turn_cache_read: none, first_turn_input: none, gap_minutes: none, context_tokens: none,
});

const resumed: FixRoundGroup = {
  ...empty("sticky_resumed"), rounds: 2, usd: fig(3.5, 2), first_turn_cache_write: fig(3000, 1),
  first_turn_cache_write_5m: fig(1000, 1), first_turn_cache_write_1h: fig(2000, 1), first_turn_cache_read: fig(150000, 1),
};

const report: FixRoundReport = {
  from: "2026-11-13T00:00:00Z",
  to: "2026-11-20T00:00:00Z",
  by_decision: [resumed, empty("fresh"), empty("unknown")],
  by_policy: [],
  agents: [{ agent_id: "a1", slug: "covey-dev", by_decision: [], by_policy: [{ ...resumed, key: "always" }, empty("conditional")] }],
  rounds_per_pr: [{ agent_id: "a1", slug: "covey-dev", policy: "always", prs: 2, rounds: fig(1.5, 3) }],
  prs: [],
};

describe("Fix rounds", () => {
  beforeEach(() => {
    i18n.changeLanguage("en");
  });

  it("windows are whole weeks back from now", () => {
    const now = new Date("2026-11-20T12:00:00Z");
    expect(fixWindow(now, 0)).toEqual({ from: new Date("2026-11-13T12:00:00Z"), to: now });
    expect(fixWindow(now, 2)).toEqual({ from: new Date("2026-10-30T12:00:00Z"), to: new Date("2026-11-06T12:00:00Z") });
  });

  it("shows each figure with n, and no data where n = 0", () => {
    renderWithProviders(<FixRoundsView rep={report} />);
    const table = document.querySelector("[data-fix-decision]") as HTMLElement;
    const row = within(table).getByText("resumed").closest("tr")!;
    expect(row.textContent).toContain("$3.50");
    expect(row.textContent).toContain("n=2");
    expect(row.textContent).toContain("3,000");
    expect(row.textContent).toContain("150,000");
    const fresh = within(table).getByText("fresh").closest("tr")!;
    expect(within(fresh).getAllByText("no data").length).toBe(7);
    expect(fresh.textContent).not.toContain("$0.00");
    // The A/B lists only arms with rounds; rounds per PR with its n.
    const policy = document.querySelector("[data-fix-policy]") as HTMLElement;
    expect(within(policy).queryByText("conditional")).toBeNull();
    expect(within(policy).getByText("always")).toBeTruthy();
    const prs = document.querySelector("[data-fix-prs]") as HTMLElement;
    expect(prs.textContent).toContain("1.5");
    expect(prs.textContent).toContain("n=3");
  });

  it("reads the week from the endpoint with an RFC 3339 window", async () => {
    const { calls } = mockFetch({ "/api/v1/cost/fix-rounds": { ...report, by_decision: [empty("sticky_resumed"), empty("fresh"), empty("unknown")], agents: [], rounds_per_pr: [] } });
    renderWithProviders(<FixRounds />);
    expect(await screen.findByText("No fix rounds in this window.")).toBeTruthy();
    await waitFor(() => expect(calls.length).toBe(1));
    const url = new URL(calls[0].split(" ")[1], "http://x");
    const from = new Date(url.searchParams.get("from")!);
    const to = new Date(url.searchParams.get("to")!);
    expect(to.getTime() - from.getTime()).toBe(7 * 24 * 60 * 60 * 1000);
  });
});
