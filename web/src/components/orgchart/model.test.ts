import { describe, it, expect } from "vitest";
import { buildModel, descendantsOf, allMembers, memberNodeId, deptNodeId, orgPath, UNASSIGNED } from "./model";
import type { Agent, Human, OrgChart } from "../../api";

const human = (id: string, extra: Partial<Human> = {}): Human =>
  ({ id, org_id: "o", email: `${id}@x`, display_name: id, role: "org_admin", job_title: "", identities: {}, phone: "", responsibilities: "", custom: {}, created_at: "", ...extra }) as Human;
const agent = (id: string, extra: Partial<Agent> = {}): Agent =>
  ({ id, slug: id, display_name: id, runtime: "claude-code", model: "", effort: "", max_turns: 0, recording_level: "", sandbox_image: "", warm_sandbox: false, status: "sleeping", job_title: "", identities: {}, phone: "", responsibilities: "", custom: {}, killed: false, budget_usd: 0, created_at: "", ...extra }) as Agent;

const sizes = { org: { w: 10, h: 1 }, dept: { w: 10, h: 1 }, member: { w: 10, h: 1 } };
const open = () => true;

const chart: OrgChart = {
  humans: [
    human("heike", { department_id: "pc" }),
    human("lukas", { department_id: "pc", manager_id: "heike" }),
    human("benjamin", { department_id: "dev" }),
  ],
  agents: [
    agent("lothar", { department_id: "pc", supervisor_id: "heike" }),
    agent("egon", { department_id: "dev", supervisor_id: "benjamin" }),
    agent("delivery", { department_id: "dev", supervisor_id: "egon" }),
    agent("doctor"),
    // Reports to someone in ANOTHER department: a root of its own one.
    agent("stray", { department_id: "pc", supervisor_id: "benjamin" }),
  ],
  departments: [
    { id: "pc", org_id: "o", name: "People", description: "", color: "", leads: [{ kind: "human", id: "heike" }], created_at: "" },
    { id: "dev", org_id: "o", name: "Dev", description: "", color: "", leads: [], created_at: "" },
  ],
};

const ids = (t: { id: string; children: unknown[] }) => t.id;

describe("buildModel", () => {
  it("hangs departments under the organisation and the loose members last", () => {
    const m = buildModel(chart, open, sizes);
    expect(m.root.children.map(ids)).toEqual([deptNodeId("pc"), deptNodeId("dev"), UNASSIGNED]);
    expect(m.root.stackChildren).toBe(false);
  });

  it("resolves reporting lines only inside a department, leads first", () => {
    const m = buildModel(chart, open, sizes);
    const pc = m.root.children[0];
    // heike (lead) before stray (reports outside → root here).
    expect(pc.children.map(ids)).toEqual([memberNodeId("heike"), memberNodeId("stray")]);
    expect(pc.children[0].children.map(ids).sort()).toEqual([memberNodeId("lothar"), memberNodeId("lukas")]);
    const dev = m.root.children[1];
    expect(dev.children.map(ids)).toEqual([memberNodeId("benjamin")]);
    expect(dev.children[0].children[0].children.map(ids)).toEqual([memberNodeId("delivery")]);
  });

  it("stops at a collapsed node and remembers how many it hides", () => {
    const m = buildModel(chart, (id) => id !== "benjamin", sizes);
    const dev = m.root.children[1];
    expect(dev.children[0].children).toEqual([]);
    const node = m.nodes.get(memberNodeId("benjamin"));
    expect(node?.kind === "member" && node.below).toBe(2);
    expect(node?.kind === "member" && node.open).toBe(false);
  });

  it("survives a cycle in the data", () => {
    const loop: OrgChart = {
      humans: [],
      agents: [agent("a", { department_id: "d", supervisor_id: "b" }), agent("b", { department_id: "d", supervisor_id: "a" })],
      departments: [{ id: "d", org_id: "o", name: "D", description: "", color: "", leads: [], created_at: "" }],
    };
    const m = buildModel(loop, open, sizes);
    // Neither is a root (each has a boss inside), so the department is
    // drawn empty rather than the page hanging in recursion.
    expect(m.root.children[0].children).toEqual([]);
  });

  it("names everyone below a member so they cannot become its supervisor", () => {
    // stray reports to benjamin from another department: supervision is
    // org-wide, so it counts — a cycle through two departments is still one.
    expect([...descendantsOf(allMembers(chart), "benjamin")].sort()).toEqual(["delivery", "egon", "stray"]);
    expect(descendantsOf(allMembers(chart), "doctor").size).toBe(0);
  });
});

