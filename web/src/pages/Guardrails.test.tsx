import { describe, it, expect, beforeEach } from "vitest";
import { screen } from "@testing-library/react";
import i18n from "../i18n";
import Guardrails from "./Guardrails";
import { mockFetch, renderWithProviders, testPrincipal } from "../test/render";

// #83: a budget cap under a cent read "≤ <$0.01" — "at most less than a cent".

const rule = (id: string, usd: number) => ({
  id, scope_level: "org", rule_type: "budget_limit", pattern: "*", params: { usd }, enabled: true, created_at: "2026-11-17T10:00:00Z",
});

describe("Guardrails: budget caps", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("writes a cap under a cent with its digits, never ≤ <$0.01", async () => {
    mockFetch({ "/api/v1/guardrails": [rule("r1", 0.0042), rule("r2", 25)], "/api/v1/agents": [] });
    renderWithProviders(<Guardrails me={testPrincipal("viewer")} />);
    expect(await screen.findByText("≤ $0.0042")).toBeInTheDocument();
    expect(screen.getByText("≤ $25.00")).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/≤\s*</);
  });
});
