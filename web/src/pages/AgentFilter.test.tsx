import { describe, it, expect, beforeEach } from "vitest";
import { useState } from "react";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { CellAgent } from "../api";
import { useGerman } from "../test/render";
import { AgentFilter, groupByDepartment } from "./StackedCostChart";

// The agent filter of the cost page, grouped by department: the order of the
// groups, what a heading selects, the group without a department, the fold.

const agent = (id: string, usd: number, dept: string | null): CellAgent => ({
  id, label: id, reported_usd: 0, measured_usd: usd,
  department_id: dept ? `d-${dept}` : null, department: dept,
});

const fleet = [
  agent("qa", 3, "Engineering"),
  agent("dev", 9, "Engineering"),
  agent("po", 5, "Operations"),
  agent("clerk", 1, "Operations"),
  agent("designer", 20, "Design"),
  agent("stray", 50, null),
];

function Harness({ agents, start = [] }: { agents: CellAgent[]; start?: string[] }) {
  const [sel, setSel] = useState(new Set(start));
  return (
    <>
      <AgentFilter agents={agents} selected={sel} onChange={setSel} />
      <output data-testid="sel">{[...sel].sort().join(",")}</output>
    </>
  );
}
const picked = () => screen.getByTestId("sel").textContent;
const heading = (name: string) => within(screen.getByRole("group", { name })).getAllByRole("button")[0];

describe("groupByDepartment", () => {
  it("orders departments by cost, descending, agents by cost, no department last", () => {
    const g = groupByDepartment(fleet);
    expect(g.map((x) => x.name)).toEqual(["Design", "Engineering", "Operations", null]);
    expect(g.map((x) => x.usd)).toEqual([20, 12, 6, 50]);
    expect(g[1].agents.map((a) => a.id)).toEqual(["dev", "qa"]);
  });

  it("groups by department id: two departments of one name stay two", () => {
    const g = groupByDepartment([
      { ...agent("a", 1, "Ops"), department_id: "x" },
      { ...agent("b", 2, "Ops"), department_id: "y" },
    ]);
    expect(g.map((x) => x.key)).toEqual(["y", "x"]);
  });
});

describe("AgentFilter by department", () => {
  beforeEach(() => useGerman());

  it("shows departments by cost with the agents without one last under a localized heading", () => {
    render(<Harness agents={fleet} />);
    const groups = screen.getAllByRole("group").map((g) => g.getAttribute("aria-label"));
    expect(groups).toEqual(["Design", "Engineering", "Operations", "Keine Abteilung"]);
    const chips = within(screen.getByRole("group", { name: "Engineering" })).getAllByRole("button").slice(1);
    expect(chips.map((c) => c.textContent)).toEqual([expect.stringContaining("dev"), expect.stringContaining("qa")]);
  });

  it("keeps the flat row when nobody has a department", () => {
    render(<Harness agents={[agent("a", 1, null), agent("b", 2, null)]} />);
    expect(screen.queryByRole("group")).toBeNull();
    expect(screen.queryByText("Keine Abteilung")).toBeNull();
  });

  it("a heading selects its department, again clears it, a part makes it mixed", async () => {
    const user = userEvent.setup();
    render(<Harness agents={fleet} />);
    expect(heading("Engineering")).toHaveAttribute("aria-pressed", "false");

    await user.click(heading("Engineering"));
    expect(picked()).toBe("dev,qa");
    expect(heading("Engineering")).toHaveAttribute("aria-pressed", "true");
    expect(heading("Operations")).toHaveAttribute("aria-pressed", "false");

    await user.click(screen.getByRole("button", { name: /^qa/ }));
    expect(picked()).toBe("dev");
    expect(heading("Engineering")).toHaveAttribute("aria-pressed", "mixed");

    await user.click(heading("Engineering")); // mixed completes the department
    expect(picked()).toBe("dev,qa");
    await user.click(heading("Engineering"));
    expect(picked()).toBe("");
  });

  it("the group without a department toggles like any other", async () => {
    const user = userEvent.setup();
    render(<Harness agents={fleet} start={["po"]} />);
    await user.click(heading("Keine Abteilung"));
    expect(picked()).toBe("po,stray");
    expect(heading("Keine Abteilung")).toHaveAttribute("aria-pressed", "true");
  });

  it("a selected agent stays selected when its department is gone from the data", () => {
    const { rerender } = render(<Harness agents={fleet} start={["qa"]} />);
    expect(picked()).toBe("qa");
    rerender(<Harness agents={fleet.map((a) => (a.id === "qa" ? agent("qa", 3, "Design") : a))} start={["qa"]} />);
    expect(picked()).toBe("qa");
    expect(heading("Design")).toHaveAttribute("aria-pressed", "mixed");
  });

  describe("a long department", () => {
    const many = Array.from({ length: 8 }, (_, i) => agent(`w${i}`, 80 - i, "Big"));

    it("folds to five chips and \"+3 weitere\", unfolds and folds back", async () => {
      const user = userEvent.setup();
      render(<Harness agents={many} />);
      const g = screen.getByRole("group", { name: "Big" });
      expect(within(g).getAllByRole("button", { name: /^w\d/ })).toHaveLength(5);
      await user.click(within(g).getByRole("button", { name: "+3 weitere" }));
      expect(within(g).getAllByRole("button", { name: /^w\d/ })).toHaveLength(8);
      await user.click(within(g).getByRole("button", { name: "Weniger anzeigen" }));
      expect(within(g).getAllByRole("button", { name: /^w\d/ })).toHaveLength(5);
    });

    it("the heading selects the folded chips too, and a hidden selection stays visible", async () => {
      const user = userEvent.setup();
      render(<Harness agents={many} start={["w7"]} />);
      const g = screen.getByRole("group", { name: "Big" });
      expect(within(g).getAllByRole("button", { name: /^w\d/ })).toHaveLength(6);
      expect(within(g).getByRole("button", { name: "+2 weitere" })).toBeInTheDocument();
      await user.click(heading("Big"));
      expect(picked().split(",")).toHaveLength(8);
    });

    it("does not fold five or fewer", () => {
      render(<Harness agents={many.slice(0, 5)} />);
      expect(screen.queryByRole("button", { name: /weitere/ })).toBeNull();
    });
  });
});
