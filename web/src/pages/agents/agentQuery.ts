import { queryOptions } from "@tanstack/react-query";
import { api, isNotFound, type Agent } from "../../api";

/* One agent, as the agent's page and the bar above it both read it. They
 * share the options and so the request: the bar names the agent in its crumb
 * without asking the server a second time. */
export const agentQuery = (id: string | undefined) =>
  queryOptions({
    queryKey: ["agent", id],
    queryFn: () => api<Agent>(`/agents/${id}`),
    // A 404 does not change on a second try; everything else keeps the default one retry.
    retry: (failures: number, err: unknown) => !isNotFound(err) && failures < 1,
  });
