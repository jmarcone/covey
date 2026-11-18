import { afterEach, describe, expect, it } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import i18n, { ladeSprache } from "../i18n";
import { LANGS } from "../langs";
import { getHelpTopics, topicForPath } from "../help";
import { renderWithProviders } from "../test/render";
import HelpDrawer from "./HelpDrawer";

/* The help is written in German and English. German readers get German,
   everyone else English — before, every locale but English got German, and
   a French reader opened a drawer full of it. */

afterEach(async () => {
  cleanup();
  await i18n.changeLanguage("de");
});

describe("getHelpTopics", () => {
  it("gives German to German readers and English to every other locale", () => {
    const de = getHelpTopics("de");
    const en = getHelpTopics("en");
    expect(de[0].title).toBe("Erste Schritte");
    expect(en[0].title).toBe("Getting Started");
    for (const lang of LANGS) {
      expect(getHelpTopics(lang), lang).toBe(lang === "de" ? de : en);
    }
  });

  it("keeps the same topic ids in both languages, so topicForPath fits either", () => {
    expect(getHelpTopics("de").map((t) => t.id)).toEqual(getHelpTopics("en").map((t) => t.id));
    expect(topicForPath("/egress")).toBe("egress");
    expect(topicForPath("/nowhere")).toBe("erste-schritte");
  });
});

describe("HelpDrawer", () => {
  it("shows English topics in French, says so, and marks them lang=en", async () => {
    await ladeSprache("fr");
    renderWithProviders(<HelpDrawer open onClose={() => {}} />, { route: "/egress" });
    expect(screen.getByRole("dialog", { name: "Aide" })).toBeTruthy();
    expect(screen.getByText("Les rubriques d'aide sont rédigées en anglais.")).toBeTruthy();
    const topic = screen.getByText("Getting Started");
    expect(topic.closest("[lang]")?.getAttribute("lang")).toBe("en");
    expect(screen.queryByText("Erste Schritte")).toBeNull();
  });

  it("shows English topics in Japanese too", async () => {
    await ladeSprache("ja");
    renderWithProviders(<HelpDrawer open onClose={() => {}} />);
    expect(screen.getByText("Getting Started")).toBeTruthy();
    expect(screen.queryByText("Erste Schritte")).toBeNull();
  });

  it("shows German topics in German without the note", async () => {
    await ladeSprache("de");
    renderWithProviders(<HelpDrawer open onClose={() => {}} />);
    expect(screen.getByText("Erste Schritte")).toBeTruthy();
    expect(screen.queryByText("Die Hilfethemen sind auf Deutsch geschrieben.")).toBeNull();
  });
});
