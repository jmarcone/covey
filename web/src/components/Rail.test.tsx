import { readFileSync } from "node:fs";
import { useNavigate } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { NavIcon } from "./navicons";
import Rail, { agentsEntryTarget, railPlace } from "./Rail";
import { forgetUnsavedViews, rememberView } from "../pages/agents/views";
import { mockFetch, renderWithProviders, testPrincipal, useGerman } from "../test/render";

beforeEach(() => {
  useGerman();
  localStorage.clear();
  forgetUnsavedViews();
});

const AGENT = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa";

/* Agents are a main view with their own rail entry: the office, the list
   and every agent's page belong to it, whichever shell renders them. */
describe("railPlace", () => {
  const faelle: [string, boolean, string][] = [
    ["/", true, "agents"],
    ["/agents", true, "agents"],
    ["/agents/board", true, "agents"],
    [`/agents/${AGENT}`, true, "agents"],
    ["/team", true, "team"],
    [`/team/${AGENT}`, true, "team"],
    ["/team/notes", true, "notes"],
    ["/notes", false, "notes"],
    ["/inbox", false, "inbox"],
    ["/workflows/x", true, "workflows"],
    ["/costs", true, "costs"],
    ["/costs/details", false, "costs"],
    ["/org", true, "agents"],
    ["/org", false, "agents"],
    // The old address of the platform, not the chart.
    ["/orgs", true, "admin"],
    ["/targets", true, "admin"],
    // Not a prefix match on the letters: /agentsfoo is not the area.
    ["/agentsfoo", true, "admin"],
    // With the team surface /inbox leads to /team.
    ["/inbox", true, "team"],
  ];
  for (const [pfad, team, ort] of faelle) {
    it(`${pfad} (team surface ${team ? "on" : "off"}) → ${ort}`, () => {
      expect(railPlace(pfad, team)).toBe(ort);
    });
  }
});

describe("the Agents entry's target", () => {
  const ADA = "aaaaaaaa-0000-0000-0000-000000000001";
  const BOB = "bbbbbbbb-0000-0000-0000-000000000002";

  it("opens the list while nothing is chosen, with or without the team surface", () => {
    expect(agentsEntryTarget(ADA, true)).toBe("/agents");
    expect(agentsEntryTarget(ADA, false)).toBe("/agents");
  });

  it("follows the choice made in the switch, but never an office the installation does not have", () => {
    rememberView(ADA, "office");
    expect(agentsEntryTarget(ADA, true)).toBe("/");
    expect(agentsEntryTarget(ADA, false)).toBe("/agents");
    rememberView(ADA, "list");
    expect(agentsEntryTarget(ADA, true)).toBe("/agents");
    rememberView(ADA, "org");
    expect(agentsEntryTarget(ADA, true)).toBe("/org");
    expect(agentsEntryTarget(ADA, false)).toBe("/org");
  });

  it("is the choice of the principal on screen, not of the one who used the browser before", () => {
    rememberView(ADA, "list");
    rememberView(BOB, "office");
    expect(agentsEntryTarget(BOB, true)).toBe("/");
    expect(agentsEntryTarget(ADA, true)).toBe("/agents");
  });
});

// A phone on its side, or high zoom: jsdom lays nothing out, so the stylesheet is read.
describe("the rail in a short window", () => {
  const css = readFileSync(`${process.cwd()}/src/app.css`, "utf8");
  const media = (px: number) => css.split(`@media (max-height: ${px}px) {`)[1]?.split("\n}")[0] ?? "";

  it("gives up air and keeps the mark of the current place", () => {
    expect(media(500)).toContain(".tm-rail { gap: 1px;");
    expect(media(500)).toContain(".tm-rail-item.on::before { top: min(10px, 24%); bottom: min(10px, 24%); }");
  });

  it("gives up the bird before search leaves the window", () => {
    expect(media(300)).toContain(".tm-rail-mark { display: none; }");
  });
});

function renderRail(teamSurface: boolean, route: string, pending = 3) {
  mockFetch({
    "/api/v1/inbox": { items: [], pending },
    "/api/v1/me/threads": { threads: [] },
    "/api/v1/setup/state": {},
  });
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  localStorage.setItem("covey.tour.team", "1");
  return renderWithProviders(
    <Rail me={{ ...testPrincipal(), TeamSurface: teamSurface }} onLogout={() => {}} onHelp={() => {}} onSearch={() => {}} />,
    { route },
  );
}

const orte = () =>
  within(screen.getByRole("navigation", { name: "Bereiche" }))
    .getAllByRole("link")
    .filter((a) => a.classList.contains("tm-rail-item"))
    .map((a) => a.getAttribute("data-tour"));

