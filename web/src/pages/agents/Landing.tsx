import { useEffect, useState, type ReactElement } from "react";
import { Navigate, useLocation, useNavigate } from "react-router";
import type { Principal } from "../../api";
import { chosenView, viewPath } from "./views";

/* The root of the console: the view the person chose in the switch, and the
 * list until they choose. The office lives here, the list and the org chart
 * have addresses of their own. The office is handed in, not imported, so that
 * its bundle (three.js) is only fetched when it is the one shown.
 *
 * An entry of the history that showed the office goes on showing it: the
 * root would otherwise follow a later choice, and the browser's Back from
 * the list or the org chart would land where it started. */
export default function Landing({ me, office }: { me: Principal; office: ReactElement }) {
  const { search, state } = useLocation();
  const marked = isOfficeEntry(state);
  // Held while mounted: the office rewrites its address (?dept=), and that drops the entry's state.
  const [back] = useState(marked);
  const view = chosenView(me.ID, !!me.TeamSurface);
  if (view === "office" || (me.TeamSurface && (marked || back))) {
    return (
      <>
        <MarkEntry />
        {office}
      </>
    );
  }
  // The query goes with it: a filter on the root (the department of a crumb)
  // is a filter of the list too. The org chart has no such filter, so a
  // department leads to the list, whatever was chosen.
  const target = new URLSearchParams(search).has("dept") ? "list" : view;
  return <Navigate to={`${viewPath(target)}${search}`} replace />;
}

const isOfficeEntry = (state: unknown) => (state as { view?: unknown } | null)?.view === "office";

/* Before the office and not in it: its effect runs first, so an address the
   office writes wins over this one, and is marked in its turn. */
function MarkEntry() {
  const { search, hash, state } = useLocation();
  const navigate = useNavigate();
  useEffect(() => {
    if (!isOfficeEntry(state)) navigate({ search, hash }, { replace: true, state: { ...state, view: "office" } });
  }, [search, hash, state, navigate]);
  return null;
}
