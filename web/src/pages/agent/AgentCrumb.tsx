import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { Trans, useTranslation } from "react-i18next";
import { api, type Agent, type OrgChart } from "../../api";
import { NavIcon } from "../../components/navicons";
import { memberDept, memberName, orgPath, type Member } from "../../components/orgchart/model";
import { fromView, type AgentsView } from "../agents/views";
import { CRUMB_QUERIES, NOTE_QUERY, deptTarget, foldChain, type CrumbMode } from "./crumb";

/* The agent's place in the organisation, as steps one can click, in the
 * agents area's bar right after the view switch:
 *
 *   [Office | List | Org Chart] › Department › ⋯ › direct supervisor › this agent
 *
 * The switch is the root: its marked segment already leads back to the view
 * the page was opened from, so there is no "Agents" step of its own. The path
 * is the org chart's (orgPath): the supervisors above the agent inside its
 * department. Whoever sits higher than the third level is folded behind one
 * `⋯`, and on a phone everything after the switch is; below 641 px that
 * includes whom the agent reports to in another department. */

// The widest viewport wins; without matchMedia (tests) there is room. Both
// functions stay the same between renders, or useSyncExternalStore would
// take the listeners off and put them back on every time.
function subscribeMode(cb: () => void) {
  const lists = [...CRUMB_QUERIES.map((q) => q.query), NOTE_QUERY].map((q) => window.matchMedia?.(q));
  lists.forEach((l) => l?.addEventListener("change", cb));
  return () => lists.forEach((l) => l?.removeEventListener("change", cb));
}
function currentMode(): CrumbMode {
  if (!window.matchMedia) return "wide";
  return CRUMB_QUERIES.find((q) => window.matchMedia(q.query).matches)?.mode ?? "phone";
}
const serverMode = (): CrumbMode => "wide";
const currentNoteRoom = () => window.matchMedia?.(NOTE_QUERY).matches ?? true;
const serverNoteRoom = () => true;

function useCrumbMode(): CrumbMode {
  return useSyncExternalStore(subscribeMode, currentMode, serverMode);
}

export function AgentCrumb({ agent, origin }: { agent: Pick<Agent, "id" | "display_name">; origin: AgentsView }) {
  const { t } = useTranslation();
  const mode = useCrumbMode();
  const noteRoom = useSyncExternalStore(subscribeMode, currentNoteRoom, serverNoteRoom);
  // The same key as the org page: one request, however many steps.
  const chart = useQuery({ queryKey: ["orgchart"], queryFn: () => api<OrgChart>("/org/chart") });
  const path = chart.data ? orgPath(chart.data, agent.id) : null;

  // What a link to another agent carries, so that page knows its origin too.
  const state = fromView(origin);
  const here = (
    <li>
      <span className="acrumb-step acrumb-here" aria-current="page" title={agent.display_name}>
        {agent.display_name}
      </span>
    </li>
  );
  // Until the chart is there — or when it fails or does not know the agent —
  // the crumb is the agent alone, and grows when the rest arrives. Where the
  // full crumb leaves the agent step out (below 1024 px, the heading names
  // it), so does this one: the bar must not show the name first and then
  // drop it.
  if (!path) {
    if (!foldChain([], mode).agent) return null;
    return (
      <nav className="acrumb" aria-label={t("agent.crumb.label")}>
        <ol>{here}</ol>
      </nav>
    );
  }

  const deptName = path.dept?.name ?? t("dashboard.withoutDepartment");
  // "All agents in Without a department" reads wrong; `none` has its own title.
  const deptTitle = path.dept ? t("agent.crumb.allIn", { department: deptName }) : t("agent.crumb.allWithout");
  const dept = deptTarget(origin, path.dept?.id ?? null);
  const fold = foldChain(path.chain, mode);
  const outside = path.outsideBoss;
  const outsideDept = outside
    ? (chart.data!.departments.find((d) => d.id === memberDept(outside))?.name ?? t("dashboard.withoutDepartment"))
    : "";
  // Where the bar has no room for it, the note is a row of the menu.
  const noteInMenu = !!outside && !noteRoom;
  const lastShown = fold.shown[0] ?? null;
  const menuLabel = fold.dept
    ? t("agent.crumb.between", { from: deptName, to: lastShown ? memberName(lastShown) : agent.display_name })
    : t("agent.crumb.label");

  return (
    <nav className="acrumb" aria-label={t("agent.crumb.label")}>
      <ol>
        {fold.dept && (
          <li>
            <Link
              to={dept.to}
              state={dept.state}
              className="acrumb-step"
              title={deptTitle}
            >
              {deptName}
            </Link>
          </li>
        )}
        {(fold.folded.length > 0 || !fold.dept) && (
          <li>
            <FoldMenu
              label={menuLabel}
              members={fold.folded}
              // The department is a step of its own, unless there is no room.
              deptRow={fold.dept ? null : { name: deptName, to: dept.to, state: dept.state, title: deptTitle }}
              reportsTo={noteInMenu ? { boss: outside!, department: outsideDept } : null}
              state={state}
            />
          </li>
        )}
        {fold.shown.map((m) => (
          <li key={m.id}>
            <MemberStep m={m} state={state} />
          </li>
        ))}
        {fold.agent && here}
      </ol>
      {outside && !noteInMenu && (
        <span className="acrumb-note">
          <Trans
            i18nKey="agent.crumb.reportsTo"
            values={{ name: memberName(outside), department: outsideDept }}
            components={{
              link: (
                <Link
                  to={outside.kind === "human" ? `/people/${outside.id}` : `/agents/${outside.id}`}
                  state={outside.kind === "human" ? undefined : state}
                  className="acrumb-link"
                  title={memberName(outside)}
                />
              ),
            }}
          />
        </span>
      )}
    </nav>
  );
}

