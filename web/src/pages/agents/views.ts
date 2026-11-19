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
 * unfiltered list. `?dept=` is the exception in both views: an id belongs to
 * one organisation, and the tab outlives a switch of organisation, so a
 * remembered one would reopen the view on another organisation's department.
 * The office's is no filter but a pointer, and the list's comes with the
 * crumb's link; either stays in the address, so a reload or a shared link shows
 * it again, but the switch does not keep it. */
export function rememberViewUrl(view: AgentsView, path: string, search: string) {
  const params = new URLSearchParams(search);
  params.delete("dept");
  const rest = params.toString();
  search = rest ? `?${rest}` : "";
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

const choiceKey = (principal: string) => `covey.agents.view.${principal}`;

/* A pick the browser refused to store, per principal, for the length of this
 * tab. Office lives at "/" and "/" shows it only on a stored pick, so a
 * storage that throws or is full would take the office away; the pick is
 * kept here and wins until a write goes through again. */
const unsaved = new Map<string, AgentsView>();

/* Drops what no write has kept. For tests: a tab is a fresh module. */
export function forgetUnsavedViews() {
  unsaved.clear();
}

/* The view a person opens the agents area on, chosen in the switch. Kept in
 * the browser, per principal: two people on one browser keep their own, and
 * the server learns nothing of it. With nothing stored, or nothing readable,
 * it is the list: the one view every installation has and the lighter one.
 * The rail's Agents entry and the root read it by this one rule. */
export function chosenView(principal: string, teamSurface: boolean): AgentsView {
  let v: string | null | undefined = unsaved.get(principal);
  if (v === undefined) {
    try {
      v = localStorage.getItem(choiceKey(principal));
    } catch {
      v = null;
    }
  }
  // Only a view this installation still offers: the value may predate a
  // change of the team surface.
  return viewsFor(teamSurface).find((o) => o === v) ?? "list";
}

export function rememberView(principal: string, view: AgentsView) {
  try {
    localStorage.setItem(choiceKey(principal), view);
    unsaved.delete(principal);
  } catch {
    // Not stored: the pick holds for this tab, and the next visit opens the list.
    unsaved.set(principal, view);
  }
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
