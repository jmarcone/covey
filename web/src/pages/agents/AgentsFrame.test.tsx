import { beforeEach, describe, expect, it } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, useSearchParams } from "react-router";
import AgentsFrame from "./AgentsFrame";
import { fromView, originOf, viewUrl, viewsFor } from "./views";
import type { OrgChart } from "../../api";
import { mockFetch, useGerman } from "../../test/render";

const ID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";

function bar(route: string | { pathname: string; search?: string; state?: unknown }, teamSurface = true) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 0 } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[route]}>
        <AgentsFrame teamSurface={teamSurface} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const chart = {
  departments: [{ id: "dev", org_id: "o", name: "Entwicklung", description: "", color: "", leads: [], created_at: "" }],
  humans: [],
  agents: [
    { id: "lead", slug: "lead", display_name: "Chefin", department_id: "dev" },
    { id: ID, slug: "cc", display_name: "Cost Controller", department_id: "dev", supervisor_id: "lead" },
  ],
} as unknown as OrgChart;

const switchLinks = () => within(screen.getByRole("navigation", { name: "Ansichten der Agenten" })).getAllByRole("link");

beforeEach(() => {
  useGerman();
  sessionStorage.clear();
  mockFetch({ [`/api/v1/agents/${ID}`]: { id: ID, display_name: "Cost Controller" } });
});

describe("AgentsFrame", () => {
  it("marks the office at the root and the list at /agents", () => {
    const office = bar("/");
    expect(switchLinks().map((a) => [a.textContent, a.getAttribute("aria-current")])).toEqual([
      ["Büro", "page"],
      ["Liste", null],
    ]);
    office.unmount();

    bar("/agents");
    expect(switchLinks().map((a) => [a.textContent, a.getAttribute("aria-current")])).toEqual([
      ["Büro", null],
      ["Liste", "page"],
    ]);
  });

  it("offers no office without the team surface", () => {
    bar("/agents", false);
    expect(switchLinks().map((a) => a.textContent)).toEqual(["Liste"]);
  });

  it("marks the view an agent's page was reached from, and names the agent after it", async () => {
    bar({ pathname: `/agents/${ID}`, state: fromView("office") });
    const [office, list] = switchLinks();
    // Marked as the way back, but not the current page.
    expect(office).toHaveAttribute("title", "Zurück: Büro");
    expect(office).toHaveClass("on", "back");
    expect(office).not.toHaveAttribute("aria-current");
    expect(list).not.toHaveClass("on");
    await waitFor(() => expect(screen.getByText("Cost Controller")).toBeInTheDocument());
  });

  it("falls back to the list for a bookmark, and for an origin the installation does not offer", () => {
    const bookmark = bar(`/agents/${ID}`);
    expect(switchLinks()[1]).toHaveClass("on", "back");
    bookmark.unmount();

    bar({ pathname: `/agents/${ID}`, state: fromView("office") }, false);
    expect(switchLinks()).toHaveLength(1);
    expect(switchLinks()[0]).toHaveClass("on", "back");
  });

  it("leads back to a view with the filters it had", () => {
    const list = bar("/agents?status=working");
    list.unmount();
    bar({ pathname: `/agents/${ID}`, state: fromView("list") });
    expect(switchLinks()[1].getAttribute("href")).toBe("/agents?status=working");
    expect(switchLinks()[0].getAttribute("href")).toBe("/");
  });

  /* A tab of the agent's page pushes an entry of its own, without the router
     state that named the origin: the origin must survive it. */
  it("keeps the origin marked when a tab of the agent's page changes the address", () => {
    function TabSwitch() {
      const [, setSp] = useSearchParams();
      // As the agent's page does: a new history entry, no state.
      return <button onClick={() => setSp({ tab: "memory" }, { replace: false })}>Gedächtnis</button>;
    }
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 0 } } });
    render(
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={[{ pathname: `/agents/${ID}`, state: fromView("office") }]}>
          <AgentsFrame teamSurface />
          <TabSwitch />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(switchLinks()[0]).toHaveClass("on", "back");
    fireEvent.click(screen.getByRole("button", { name: "Gedächtnis" }));
    const [office, list] = switchLinks();
    expect(office).toHaveClass("on", "back");
    expect(list).not.toHaveClass("on");
  });

  /* The crumb is the bar's second half. The switch is its root: its marked
     segment leads back, so there is no "Agents" step of its own, and the
     agent's page below draws no path of its own either. */
  describe("the organisation path", () => {
    const withChart = () =>
      mockFetch({ [`/api/v1/agents/${ID}`]: { id: ID, display_name: "Cost Controller" }, "/api/v1/org/chart": chart });
    const crumb = () => screen.findByRole("navigation", { name: "Organisationspfad" });

    it("follows the switch and ends in the agent, with one request for the chart", async () => {
      const netz = withChart();
      bar({ pathname: `/agents/${ID}`, state: fromView("list") });
      const pfad = await crumb();
      await within(pfad).findByRole("link", { name: "Entwicklung" });
      expect(within(pfad).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["Entwicklung", "Chefin", "Cost Controller"]);
      expect(pfad.previousElementSibling).toBe(screen.getByRole("navigation", { name: "Ansichten der Agenten" }));
      expect(netz.calls.filter((c) => c.includes("/org/chart"))).toHaveLength(1);
    });

    it("has no second Agents root next to the switch", async () => {
      withChart();
      bar({ pathname: `/agents/${ID}`, state: fromView("list") });
      await within(await crumb()).findByRole("link", { name: "Entwicklung" });
      expect(screen.queryByRole("link", { name: "Agenten" })).not.toBeInTheDocument();
      expect(screen.getAllByRole("link").map((a) => a.textContent)).toEqual(["Büro", "Liste", "Entwicklung", "Chefin"]);
    });

    it("leads the department step to the office the page came from", async () => {
      withChart();
      bar({ pathname: `/agents/${ID}`, state: fromView("office") });
      expect(await screen.findByRole("link", { name: "Entwicklung" })).toHaveAttribute("href", "/?dept=dev");
    });

    it("never leads the department step to the office without the team surface", async () => {
      withChart();
      bar({ pathname: `/agents/${ID}`, state: fromView("office") }, false);
      expect(await screen.findByRole("link", { name: "Entwicklung" })).toHaveAttribute("href", "/agents?dept=dev");
    });
  });

  it("shows no crumb on a view", () => {
    bar("/agents");
    expect(screen.queryByText("/")).not.toBeInTheDocument();
  });
});