describe("Rail", () => {
  it("reads Agents, Team, Workflows, Notes, Costs, Admin with the team surface, and no office", async () => {
    renderRail(true, "/");
    expect(orte()).toEqual(["agents", "team", "workflows", "notes", "costs", "admin"]);
    const agenten = screen.getByRole("link", { name: /^Agenten/ });
    expect(agenten).toHaveAttribute("aria-current", "page");
    expect(agenten.getAttribute("href")).toBe("/agents");
    // The office's number moved with it: open decisions.
    await waitFor(() => expect(agenten.querySelector(".tm-rail-zahl")).toHaveTextContent("3"));
    expect(agenten).toHaveAccessibleName("Agenten · 3 Punkte warten auf eine Entscheidung.");
    expect(screen.queryByRole("link", { name: /^Büro/ })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Verwaltung" }).getAttribute("href")).toBe("/targets");
  });

  it("puts the inbox in the team's slot without the team surface, with the count there", async () => {
    renderRail(false, "/inbox");
    expect(orte()).toEqual(["agents", "inbox", "workflows", "notes", "costs", "admin"]);
    const eingang = screen.getByRole("link", { name: /^Posteingang/ });
    expect(eingang).toHaveAttribute("aria-current", "page");
    await waitFor(() => expect(eingang.querySelector(".tm-rail-zahl")).toHaveTextContent("3"));
    const agenten = screen.getByRole("link", { name: "Agenten" });
    expect(agenten.querySelector(".tm-rail-zahl")).toBeNull();
    expect(agenten.getAttribute("href")).toBe("/agents");
  });

  it("marks Agents on an agent's page and Admin on an admin page", () => {
    renderRail(true, `/agents/${AGENT}`);
    expect(screen.getByRole("link", { name: /^Agenten/ })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Verwaltung" })).not.toHaveAttribute("aria-current");
  });

  for (const route of ["/costs", "/costs/health"]) {
    it(`marks Costs and not Admin on ${route}`, async () => {
      renderRail(true, route);
      const kosten = screen.getByRole("link", { name: "Kosten" });
      expect(kosten).toHaveAttribute("aria-current", "page");
      expect(kosten).toHaveAttribute("href", "/costs");
      const chart = render(<NavIcon name="chart" />).container.querySelector(".ic")!;
      expect(kosten.querySelector(".ic")!.innerHTML).toBe(chart.innerHTML);
      expect(screen.getByRole("link", { name: "Verwaltung" })).not.toHaveAttribute("aria-current");
      // No badge, whatever waits elsewhere.
      await waitFor(() => expect(screen.getByRole("link", { name: /^Agenten ·/ })).toBeInTheDocument());
      expect(kosten.querySelector(".tm-rail-zahl")).toBeNull();
    });
  }

  it("marks Agents and not Admin on the org chart", () => {
    renderRail(false, "/org");
    expect(screen.getByRole("link", { name: "Agenten" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Verwaltung" })).not.toHaveAttribute("aria-current");
  });

  // Without an office the mark is the Agents entry: both open the chosen view.
  it("leads the mark to the chosen view without the team surface", () => {
    rememberView(testPrincipal().ID, "org");
    renderRail(false, "/workflows");
    expect(screen.getByRole("link", { name: "covey" })).toHaveAttribute("href", "/org");
  });

  /* The entry follows the choice, whatever the address: the same view the
     root opens, so the two cannot disagree. */
  it("leads the Agents entry to the chosen view wherever the person is", () => {
    function Shell() {
      const go = useNavigate();
      return (
        <>
          <Rail me={{ ...testPrincipal(), TeamSurface: true }} onLogout={() => {}} onHelp={() => {}} onSearch={() => {}} />
          <button onClick={() => go("/agents")}>zur Liste</button>
          <button onClick={() => go("/workflows")}>zu den Workflows</button>
        </>
      );
    }
    mockFetch({
      "/api/v1/inbox": { items: [], pending: 0 },
      "/api/v1/me/threads": { threads: [] },
      "/api/v1/setup/state": {},
    });
    vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
    localStorage.setItem("covey.tour.team", "1");
    rememberView(testPrincipal().ID, "office");
    renderWithProviders(<Shell />, { route: "/workflows" });
    const ziel = () => screen.getByRole("link", { name: /^Agenten/ }).getAttribute("href");

    expect(ziel()).toBe("/");
    // Reaching the list by another way (Setup, an unknown address) is no choice.
    fireEvent.click(screen.getByRole("button", { name: "zur Liste" }));
    expect(ziel()).toBe("/");
    fireEvent.click(screen.getByRole("button", { name: "zu den Workflows" }));
    expect(ziel()).toBe("/");
  });
});
