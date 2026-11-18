import { describe, it, expect } from "vitest";
import { deptTarget, foldChain, originView, viewPath } from "./crumb";

describe("originView", () => {
  it("reads the view out of the navigation state", () => {
    expect(originView({ from: "office" })).toBe("office");
    expect(originView({ from: "board" })).toBe("board");
    expect(originView({ from: "list" })).toBe("list");
  });

  // A bookmark or a notification link has no state, and anything else in it
  // is not ours to trust.
  it("lands on the list without a view, or with one it does not know", () => {
    expect(originView(null)).toBe("list");
    expect(originView(undefined)).toBe("list");
    expect(originView({})).toBe("list");
    expect(originView({ from: "dashboard" })).toBe("list");
    expect(originView("office")).toBe("list");
  });
});

describe("deptTarget", () => {
  it("sends the list to /agents with the department", () => {
    expect(deptTarget({ from: "list" }, "d1")).toEqual({ to: "/agents?dept=d1", state: { from: "list" } });
  });

  it("sends the office to / with the department", () => {
    expect(deptTarget({ from: "office" }, "d1")).toEqual({ to: "/?dept=d1", state: { from: "office" } });
  });

  it("sends the board to /agents/board with the department", () => {
    expect(deptTarget({ from: "board" }, "d1")).toEqual({ to: "/agents/board?dept=d1", state: { from: "board" } });
  });

  it("falls back to the list without a view, and carries that view on", () => {
    expect(deptTarget(null, "d1")).toEqual({ to: "/agents?dept=d1", state: { from: "list" } });
  });

  it("names the agents without a department none", () => {
    expect(deptTarget({ from: "office" }, null).to).toBe("/?dept=none");
  });

  it("escapes the id", () => {
    expect(deptTarget(null, "a b&c").to).toBe("/agents?dept=a%20b%26c");
  });

  it("keeps the view's path apart from the query", () => {
    expect(viewPath("office")).toBe("/");
    expect(viewPath("list")).toBe("/agents");
    expect(viewPath("board")).toBe("/agents/board");
  });
});

describe("foldChain", () => {
  const chain = (n: number) => Array.from({ length: n }, (_, i) => `s${i + 1}`);

  it("shows up to two supervisors on a wide screen", () => {
    for (const n of [0, 1, 2]) {
      const f = foldChain(chain(n), "wide");
      expect(f.shown).toEqual(chain(n));
      expect(f.folded).toEqual([]);
      expect(f.dept && f.agent).toBe(true);
    }
  });

  it("folds all but the direct supervisor from three on", () => {
    const f = foldChain(chain(3), "wide");
    expect(f.folded).toEqual(["s1", "s2"]);
    expect(f.shown).toEqual(["s3"]);
    expect(foldChain(chain(5), "wide").folded).toEqual(["s1", "s2", "s3", "s4"]);
  });

  it("folds from two on below 1280 px", () => {
    expect(foldChain(chain(1), "mid")).toMatchObject({ shown: ["s1"], folded: [] });
    expect(foldChain(chain(2), "mid")).toMatchObject({ shown: ["s2"], folded: ["s1"] });
  });

  it("drops the agent's own step below 1024 px", () => {
    expect(foldChain(chain(2), "mid").agent).toBe(true);
    expect(foldChain(chain(2), "narrow")).toMatchObject({ agent: false, dept: true, shown: ["s2"], folded: ["s1"] });
  });

  it("keeps only the fold on a phone, with the whole chain behind it", () => {
    expect(foldChain(chain(2), "phone")).toEqual({ shown: [], folded: ["s1", "s2"], dept: false, agent: false });
    expect(foldChain(chain(0), "phone").folded).toEqual([]);
  });
});
