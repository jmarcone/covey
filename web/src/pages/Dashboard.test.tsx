import { describe, it, expect, beforeEach } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useLocation, useNavigate } from "react-router";
import Dashboard, { deptFilter, groupByDepartment, matches, stateOf } from "./Dashboard";
import { mockFetch, renderWithProviders, testPrincipal, useGerman } from "../test/render";
import type { Agent, Department } from "../api";

/* The overview used to be a wall of tiles in filing order. From about a dozen
   agents on, the question is no longer "who is here" but "who in support" and
   "where is Brunhilde" — and the order the org chart already has answers both.
   The sorting and the search are pure functions, so that exactly what someone
   searches for is what can be checked. */

const agent = (over: Partial<Agent>): Agent =>
  ({
    id: over.slug ?? "id",
    slug: "slug",
    display_name: "Name",
    job_title: "",
    status: "sleeping",
    ...over,
  }) as Agent;

const dept = (id: string, name: string, color = ""): Department =>
  ({ id, org_id: "o", name, description: "", color, leads: [], created_at: "" }) as Department;

describe("groupByDepartment", () => {
  const support = dept("d1", "Support", "#abc");
  const dev = dept("d2", "Entwicklung");
  const staff = [
    agent({ slug: "egon", display_name: "Egon Rastlos", department_id: "d2" }),
    agent({ slug: "wanda", display_name: "Wanda Wachsam", department_id: "d1" }),
    agent({ slug: "solo", display_name: "Solo Ohnehaus" }),
  ];

  it("sorts departments alphabetically and puts the unassigned last", () => {
    const groups = groupByDepartment(staff, [support, dev], "");
    expect(groups.map((g) => g.name)).toEqual(["Entwicklung", "Support", ""]);
    expect(groups[2].id).toBeNull();
    expect(groups[2].agents.map((a) => a.slug)).toEqual(["solo"]);
  });

  it("keeps the department's colour so both views look like the same order", () => {
    const groups = groupByDepartment(staff, [support, dev], "");
    expect(groups.find((g) => g.name === "Support")?.color).toBe("#abc");
  });

  // A department heading without a hit is, while searching, exactly the
  // line that costs attention.
  it("drops groups that have no hit", () => {
    const groups = groupByDepartment(staff, [support, dev], "egon");
    expect(groups).toHaveLength(1);
    expect(groups[0].name).toBe("Entwicklung");
  });

  // Whoever types "support" means the department — even when the word does
  // not appear in the person's name.
  it("finds people by their department", () => {
    const groups = groupByDepartment(staff, [support, dev], "support");
    expect(groups.map((g) => g.agents.map((a) => a.slug))).toEqual([["wanda"]]);
  });
});

describe("Zustandsfilter und Reihenfolge", () => {
  const staff = [
    agent({ slug: "schlaefer", display_name: "Zora Zuletzt", status: "sleeping" }),
    agent({ slug: "arbeiter", display_name: "Anna Aktiv", status: "working" }),
    agent({ slug: "gestoppt", display_name: "Karl Kalt", status: "sleeping", killed: true }),
  ];

  // Whoever runs stands on top. Otherwise the filing order decides, and
  // that tells nobody anything.
  it("sorts the busy ones first, the stopped ones last", () => {
    const [g] = groupByDepartment(staff, [], "");
    expect(g.agents.map((a) => a.slug)).toEqual(["arbeiter", "schlaefer", "gestoppt"]);
  });

  it("filters by state and combines with the search", () => {
    const [g] = groupByDepartment(staff, [], "", ["sleeping"]);
    expect(g.agents.map((a) => a.slug)).toEqual(["schlaefer"]);
    expect(groupByDepartment(staff, [], "anna", ["sleeping"])).toHaveLength(0);
  });

  // A stopped agent still carries its last status — the badge shows
  // "stopped" all the same, and the filter has to mean the same thing.
  it("a stopped agent is stopped, whatever its status says", () => {
    expect(stateOf(staff[2])).toBe("killed");
    const [g] = groupByDepartment(staff, [], "", ["killed"]);
    expect(g.agents.map((a) => a.slug)).toEqual(["gestoppt"]);
  });
});

describe("matches", () => {
  const a = agent({ slug: "tester-1", display_name: "Egon Rastlos", job_title: "QA", status: "working" });

  it("searches name, role, slug and state", () => {
    for (const q of ["egon", "RASTLOS", "qa", "tester", "working"]) {
      expect(matches(a, "", q)).toBe(true);
    }
    expect(matches(a, "", "brunhilde")).toBe(false);
  });

  it("an empty query keeps everybody", () => {
    expect(matches(a, "", "   ")).toBe(true);
  });
});

