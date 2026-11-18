import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { useLocation, useMatch } from "react-router";
import ViewSwitch from "../../components/ViewSwitch";
import { AgentCrumb } from "../agent/AgentCrumb";
import { agentQuery } from "./agentQuery";
import { originOf, rememberViewUrl, viewOf, viewsFor } from "./views";

/* The bar of the agents area, above the view: the view switch, and on an
 * agent's page the organisation path after it (AgentCrumb). It stands outside
 * the page that fades in on every navigation (AppShell), so it neither
 * remounts nor flickers when one moves from the office to the list — the only
 * thing that changes is the content.
 *
 * It renders before any data arrives; loading, empty and error states stay
 * with each view. */
export default function AgentsFrame({ teamSurface }: { teamSurface: boolean }) {
  const { pathname, search, state } = useLocation();
  const current = viewOf(pathname);
  const detail = useMatch("/agents/:id")?.params.id;
  const views = viewsFor(teamSurface);

  useEffect(() => {
    if (current) rememberViewUrl(current, pathname, search);
  }, [current, pathname, search]);

  /* The agent the crumb ends in: the same query as the agent's page, so the
     server is asked once and the page and the bar show the same answer. */
  const agent = useQuery({ ...agentQuery(detail), enabled: !!detail });
  const origin = detail ? originOf(detail, state, teamSurface) : undefined;

  return (
    <div className="av-bar">
      <ViewSwitch views={views} current={current} origin={origin} />
      {origin && agent.data && <AgentCrumb agent={agent.data} origin={origin} />}
    </div>
  );
}
