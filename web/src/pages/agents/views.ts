/* The views of the agents area and what is remembered between them.

 * Office and list are pages of their own, each with an address; the agent's
 * page is the way into one of them and knows which. Nothing here is state of
 * the server, only of this browser tab. */

export type AgentsView = "office" | "list";

/* The address a view lives at. An agent id is a UUID, so none of the views
 * can clash with /agents/<id>. */
const BASE: Record<AgentsView, string> = { office: "/", list: "/agents" };

/* The view's own address, without anything remembered: what a link that
 * starts a filter of its own builds on (the crumb's department step). */
export const viewPath = (v: AgentsView): string => BASE[v];

/* The views this installation offers, in the order of the switch. The office
 * needs the team surface: its card and rows open conversations. */
export function viewsFor(teamSurface: boolean): AgentsView[] {
  return teamSurface ? ["office", "list"] : ["list"];
}

export function viewOf(path: string): AgentsView | null {
  return path === "/" ? "office" : path === "/agents" ? "list" : null;
}

const key = (v: AgentsView) => `covey.agents.url.${v}`;

/* The address with its filters, kept per view for the length of the tab: the
 * way back from an agent's page lands where the person left, not on the
 * unfiltered list. */
export function rememberViewUrl(view: AgentsView, path: string, search: string) {
  try {
    sessionStorage.setItem(key(view), path + search);
  } catch {
    // Without storage the way back opens the view unfiltered; nothing more is lost.
  }
}

export function viewUrl(view: AgentsView): string {
  try {
    const url = sessionStorage.getItem(key(view));
    // Only an address that still belongs to the view: a stale or edited value
    // must not send the person somewhere else.
    if (url && (url === BASE[view] || url.startsWith(BASE[view] + "?"))) return url;
  } catch {
    // see rememberViewUrl
  }
  return BASE[view];
}

/* The router state a link to an agent's page carries. */
export function fromView(view: AgentsView) {
  return { from: view };
}

const fromKey = (agentId: string) => `covey.agents.from.${agentId}`;

/* The view an agent's page was reached from. The router state carries it only
 * on the entry that opened the page: every tab, memory page or folder pushes
 * an entry of its own without it. So the origin is kept per agent for the
 * length of the tab, and read back when the state is gone. A bookmark, a link
 * in a message or a notification has no origin, and the list is the one view
 * every installation has. */
export function originOf(agentId: string | undefined, state: unknown, teamSurface: boolean): AgentsView {
  let from = (state as { from?: unknown } | null)?.from;
  if (agentId) {
    try {
      if (from === "office" || from === "list") sessionStorage.setItem(fromKey(agentId), from);
      else from = sessionStorage.getItem(fromKey(agentId));
    } catch {
      // Without storage only the entry that opened the page knows the origin.
    }
  }
  return viewsFor(teamSurface).find((v) => v === from) ?? "list";
}
