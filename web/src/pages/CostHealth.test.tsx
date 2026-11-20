import { describe, it, expect, beforeEach, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import type { CostHealthCheck, CostHealthReport } from "../api";
import i18n from "../i18n";
import { mockFetch, renderWithProviders } from "../test/render";
import Costs from "./Costs";
import CostHealthPage, { CostHealthBadge, IncompleteMark, incompleteParts, worstCheck } from "./CostHealth";

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
    expect(link.textContent).toContain("Cost basis set: No cost basis or plan in force for: claude_code_oauth_token #0.");
  });

  it("is red on a failure, and names the failed check before any warning", async () => {
    mockFetch({ "/cost/health": report("fail", [staleC1, missingC6]) });
    badge();
    const link = (await screen.findByText(/werkbank/)).closest("a")!;
    expect(link.getAttribute("data-health-badge")).toBe("fail");
    expect(within(link).getByText("Failed").className).toContain("pill err");
    expect(link.textContent).toContain("Uploader fresh: Stopped pushing: operator @ werkbank (1 h 16 min).");
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

  it("speaks the reader's language", async () => {
    i18n.changeLanguage("de");
    mockFetch({ "/cost/health": report("fail", [staleC1]) });
    badge();
    const link = (await screen.findByText(/werkbank/)).closest("a")!;
    expect(link.textContent).toContain("Fehler");
    expect(link.textContent).toContain("Uploader aktuell: Sendet nicht mehr: operator @ werkbank");
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

  it("marks the outside part on a failed c1 and the multiple on a failed c2 or c3", () => {
    expect(incompleteParts(report("warn", [missingC6]))).toEqual({ outside: null, multiple: null });
    expect(incompleteParts(report("fail", [staleC1])).outside?.id).toBe("c1");
    // A stale meter only warns: the multiple stays unmarked.
    expect(incompleteParts(report("warn", [check("c2", "warn", "stale", { reading_minutes: 180 })])).multiple).toBeNull();
    expect(incompleteParts(report("fail", [check("c2", "fail", "none")])).multiple?.id).toBe("c2");
    expect(incompleteParts(report("fail", [check("c3", "fail", "drops", { drops: 1, last_drop_at: AT })])).multiple?.id).toBe("c3");
  });

  it("says incomplete with the reason, linking to the checks", () => {
    renderWithProviders(<IncompleteMark check={staleC1} part="outside" />, { route: "/costs", path: "/costs" });
    const mark = screen.getByText("incomplete");
    expect(mark.getAttribute("href")).toBe("/costs/health");
    expect(mark.getAttribute("aria-label")).toContain("Incomplete: the sessions outside covey. Uploader fresh: Stopped pushing");
  });
});

describe("/costs/health", () => {
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
    expect(document.querySelector("[data-health-check='c8']")!.textContent).toContain("7 outside calls lie before the first meter reading");
    expect(document.querySelector("[data-health-checked]")).not.toBeNull();
  });
});
