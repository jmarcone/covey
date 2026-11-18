import { useEffect } from "react";
import { useLocation, useNavigate } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import Rail, { agentsEntryTarget, railPlace, rememberAgentsView } from "./Rail";
import { mockFetch, renderWithProviders, testPrincipal, useGerman } from "../test/render";

beforeEach(() => {
  useGerman();
  localStorage.clear();
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
    ["/org", true, "admin"],
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
  it("opens the office with the team surface, the list without", () => {
    expect(agentsEntryTarget(true)).toBe("/");
    expect(agentsEntryTarget(false)).toBe("/agents");
  });

  it("opens the view used last, but never an office the installation does not have", () => {
    rememberAgentsView("/agents");
    expect(agentsEntryTarget(true)).toBe("/agents");
    rememberAgentsView(`/agents/${AGENT}`); // a detail is no view: nothing changes
    expect(agentsEntryTarget(true)).toBe("/agents");
    rememberAgentsView("/");
    expect(agentsEntryTarget(true)).toBe("/");
    expect(agentsEntryTarget(false)).toBe("/agents");
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
  it("reads Agents, Team, Workflows, Notes, Admin with the team surface, and no office", async () => {
    renderRail(true, "/");
    expect(orte()).toEqual(["agents", "team", "workflows", "notes", "admin"]);
    const agenten = screen.getByRole("link", { name: /^Agenten/ });
    expect(agenten).toHaveAttribute("aria-current", "page");
    expect(agenten.getAttribute("href")).toBe("/");
    // The office's number moved with it: open decisions.
    await waitFor(() => expect(agenten.querySelector(".tm-rail-zahl")).toHaveTextContent("3"));
    expect(agenten).toHaveAccessibleName("Agenten · 3 Punkte warten auf eine Entscheidung.");
    expect(screen.queryByRole("link", { name: /^Büro/ })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Verwaltung" }).getAttribute("href")).toBe("/org");
  });

  it("puts the inbox in the team's slot without the team surface, with the count there", async () => {
    renderRail(false, "/inbox");
    expect(orte()).toEqual(["agents", "inbox", "workflows", "notes", "admin"]);
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

  it("keeps Admin marked on /costs while costs are a page of its column", () => {
    renderRail(true, "/costs");
    expect(screen.getByRole("link", { name: "Verwaltung" })).toHaveAttribute("aria-current", "page");
  });

  /* The shell writes the view down in an effect, after the rail has
     rendered: the link must follow the address, not lag one view behind. */
  it("leads the Agents entry to the view on screen while moving between office and list", () => {
    function Shell() {
      const path = useLocation().pathname;
      const go = useNavigate();
      // As AppShell does: remember the view after rendering.
      useEffect(() => rememberAgentsView(path), [path]);
      return (
        <>
          <Rail me={{ ...testPrincipal(), TeamSurface: true }} onLogout={() => {}} onHelp={() => {}} onSearch={() => {}} />
          <button onClick={() => go("/agents")}>zur Liste</button>
          <button onClick={() => go("/")}>zum Büro</button>
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
    renderWithProviders(<Shell />, { route: "/" });
    const ziel = () => screen.getByRole("link", { name: /^Agenten/ }).getAttribute("href");

    expect(ziel()).toBe("/");
    fireEvent.click(screen.getByRole("button", { name: "zur Liste" }));
    expect(ziel()).toBe("/agents");
    fireEvent.click(screen.getByRole("button", { name: "zum Büro" }));
    expect(ziel()).toBe("/");
    // Elsewhere the entry opens the view used last.
    fireEvent.click(screen.getByRole("button", { name: "zur Liste" }));
    fireEvent.click(screen.getByRole("button", { name: "zu den Workflows" }));
    expect(ziel()).toBe("/agents");
  });
});