/* A supervisor: an agent opens its page and keeps where we came from; a
 * human opens the profile and wears the person icon — the icon tells the two
 * apart, a colour would not. */
function MemberStep({ m, state }: { m: Member; state: unknown }) {
  const name = memberName(m);
  const human = m.kind === "human";
  return (
    <Link
      to={human ? `/people/${m.id}` : `/agents/${m.id}`}
      state={human ? undefined : state}
      className="acrumb-step"
      title={name}
    >
      {human && <NavIcon name="user" />}
      {name}
    </Link>
  );
}

/* The `⋯` step: a button that opens a menu of the folded levels, top down.
 * Enter or Space opens it, the arrows move, Esc closes it and hands the
 * focus back to the button. */
function FoldMenu({
  label,
  members,
  deptRow,
  reportsTo,
  state,
}: {
  label: string;
  members: Member[];
  deptRow: { name: string; to: string; state: unknown; title: string } | null;
  /** The supervisor in another department, where the bar has no room to say so. */
  reportsTo: { boss: Member; department: string } | null;
  state: unknown;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const count = members.length + (deptRow ? 1 : 0) + (reportsTo ? 1 : 0);
  const wrap = useRef<HTMLSpanElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const items = () => Array.from(wrap.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []);

  useEffect(() => {
    if (!open) return;
    items()[0]?.focus();
    const away = (e: MouseEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
  }, [open]);

  const onKey = (e: React.KeyboardEvent) => {
    if (!open) return;
    const list = items();
    const at = list.indexOf(document.activeElement as HTMLElement);
    const move = (i: number) => {
      e.preventDefault();
      list[(i + list.length) % list.length]?.focus();
    };
    if (e.key === "ArrowDown") move(at + 1);
    else if (e.key === "ArrowUp") move(at < 0 ? list.length - 1 : at - 1);
    else if (e.key === "Home") move(0);
    else if (e.key === "End") move(list.length - 1);
    else if (e.key === "Escape") {
      e.preventDefault();
      setOpen(false);
      button.current?.focus();
    } else if (e.key === "Tab") setOpen(false);
  };

  return (
    <span className="acrumb-fold" ref={wrap} onKeyDown={onKey}>
      <button
        ref={button}
        type="button"
        className="acrumb-more"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t("agent.crumb.more", { count })}
        onClick={() => setOpen(!open)}
      >
        ⋯
      </button>
      {open && (
        <div className="acrumb-menu" role="menu" aria-label={label}>
          <div className="acrumb-menu-head" aria-hidden="true">
            {label}
          </div>
          {deptRow && (
            <Link role="menuitem" tabIndex={-1} to={deptRow.to} state={deptRow.state} title={deptRow.title} onClick={() => setOpen(false)}>
              {deptRow.name}
            </Link>
          )}
          {members.map((m, i) => {
            const human = m.kind === "human";
            return (
              <Link
                key={m.id}
                role="menuitem"
                tabIndex={-1}
                to={human ? `/people/${m.id}` : `/agents/${m.id}`}
                state={human ? undefined : state}
                title={memberName(m)}
                style={{ paddingLeft: 9 + (i + (deptRow ? 1 : 0)) * 14 }}
                onClick={() => setOpen(false)}
              >
                {human ? <NavIcon name="user" /> : <span className="acrumb-gap" aria-hidden="true" />}
                <span className="acrumb-clip">{memberName(m)}</span>
              </Link>
            );
          })}
          {reportsTo && (
            <Link
              role="menuitem"
              tabIndex={-1}
              className="acrumb-menu-note"
              to={reportsTo.boss.kind === "human" ? `/people/${reportsTo.boss.id}` : `/agents/${reportsTo.boss.id}`}
              state={reportsTo.boss.kind === "human" ? undefined : state}
              onClick={() => setOpen(false)}
            >
              <span>
                <Trans
                  i18nKey="agent.crumb.reportsTo"
                  values={{ name: memberName(reportsTo.boss), department: reportsTo.department }}
                  components={{ link: <span /> }}
                />
              </span>
            </Link>
          )}
        </div>
      )}
    </span>
  );
}
