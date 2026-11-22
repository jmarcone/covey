// The pure side of the agent page's organisation path: where its steps lead
// and which of them fit. The component that draws it is AgentCrumb.tsx, in
// the agents area's bar (pages/agents/AgentsFrame.tsx).
import { fromView, viewPath, type AgentsView } from "../agents/views";

/** A department step: the view the page was opened from (`originOf`, which
 *  already leaves out the office on an installation without the team
 *  surface), narrowed to one department, `null` being the agents without
 *  one. The org chart cannot be narrowed: from there it is the list. The
 *  view's other filters are dropped, and `from` travels on so the next step
 *  still knows where it started. */
export function deptTarget(origin: AgentsView, deptId: string | null): { to: string; state: { from: AgentsView } } {
  const view = origin === "org" ? "list" : origin;
  return { to: `${viewPath(view)}?dept=${encodeURIComponent(deptId ?? "none")}`, state: fromView(view) };
}

/** How much of the path the viewport has room for (spec: agent-header §1). */
export type CrumbMode = "wide" | "mid" | "narrow" | "phone";

export const CRUMB_QUERIES: { mode: CrumbMode; query: string }[] = [
  { mode: "wide", query: "(min-width: 1280px)" },
  { mode: "mid", query: "(min-width: 1024px)" },
  { mode: "narrow", query: "(min-width: 769px)" },
];

/** From this width the bar has room for whom the agent reports to in another
 *  department, after the switch's three views; below it the note is in the menu. */
export const NOTE_QUERY = "(min-width: 641px)";

export type Folded<T> = {
  /** Supervisors written out as steps, top first. */
  shown: T[];
  /** Supervisors behind the `⋯` step, top first. */
  folded: T[];
  /** The department is a step of its own; on a phone it is in the menu. */
  dept: boolean;
  /** The agent's own step; the heading right below names it as well. */
  agent: boolean;
};

/** Which steps a width keeps. Wide shows up to two supervisors and folds all
 *  but the direct one from three on; from 1024 px two are already one too
 *  many; below 1024 px the agent step goes (the heading names it); a phone
 *  keeps the root and one `⋯` that holds everything else. */
export function foldChain<T>(chain: T[], mode: CrumbMode): Folded<T> {
  if (mode === "phone") return { shown: [], folded: chain, dept: false, agent: false };
  const limit = mode === "wide" ? 2 : 1;
  const fold = chain.length > limit;
  return {
    shown: fold ? chain.slice(-1) : chain,
    folded: fold ? chain.slice(0, -1) : [],
    dept: true,
    agent: mode !== "narrow",
  };
}
