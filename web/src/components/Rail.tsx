import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link, useLocation } from "react-router";
import { api, inbox, myThreads, setupIsOpen, type Principal, type SetupState } from "../api";
import { chosenView, viewPath } from "../pages/agents/views";
import { BirdMark } from "./BirdMark";
import { NavIcon } from "./navicons";
import ShellFoot from "./ShellFoot";
import Tour, { TEAM_TOUR, tourGesehen } from "./Tour";

/* The rail (#388, #389): the thin column of icons at the left of every
 * signed-in page — agents, team, workflows, notes, costs, administration;
 * search and the person below. Both shells carry it, the same in both, so it never
 * disappears when one moves from a conversation to the settings: what
 * changes is the column beside it.
 *
 * Without the team surface (#328) there is no team to go to, and the notes
 * live in the console. Its slot goes to the inbox, where the decisions are
 * made without a conversation. */
export type RailPlace = "agents" | "team" | "inbox" | "workflows" | "costs" | "notes" | "admin";

/* Which place an address belongs to. One function for both shells, so the
 * mark cannot depend on which of them happens to render the page.
 *
 * The agents area is the office, the list, the org chart and every agent's
 * page: daily work, not a setting. An agent id is a UUID, so /agents/board and
 * /agents/<id> both stay in it without a clash. The inbox is a place of
 * its own only without the team surface; with it /inbox leads to /team. */
export function railPlace(path: string, teamSurface: boolean): RailPlace {
  const under = (p: string) => path === p || path.startsWith(p + "/");
  if (path === "/" || under("/agents") || under("/org")) return "agents";
  if (under("/team/notes") || under("/notes")) return "notes";
  if (under("/team")) return "team";
  if (path === "/inbox") return teamSurface ? "team" : "inbox";
  if (under("/workflows")) return "workflows";
  if (under("/costs")) return "costs";
  return "admin";
}

/* The agents entry opens the view the person chose in the switch of the
 * agents area, the one the root opens too. A view the installation does not
 * offer — the office without the team surface — is the list. */
export function agentsEntryTarget(principal: string, teamSurface: boolean): string {
  return viewPath(chosenView(principal, teamSurface));
}

export default function Rail({
  me,
  onLogout,
  onHelp,
  onSearch,
}: {
  me: Principal;
  onLogout: () => void;
  onHelp: () => void;
  onSearch: () => void;
}) {
  const { t } = useTranslation();
  const team = !!me.TeamSurface;
  const path = useLocation().pathname;
  const active = railPlace(path, team);
  const agentsTo = agentsEntryTarget(me.ID, team);
  const threads = useQuery({
    queryKey: ["threads"],
    queryFn: myThreads,
    refetchInterval: 20_000,
    retry: false,
    enabled: team,
  });
  const unread = (threads.data ?? []).reduce((n, th) => n + th.unread, 0);
  /* Open decisions, the same query the office keeps warm. With the team
     surface the number stands on Agents, because the office lists them;
     without it on the inbox, where they are made. */
  const waiting = useQuery({
    queryKey: ["inbox", "workspace-liste"],
    queryFn: () => inbox({ status: "open", limit: 100 }),
    refetchInterval: 30_000,
  });
  const pending = waiting.data?.pending ?? 0;
  /* Setup, while it is not finished (#391): right under the mark, where
     whoever just installed covey looks first. Those not allowed to set up
     get a 403 here and see nothing. */
  const setup = useQuery({
    queryKey: ["setup"],
    queryFn: () => api<SetupState>("/setup/state"),
    retry: false,
    staleTime: 60_000,
  });
  const setupOpen = setupIsOpen(setup.data);
  /* The tour (#402) belongs to the team surface, and the rail is the one
     thing both shells carry — so it lives here, and starts on the first
     visit. */
  const [tour, setTour] = useState(() => team && !tourGesehen());

  // A badge counts unread messages, or on agents and the inbox open decisions.
  const item = (place: RailPlace, to: string, icon: string, label: string, badge = 0) => (
    <Link
      to={to}
      className={`tm-rail-item${active === place ? " on" : ""}`}
      data-tour={place}
      aria-current={active === place ? "page" : undefined}
      title={label}
      aria-label={
        badge > 0
          ? `${label} · ${place === "team" ? t("team.ungelesen", { count: badge }) : t("team.wartetLead", { count: badge })}`
          : label
      }
    >
      <NavIcon name={icon} />
      {badge > 0 && <span className="tm-rail-zahl">{Math.min(badge, 99)}</span>}
      <span className="tm-rail-wort">{label}</span>
    </Link>
  );

  return (
    <nav className="tm-rail" aria-label={t("team.bereiche")}>
      <Link to={team ? "/" : agentsTo} className="tm-rail-mark" aria-label="covey">
        <BirdMark size={30} />
      </Link>
      {setupOpen && (
        <Link to="/setup" className="tm-rail-item tm-rail-setup" title={t("nav.setupPage")} aria-label={t("nav.setupPage")}>
          <NavIcon name="checklist" />
          <span className="tm-rail-punkt" aria-hidden="true" />
          <span className="tm-rail-wort">{t("nav.setupPage")}</span>
        </Link>
      )}
      {/* Agents are a main view, not a setting: the office, the list, the
          org chart and every agent's page sit behind this one entry. */}
      {item("agents", agentsTo, "robot", t("nav.agents"), team ? pending : 0)}
      {team ? item("team", "/team", "chat", t("team.workspace"), unread) : item("inbox", "/inbox", "bell", t("nav.inbox"), pending)}
      {/* Workflows (#560) are their own place, not a setting: the live view
          is daily work, and a workflow crosses departments rather than
          belonging to one. */}
      {item("workflows", "/workflows", "flow", t("nav.workflows"))}
      {item("notes", team ? "/team/notes" : "/notes", "note", t("mobile.notizen"))}
      {/* Costs are read every week, not set up once (#17): a place of their
          own beside Admin, open to every role as their pages are. No badge:
          nothing would keep a count of paused agents fresh. */}
      {item("costs", "/costs", "chart", t("nav.costs"))}
      {/* Admin lands on the first item of its column, a page every role can
          open. */}
      {item("admin", "/targets", "sliders", t("team.verwaltung"))}
      <span className="tm-rail-luft" />
      <button className="tm-rail-item" data-tour="search" onClick={onSearch} title={`${t("team.suche")} (⌘K)`} aria-label={t("team.suche")}>
        <NavIcon name="search" />
      </button>
      <ShellFoot me={me} onLogout={onLogout} onHelp={onHelp} onTour={team ? () => setTour(true) : undefined} compact />
      {tour && <Tour schritte={TEAM_TOUR} onEnde={() => setTour(false)} />}
    </nav>
  );
}
