import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { AgentCrumb } from "./AgentCrumb";
import type { AgentsView } from "../agents/views";
import { mockFetch, useGerman } from "../../test/render";
import type { Agent, Human, OrgChart } from "../../api";

const human = (id: string, extra: Partial<Human> = {}): Human =>
  ({ id, org_id: "o", email: `${id}@x`, display_name: id, role: "org_admin", job_title: "", identities: {}, phone: "", responsibilities: "", custom: {}, created_at: "", ...extra }) as Human;
const agent = (id: string, extra: Partial<Agent> = {}): Agent =>
  ({ id, slug: id, display_name: id, runtime: "claude-code", model: "", status: "sleeping", job_title: "", killed: false, budget_usd: 0, created_at: "", ...extra }) as Agent;

const chart: OrgChart = {
  departments: [
    { id: "dev", org_id: "o", name: "Entwicklung", description: "", color: "", leads: [], created_at: "" },
    { id: "res", org_id: "o", name: "Forschung", description: "", color: "", leads: [], created_at: "" },
  ],
  humans: [human("admin", { department_id: "dev" })],
  agents: [
    agent("coord", { department_id: "dev", supervisor_id: "admin" }),
    agent("lead", { department_id: "dev", supervisor_id: "coord" }),
    agent("dev1", { department_id: "dev", supervisor_id: "lead" }),
    agent("analyst", { department_id: "res", supervisor_id: "lead" }),
    agent("solo"),
    // A supervisor whose department the chart no longer has.
    agent("ghost", { department_id: "gone" }),
    agent("ward", { department_id: "res", supervisor_id: "ghost" }),
  ],
};

// The viewport the crumb sees: min-width queries answered from one number.
function viewport(width: number) {
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: Number(/min-width:\s*(\d+)px/.exec(q)?.[1] ?? 0) <= width,
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
}

const Where = () => {
  const l = useLocation();
  return <p data-testid="where">{`${l.pathname}${l.search} ${JSON.stringify(l.state)}`}</p>;
};

// `chart: "fail"` leaves the org chart unmocked, so it answers 404.
function show(id: string, origin: AgentsView = "list", o: { chart?: "fail" } = {}) {
  const netz = mockFetch(o.chart === "fail" ? {} : { "/api/v1/org/chart": chart });
  const a = chart.agents.find((x) => x.id === id)!;
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[`/agents/${id}`]}>
        <Routes>
          <Route path="/agents/:id" element={<AgentCrumb agent={a} origin={origin} />} />
        </Routes>
        <Where />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return netz;
}

beforeEach(() => {
  useGerman();
  viewport(1440);
});
afterEach(() => vi.unstubAllGlobals());