describe("orgPath", () => {
  const ofAgent = (a: Agent[], h: Human[] = []): OrgChart => ({ humans: h, agents: a, departments: chart.departments });
  const names = (p: ReturnType<typeof orgPath>) => p?.chain.map((m) => m.id);

  it("walks agent supervisors up to the top of the department", () => {
    const p = orgPath(chart, "delivery");
    expect(p?.dept?.id).toBe("dev");
    // benjamin is a human: the chain crosses from agents to humans.
    expect(names(p)).toEqual(["benjamin", "egon"]);
    expect(p?.outsideBoss).toBeNull();
  });

  it("follows manager_id for a human", () => {
    const p = orgPath(chart, "lukas");
    expect(p?.dept?.id).toBe("pc");
    expect(names(p)).toEqual(["heike"]);
    expect(p?.chain[0].kind).toBe("human");
  });

  it("gives an empty chain for someone without a boss", () => {
    const p = orgPath(chart, "heike");
    expect(p?.dept?.id).toBe("pc");
    expect(p?.chain).toEqual([]);
    expect(p?.outsideBoss).toBeNull();
  });

  it("keeps the whole chain at three levels, top first", () => {
    const c = ofAgent([
      agent("a", { department_id: "dev", supervisor_id: "b" }),
      agent("b", { department_id: "dev", supervisor_id: "c" }),
      agent("c", { department_id: "dev", supervisor_id: "d" }),
      agent("d", { department_id: "dev" }),
    ]);
    expect(names(orgPath(c, "a"))).toEqual(["d", "c", "b"]);
    expect(names(orgPath(c, "b"))).toEqual(["d", "c"]);
  });

  it("stops at the department boundary and names the boss outside", () => {
    const p = orgPath(chart, "stray");
    expect(p?.dept?.id).toBe("pc");
    expect(p?.chain).toEqual([]);
    expect(p?.outsideBoss?.id).toBe("benjamin");
  });

  it("notes no outside boss when only a higher level is outside", () => {
    const c = ofAgent([
      agent("a", { department_id: "dev", supervisor_id: "b" }),
      agent("b", { department_id: "dev", supervisor_id: "x" }),
      agent("x", { department_id: "pc" }),
    ]);
    const p = orgPath(c, "a");
    expect(names(p)).toEqual(["b"]);
    expect(p?.outsideBoss).toBeNull();
  });

  it("stops at a boss the chart does not have", () => {
    const c = ofAgent([agent("a", { department_id: "dev", supervisor_id: "gone" })]);
    const p = orgPath(c, "a");
    expect(p?.chain).toEqual([]);
    expect(p?.outsideBoss).toBeNull();
  });

  it("stops at a cycle instead of looping", () => {
    const c = ofAgent([
      agent("a", { department_id: "dev", supervisor_id: "b" }),
      agent("b", { department_id: "dev", supervisor_id: "c" }),
      agent("c", { department_id: "dev", supervisor_id: "a" }),
    ]);
    expect(names(orgPath(c, "a"))).toEqual(["c", "b"]);
  });

  it("treats no department as one: agents without one chain among themselves", () => {
    const c = ofAgent([agent("a", { supervisor_id: "b" }), agent("b")]);
    const p = orgPath(c, "a");
    expect(p?.dept).toBeNull();
    expect(names(p)).toEqual(["b"]);
  });

  it("is null for a member the chart does not have", () => {
    expect(orgPath(chart, "nobody")).toBeNull();
  });
});