describe("views", () => {
  beforeEach(() => sessionStorage.clear());

  it("offers the office only with the team surface", () => {
    expect(viewsFor(true)).toEqual(["office", "list"]);
    expect(viewsFor(false)).toEqual(["list"]);
  });

  it("ignores a remembered address that no longer belongs to the view", () => {
    sessionStorage.setItem("covey.agents.url.list", "/costs");
    expect(viewUrl("list")).toBe("/agents");
    sessionStorage.setItem("covey.agents.url.list", "/agentsfoo?x=1");
    expect(viewUrl("list")).toBe("/agents");
    sessionStorage.setItem("covey.agents.url.list", "/agents?sort=name");
    expect(viewUrl("list")).toBe("/agents?sort=name");
  });

  // A department id belongs to one organisation, and the tab outlives a switch of it.
  it("remembers no ?dept= of either view, but the other filters", () => {
    bar("/?dept=dev&x=1").unmount();
    expect(viewUrl("office")).toBe("/?x=1");
    bar("/?dept=dev").unmount();
    expect(viewUrl("office")).toBe("/");
    bar("/agents?dept=dev&sort=name").unmount();
    expect(viewUrl("list")).toBe("/agents?sort=name");
    bar("/agents?dept=dev");
    expect(viewUrl("list")).toBe("/agents");
  });

  it("reads the origin from the router state", () => {
    expect(originOf(undefined, fromView("office"), true)).toBe("office");
    expect(originOf(undefined, fromView("office"), false)).toBe("list");
    expect(originOf(undefined, null, true)).toBe("list");
    expect(originOf(undefined, { from: "members" }, true)).toBe("list");
  });

  it("remembers the origin per agent once the router state is gone", () => {
    const other = "ffffffff-ffff-ffff-ffff-ffffffffffff";
    expect(originOf(ID, fromView("office"), true)).toBe("office");
    expect(originOf(ID, null, true)).toBe("office");
    // Another agent has an origin of its own, here none.
    expect(originOf(other, null, true)).toBe("list");
    // A new way in replaces the old one.
    expect(originOf(ID, fromView("list"), true)).toBe("list");
    expect(originOf(ID, null, true)).toBe("list");
    // A stored office the installation does not offer still falls back.
    originOf(ID, fromView("office"), true);
    expect(originOf(ID, null, false)).toBe("list");
  });
});
