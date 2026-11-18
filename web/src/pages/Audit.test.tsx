import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import i18n from "../i18n";
import Audit from "./Audit";
import { mockFetch, renderWithProviders } from "../test/render";

/* The audit table wrote its times with `toLocaleString()` and no locale, so
   they followed the browser: a German browser with the UI in English showed
   `18.11.2026, 12:00:00` next to English column heads (#21). The date follows
   the language of the UI now, whatever the browser asks for. */

const eintrag = {
  id: 1,
  actor_email: "admin@covey.local",
  actor_role: "org_admin",
  method: "POST",
  path: "/api/v1/agents",
  status: 200,
  created_at: "2026-11-18T12:00:00Z",
};

describe("Audit — Datum", () => {
  beforeEach(() => {
    // The browser asks for German whatever the UI says.
    vi.spyOn(navigator, "language", "get").mockReturnValue("de-DE");
    vi.spyOn(navigator, "languages", "get").mockReturnValue(["de-DE", "de"]);
  });
  afterEach(async () => {
    vi.restoreAllMocks();
    await i18n.changeLanguage("en");
  });

  it("steht auf der englischen Oberfläche englisch, im deutschen Browser", async () => {
    await i18n.changeLanguage("en");
    mockFetch({ "/api/v1/audit": [eintrag], "/api/v1/audit/conversations": [] });
    const zeit = vi.spyOn(Date.prototype, "toLocaleString");
    renderWithProviders(<Audit />);
    expect(await screen.findByText(/^11\/18\/2026/)).toBeInTheDocument();
    // Not by luck of the test machine's default: the call carries the locale.
    expect(zeit).toHaveBeenCalled();
    for (const call of zeit.mock.calls) expect(call[0]).toBe("en");
  });

  it("steht auf der deutschen Oberfläche deutsch", async () => {
    await i18n.changeLanguage("de");
    mockFetch({ "/api/v1/audit": [eintrag], "/api/v1/audit/conversations": [] });
    renderWithProviders(<Audit />);
    expect(await screen.findByText(/^18\.11\.2026/)).toBeInTheDocument();
  });
});