describe("AgentCrumb", () => {
  it("shows the department, the supervisors and the agent", async () => {
    show("lead");
    await screen.findByRole("link", { name: "Entwicklung" });
    // coord and admin are two supervisors: both are written out.
    expect(screen.getByRole("link", { name: "admin" })).toHaveAttribute("href", "/people/admin");
    expect(screen.getByRole("link", { name: "coord" })).toHaveAttribute("href", "/agents/coord");
    expect(screen.getByText("lead")).toHaveAttribute("aria-current", "page");
    expect(screen.queryByRole("button", { name: /weitere Ebene/ })).not.toBeInTheDocument();
  });

  it("asks for the org chart once, however long the path", async () => {
    const netz = show("dev1");
    await screen.findByRole("link", { name: "Entwicklung" });
    expect(netz.calls.filter((c) => c.includes("/org/chart"))).toHaveLength(1);
  });

  it("folds all but the direct supervisor behind one ⋯ from three levels on", async () => {
    const nutzer = userEvent.setup();
    show("dev1");
    await screen.findByRole("link", { name: "Entwicklung" });
    const more = screen.getByRole("button", { name: "2 weitere Ebenen zeigen" });
    expect(screen.getAllByRole("button", { name: /weitere Ebene/ })).toHaveLength(1);
    expect(more).toHaveAttribute("aria-haspopup", "menu");
    expect(more).toHaveAttribute("aria-expanded", "false");
    // Only the direct supervisor stands as a step.
    expect(screen.getByRole("link", { name: "lead" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "admin" })).not.toBeInTheDocument();

    await nutzer.click(more);
    expect(more).toHaveAttribute("aria-expanded", "true");
    const items = screen.getAllByRole("menuitem");
    // Top down: the human first, with its profile as the target.
    expect(items.map((i) => i.textContent)).toEqual(["admin", "coord"]);
    expect(items[0]).toHaveAttribute("href", "/people/admin");
    expect(items[1]).toHaveAttribute("href", "/agents/coord");
    expect(screen.getByRole("menu")).toHaveAccessibleName("Zwischen Entwicklung und lead");
  });

  it("opens with Enter, moves with the arrows, and Esc closes it and returns the focus", async () => {
    const nutzer = userEvent.setup();
    show("dev1");
    const more = await screen.findByRole("button", { name: /weitere Ebenen/ });
    more.focus();
    await nutzer.keyboard("{Enter}");
    const [first, second] = screen.getAllByRole("menuitem");
    await waitFor(() => expect(first).toHaveFocus());
    await nutzer.keyboard("{ArrowDown}");
    expect(second).toHaveFocus();
    await nutzer.keyboard("{ArrowDown}");
    expect(first).toHaveFocus();
    await nutzer.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(more).toHaveFocus();
  });

  // The native button takes Space as it takes Enter; it is the second key
  // the spec names, and a div with an onClick would only take one of them.
  it("opens with Space, too", async () => {
    const nutzer = userEvent.setup();
    show("dev1");
    const more = await screen.findByRole("button", { name: /weitere Ebenen/ });
    more.focus();
    await nutzer.keyboard(" ");
    expect(more).toHaveAttribute("aria-expanded", "true");
    expect(screen.getAllByRole("menuitem")).toHaveLength(2);
  });

  // The person icon is the one thing that tells a human step from an agent's.
  it("opens a human step on the profile, with the person icon and without the origin", async () => {
    const nutzer = userEvent.setup();
    show("coord");
    const admin = await screen.findByRole("link", { name: "admin" });
    expect(admin.querySelector(".ic")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Entwicklung" }).querySelector(".ic")).not.toBeInTheDocument();
    await nutzer.click(admin);
    expect((await screen.findByTestId("where")).textContent).toBe("/people/admin null");
  });

  it("marks the human in the ⋯ menu with the person icon, and only the human", async () => {
    const nutzer = userEvent.setup();
    show("dev1");
    await nutzer.click(await screen.findByRole("button", { name: /weitere Ebenen/ }));
    const [admin, coord] = screen.getAllByRole("menuitem");
    expect(admin.querySelector(".ic")).toBeInTheDocument();
    expect(coord.querySelector(".ic")).not.toBeInTheDocument();
  });

  it("keeps where the page was opened from on an agent step", async () => {
    const nutzer = userEvent.setup();
    show("lead", "office");
    await nutzer.click(await screen.findByRole("link", { name: "coord" }));
    expect((await screen.findByTestId("where")).textContent).toBe('/agents/coord {"from":"office"}');
  });

  it("leads the department step to the view the page came from, with ?dept=", async () => {
    const nutzer = userEvent.setup();
    show("lead", "office");
    await nutzer.click(await screen.findByRole("link", { name: "Entwicklung" }));
    expect((await screen.findByTestId("where")).textContent).toBe('/?dept=dev {"from":"office"}');
  });

  it("leads the department step to the list without a view", async () => {
    const nutzer = userEvent.setup();
    show("lead");
    await nutzer.click(await screen.findByRole("link", { name: "Entwicklung" }));
    expect((await screen.findByTestId("where")).textContent).toBe('/agents?dept=dev {"from":"list"}');
  });

  // The view switch in front of the crumb is the root, and its marked segment
  // leads back; a second "Agenten" step would be a second control for it.
  it("has no root step of its own", async () => {
    show("lead", "office");
    await screen.findByRole("link", { name: "Entwicklung" });
    expect(screen.queryByRole("link", { name: "Agenten" })).not.toBeInTheDocument();
    expect(screen.getAllByRole("listitem").map((li) => li.textContent)).toEqual(["Entwicklung", "admin", "coord", "lead"]);
  });

  it("names the agents without a department none", async () => {
    show("solo");
    const link = await screen.findByRole("link", { name: "Ohne Abteilung" });
    expect(link).toHaveAttribute("href", "/agents?dept=none");
    // Not "Alle Agenten in Ohne Abteilung".
    expect(link).toHaveAttribute("title", "Alle Agenten ohne Abteilung");
  });

  it("titles a department step with its name", async () => {
    show("lead");
    expect(await screen.findByRole("link", { name: "Entwicklung" })).toHaveAttribute("title", "Alle Agenten in Entwicklung");
  });

  // The org chart makes this one a root of Forschung; whom it reports to is
  // a note, not a step.
  it("ends at the department for a supervisor in another one, and says whom it reports to", async () => {
    show("analyst");
    await screen.findByRole("link", { name: "Forschung" });
    expect(screen.queryByRole("link", { name: "lead" })).toHaveAttribute("href", "/agents/lead");
    expect(screen.getByText(/berichtet an/)).toHaveTextContent("berichtet an lead (Entwicklung)");
    expect(screen.getAllByRole("listitem").map((li) => li.textContent)).toEqual(["Forschung", "analyst"]);
  });

  it("names an outside supervisor's department without a department when the chart has none for it", async () => {
    show("ward");
    await screen.findByRole("link", { name: "Forschung" });
    expect(screen.getByText(/berichtet an/)).toHaveTextContent("berichtet an ghost (Ohne Abteilung)");
  });

  it("keeps whom it reports to in the bar from 641 px, where the switch leaves it room", async () => {
    viewport(641);
    show("analyst");
    expect(await screen.findByRole("link", { name: "lead" })).toHaveClass("acrumb-link");
    await userEvent.click(screen.getByRole("button", { name: "1 weitere Ebene zeigen" }));
    expect(screen.getAllByRole("menuitem").map((i) => i.textContent)).toEqual(["Forschung"]);
  });

  it("puts whom it reports to into the ⋯ menu on a phone, where the switch leaves a note no room", async () => {
    const nutzer = userEvent.setup();
    viewport(375);
    show("analyst", "org");
    await nutzer.click(await screen.findByRole("button", { name: "2 weitere Ebenen zeigen" }));
    const [dept, boss] = screen.getAllByRole("menuitem");
    expect(dept).toHaveAttribute("href", "/agents?dept=res");
    expect(boss).toHaveTextContent("berichtet an lead (Entwicklung)");
    expect(document.querySelector(".acrumb-note")).toBeNull();
    await nutzer.click(boss);
    expect((await screen.findByTestId("where")).textContent).toBe('/agents/lead {"from":"org"}');
  });

  it("is the agent alone while the chart is on its way", () => {
    show("lead");
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(screen.getByText("lead")).toHaveAttribute("aria-current", "page");
    expect(screen.queryByRole("link", { name: "Entwicklung" })).not.toBeInTheDocument();
  });

  it("stays the agent alone when the chart fails", async () => {
    const netz = show("lead", "list", { chart: "fail" });
    await waitFor(() => expect(netz.unmatched).toContain("GET /api/v1/org/chart"));
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(screen.getByText("lead")).toHaveAttribute("aria-current", "page");
  });

  // Below 1024 px the full crumb has no agent step, so the stand-in has none
  // either: the name must not show first and then go.
  it.each([
    ["while the chart is on its way", 390, undefined],
    ["while the chart is on its way", 900, undefined],
    ["when the chart fails", 390, "fail" as const],
  ])("draws nothing %s at %i px", async (_wann, width, fails) => {
    viewport(width);
    const netz = show("lead", "list", { chart: fails });
    if (fails) await waitFor(() => expect(netz.unmatched).toContain("GET /api/v1/org/chart"));
    expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
    expect(screen.queryByText("lead")).not.toBeInTheDocument();
  });

  it("folds into one ⋯ on a phone, and the menu holds the department and the chain", async () => {
    viewport(390);
    const nutzer = userEvent.setup();
    show("dev1");
    const more = await screen.findByRole("button", { name: "4 weitere Ebenen zeigen" });
    // Department, and no step before the ⋯.
    expect(screen.queryByRole("link", { name: "Entwicklung" })).not.toBeInTheDocument();
    expect(screen.queryByText("dev1")).not.toBeInTheDocument();
    await nutzer.click(more);
    expect(screen.getAllByRole("menuitem").map((i) => i.textContent)).toEqual(["Entwicklung", "admin", "coord", "lead"]);
    expect(screen.getByRole("menu")).toHaveAccessibleName("Organisationspfad");
  });

  it("drops the agent's own step between 769 and 1023 px", async () => {
    viewport(900);
    show("lead");
    await screen.findByRole("link", { name: "Entwicklung" });
    expect(screen.queryByText("lead")).not.toBeInTheDocument();
    // Two supervisors are one too many from 1024 px down.
    expect(screen.getByRole("button", { name: "1 weitere Ebene zeigen" })).toBeInTheDocument();
  });
});