describe("deptFilter", () => {
  const solo = agent({ slug: "solo" });
  const wanda = agent({ slug: "wanda", department_id: "d1" });

  it("lets everyone through without a department asked for", () => {
    expect(deptFilter(solo, "")).toBe(true);
    expect(deptFilter(wanda, "")).toBe(true);
  });

  it("keeps the members of one department", () => {
    expect(deptFilter(wanda, "d1")).toBe(true);
    expect(deptFilter(wanda, "d2")).toBe(false);
    expect(deptFilter(solo, "d1")).toBe(false);
  });

  it("takes none for the agents without a department", () => {
    expect(deptFilter(solo, "none")).toBe(true);
    expect(deptFilter(wanda, "none")).toBe(false);
  });

  it("narrows the groups", () => {
    const support = dept("d1", "Support");
    const dev = dept("d2", "Entwicklung");
    const staff = [
      agent({ slug: "egon", display_name: "Egon", department_id: "d2" }),
      agent({ slug: "wanda", display_name: "Wanda", department_id: "d1" }),
      agent({ slug: "solo", display_name: "Solo" }),
    ];
    expect(groupByDepartment(staff, [support, dev], "", [], "d1").map((g) => g.name)).toEqual(["Support"]);
    expect(groupByDepartment(staff, [support, dev], "", [], "none").flatMap((g) => g.agents.map((a) => a.slug))).toEqual(["solo"]);
    expect(groupByDepartment(staff, [support, dev], "", [], "nope")).toEqual([]);
  });
});

// The agent page's crumb links to /agents?dept=<id|none>.
describe("the list with ?dept=", () => {
  const support = dept("d1", "Support");
  const dev = dept("d2", "Entwicklung");
  const staff = [
    agent({ id: "a1", slug: "egon", display_name: "Egon Rastlos", department_id: "d2" }),
    agent({ id: "a2", slug: "wanda", display_name: "Wanda Wachsam", department_id: "d1" }),
    agent({ id: "a3", slug: "solo", display_name: "Solo Ohnehaus" }),
  ];
  const Where = () => {
    const l = useLocation();
    const back = useNavigate();
    return (
      <>
        <p data-testid="where">{`${l.pathname}${l.search}`}</p>
        <p data-testid="state">{JSON.stringify(l.state)}</p>
        <button onClick={() => back(-1)}>zurück</button>
      </>
    );
  };
  const zeige = (query: string) => {
    mockFetch({
      "/api/v1/agents": staff,
      "/api/v1/departments": [support, dev],
      "/api/v1/fleet": { fleet_killed: false },
    });
    return renderWithProviders(
      <>
        <Dashboard me={testPrincipal()} />
        <Where />
      </>,
      { route: `/agents${query}` },
    );
  };
  beforeEach(() => useGerman());

  it("shows only that department", async () => {
    zeige("?dept=d1");
    await screen.findByText("Wanda Wachsam");
    expect(screen.queryByText("Egon Rastlos")).not.toBeInTheDocument();
    expect(screen.queryByText("Solo Ohnehaus")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Abteilungsfilter entfernen" })).toHaveTextContent("Abteilung: Support");
  });

  it("shows only the agents without a department for none", async () => {
    zeige("?dept=none");
    await screen.findByText("Solo Ohnehaus");
    expect(screen.queryByText("Wanda Wachsam")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Abteilungsfilter entfernen" })).toHaveTextContent("Abteilung: Ohne Abteilung");
  });

  it("answers an id nobody has with an empty state and a way back", async () => {
    const nutzer = userEvent.setup();
    zeige("?dept=gone");
    expect(await screen.findByText(/Diese Abteilung gibt es nicht/)).toBeInTheDocument();
    expect(screen.queryByText("Wanda Wachsam")).not.toBeInTheDocument();
    await nutzer.click(screen.getByRole("button", { name: "Alle Agenten zeigen" }));
    expect(await screen.findByText("Wanda Wachsam")).toBeInTheDocument();
    expect(screen.getByText("Egon Rastlos")).toBeInTheDocument();
    expect(screen.getByTestId("where")).toHaveTextContent(/^\/agents$/);
  });

  it("takes the filter off with its chip", async () => {
    const nutzer = userEvent.setup();
    zeige("?dept=d1");
    await nutzer.click(await screen.findByRole("button", { name: "Abteilungsfilter entfernen" }));
    expect(await screen.findByText("Egon Rastlos")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Abteilungsfilter entfernen" })).not.toBeInTheDocument();
    expect(screen.getByTestId("where")).toHaveTextContent(/^\/agents$/);
  });

  it("brings the filter back with the browser's Back after the chip took it off", async () => {
    const nutzer = userEvent.setup();
    zeige("?dept=d1");
    await nutzer.click(await screen.findByRole("button", { name: "Abteilungsfilter entfernen" }));
    expect(await screen.findByText("Egon Rastlos")).toBeInTheDocument();
    await nutzer.click(screen.getByRole("button", { name: "zurück" }));
    expect(screen.getByTestId("where")).toHaveTextContent("/agents?dept=d1");
    expect(await screen.findByRole("button", { name: "Abteilungsfilter entfernen" })).toBeInTheDocument();
    expect(screen.queryByText("Egon Rastlos")).not.toBeInTheDocument();
  });

  it("opens an agent with the list as its origin", async () => {
    const nutzer = userEvent.setup();
    zeige("");
    await nutzer.click(await screen.findByText("Wanda Wachsam"));
    expect(screen.getByTestId("where")).toHaveTextContent("/agents/a2");
    expect(screen.getByTestId("state")).toHaveTextContent('{"from":"list"}');
  });
});
