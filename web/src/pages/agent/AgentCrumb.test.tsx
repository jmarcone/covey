import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { AgentCrumb } from "./AgentCrumb";
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

function show(id: string, state?: unknown) {
  const netz = mockFetch({ "/api/v1/org/chart": chart });
  const a = chart.agents.find((x) => x.id === id)!;
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[{ pathname: `/agents/${id}`, state }]}>
        <Routes>
          <Route path="/agents/:id" element={<AgentCrumb agent={a} />} />
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
  it("shows the root, the department, the supervisor and the agent", async () => {
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

  it("opens a human step on the profile", async () => {
    const nutzer = userEvent.setup();
    show("coord");
    await nutzer.click(await screen.findByRole("link", { name: "admin" }));
    expect((await screen.findByTestId("where")).textContent).toMatch(/^\/people\/admin/);
  });

  it("keeps where the page was opened from on an agent step", async () => {
    const nutzer = userEvent.setup();
    show("lead", { from: "office" });
    await nutzer.click(await screen.findByRole("link", { name: "coord" }));
    expect((await screen.findByTestId("where")).textContent).toBe('/agents/coord {"from":"office"}');
  });

  it("leads the department step to the view the page came from, with ?dept=", async () => {
    const nutzer = userEvent.setup();
    show("lead", { from: "office" });
    await nutzer.click(await screen.findByRole("link", { name: "Entwicklung" }));
    expect((await screen.findByTestId("where")).textContent).toBe('/?dept=dev {"from":"office"}');
  });

  it("leads the department step to the list without a view", async () => {
    const nutzer = userEvent.setup();
    show("lead");
    await nutzer.click(await screen.findByRole("link", { name: "Entwicklung" }));
    expect((await screen.findByTestId("where")).textContent).toBe('/agents?dept=dev {"from":"list"}');
  });

  it("makes the root lead back to the view it came from", async () => {
    show("lead", { from: "office" });
    expect(await screen.findByRole("link", { name: "Agenten" })).toHaveAttribute("href", "/");
  });

  it("names the agents without a department none", async () => {
    show("solo");
    const link = await screen.findByRole("link", { name: "Ohne Abteilung" });
    expect(link).toHaveAttribute("href", "/agents?dept=none");
  });

  // The org chart makes this one a root of Forschung; whom it reports to is
  // a note, not a step.
  it("ends at the department for a supervisor in another one, and says whom it reports to", async () => {
    show("analyst");
    await screen.findByRole("link", { name: "Forschung" });
    expect(screen.queryByRole("link", { name: "lead" })).toHaveAttribute("href", "/agents/lead");
    expect(screen.getByText(/berichtet an/)).toHaveTextContent("berichtet an lead (Entwicklung)");
    expect(screen.getAllByRole("listitem").map((li) => li.textContent)).toEqual(["Agenten", "Forschung", "analyst"]);
  });

  it("is the root and the agent while the chart is on its way", () => {
    show("lead");
    expect(screen.getByRole("link", { name: "Agenten" })).toBeInTheDocument();
    expect(screen.getByText("lead")).toHaveAttribute("aria-current", "page");
    expect(screen.queryByRole("link", { name: "Entwicklung" })).not.toBeInTheDocument();
  });

  it("folds into the root and one ⋯ on a phone, and the menu holds the department and the chain", async () => {
    viewport(390);
    const nutzer = userEvent.setup();
    show("dev1");
    const more = await screen.findByRole("button", { name: "4 weitere Ebenen zeigen" });
    // Department, and no step but the root before the ⋯.
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
