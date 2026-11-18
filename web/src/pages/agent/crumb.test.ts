import { describe, it, expect } from "vitest";
import { fromView, originOf } from "../agents/views";
import { deptTarget, foldChain } from "./crumb";

describe("deptTarget", () => {
  it("sends the list to /agents with the department", () => {
    expect(deptTarget("list", "d1")).toEqual({ to: "/agents?dept=d1", state: { from: "list" } });
  });

  it("sends the office to / with the department", () => {
    expect(deptTarget("office", "d1")).toEqual({ to: "/?dept=d1", state: { from: "office" } });
  });

  it("names the agents without a department none", () => {
    expect(deptTarget("office", null).to).toBe("/?dept=none");
  });

  it("escapes the id", () => {
    expect(deptTarget("list", "a b&c").to).toBe("/agents?dept=a%20b%26c");
  });

  // The origin is decided by originOf, which knows the installation: without
  // the team surface a "from the office" is the list, and so is its target.
  it("never targets the office without the team surface", () => {
    const origin = originOf(undefined, fromView("office"), false);
    expect(deptTarget(origin, "d1").to).toBe("/agents?dept=d1");
    expect(deptTarget(originOf(undefined, fromView("office"), true), "d1").to).toBe("/?dept=d1");
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
