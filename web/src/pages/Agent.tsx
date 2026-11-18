import { useState, useEffect } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, Navigate, useLocation, useParams, useSearchParams } from "react-router";
import { useTranslation } from "react-i18next";
import { post, isDraft, isNotFound, type Principal } from "../api";
import { agentQuery } from "./agents/agentQuery";
import { originOf, viewUrl } from "./agents/views";
import { NavIcon } from "../components/navicons";
import { AgentFiles } from "../components/AgentFiles";
import { PhaseBadge } from "../components/PhaseBadge";
import { useEdgeFade } from "../components/useEdgeFade";
import { AgentHome } from "../components/AgentHome";
import { HireDialog } from "../components/HireDialog";
import { canFiles, canKill, canManage, canRecord, canSecrets } from "./agent/roles";
import { AgentTooling } from "./agent/Tooling";
import { AgentSettings } from "./agent/Settings";
import { CostBar } from "./agent/CostBar";
import { Performance } from "./agent/Performance";
import { LintFindings } from "./agent/LintFindings";
import { WorkRecord } from "./agent/WorkRecord";
import { Backlog } from "./agent/Backlog";
import { Recording } from "./agent/Recording";
import { Memories } from "./agent/Memories";

// The valid values of ?tab=. The second group is merged, but as a URL still
// valid: shared links and bookmarks should not run into empty space, but
// land where the content now lives (see MOVED below).
// Plus the English names of the German slugs — whoever types "workspace" or
// "settings" means the workspace or the settings.
const TABS = [
  "backlog", "recording", "akte", "memory", "dateien", "werkzeuge", "einstellungen",
  "heartbeat", "tools", "skills", "webhook", "config", "secrets", "egress", "dreams",
  "workspace", "files", "settings",
] as const;
type TabKey = (typeof TABS)[number];

// MOVED: old tab → [new tab, parameter name, value].
const MOVED: Partial<Record<TabKey, [TabKey, string, string]>> = {
  heartbeat: ["einstellungen", "sub", "heartbeat"],
  webhook: ["einstellungen", "sub", "webhook"],
  config: ["einstellungen", "sub", "config"],
  secrets: ["einstellungen", "sub", "secrets"],
  egress: ["einstellungen", "sub", "egress"],
  settings: ["einstellungen", "sub", "general"],
  tools: ["werkzeuge", "sub", "mcp"],
  skills: ["werkzeuge", "sub", "skills"],
  dreams: ["memory", "view", "dreams"],
  workspace: ["dateien", "dir", ""],
  files: ["dateien", "dir", ""],
};

