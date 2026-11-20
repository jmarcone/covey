import { describe, it, expect, beforeEach, vi } from "vitest";
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import type { CostHealthCheck, CostHealthReport } from "../api";
import i18n from "../i18n";
import { mockFetch, renderWithProviders } from "../test/render";
import Costs from "./Costs";
import CostHealthPage, { CostHealthBadge, IncompleteMark, credentialIncomplete, incompleteParts, reasonText, worstCheck } from "./CostHealth";

// Cost health (#178): the badge beside the cost page's headline shows the
// worst check and its reason and links to /costs/health; a failed check
// marks the part it affects "incomplete" and never hides it.

const AT = "2026-11-20T12:00:00Z";

function check(id: string, status: CostHealthCheck["status"], reason: string, facts: CostHealthCheck["facts"] = {}): CostHealthCheck {
  return { id, status, reason, facts, checked_at: AT };
}

const passing = (): CostHealthCheck[] => [
  check("c1", "pass", "fresh", { last_push_minutes: 12 }),
  check("c2", "pass", "fresh", { reading_minutes: 30, week_percent: 41, fleet_killed: false }),
  check("c3", "pass", "monotonic"),
  check("c4", "pass", "unique"),
  check("c5", "pass", "priced", { models: 3 }),
  check("c6", "pass", "set"),
  check("c7", "pass", "not_linked"),
  check("c8", "info", "pre_meter", { calls: 7 }),
];

function report(status: CostHealthReport["status"], replace: CostHealthCheck[] = []): CostHealthReport {
  const checks = passing().map((c) => replace.find((r) => r.id === c.id) ?? c);
  return { status, checked_at: AT, checks };
}

const staleC1 = check("c1", "fail", "stale", {
  last_push_minutes: 76, stale_hosts: [{ source: "operator", host: "werkbank", pushed_at: "2026-11-20T10:44:00Z", minutes: 76 }],
});
const missingC6 = check("c6", "warn", "missing", { credentials: ["claude_code_oauth_token #0"] });

beforeEach(() => {
  vi.unstubAllGlobals();
  i18n.changeLanguage("en");
});

function badge() {
  return renderWithProviders(<CostHealthBadge />, { route: "/costs", path: "/costs" });
}

