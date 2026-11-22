import { beforeEach, describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import { Onboarding } from "./Onboarding";
import { rememberView } from "../pages/agents/views";
import { mockFetch, renderWithProviders, testPrincipal, useGerman } from "../test/render";

const STEPS = ["credential", "agent", "config", "task", "run"];

beforeEach(() => {
  useGerman();
  localStorage.clear();
});

describe("Onboarding", () => {
  /* The root opens the view chosen last, and only the list has the button
     that creates an agent and the way to each of them. */
  for (const [i, open] of STEPS.slice(1).entries()) {
    it(`leads the ${open} step to the list, whatever view was chosen`, async () => {
      const me = { ...testPrincipal(), Role: "org_admin" as const };
      rememberView(me.ID, "org");
      mockFetch({
        "/api/v1/onboarding": { done: false, steps: STEPS.map((key, n) => ({ key, done: n <= i })) },
      });
      renderWithProviders(<Onboarding me={me} />, { route: "/agents" });
      expect(await screen.findByRole("link")).toHaveAttribute("href", "/agents");
    });
  }
});