export default function AgentPage({ me }: { me: Principal }) {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();
  const { state } = useLocation();
  const qc = useQueryClient();
  const agent = useQuery(agentQuery(id));

  // Tab state lives in the URL (?tab=…) — real navigation: shareable links,
  // browser forward/back. The memory tab additionally carries ?page=<slug>.
  const [sp, setSp] = useSearchParams();
  // Only known tabs count. Before, every unknown value fell through the
  // `|| "backlog"` — it only fires on null and "" —, and ?tab=workspace
  // showed an empty page instead of the workspace. A link someone types by
  // hand or brings along from an older version should land somewhere.
  const tab = (TABS as readonly string[]).includes(sp.get("tab") ?? "")
    ? (sp.get("tab") as TabKey)
    : "backlog";
  const setTab = (key: typeof tab) =>
    setSp(
      (prev) => {
        const n = new URLSearchParams(prev);
        n.set("tab", key);
        n.delete("sub"); // the sub-item belongs to the tab you are leaving
        if (key !== "memory") n.delete("page"); // wiki page only in the memory tab
        if (key !== "dateien") {
          n.delete("dir"); // folder and file only in the workspace tab
          n.delete("file");
        }
        return n;
      },
      { replace: false },
    );
  const [recTask, setRecTask] = useState<{ id: string; title: string } | null>(null);
  const [hiring, setHiring] = useState(false);

  // What you set up once lives under the settings; what belongs together
  // lives under one tab. Old links land at the new place instead of on the
  // backlog — shared links and bookmarks should not run into empty space.
  useEffect(() => {
    const to = MOVED[tab];
    if (!to) return;
    setSp(
      (prev) => {
        const n = new URLSearchParams(prev);
        n.set("tab", to[0]);
        if (to[2] === "") n.delete(to[1]);
        else n.set(to[1], to[2]);
        return n;
      },
      { replace: true },
    );
  }, [tab, setSp]);

  // The tab row scrolls sideways on a phone: the selected tab is brought into
  // view, and the clipped edge fades. The row exists only once the agent has
  // loaded, hence the dependency.
  const tabsRef = useEdgeFade<HTMLDivElement>([agent.isLoading, tab]);
  useEffect(() => {
    const row = tabsRef.current;
    const on = row?.querySelector<HTMLElement>('[aria-current="true"]');
    if (row && on) row.scrollLeft = on.offsetLeft - (row.clientWidth - on.offsetWidth) / 2;
  }, [agent.isLoading, tab, tabsRef]);

  const act = useMutation({
    mutationFn: (action: string) => post(`/agents/${id}/${action}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["agent", id] });
      qc.invalidateQueries({ queryKey: ["agents"] });
    },
  });

  if (agent.isLoading) return null;
  /* Not found means: not in the organisation this session works in. Most often
     after signing in again — ?weiter= brings back the old address, and the new
     session starts in the account's oldest seat — or after following a link
     into another organisation. A sentence with nothing to click is a dead end;
     the start page is not — the view the person came from, or the list. */
  if (isNotFound(agent.error)) return <Navigate to={viewUrl(originOf(id, state, !!me.TeamSurface))} replace />;
  if (agent.isError || !agent.data) return <p className="danger-text">{t("agent.notFound")}</p>;
  const a = agent.data;

  return (
    <div>
      <div className="flex items-center gap-3 mb-5 flex-wrap">
        <h1 className="text-[22px]">{a.display_name}</h1>
        {isDraft(a) ? (
          <span className="badge state st-draft">{t("dashboard.draftBadge")}</span>
        ) : a.wake_trouble && !a.killed ? (
          /* Not `schläft`: this agent is trying to wake up and cannot.
             The reason belongs next to the state — an error that only the
             raw recording data knows will not be read (#139). */
          <span
            className="badge state st-wake-failed"
            title={t("agent.wakeFailedWhy", { n: a.wake_trouble.failures, err: a.wake_trouble.error ?? "" })}
          >
            {t("status.wakeFailed")}
          </span>
        ) : (
          <span className={`badge state st-${a.killed ? "killed" : a.status}`}>
            {t(`status.${a.killed ? "killed" : a.status}`, a.status)}
          </span>
        )}
        {(a.status === "working" || a.status === "triage" || a.status === "triggered") && (
          <span className="live-dot" title={t("agent.sandbox")} />
        )}
        {/* The status says `triggered`; what the agent is waiting for
            only the phase tells — and on a fresh host that is the longest
            wait the platform has. */}
        {a.phase && <PhaseBadge phase={a.phase} />}
        <span className="muted text-xs mono">
          runtime: {a.runtime}
          {a.model && ` · ${a.model}`}
        </span>
        <span className="ml-auto" />
        {/* The conversation is where this colleague is talked to; it exists
            with the team surface only, and not for a draft, which has not
            started yet. */}
        {me.TeamSurface && !isDraft(a) && (
          <Link to={`/team/${a.id}`} className="btn sm no-underline">
            <NavIcon name="chat" />
            {t("team.gespraech")}
          </Link>
        )}
        {/* A draft has no first day — `wecken` would be the wrong button
            where `einstellen` stands. */}
        {canManage(me.Role) && !isDraft(a) && (
          <button className="btn sm" onClick={() => act.mutate("wake")}>
            {t("agent.wake")}
          </button>
        )}
        {canManage(me.Role) && isDraft(a) && (
          <button className="btn sm primary" onClick={() => setHiring(true)}>
            {t("hire.action")}
          </button>
        )}
        {/* Kill switch only for one that can run. Stopping a draft
            is not an action — it has not started. */}
        {canKill(me.Role) && !isDraft(a) &&
          (a.killed ? (
            <button className="btn sm" onClick={() => act.mutate("resume")}>
              {t("agent.resume")}
            </button>
          ) : (
            <button className="btn sm danger" onClick={() => act.mutate("kill")} title={t("agent.stopTitle")}>
              {t("agent.stop")}
            </button>
          ))}
      </div>

      {isDraft(a) && (
        <div className="card mb-4" style={{ borderStyle: "dashed" }}>
          <div className="text-sm" style={{ fontWeight: 600, marginBottom: 2 }}>
            {t("hire.bannerTitle")}
          </div>
          <p className="muted text-xs" style={{ maxWidth: 640 }}>{t("hire.bannerLead")}</p>
        </div>
      )}

      {hiring && <HireDialog agent={a} onClose={() => setHiring(false)} />}

      <CostBar agentId={a.id} budget={a.budget_usd} />

      <Performance agentId={a.id} />

      <LintFindings agentId={a.id} />

      <div className="agent-tabs mb-4 mt-5" ref={tabsRef}>
        {(
          [
            ["backlog", t("agent.tabs.backlog")],
            ["recording", t("agent.tabs.recording")],
            ["akte", t("agent.tabs.record")],
            ["memory", t("agent.tabs.memory")],
            ["dateien", t("agent.tabs.files")],
            ["werkzeuge", t("agent.tabs.toolsSkills")],
            ["einstellungen", t("agent.tabs.settings")],
          ] as const
        )
          // The workspace shows what lies in the agent's home — only its
          // managers and security see that, not every role. The work record
          // follows the same thought one step further (spec/21): a
          // cost total says what was spent, a record says how someone
          // worked — controlling does not see it.
          .filter(([key]) => key !== "dateien" || canFiles(me.Role))
          .filter(([key]) => key !== "akte" || canRecord(me.Role))
          .map(([key, label]) => (
          <button
            key={key}
            aria-current={tab === key ? "true" : undefined}
            onClick={() => {
              if (key === "recording") setRecTask(null);
              setTab(key);
            }}
            className="btn sm"
            style={{
              border: "none",
              borderRadius: "8px 8px 0 0",
              borderBottom: tab === key ? "2px solid var(--text-accent)" : "2px solid transparent",
              color: tab === key ? "var(--text-primary)" : "var(--text-secondary)",
            }}
          >
            {label}
          </button>
        ))}
      </div>

      {tab === "backlog" && (
        <Backlog
          agentId={a.id}
          phase={a.phase}
          canManage={canManage(me.Role)}
          onShowRecording={(id, title) => {
            setRecTask({ id, title });
            setTab("recording");
          }}
        />
      )}
      {tab === "recording" && (
        <Recording agentId={a.id} taskFilter={recTask} onClearFilter={() => setRecTask(null)} />
      )}
      {tab === "akte" && canRecord(me.Role) && <WorkRecord agentId={a.id} />}
      {tab === "memory" && <Memories agentId={a.id} canManage={canManage(me.Role)} />}
      {tab === "dateien" && canFiles(me.Role) && (
        <>
          <AgentFiles agent={a} canWrite={canManage(me.Role)} />
          {/* The home store next to the file browser (spec/16): what the home
              weighs, that only this agent holds it, and the snapshots. */}
          <AgentHome agent={a} canWrite={canManage(me.Role)} />
        </>
      )}
      {tab === "werkzeuge" && (
        <AgentTooling
          agentId={a.id}
          canManage={canManage(me.Role)}
          canSecrets={canSecrets(me.Role)}
        />
      )}
      {tab === "einstellungen" && (
        <AgentSettings
          agent={a}
          editable={canManage(me.Role)}
          canManage={canManage(me.Role)}
          canSecrets={canSecrets(me.Role)}
          isSecurity={me.Role === "security"}
        />
      )}
    </div>
  );
}

// Tools bundle what the agent works with: what it can do in the attached
// target systems, which of these MCP tools it may use and which
// Skills it pulls. As three separate tabs the question "what can the