describe("the cost health badge", () => {
  it("is green with all checks passed, and links to the checks", async () => {
    mockFetch({ "/cost/health": report("pass") });
    badge();
    const b = await screen.findByText("All data checks passed");
    const link = b.closest("a")!;
    expect(link.getAttribute("data-health-badge")).toBe("pass");
    expect(link.getAttribute("href")).toBe("/costs/health");
    expect(within(link).getByText("OK").className).toContain("pill ok");
  });

  it("is amber on a warning, with the warning's reason", async () => {
    mockFetch({ "/cost/health": report("warn", [missingC6]) });
    badge();
    const link = (await screen.findByText(/claude_code_oauth_token #0/)).closest("a")!;
    expect(link.getAttribute("data-health-badge")).toBe("warn");
    expect(within(link).getByText("Warning").className).toContain("pill warn");
    expect(link.textContent).toContain("Cost basis set: No plan in force, and not the basis “Usage”, for: claude_code_oauth_token #0.");
  });

  it("is red on a failure, and names the failed check before any warning", async () => {
    mockFetch({ "/cost/health": report("fail", [staleC1, missingC6]) });
    badge();
    const link = (await screen.findByText(/werkbank/)).closest("a")!;
    expect(link.getAttribute("data-health-badge")).toBe("fail");
    expect(within(link).getByText("Failed").className).toContain("pill err");
    expect(link.textContent).toContain("Uploader fresh: Stopped pushing or switched off: operator @ werkbank (1 h 16 min).");
    expect(link.textContent).not.toContain("Cost basis set");
    expect(link.getAttribute("aria-label")).toContain("Failed");
  });

  it("says when the checks could not run, and is never green then", async () => {
    mockFetch({});
    badge();
    const b = await screen.findByText("Data checks unavailable");
    expect(b.closest("a")!.getAttribute("data-health-badge")).toBe("error");
    expect(screen.queryByText("All data checks passed")).toBeNull();
  });

  it("is never the old green when a refetch fails after a good answer: unavailable, the last result named with its time", async () => {
    mockFetch({ "/cost/health": report("pass") });
    const { qc } = badge();
    await screen.findByText("All data checks passed");
    mockFetch({});
    await qc.refetchQueries({ queryKey: ["cost", "health"] });
    const link = (await screen.findByText("Data checks unavailable")).closest("a")!;
    expect(link.getAttribute("data-health-badge")).toBe("error");
    expect(screen.queryByText("All data checks passed")).toBeNull();
    expect(link.querySelector(".pill.ok")).toBeNull();
    expect(link.querySelector("[data-health-last]")!.textContent).toMatch(/^Last result .+: OK$/);
  });

  it("speaks the reader's language", async () => {
    i18n.changeLanguage("de");
    mockFetch({ "/cost/health": report("fail", [staleC1]) });
    badge();
    const link = (await screen.findByText(/werkbank/)).closest("a")!;
    expect(link.textContent).toContain("Fehler");
    expect(link.textContent).toContain("Uploader aktuell: Sendet nicht mehr oder ist ausgeschaltet: operator @ werkbank");
  });

  it("stands beside the cost page's headline", async () => {
    mockFetch({ "/cost/health": report("fail", [staleC1]) });
    renderWithProviders(<Costs />, { route: "/costs", path: "/costs" });
    const h1 = await screen.findByRole("heading", { level: 1 });
    await waitFor(() => expect(h1.parentElement!.querySelector("[data-health-badge='fail']")).not.toBeNull());
  });
});

describe("the worst check and the incomplete parts", () => {
  it("takes the first check with the report's status, as the server does", () => {
    expect(worstCheck(report("pass"))).toBeNull();
    expect(worstCheck(report("warn", [missingC6]))?.id).toBe("c6");
    expect(worstCheck(report("fail", [staleC1, missingC6]))?.id).toBe("c1");
  });

  it("marks the outside part on a failed c1, the multiple on a failed c2 or c3, the token prices on a failed c4 or c5", () => {
    const ids = (cs: CostHealthCheck[]) => cs.map((c) => c.id);
    expect(incompleteParts(report("warn", [missingC6]))).toEqual({ outside: [], multiple: [], tokenPrice: [] });
    expect(ids(incompleteParts(report("fail", [staleC1])).outside)).toEqual(["c1"]);
    // A stale meter only warns: the multiple stays unmarked.
    expect(incompleteParts(report("warn", [check("c2", "warn", "stale", { reading_minutes: 180 })])).multiple).toEqual([]);
    expect(ids(incompleteParts(report("fail", [check("c2", "fail", "none")])).multiple)).toEqual(["c2"]);
    expect(ids(incompleteParts(report("fail", [check("c3", "fail", "drops", { drops: 1, last_drop_at: AT })])).multiple)).toEqual(["c3"]);
    expect(ids(incompleteParts(report("fail", [check("c4", "fail", "duplicates", { external_duplicates: 1, agent_duplicates: 0 })])).tokenPrice)).toEqual(["c4"]);
    expect(ids(incompleteParts(report("fail", [check("c5", "fail", "unpriced", { unpriced: ["x"], unpriced_outside: ["x"] })])).tokenPrice)).toEqual(["c5"]);
  });

  // Review round 2, finding 17: both token-price checks are kept, and each
  // counts only where the page holds the calls it concerns.
  it("keeps both token-price checks, and each only for the side whose calls it concerns", () => {
    const ids = (cs: CostHealthCheck[]) => cs.map((c) => c.id);
    const agentsDup = check("c4", "fail", "duplicates", { external_duplicates: 0, agent_duplicates: 2 });
    const outsideUnpriced = check("c5", "fail", "unpriced", { unpriced: ["x"], unpriced_outside: ["x"] });
    const both = report("fail", [agentsDup, outsideUnpriced]);
    expect(ids(incompleteParts(both).tokenPrice)).toEqual(["c4", "c5"]);
    expect(ids(incompleteParts(both, { agents: false, outside: true }).tokenPrice)).toEqual(["c5"]);
    expect(ids(incompleteParts(both, { agents: true, outside: false }).tokenPrice)).toEqual(["c4"]);
    const agentsUnpriced = check("c5", "fail", "unpriced", { unpriced: ["y"], unpriced_agents: ["y"] });
    expect(ids(incompleteParts(report("fail", [agentsUnpriced]), { agents: true, outside: false }).tokenPrice)).toEqual(["c5"]);
    expect(ids(incompleteParts(report("fail", [agentsUnpriced]), { agents: false, outside: true }).tokenPrice)).toEqual([]);
  });

  // Review round 2, finding 14: per credential, by the names the checks give.
  it("marks a credential's own figures for a reset on its meter, and its outside part for a stopped uploader of its seat", () => {
    const ids = (cs: CostHealthCheck[]) => cs.map((c) => c.id);
    const c3 = check("c3", "fail", "drops", { drops: 1, last_drop_at: AT, credentials: ["seat_b #0"] });
    const rep = report("fail", [staleC1, c3]);
    expect(ids(credentialIncomplete(rep, "seat_b #0", null).multiple)).toEqual(["c3"]);
    expect(ids(credentialIncomplete(rep, "claude_code_oauth_token #0", null).multiple)).toEqual([]);
    // A seat that spent with no reading: c2 names the worst one.
    const c2 = check("c2", "fail", "none", { credential: "seat_b #0" });
    expect(ids(credentialIncomplete(report("fail", [c3, c2]), "seat_b #0", null).multiple)).toEqual(["c3", "c2"]);
    expect(ids(credentialIncomplete(report("fail", [c2]), "claude_code_oauth_token #0", null).multiple)).toEqual([]);
    // staleC1's host is of the source "operator".
    expect(ids(credentialIncomplete(rep, "seat_b #0", ["operator"]).outside)).toEqual(["c1"]);
    expect(ids(credentialIncomplete(rep, "seat_b #0", ["projects"]).outside)).toEqual([]);
    expect(ids(credentialIncomplete(rep, "seat_b #0", null).outside)).toEqual([]);
  });

  it("says incomplete with the reason, linking to the checks; nothing without a check", () => {
    renderWithProviders(<IncompleteMark checks={[staleC1]} part="outside" />, { route: "/costs", path: "/costs" });
    const mark = screen.getByText("incomplete");
    expect(mark.getAttribute("href")).toBe("/costs/health");
    expect(mark.getAttribute("aria-label")).toContain("Incomplete: the sessions outside covey. Uploader fresh: Stopped pushing");
    cleanup();
    renderWithProviders(<IncompleteMark checks={[]} part="outside" />, { route: "/costs", path: "/costs" });
    expect(screen.queryByText("incomplete")).toBeNull();
  });

  it("names the credentials of a drop, and says when uploads ended longer ago than the page reaches", () => {
    const tt = i18n.getFixedT("en");
    expect(reasonText(tt, check("c3", "fail", "drops", { drops: 1, last_drop_at: AT, credentials: ["a #0", "b #1"] })))
      .toContain("The weekly meter of a #0, b #1 dropped inside one week 1×");
    expect(reasonText(tt, check("c1", "pass", "gone", { last_push_minutes: 91 * 24 * 60 })))
      .toMatch(/^No machine has pushed for .+, longer than the longest range the cost page shows/);
  });
});

describe("/costs/health", () => {
  it("keeps the last result after a failed refetch, says so, and drops its status from the title", async () => {
    mockFetch({ "/cost/health": report("pass") });
    const { qc } = renderWithProviders(<CostHealthPage />, { route: "/costs/health", path: "/costs/health" });
    await waitFor(() => expect(document.querySelector("[data-health-table]")).not.toBeNull());
    expect(document.querySelector("h1")!.parentElement!.querySelector("[data-health-status]")).not.toBeNull();
    mockFetch({});
    await qc.refetchQueries({ queryKey: ["cost", "health"] });
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/^The checks could not run again\. Below: the result from .+, possibly out of date\.$/);
    expect(document.querySelector("[data-health-table]")).not.toBeNull();
    expect(document.querySelector("h1")!.parentElement!.querySelector("[data-health-status]")).toBeNull();
  });

  it("words the reasons of round 1: per seat, early resets, the sync that stopped, plurals, a nameless model", async () => {
    mockFetch({ "/cost/health": report("fail", [
      check("c1", "info", "quiet", { quiet_hosts: [{ source: "operator", host: "notebook", pushed_at: AT, minutes: 180 }] }),
      check("c2", "warn", "stale", { credential: "claude_code_oauth_token #0", reading_minutes: 150, week_percent: 30, fleet_killed: true }),
      check("c3", "fail", "early_reset", { drops: 0, early_resets: 1, last_drop_at: AT, credentials: ["claude_code_oauth_token #0"] }),
      check("c4", "fail", "duplicates", { external_duplicates: 1, agent_duplicates: 0 }),
      check("c5", "fail", "unpriced", { models: 2, unpriced: ["", "claude-x"] }),
      check("c6", "warn", "unattributed", { unattributed: 1 }),
      check("c7", "warn", "stopped", { last_attempt_minutes: 121, last_success_at: "2026-11-20T09:00:00Z" }),
      check("c8", "info", "pre_meter", { calls: 1 }),
    ]) });
    renderWithProviders(<CostHealthPage />, { route: "/costs/health", path: "/costs/health" });
    await waitFor(() => expect(document.querySelector("[data-health-table]")).not.toBeNull());
    const row = (id: string) => document.querySelector(`[data-health-check='${id}']`)!.textContent;
    expect(row("c1")).toContain("No push for a while from operator @ notebook (3 h).");
    expect(row("c2")).toContain("claude_code_oauth_token #0 spent more than 2 h after its last weekly reading (2 h 30 min ago, 30 %). " +
      "The fleet is paused, so no run reads the meter: add one with “Enter a meter reading” at the credential on the cost page.");
    expect(row("c3")).toContain("The weekly meter of claude_code_oauth_token #0 started over before its reset time 1×");
    expect(row("c4")).toContain("1 call booked more than once: 1 outside, 0 agent lines.");
    expect(row("c5")).toContain("No list price for: (no model name), claude-x.");
    expect(row("c6")).toContain("1 agent line in 7 days names no credential");
    expect(row("c7")).toContain("The bill sync has not run for 2 h 1 min (it runs every hour); last success 3 h ago.");
    expect(row("c8")).toContain("1 outside call lies before its credential’s first meter reading");
  });

  it("lists every check with its status and reason, and when they ran", async () => {
    mockFetch({ "/cost/health": report("warn", [check("c7", "warn", "failed", {
      sync_status: "failed", last_success_at: "2026-11-20T10:00:00Z", last_error: "analytics: 401" })]) });
    renderWithProviders(<CostHealthPage />, { route: "/costs/health", path: "/costs/health" });
    await screen.findByText("Cost data checks");
    await waitFor(() => expect(document.querySelector("[data-health-table]")).not.toBeNull());
    const rows = document.querySelectorAll("[data-health-check]");
    expect([...rows].map((r) => r.getAttribute("data-health-check"))).toEqual(["c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8"]);
    const c7 = document.querySelector("[data-health-check='c7']")!;
    expect(c7.textContent).toContain("The last bill sync failed; last success 2 h ago.");
    expect(c7.querySelector("code")!.textContent).toBe("analytics: 401");
    expect(document.querySelector("[data-health-check='c8'] [data-health-status]")!.getAttribute("data-health-status")).toBe("info");
    expect(document.querySelector("[data-health-check='c8']")!.textContent).toContain("7 outside calls lie before their credential’s first meter reading");
    expect(document.querySelector("[data-health-checked]")).not.toBeNull();
  });
});
