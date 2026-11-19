import type { ReactElement } from "react";
import { Navigate, useLocation } from "react-router";
import type { Principal } from "../../api";
import { chosenView } from "./views";

/* The root of the console: the view the person chose in the switch, and the
 * list until they choose. The office is handed in, not imported, so that its
 * bundle (three.js) is only fetched when it is the one shown. */
export default function Landing({ me, office }: { me: Principal; office: ReactElement }) {
  const { search } = useLocation();
  // The query goes with it: a filter on the root (the department of a crumb)
  // is a filter of the list too.
  return me.TeamSurface && chosenView(me.ID, true) === "office" ? office : <Navigate to={`/agents${search}`} replace />;
}
