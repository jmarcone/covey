import { useEffect, useId, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import i18n, { uiLocale } from "../../i18n";
import {
  api,
  ApiError,
  post,
  patch,
  del,
  buildInfo,
  type AgentPhase,
  type Stage,
  type Task,
  type TaskNote,
} from "../../api";
import { fmtUSD } from "../../format";
import { upstreamIssueURL } from "../../upstream";
import { PhaseBadge } from "../../components/PhaseBadge";
import { useNarrow } from "../../components/ConsoleDrawer";
import { useEdgeFade } from "../../components/useEdgeFade";

const PARKED_PREFIX = "parked:";

/** What a parked task waits for — its correlation key without "parked:" — or
 *  undefined when the task is not a blocked, parked one. */
export function parkedOn(task: Pick<Task, "state" | "correlation_key">): string | undefined {
  if (task.state !== "blocked" || !task.correlation_key?.startsWith(PARKED_PREFIX)) return undefined;
  return task.correlation_key.slice(PARKED_PREFIX.length);
}

type BacklogProps = {
  agentId: string;
  // What the platform is currently working on for this agent. It hangs on the
  // agent, shown at the task that runs: whoever looks at the task is waiting
  // for exactly this process.
  phase?: AgentPhase;
  canManage: boolean;
  onShowRecording: (taskId: string, title: string) => void;
};

// One board per agent. The router keeps this component mounted when the page
// moves to another agent (Back, a search hit), and with it the draft, the
// search and the open form of the one before: a draft started on A would be
// filed on B. The key throws all of that away with the agent.
export function Backlog(props: BacklogProps) {
  return <AgentBacklog key={props.agentId} {...props} />;
}

function AgentBacklog({ agentId, phase, canManage, onShowRecording }: BacklogProps) {
  const { t } = useTranslation();
  const [showArchive, setShowArchive] = useState(false);
  const tasks = useQuery({
    queryKey: ["backlog", agentId, showArchive],
    queryFn: () => api<Task[]>(`/agents/${agentId}/backlog${showArchive ? "?archived=1" : ""}`),
  });
  const invalBacklog = useInvalidateBacklog(agentId);
  // Search (debounced, then the backend's ?q=). It replaces the board with a
  // flat list of hits: a search runs across the columns and across the archive,
  // and both of those are exactly what the board does not show.
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  useEffect(() => {
    const h = setTimeout(() => setDebounced(query.trim()), 250);
    return () => clearTimeout(h);
  }, [query]);
  const searching = debounced.length > 0;
  const search = useQuery({
    queryKey: ["backlog-search", agentId, debounced],
    queryFn: () => api<Task[]>(`/agents/${agentId}/backlog?q=${encodeURIComponent(debounced)}`),
    enabled: searching,
  });
  const hits = search.data ?? [];
  const cleanup = useMutation({
    mutationFn: () => post<{ archived: number }>(`/agents/${agentId}/backlog/cleanup`),
    onSuccess: invalBacklog,
  });
  const cleanupCount = (tasks.data ?? []).filter(
    (t) => terminalStates.includes(t.state) && !t.archived_at,
  ).length;
  // The New task form sits behind a button: the board is what the tab is for.
  // Closing the form keeps the draft — Escape is a way of looking at the board
  // again, not of throwing the text away; adding the task or leaving the tab
  // does that.
  const [composing, setComposing] = useState(false);
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const titleRef = useRef<HTMLInputElement>(null);
  const newTaskRef = useRef<HTMLButtonElement>(null);
  const formId = useId();
  const titleId = useId();
  // The card the last add created. It is marked for a moment so that the eye
  // finds it on a full board; the id comes from the POST, not from guessing.
  const [added, setAdded] = useState<string | null>(null);
  const create = useMutation({
    mutationFn: () => post<Task>(`/agents/${agentId}/tasks`, { title, body }),
    onSuccess: (task) => {
      setTitle("");
      setBody("");
      setComposing(false);
      setAdded(task.id);
      // In the stacked board a folded stage hides the card, and the mark would
      // run out behind the fold: the stage the task lands in is opened.
      setFolded((prev) => new Map(prev).set(stageKey(task.stage_id && known.has(task.stage_id) ? task.stage_id : null), true));
      invalBacklog();
    },
  });
  // Not while the POST is out: resetting the mutation then detaches it from
  // the form, Add comes back enabled on the same draft (a second task), and the
  // answer that is still on its way would close or clear a form opened since,
  // or lose its error. The form stays as it is until the answer is there.
  const closeComposer = () => {
    if (create.isPending) return;
    setComposing(false);
    create.reset();
  };
  // Opening puts the cursor into Title; closing hands the focus back to the
  // button the form came from, so the keyboard does not fall to the top of
  // the page. Only if it has nowhere else to be: when the form unmounts with
  // the focus inside it, the focus falls to the body; a reader who has gone to
  // the search or a card's note while a slow POST was out keeps it there.
  const wasComposing = useRef(false);
  useEffect(() => {
    if (composing) titleRef.current?.focus();
    else if (wasComposing.current && (!document.activeElement || document.activeElement === document.body)) {
      newTaskRef.current?.focus({ preventScroll: true });
    }
    wasComposing.current = composing;
  }, [composing]);
  // "n" opens the form from the board, as "/" reaches the search on the
  // dashboard. The listener lives as long as the Backlog does, so it is there
  // only while the tab is shown. It stays out of the way of anyone typing (the
  // guard of the "?" in AppShell) and leaves Ctrl/Cmd/Alt+N to the browser;
  // preventDefault keeps the n itself out of Title, which has the focus by the
  // time the key's input would land.
  useEffect(() => {
    if (!canManage) return;
    // True from the press that opened the form until that key is let go: a held
    // key repeats into whatever has the focus, and by then that is Title.
    let openedByKey = false;
    const onKey = (e: KeyboardEvent) => {
      if (!isNewTaskKey(e)) return;
      if (e.repeat) {
        if (openedByKey) e.preventDefault();
        return;
      }
      openedByKey = false;
      if (e.ctrlKey || e.metaKey || e.altKey || e.defaultPrevented || e.isComposing) return;
      const el = e.target as HTMLElement;
      if (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT" || el.isContentEditable) return;
      // Not behind an open dialog: the key belongs to what is in front.
      if (document.querySelector(DIALOG_OPEN)) return;
      e.preventDefault();
      openedByKey = true;
      setComposing(true);
      titleRef.current?.focus();
    };
    const onUp = (e: KeyboardEvent) => {
      if (isNewTaskKey(e)) openedByKey = false;
    };
    window.addEventListener("keydown", onKey);
    window.addEventListener("keyup", onUp);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("keyup", onUp);
    };
  }, [canManage]);
  // The mark lasts about two seconds from the moment the card is on the board:
  // the refetch after the add may take longer than the flash would. It does not
  // run while a search hides the board (its hits carry no mark), and a mark that
  // has not found its card after ADDED_GIVE_UP_MS — the refetch failed, or the
  // search stayed open — is dropped, so that it does not flash on some later
  // refetch.
  const addedShown = !!added && !searching && (tasks.data ?? []).some((tk) => tk.id === added);
  useEffect(() => {
    if (!addedShown) return;
    const h = setTimeout(() => setAdded(null), ADDED_MARK_MS);
    return () => clearTimeout(h);
  }, [addedShown]);
  useEffect(() => {
    if (!added) return;
    const h = setTimeout(() => setAdded(null), ADDED_GIVE_UP_MS);
    return () => clearTimeout(h);
  }, [added]);

  const stages = useQuery({
    queryKey: ["stages", agentId],
    queryFn: () => api<Stage[]>(`/agents/${agentId}/stages`),
  });
  const move = useMutation({
    mutationFn: ({ taskId, stageId }: { taskId: string; stageId: string | null }) =>
      post(`/tasks/${taskId}/stage`, { stage_id: stageId }),
    onSuccess: invalBacklog,
  });
  const [dragTask, setDragTask] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  // Columns show the newest COLUMN_LIMIT cards; whoever wants the older ones
  // unfolds the column. Per column, because one deep column should not push
  // every other one long.
  const [unfolded, setUnfolded] = useState<Set<string>>(new Set());
  const toggleUnfold = (key: string) =>
    setUnfolded((prev) => {
      const next = new Set(prev);
      if (!next.delete(key)) next.add(key);
      return next;
    });

  const stageList = stages.data ?? [];
  const known = new Set(stageList.map((s) => s.id));
  const inStage = (tk: Task, id: string | null) =>
    id === null ? !tk.stage_id || !known.has(tk.stage_id) : tk.stage_id === id;
  const orphans = (tasks.data ?? []).filter((tk) => inStage(tk, null));
  const columns: { id: string | null; name: string; color: string }[] = [
    ...(orphans.length ? [{ id: null, name: t("agent.backlog.noStageColumn"), color: "var(--text-muted)" }] : []),
    ...stageList.map((s) => ({ id: s.id, name: stageLabel(s, t), color: s.color || "var(--text-secondary)" })),
  ];

  // Up to 768 px the stages stack, one heading per stage as a button. Open by
  // default: the first stage with a task in it and the stage holding the
  // running one — what you came to look at. A stage you toggled stays as you
  // left it.
  const stacked = useNarrow(STACK_BELOW);
  const [folded, setFolded] = useState<Map<string, boolean>>(new Map());
  const stageKey = (id: string | null) => id ?? "__none";
  const firstFilled = columns.find((c) => (tasks.data ?? []).some((tk) => inStage(tk, c.id)));
  const running = columns.find((c) => (tasks.data ?? []).some((tk) => tk.state === "in_progress" && inStage(tk, c.id)));
  const stageOpen = (id: string | null) =>
    folded.get(stageKey(id)) ?? (firstFilled?.id === id || running?.id === id);
  const boardRef = useEdgeFade<HTMLDivElement>([columns.length, stacked]);

  const drop = (stageId: string | null) => {
    if (dragTask) move.mutate({ taskId: dragTask, stageId });
    setDragTask(null);
  };

  return (
    <div>
      {/* The toolbar does not wait for the board: whoever came to add a task
          should not have to wait for the backlog to load first. */}
      <div className="flex items-center gap-2 mb-3 bl-bar">
        <input
          type="search"
          className="bl-search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t("agent.backlog.searchPlaceholder")}
          title={t("agent.backlog.searchHint")}
        />
        {canManage && (
          <button
            ref={newTaskRef}
            className="btn create sm bl-new"
            disabled={composing}
            aria-expanded={composing}
            aria-controls={formId}
            aria-keyshortcuts="N"
            title={t("agent.backlog.newTaskShortcut", { label: t("agent.backlog.newTask") })}
            onClick={() => setComposing(true)}
          >
            <span aria-hidden="true">+</span> {t("agent.backlog.newTask")}
          </button>
        )}
        <button
          className="btn sm"
          style={canManage ? undefined : { marginLeft: "auto" }}
          disabled={searching}
          title={searching ? t("agent.backlog.searchHint") : undefined}
          onClick={() => setShowArchive((v) => !v)}
        >
          {showArchive ? t("agent.backlog.hideArchive") : t("agent.backlog.showArchive")}
        </button>
        {canManage && (
          <>
            <button
              className="btn sm"
              disabled={cleanupCount === 0 || cleanup.isPending}
              title={t("agent.backlog.cleanupTitle")}
              onClick={() => cleanup.mutate()}
            >
              {t("agent.backlog.cleanup")}{cleanupCount > 0 ? ` (${cleanupCount})` : ""}
            </button>
            <button className="btn sm" onClick={() => setEditing((v) => !v)}>
              {editing ? t("agent.backlog.doneEditing") : t("agent.backlog.editColumns")}
            </button>
          </>
        )}
      </div>

      {canManage && composing && (
        <form
          id={formId}
          className="card mb-4"
          onSubmit={(e) => {
            e.preventDefault();
            // `required` stops "" but not blanks, and the server's refusal of
            // them is English. A blank title is emptied instead, so the next
            // press meets `required` in the user's language, as "" does.
            if (!title.trim()) {
              setTitle("");
              titleRef.current?.focus();
              return;
            }
            create.mutate();
          }}
          onKeyDown={(e) => {
            // Escape while an input method is composing (ja, zh) cancels the
            // composition, not the form.
            if (e.key === "Escape" && !e.nativeEvent.isComposing) {
              e.preventDefault();
              closeComposer();
            }
          }}
        >
          <label htmlFor={titleId}>{t("agent.backlog.newTask")}</label>
          <input
            id={titleId}
            ref={titleRef}
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder={t("agent.backlog.titlePlaceholder")}
            className="mb-2"
            required
            // readOnly, not disabled, so the focus stays and Escape is still
            // heard. The answer clears both fields and would take whatever was
            // typed while the POST was out, so they take no input until then.
            readOnly={create.isPending}
          />
          <textarea
            value={body}
            onChange={(e) => setBody(e.target.value)}
            readOnly={create.isPending}
            placeholder={t("agent.backlog.descPlaceholder")}
            aria-label={t("agent.backlog.descPlaceholder")}
            rows={2}
            className="mb-2"
          />
          {create.isError && (
            <p className="danger-text text-xs mb-2" role="alert">
              {t("agent.backlog.addFailed", { error: (create.error as Error).message })}
            </p>
          )}
          <div className="flex gap-2">
            <button className="btn primary sm" disabled={create.isPending}>
              {t("agent.backlog.addToBacklog")}
            </button>
            <button type="button" className="btn sm" disabled={create.isPending} onClick={closeComposer}>
              {t("agent.backlog.cancelNewTask")}
            </button>
          </div>
        </form>
      )}
      {tasks.data && stages.data && (
        <>
          {!searching && (
            <p className="muted text-xs mb-3">
              {stageList.length === 0
                ? t("agent.backlog.noStages")
                : canManage
                  ? t("agent.backlog.stagesInfo")
                  : t("agent.backlog.stagesReadonly")}
            </p>
          )}

          {editing && canManage && <StageEditor agentId={agentId} stages={stageList} />}

          {searching ? (
            <SearchResults
              agentId={agentId}
              canManage={canManage}
              query={debounced}
              hits={hits}
              pending={search.isPending}
              stageName={(tk) => {
                const st = stageList.find((st) => st.id === tk.stage_id);
                return st && stageLabel(st, t);
              }}
              onShowRecording={onShowRecording}
            />
          ) : columns.length === 0 ? (
            <p className="muted">{t("agent.backlog.empty")}</p>
          ) : (
            <div
              className={`kanban${stacked ? " stacked" : ""}`}
              style={{ ["--kcols" as string]: columns.length }}
              ref={boardRef}
            >
              {columns.map((col) => {
                const all = (tasks.data ?? [])
                  .filter((tk) => inStage(tk, col.id))
                  .sort((a, b) => b.created_at.localeCompare(a.created_at));
                const key = stageKey(col.id);
                const open = unfolded.has(key);
                const items = open ? all : all.slice(0, COLUMN_LIMIT);
                const hidden = all.length - items.length;
                const shown = !stacked || stageOpen(col.id);
                return (
                  <div
                    className={`kcol${dragTask ? " droppable" : ""}`}
                    key={key}
                    onDragOver={(e) => {
                      if (dragTask) e.preventDefault();
                    }}
                    onDrop={() => drop(col.id)}
                  >
                    {stacked ? (
                      <button
                        type="button"
                        className="kh kh-btn"
                        aria-expanded={shown}
                        aria-controls={`kcol-${key}`}
                        onClick={() => setFolded((prev) => new Map(prev).set(key, !shown))}
                      >
                        <span className="dot" style={{ background: col.color }} />
                        <span className="knm" title={col.name}>{col.name}</span>
                        <span className="n">{all.length}</span>
                        <span className="chev" aria-hidden="true">›</span>
                      </button>
                    ) : (
                      <div className="kh">
                        <span className="dot" style={{ background: col.color }} />
                        <span className="knm" title={col.name}>{col.name}</span>
                        <span className="n">{all.length}</span>
                      </div>
                    )}
                    <div id={`kcol-${key}`} className={stacked ? "kc-stage" : undefined} hidden={!shown}>
                      {items.map((tk) => (
                        <TaskCard
                          key={tk.id}
                          task={tk}
                          agentId={agentId}
                          phase={phase}
                          canManage={canManage}
                          onShowRecording={onShowRecording}
                          justAdded={tk.id === added}
                          onDragStart={() => setDragTask(tk.id)}
                          onDragEnd={() => setDragTask(null)}
                        />
                      ))}
                      {all.length === 0 && <div className="kc-empty">—</div>}
                      {hidden > 0 && (
                        <button className="btn sm kc-more" onClick={() => toggleUnfold(key)}>
                          {t("agent.backlog.showOlder", { count: hidden })}
                        </button>
                      )}
                      {open && all.length > COLUMN_LIMIT && (
                        <button className="btn sm kc-more" onClick={() => toggleUnfold(key)}>
                          {t("agent.backlog.showFewer")}
                        </button>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </>
      )}
    </div>
  );
}

// useInvalidateBacklog refreshes both views of the backlog: the board and the
// search. Whatever a card does — archive, discard, reschedule — has to arrive in
// the list the user is currently looking at, and during a search that is the
// list of hits.
function useInvalidateBacklog(agentId: string) {
  const qc = useQueryClient();
  return () => {
    qc.invalidateQueries({ queryKey: ["backlog", agentId] });
    qc.invalidateQueries({ queryKey: ["backlog-search", agentId] });
  };
}

// SearchResults shows the hits as one flat list rather than as a board. A hit
// may come from any column and from the archive, so the column it sits in is
// written onto the card — otherwise the found task would have lost its place.
function SearchResults({
  agentId,
  canManage,
  query,
  hits,
  pending,
  stageName,
  onShowRecording,
}: {
  agentId: string;
  canManage: boolean;
  query: string;
  hits: Task[];
  pending: boolean;
  stageName: (task: Task) => string | undefined;
  onShowRecording: (taskId: string, title: string) => void;
}) {
  const { t } = useTranslation();
  if (pending) return <p className="muted">{t("agent.backlog.searching")}</p>;
  if (hits.length === 0) return <p className="muted">{t("agent.backlog.searchEmpty", { q: query })}</p>;
  return (
    <div className="bl-results">
      <div className="muted text-xs mb-2">
        {t("agent.backlog.searchResults", { count: hits.length })}
        {hits.length >= SEARCH_LIMIT ? ` · ${t("agent.backlog.searchCapped", { n: SEARCH_LIMIT })}` : ""}
      </div>
      {hits.map((tk) => (
        <TaskCard
          key={tk.id}
          task={tk}
          agentId={agentId}
          canManage={canManage}
          stageName={stageName(tk)}
          onShowRecording={onShowRecording}
        />
      ))}
    </div>
  );
}

// stageLabel is what a column is called on screen. A default column nobody
// renamed follows the chosen language — switching it relabels the board without
// asking the server again; a renamed one, and every other column, shows its name.
export function stageLabel(s: Stage, t: TFunction): string {
  return s.key && !s.renamed ? t(`agent.backlog.defaultStage.${s.key}`) : s.name;
}

const STAGE_PALETTE = [
  "var(--text-muted)",
  "var(--text-accent)",
  "var(--text-warning)",
  "var(--text-success)",
  "var(--text-danger)",
  "var(--text-pro)",
];

function StageEditor({ agentId, stages }: { agentId: string; stages: Stage[] }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const inval = () => qc.invalidateQueries({ queryKey: ["stages", agentId] });
  const invalBoth = () => {
    inval();
    qc.invalidateQueries({ queryKey: ["backlog", agentId] });
  };
  const [name, setName] = useState("");
  // Bumped when a change is refused: the column fields are uncontrolled, so a
  // new key is what puts the stored name back into them. Otherwise the refused
  // text stayed in the field and looked saved once the message was gone.
  const [refills, setRefills] = useState(0);
  // Each of the two mutations clears the other's error when it starts, so the
  // message below speaks of the last change only: a refused "Add" is not still
  // on screen after a rename that went through.
  const create = useMutation({
    mutationFn: () =>
      post(`/agents/${agentId}/stages`, {
        name,
        color: STAGE_PALETTE[stages.length % STAGE_PALETTE.length],
      }),
    onMutate: (): void => update.reset(),
    onSuccess: () => {
      setName("");
      inval();
    },
  });
  // A color change or a move sends the stored name back, so nothing is renamed
  // by the way. default_name gives a default column its own name back: it
  // shows the reader's language again.
  const update = useMutation({
    mutationFn: (s: Stage & { default_name?: boolean }) =>
      patch(`/stages/${s.id}`, { name: s.name, color: s.color, position: s.position, default_name: s.default_name }),
    onMutate: (): void => create.reset(),
    onSuccess: inval,
    onError: () => setRefills((n) => n + 1),
  });
  // 409: the board has that name already, or a default column of that meaning
  // ("Done" beside the done column). Said, rather than the field just
  // keeping what was typed.
  const refused = [create.error, update.error].some((e) => e instanceof ApiError && e.status === 409);
  const remove = useMutation({
    mutationFn: (id: string) => del(`/stages/${id}`),
    onSuccess: invalBoth,
  });
  const reorder = useMutation({
    mutationFn: (order: string[]) => post(`/agents/${agentId}/stages/reorder`, { order }),
    onSuccess: inval,
  });

  const swap = (i: number, j: number) => {
    if (j < 0 || j >= stages.length) return;
    const ids = stages.map((s) => s.id);
    [ids[i], ids[j]] = [ids[j], ids[i]];
    reorder.mutate(ids);
  };
  const cycleColor = (s: Stage) => {
    const idx = STAGE_PALETTE.indexOf(s.color);
    update.mutate({ ...s, color: STAGE_PALETTE[(idx + 1) % STAGE_PALETTE.length] });
  };

  return (
    <div className="card mb-4">
      <label>{t("agent.stageEditor.title")}</label>
      {stages.map((s, i) => {
        const label = stageLabel(s, t);
        return (
          <div key={s.id} className="flex items-center gap-2 mb-2">
            <button
              type="button"
              className="dot-btn"
              title={t("agent.stageEditor.changeColor")}
              onClick={() => cycleColor(s)}
            >
              <span className="dot" style={{ background: s.color || "var(--text-secondary)" }} />
            </button>
            {/* Keyed by the label, so that a switch of language refills the
                field, and by the refusals, so that a refused name gives way
                to the stored one. On a renamed default column, typing its
                label in the chosen language undoes the rename; any other text
                is stored as typed — a label of another language too. */}
            <input
              key={`${label}#${refills}`}
              className="flex-1"
              defaultValue={label}
              onBlur={(e) => {
                const typed = e.target.value.trim();
                if (!typed || typed === label) return;
                if (s.key && typed === t(`agent.backlog.defaultStage.${s.key}`)) {
                  update.mutate({ ...s, default_name: true });
                } else {
                  update.mutate({ ...s, name: typed });
                }
              }}
            />
            <button type="button" className="btn sm" onClick={() => swap(i, i - 1)} disabled={i === 0}>
              ↑
            </button>
            <button
              type="button"
              className="btn sm"
              onClick={() => swap(i, i + 1)}
              disabled={i === stages.length - 1}
            >
              ↓
            </button>
            <button type="button" className="btn sm danger" onClick={() => remove.mutate(s.id)}>
              {t("agent.stageEditor.delete")}
            </button>
          </div>
        );
      })}
      <form
        className="flex gap-2 mt-2"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <input
          className="flex-1"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={t("agent.stageEditor.newColumn")}
          required
        />
        <button className="btn primary sm" disabled={create.isPending}>
          {t("agent.stageEditor.add")}
        </button>
      </form>
      {refused && <p className="danger-text mt-2">{t("agent.stageEditor.nameTaken")}</p>}
    </div>
  );
}

function relTime(iso: string): string {
  const min = Math.round((Date.now() - new Date(iso).getTime()) / 60000);
  if (min < 1) return i18n.t("activity.timeJustNow");
  if (min < 60) return i18n.t("activity.timeMinutes", { n: min });
  const h = Math.round(min / 60);
  if (h < 24) return i18n.t("activity.timeHours", { n: h });
  const locale = uiLocale();
  return new Date(iso).toLocaleDateString(locale);
}

// originLabel makes the origin readable. Two forms carry an ID or a slug in the
// text: "continuation:<task-id>" (continuation of a run aborted at the turn
// limit) and "agent:<slug>" (the agent created the task itself or it was
// delegated to it). Everything else stays as it is.
function originLabel(origin: string): string {
  if (origin.startsWith("continuation:")) return i18n.t("agent.backlog.originContinuation");
  if (origin.startsWith("agent:")) return i18n.t("agent.backlog.originAgent", { slug: origin.slice(6) });
  if (origin === "heartbeat") return i18n.t("agent.backlog.originHeartbeat");
  return origin;
}

const terminalStates = ["done", "failed", "cancelled"];

const COLUMN_LIMIT = 7;
// Below this width the board stacks, one folding heading per stage.
const STACK_BELOW = "(max-width: 768px)";

// ADDED_MARK_MS is how long a card just added stays marked (.kc-new);
// ADDED_GIVE_UP_MS how long the mark waits for its card to show up at all.
const ADDED_MARK_MS = 2000;
const ADDED_GIVE_UP_MS = 10000;

// What counts as an open dialog for the "n" key. HelpDrawer and the notes drawer
// are role=dialog without aria-modal: they keep the focus outside, so the
// modal attribute alone would let the key open the form behind them. A dialog
// that is closed stays in the DOM inert (the console navigation at phone width
// in AppShell), and does not count.
const DIALOG_OPEN =
  '[aria-modal="true"], [role="dialog"]:not([inert]), [role="alertdialog"]:not([inert])';

// isNewTaskKey is the "n" of the shortcut. Where the layout produces no Latin
// letter (Cyrillic, Greek) the key at the N position of a QWERTY board counts
// instead; a Latin layout that puts another letter there (Dvorak's b) does not.
function isNewTaskKey(e: KeyboardEvent): boolean {
  if (e.key === "n" || e.key === "N") return true;
  return e.code === "KeyN" && !/^[a-z]$/i.test(e.key);
}

// SEARCH_LIMIT mirrors backlog.SearchMaxResults — only so that the list can say
// when it is showing the cap rather than the whole truth.
const SEARCH_LIMIT = 50;

function TaskCard({
  task,
  agentId,
  phase,
  canManage,
  stageName,
  onShowRecording,
  justAdded,
  onDragStart,
  onDragEnd,
}: {
  task: Task;
  agentId: string;
  phase?: AgentPhase;
  canManage: boolean;
  stageName?: string;
  onShowRecording: (taskId: string, title: string) => void;
  justAdded?: boolean;
  onDragStart?: () => void;
  onDragEnd?: () => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const cardRef = useRef<HTMLDivElement>(null);
  // A card just added may land below the fold of a long column or off to the
  // side of a wide board; it is brought into view, without sliding there when
  // the reader asked for less motion.
  useEffect(() => {
    if (!justAdded) return;
    const still = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
    cardRef.current?.scrollIntoView?.({ block: "nearest", inline: "nearest", behavior: still ? "auto" : "smooth" });
  }, [justAdded]);
  const invalBacklog = useInvalidateBacklog(agentId);
  const notes = useQuery({
    queryKey: ["task-notes", task.id],
    queryFn: () => api<TaskNote[]>(`/tasks/${task.id}/notes`),
    enabled: open,
  });
  // Where a platform finding is reported to: always into the project this binary
  // comes from (buildinfo.SourceRepo). NOT into the repository the organisation
  // entered for covey Doctor — that is the address where they conduct their own
  // processes, and a bug of the platform belongs where the platform is
  // maintained. A fork carries its own address in the constant and so points at
  // its tracker.
  const build = useQuery({
    queryKey: ["build-info"],
    queryFn: buildInfo,
    enabled: open,
    staleTime: 60 * 60 * 1000,
  });
  const repo = { system: build.data?.source_system, project: build.data?.source_project };
  const cancel = useMutation({
    mutationFn: () => post(`/tasks/${task.id}/cancel`),
    onSuccess: invalBacklog,
  });
  const retry = useMutation({
    mutationFn: () => post(`/tasks/${task.id}/retry`),
    onSuccess: invalBacklog,
  });
  const archive = useMutation({
    mutationFn: () => post(`/tasks/${task.id}/archive`),
    onSuccess: invalBacklog,
  });
  const archived = !!task.archived_at;
  // A task from a conversation the reader is not in comes without its text.
  const title = task.redacted ? t("agent.backlog.redactedTitle") : task.title;
  const terminal = terminalStates.includes(task.state);
  // Draggable only where there is somewhere to drop it: the search list has no
  // columns, and a grab cursor that leads nowhere is a lie.
  const draggable = canManage && !archived && !!onDragStart;
  // A task blocked on "parked:<what>" waits on purpose until <what> is ready
  // (a branch, a PR); the board says so instead of "waiting on parked:…".
  const parkedUntil = parkedOn(task);
  const subtitle =
    task.state === "blocked"
      ? parkedUntil !== undefined
        ? t("agent.backlog.parkedUntil", { key: parkedUntil })
        : task.correlation_key
          ? t("agent.backlog.waitingOn", { key: task.correlation_key })
          : t("agent.backlog.blocked")
      : relTime(task.updated_at);
  return (
    <div
      ref={cardRef}
      className={`kc${open ? " expanded" : ""}${draggable ? " draggable" : ""}${justAdded ? " kc-new" : ""}`}
      draggable={draggable}
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
      onClick={() => setOpen((v) => !v)}
      style={archived ? { opacity: 0.55 } : undefined}
    >
      <div className="kc-title">
        <span className={`badge state st-${task.state} kc-state`}>
          {parkedUntil !== undefined ? t("agent.backlog.parked") : t(`status.${task.state}`, task.state)}
        </span>
        <span className={`font-medium min-w-0 truncate${task.redacted ? " muted" : ""}`}>{title}</span>
        {archived && <span className="muted text-[11px] shrink-0">{t("agent.backlog.archived")}</span>}
        <span className="kc-prio">P{task.priority}</span>
      </div>
      {/* If the task is running and the platform is doing something for it
          right now, it stands here: "in progress" alone does not explain a
          quarter hour in which the image is being fetched. */}
      {phase && task.state === "in_progress" && (
        <div className="kc-phase">
          <PhaseBadge phase={phase} compact />
        </div>
      )}
      <div className="t">
        {subtitle} · {originLabel(task.origin)}
        {stageName && <> · {stageName}</>}
        {task.cost_usd !== undefined && (
          <>
            {" · "}
            <span title={t("agent.backlog.costHint", { n: task.cost_entries ?? 0 })}>
              {fmtUSD(task.cost_usd)}
            </span>
          </>
        )}
      </div>
      {open && (
        <div className="kc-detail fade">
          {task.redacted && <p className="muted text-xs mb-2">{t("agent.backlog.redactedHint")}</p>}
          {task.body && (
            <pre className="voice whitespace-pre-wrap m-0 mb-2 secondary">{task.body}</pre>
          )}
          {task.correlation_key && (
            <p>
              <span className="muted">{t("agent.backlog.correlationKey")}:</span>{" "}
              <span className="mono">{task.correlation_key}</span>
            </p>
          )}
          {task.state === "blocked" && !task.redacted && task.blocked_reason && (
            <p>
              <span className="muted">{t("agent.backlog.reason")}:</span> {task.blocked_reason}
            </p>
          )}
          {task.cost_usd !== undefined && (
            <p>
              <span className="muted">{t("agent.backlog.cost")}:</span>{" "}
              <span className="mono">{fmtUSD(task.cost_usd)}</span>{" "}
              <span className="muted">{t("agent.backlog.costEntries", { n: task.cost_entries ?? 0 })}</span>
            </p>
          )}
          {task.runtime_session_id && (
            <p>
              <span className="muted">runtime-session:</span> <span className="mono">{task.runtime_session_id}</span>
            </p>
          )}
          {task.parent_task_id && (
            <p>
              <span className="muted">{t("agent.backlog.parentTask")}:</span>{" "}
              <span className="mono">{task.parent_task_id}</span>
            </p>
          )}
          {task.result && <p style={{ color: "var(--text-success)" }}>{task.result}</p>}
          {task.error && <p className="danger-text">{task.error}</p>}
          {(notes.data?.length ?? 0) > 0 && (
            <div className="mt-2 mb-2">
              <div className="muted text-xs mb-1">{t("agent.backlog.agentNotes")}</div>
              {notes.data!.map((n) => {
                // The prefilled link, not the sending: the human sees the form,
                // reads it against and reports under their own name. Without a
                // set up target there is nothing here.
                const href = upstreamIssueURL({
                  repo,
                  title: task.title,
                  body: t("agent.backlog.reportBody", {
                    note: n.content,
                    agent: n.author,
                    task: task.title,
                    version: build.data ? `${build.data.version} (${build.data.commit})` : "?",
                  }),
                  truncationNote: "\n\n" + t("agent.backlog.reportTruncated"),
                });
                return (
                  <div key={n.id} className="text-xs mb-1" style={{ borderLeft: "2px solid var(--border)", paddingLeft: 8 }}>
                    <span className="secondary">{n.content}</span>{" "}
                    <span className="muted">· {relTime(n.created_at)}</span>
                    {href && (
                      <>
                        {" · "}
                        <a
                          href={href}
                          target="_blank"
                          rel="noopener noreferrer"
                          title={t("agent.backlog.reportUpstreamHint", {
                            repo: `${repo.system}:${repo.project}`,
                          })}
                          onClick={(e) => e.stopPropagation()}
                        >
                          {t("agent.backlog.reportUpstream")}
                        </a>
                      </>
                    )}
                  </div>
                );
              })}
            </div>
          )}
          <div className="flex gap-2 mt-1">
            <button
              className="btn sm"
              onClick={(e) => {
                e.stopPropagation();
                onShowRecording(task.id, title);
              }}
            >
              {t("agent.backlog.showRecording")}
            </button>
            {canManage && (task.state === "failed" || task.state === "cancelled") && (
              <button
                className="btn sm"
                disabled={retry.isPending}
                title={t("agent.backlog.rescheduleHint")}
                onClick={(e) => {
                  e.stopPropagation();
                  retry.mutate();
                }}
              >
                {t("agent.backlog.reschedule")}
              </button>
            )}
            {canManage && terminal && !archived && (
              <button
                className="btn sm"
                disabled={archive.isPending}
                title={t("agent.backlog.archiveTitle")}
                onClick={(e) => {
                  e.stopPropagation();
                  archive.mutate();
                }}
              >
                {t("agent.backlog.archive")}
              </button>
            )}
            {canManage && !terminal && (
              <button
                className="btn sm danger"
                disabled={cancel.isPending}
                onClick={(e) => {
                  e.stopPropagation();
                  if (confirm(t("agent.backlog.discardConfirm", { title }))) cancel.mutate();
                }}
              >
                {t("agent.backlog.discard")}
              </button>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
