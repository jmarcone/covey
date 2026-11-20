// Package daemon holds the bidirectional daemon protocol (spec/01) and the
// daemon side of it (runtime adapter, action proxy). The protocol is the
// system's stable seam: runtimes change, the protocol stays.
package daemon

import (
	"encoding/json"
	"fmt"
)

// Message is the envelope for both directions (JSON over WebSocket).
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Control plane → daemon. ("wake" is not a WS frame: waking = the control
// plane starts the sandbox through the SandboxProvider; the daemon then
// connects and reports ready.)
const (
	TypeInjectConfig = "inject_config"
	TypeAssignTask   = "assign_task"
	// #nosec G101 — the name of a message kind, not a credential.
	TypeInjectCredentials = "inject_credentials"
	TypeApprovalDecision  = "approval_decision"
	TypeInjectTarget      = "inject_target"
	TypeInjectOrgChart    = "inject_org_chart"
	TypeInjectWiki        = "inject_wiki"
	TypeInjectSkills      = "inject_skills"
	// #nosec G101 — the name of a message kind, not a secret.
	TypeInjectSecret = "inject_secret"
	// TypeRequestUsage/TypeUsageReport: the control plane asks the daemon what
	// the credential in effect has consumed (spec/18). It runs in the sandbox
	// because that is where the credential is; the control plane receives a
	// figure, not a binary.
	TypeRequestUsage = "request_usage"
	TypeUsageReport  = "usage_report"
	TypeKill         = "kill"
	TypeSleep        = "sleep"
)

// Daemon → control plane.
const (
	TypeReady             = "ready"
	TypeEvent             = "event"
	TypeRequestCredential = "request_credential"
	TypeRequestApproval   = "request_approval"
	TypeRequestTarget     = "request_target"
	TypeRequestOrgChart   = "request_org_chart"
	TypeBlocked           = "blocked"
	TypeTaskDone          = "task_done"
	TypeCost              = "cost"
	TypeHeartbeat         = "heartbeat"
	TypeSetStage          = "set_stage"
	// TypeReport: named fields an agent reports for its task (covey/report,
	// #563) — what a workflow reads to decide where the case goes next.
	TypeReport            = "report"
	TypeNote              = "note"
	TypeRequestWiki       = "request_wiki"
	TypeRequestSkills     = "request_skills"
	TypeRequestCreateTask = "request_create_task"
	TypeRequestHiring     = "request_hiring"
	// TypeRequestMessage/TypeInjectMessage: the meta action covey/message
	// (#537) — the agent writes to a person of its organisation, into their
	// direct conversation. Until then an agent could reach a human only by
	// stopping (a parked question) or sideways (a result nobody is told of).
	TypeRequestMessage = "request_message"
	TypeInjectMessage  = "inject_message"
	// TypeRequestTool/TypeInjectTool: the request for a tool. An agent that is
	// missing a package is root nowhere and cannot install anything for itself
	// — without this route it rebuilds apt in its own home (spec/16, #106).
	TypeRequestTool = "request_tool"
	TypeInjectTool  = "inject_tool"
	// TypeRequestPlatformAction/TypeInjectPlatformAction: a target-system
	// action that runs in the control plane rather than in the sandbox,
	// because its credential belongs to the installation and reaches beyond
	// one organisation (the installation's Teams app, #710).
	TypeRequestPlatformAction = "request_platform_action"
	TypeInjectPlatformAction  = "inject_platform_action"
	// TypeRequestNote/TypeInjectNote: covey/add_note with a task_id that is
	// not the running task — a note on, or an answer to, a colleague's task.
	// A request with an answer rather than a fire-and-forget TypeNote,
	// because only the control plane knows whose task the id names, and the
	// agent has to hear a refusal while it can still act on it.
	TypeRequestNote = "request_note"
	TypeInjectNote  = "inject_note"
	// #nosec G101 — the name of a message kind, not a secret.
	TypeRequestSecret = "request_secret"
	// TypeRequestTasks/TypeInjectTasks: the meta actions covey/list_tasks and
	// covey/get_task — the agent reads its own backlog and the tasks it
	// handed out, instead of rebuilding their state from wiki pages.
	TypeRequestTasks = "request_tasks"
	TypeInjectTasks  = "inject_tasks"
)

// Control plane → daemon (answers to request_create_task / request_hiring).
const (
	TypeInjectCreateTask = "inject_create_task"
	TypeInjectHiring     = "inject_hiring"
)

type InjectConfig struct {
	SystemPrompt string   `json:"system_prompt"`
	Runtime      string   `json:"runtime"`
	Model        string   `json:"model,omitempty"`  // empty = runtime default
	Effort       string   `json:"effort,omitempty"` // low/medium/high/xhigh/max; empty = runtime default
	AllowedTools []string `json:"allowed_tools,omitempty"`
	MaxTurns     int      `json:"max_turns,omitempty"`
	MaxBudgetUSD float64  `json:"max_budget_usd,omitempty"`
	// ActionTools are the target systems this agent may use, in the wording of
	// its prompt. The action proxy serves them as MCP tools, so a runtime that
	// speaks MCP can call an action directly instead of assembling a curl by
	// hand in the shell. An older daemon does not know the field and keeps to
	// the curl route — both paths stay open.
	ActionTools []ActionTool `json:"action_tools,omitempty"`
}

// ActionTool is one target system as a callable tool: the name the action proxy
// listens on and the description the runtime shows the model. The description
// is the plugin's PromptDoc — the same text that otherwise stands in the system
// prompt, so there is one wording, not two.
type ActionTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type AssignTask struct {
	TaskID          string `json:"task_id"`
	Title           string `json:"title"`
	Body            string `json:"body"`
	Priority        int    `json:"priority"`
	MemoryContext   string `json:"memory_context,omitempty"`
	ResumeSessionID string `json:"resume_session_id,omitempty"`
	ResumeInput     string `json:"resume_input,omitempty"`
	// WorkDirKey names the run's working directory under ~/scratch when the
	// task belongs to a topic its agent keeps a session on (#130):
	// "topic-<agent>-<pr|issue>-<n>". Every task of the topic uses it, so a
	// session started by one is found by the next. Empty = the task's own
	// directory, as before.
	WorkDirKey string `json:"work_dir_key,omitempty"`
	// ScratchOf names the task whose directory under ~/scratch the run uses
	// instead of its own: for a turn-limit continuation, the first task of
	// its chain, whose run started the session it resumes there. --resume
	// finds a transcript only under the directory it was written in.
	ScratchOf string `json:"scratch_of,omitempty"`
	// StickySessionID asks the daemon to resume the session of the agent's
	// previous task on the topic, with StickyInput as the message — if the
	// runtime holds that session in WorkDirKey. If it does not, the run starts
	// fresh with Title/Body (which carry the handover) under the fallback
	// prompt and memory context, and the daemon says so in a "sticky" event.
	// A field of its own and not ResumeSessionID: a daemon that predates it
	// ignores it and starts fresh with the handover, instead of failing on a
	// session it cannot find.
	StickySessionID       string `json:"sticky_session_id,omitempty"`
	StickyInput           string `json:"sticky_input,omitempty"`
	FallbackSystemPrompt  string `json:"fallback_system_prompt,omitempty"`
	FallbackMemoryContext string `json:"fallback_memory_context,omitempty"`
}

// EventKindSticky is the daemon's answer to a sticky resume (payload
// StickyOutcome): resumed, or fresh because the runtime does not hold the
// session.
const EventKindSticky = "sticky"

// StickyOutcome is the payload of an EventKindSticky event.
type StickyOutcome struct {
	Decision string `json:"decision"` // sticky_resumed | fresh
	Reason   string `json:"reason,omitempty"`
}

type InjectCredentials struct {
	RequestID string `json:"request_id"`
	System    string `json:"system"`
	Granted   bool   `json:"granted"`
	Reason    string `json:"reason,omitempty"`
	Token     string `json:"token,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	// CA is the PEM certificate that signs the target system's endpoint, from
	// the optional secret <system>_ca. It travels with the credential and not
	// as an action parameter: a trust anchor belongs beside the token, not in
	// the recording of every single call (see target.Credential).
	CA string `json:"ca,omitempty"`
	// Path delivers the value as a FILE at this path in the agent home instead
	// of as an environment variable — the form some engines require for their
	// subscription login (spec/19). Written for the run, removed after it.
	Path    string `json:"path,omitempty"`
	TTLSecs int    `json:"ttl_secs,omitempty"`
	// EnvVar names the runtime's target environment variable when the control
	// plane knows the credential type (from the secret's name). If it is empty,
	// the daemon guesses from the token prefix (backwards compatibility).
	EnvVar string `json:"env_var,omitempty"`
}

// ApprovalDecision is the central policy decision on an action: approved
// (possibly auto-allow), denied (guard-rail) or pending (waits for human
// approval; correlation_key wakes the task once the decision is made).
type ApprovalDecision struct {
	RequestID      string `json:"request_id"`
	Status         string `json:"status"` // approved | denied | pending
	Reason         string `json:"reason,omitempty"`
	ApprovalID     string `json:"approval_id,omitempty"`
	CorrelationKey string `json:"correlation_key,omitempty"`
}

type Ready struct {
	AgentID string `json:"agent_id"`
}

type Event struct {
	TaskID  string          `json:"task_id,omitempty"`
	Kind    string          `json:"kind"` // runtime | action | http | tool_use ...
	Payload json.RawMessage `json:"payload"`
}

// Event kinds with special handling in the control plane.
const (
	// EventKindAction is an executed target-system action (→ recording).
	EventKindAction = "action"
	// EventKindHTTP is a single HTTP request a target-system plugin made inside
	// the sandbox (payload: reqlog.Entry). It does not go into the recording but
	// into the request log — diagnostics, not an audit trail.
	EventKindHTTP = "http"
)

// RequestTarget/InjectTarget broker the definition of a manifest plugin
// (kind=custom) into the sandbox: the daemon knows compiled plugins itself,
// uploaded manifests it fetches from the control plane — only if the system is
// enabled for the organization (fail-closed).
type RequestTarget struct {
	RequestID string `json:"request_id"`
	System    string `json:"system"`
}

type InjectTarget struct {
	RequestID string `json:"request_id"`
	System    string `json:"system"`
	Granted   bool   `json:"granted"`
	Reason    string `json:"reason,omitempty"`
	// Kind tells apart the definition in Manifest: "custom" (REST manifest) or
	// "mcp" (MCP server config). Empty = custom (backwards compatibility).
	Kind     string          `json:"kind,omitempty"`
	Manifest json.RawMessage `json:"manifest,omitempty"`
}

// RequestOrgChart/InjectOrgChart broker the organization's org chart into the
// sandbox: humans and agents with their profiles (including the configurable
// profile fields), departments and reporting lines. A read-only view of the
// same data as GET /org/chart — the agent queries it at runtime through the
// meta action covey/org_chart instead of relying on a stale prompt snapshot.
type RequestOrgChart struct {
	RequestID string `json:"request_id"`
}

type InjectOrgChart struct {
	RequestID string          `json:"request_id"`
	Chart     json.RawMessage `json:"chart"`
}

type RequestCredential struct {
	RequestID string   `json:"request_id"`
	System    string   `json:"system"`
	Scopes    []string `json:"scopes,omitempty"`
	TaskID    string   `json:"task_id,omitempty"`
}

// RequestSecret/InjectSecret broker one custom, agent-scoped secret value
// (spec/04) for the {{secret:<key>}} placeholder in an action's params: the
// action proxy substitutes it server-side (in the sandbox, after the model
// has already committed to the tool call) so the plaintext value never enters
// the model's own context. Unlike RequestCredential/InjectCredentials this is
// not tied to a target system — the key is whatever name the secret was
// stored under (PUT /api/v1/agents/{id}/secrets/{key} or an org secret
// assigned to the agent). The explicit PutAgent/Assign onto this agent IS the
// authorization; there is no separate ACCESS.md scope for it.
type RequestSecret struct {
	RequestID string `json:"request_id"`
	Key       string `json:"key"`
	TaskID    string `json:"task_id,omitempty"`
}

// #nosec G101 — a message payload struct, not a hardcoded secret.
type InjectSecret struct {
	RequestID string `json:"request_id"`
	Key       string `json:"key"`
	Granted   bool   `json:"granted"`
	Reason    string `json:"reason,omitempty"`
	Value     string `json:"value,omitempty"`
}

type RequestApproval struct {
	RequestID string          `json:"request_id"`
	TaskID    string          `json:"task_id,omitempty"`
	Action    string          `json:"action"` // e.g. "zammad:reply_external"
	Params    json.RawMessage `json:"params"`
	// Do is the plugin's own action name behind the subject ("reply"); the
	// subject can be refined away from it. The control plane needs it to
	// compare the action's scope with ACCESS.md (#704). Empty for the covey
	// meta actions.
	Do string `json:"do,omitempty"`
}

type Blocked struct {
	TaskID         string `json:"task_id"`
	CorrelationKey string `json:"correlation_key"`
	Question       string `json:"question,omitempty"`
	SessionID      string `json:"session_id,omitempty"` // runtime session for --resume
	// Reply is what the run wrote before its status line (#650). Empty from
	// a daemon that predates it.
	Reply string `json:"reply,omitempty"`
}

// TaskDone closes a task. Status "incomplete" is the special case of a run cut
// off at the turn limit: it did work but reached no result. Result then carries
// the handover state ("Done / Open / Next step") the adapter obtains from the
// aborted session, and SessionID the session to resume from. The control plane
// turns that into a follow-up task instead of a silent failure (see
// orchestrator.handleIncomplete).
type TaskDone struct {
	TaskID    string `json:"task_id"`
	Status    string `json:"status"` // done | failed | escalated | incomplete
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
	Memory    string `json:"memory,omitempty"` // episode for the memory (M7)
	SessionID string `json:"session_id,omitempty"`
	// Reply is what the run wrote before its status line (#650). Empty from
	// a daemon that predates it.
	Reply string `json:"reply,omitempty"`
}

// SetStage moves a task on the kanban board into a (possibly new) stage. Purely
// presentational — no lifecycle transition. If the stage is new, the control
// plane creates it ("the agent invents columns"). Empty TaskID = current task.
// Report carries the fields of covey/report. It always names the task the
// run is working on: an agent reports for its own task, not for another.
type Report struct {
	TaskID string            `json:"task_id"`
	Fields map[string]string `json:"fields"`
}

// SetStage carries covey/set_stage. TaskID defaults to the running task; the
// control plane moves only a task of the calling agent's own (#66).
type SetStage struct {
	TaskID string `json:"task_id,omitempty"`
	Stage  string `json:"stage"`
}

// Note is a proactive note the agent takes mid-run. Scope "task" attaches it to
// the task (interim findings — task-specific), scope "memory" feeds it into the
// memory right away (generally valid insights) — without waiting for the memory
// field of task_done. Empty TaskID = current task; a note on any other task
// travels as RequestNote.
type Note struct {
	TaskID  string `json:"task_id,omitempty"`
	Scope   string `json:"scope"` // task | memory
	Content string `json:"content"`
}

// RequestNote/InjectNote are covey/add_note on another task than the running
// one: the agent adds to a colleague's open task, or answers a parked one,
// instead of opening a new task beside it. The target has to belong to the
// agent's organisation; it goes through the guard rails (subject
// covey:add_note).
//
// Answer asks for the note to resume the target if it is parked on a
// colleague's answer (a correlation key starting with "answer:") — the same
// edge as a person's POST /tasks/{id}/reply. Any other target keeps the note
// and is not woken.
type RequestNote struct {
	RequestID string `json:"request_id"`
	TaskID    string `json:"task_id,omitempty"` // the running task
	Target    string `json:"target"`            // the task the note is for
	Content   string `json:"content"`
	Answer    bool   `json:"answer,omitempty"`
	// ApprovalID: as on RequestCreateTask (#703).
	ApprovalID string `json:"approval_id,omitempty"`
}

type InjectNote struct {
	RequestID string `json:"request_id"`
	OK        bool   `json:"ok"`
	// Denied tells a refusal (another organisation, a guard rail) from a
	// failure, so the agent gets the same "denied" a guard rail gives
	// everywhere else.
	Denied bool   `json:"denied,omitempty"`
	Error  string `json:"error,omitempty"`
	// Woken: the target was blocked and the note resumed it. Reason says
	// why an answer did not wake it (not parked, parked on a key an agent's
	// answer may not end, its agent stopped).
	Woken  bool   `json:"woken,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Cost reports what one run spent: one message per run, after it ended.
type Cost struct {
	TaskID       string  `json:"task_id,omitempty"`
	USD          float64 `json:"usd"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	// The cached input side, reported separately because it is priced
	// separately. A daemon of an older build does not send the two fields; they
	// then stay 0 and the entry looks the way it always did — no migration of
	// running sandboxes needed.
	CacheReadTokens     int64  `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int64  `json:"cache_creation_tokens,omitempty"`
	Model               string `json:"model,omitempty"`
	// Basis says what USD and the tokens cover. CostBasisInvocation: the
	// run's own processes and nothing else, the figure to book.
	// CostBasisCumulative: the run resumed a session on a Claude Code CLI that
	// reports only the session's running total, and the money includes
	// earlier runs of the same session.
	//
	// EMPTY means a daemon from before the field. For claude-code that daemon
	// sent the session's running total (twice over after a turn limit), and
	// its tokens were the main model's only; the control plane has to treat
	// such an entry as cumulative, not as a run's spend.
	Basis string `json:"basis,omitempty"`
	// SessionID and SessionTotalUSD name the engine session the run belonged
	// to and its running total afterwards, as the engine reported it. A
	// cross-check (the sum of a session's USD should meet the last total),
	// never a figure to book. Empty/0 where the engine has no sessions.
	SessionID       string  `json:"session_id,omitempty"`
	SessionTotalUSD float64 `json:"session_total_usd,omitempty"`
}

// The values of Cost.Basis.
const (
	CostBasisInvocation = "invocation"
	CostBasisCumulative = "cumulative"
)

// RequestWiki/InjectWiki broker the agent's wiki tools (covey/wiki_*) and the
// home working copy into the control plane: search (vector search across the
// pages), read (one page by slug), write (create/update a page, [[slug]]
// wikilinks in the body) and list (all pages, for materializing into the home).
// The wiki lives in the control plane (source of truth, spec/05).
type RequestWiki struct {
	RequestID string   `json:"request_id"`
	Op        string   `json:"op"` // search | read | write | append | delete | list
	Query     string   `json:"query,omitempty"`
	Slug      string   `json:"slug,omitempty"`
	Title     string   `json:"title,omitempty"`
	Body      string   `json:"body,omitempty"`
	Type      string   `json:"type,omitempty"` // entity type of the page (spec/05)
	Tags      []string `json:"tags,omitempty"`
	Text      string   `json:"text,omitempty"` // append only: the paragraph to add
}

type InjectWiki struct {
	RequestID string          `json:"request_id"`
	OK        bool            `json:"ok"`
	Error     string          `json:"error,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// RequestTasks/InjectTasks are the read-only meta actions covey/list_tasks
// (Op "list") and covey/get_task (Op "get"). Which tasks the agent may read
// is decided in the control plane, from the agent the connection belongs to:
// its own tasks, and the ones it created with covey/create_task — nothing a
// request could widen. Data is the answer as the action hands it out.
type RequestTasks struct {
	RequestID string `json:"request_id"`
	Op        string `json:"op"` // list | get
	// list: own | created | all (empty = all), states (empty = any), archived
	// to include the archive, limit (0 = the default).
	Scope    string   `json:"scope,omitempty"`
	States   []string `json:"states,omitempty"`
	Archived bool     `json:"archived,omitempty"`
	Limit    int      `json:"limit,omitempty"`
	// get: the full id or a prefix of at least 8 hex characters.
	ID string `json:"id,omitempty"`
}

type InjectTasks struct {
	RequestID string          `json:"request_id"`
	OK        bool            `json:"ok"`
	Error     string          `json:"error,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// RequestSkills/InjectSkills fetch an agent's skills for materializing into the
// home. Unlike the wiki there is no way back: skills are config from the
// control plane (library or agent-owned), the agent does not edit them — a run
// that writes itself new capabilities would not be a feature but the loss of
// central control.
type RequestSkills struct {
	RequestID string `json:"request_id"`
}

// SkillDir is a skill as a directory: name, description and the files relative
// to it. SKILL.md comes WITHOUT frontmatter — the daemon generates it while
// writing, from name and description, so both are maintained in one place only.
//
// Frontmatter carries the optional fields of the Agent Skills format (license,
// compatibility, allowed-tools, metadata) as ready YAML lines, rendered by the
// control plane (skills.RenderExtra): the daemon places them, it does not need
// to know the format's field list. Binary holds the binary files (assets/
// images, templates) base64-encoded, path → content. A daemon older than these
// two fields ignores them and writes the skill without them.
type SkillDir struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Frontmatter string            `json:"frontmatter,omitempty"`
	Files       map[string]string `json:"files"`
	Binary      map[string]string `json:"binary,omitempty"`
}

type InjectSkills struct {
	RequestID string     `json:"request_id"`
	OK        bool       `json:"ok"`
	Error     string     `json:"error,omitempty"`
	Skills    []SkillDir `json:"skills"`
}

// RequestCreateTask/InjectCreateTask are the meta action covey/create_task: the
// agent creates a task — either a **subtask** for itself (splitting work that
// is too large instead of running into the turn limit) or a **delegation** to a
// colleague (Agent = that colleague's slug). The new task hangs as a child off
// the running task; that is what the loop protection counts.
//
// Unlike the other covey meta actions it goes through the guard-rails (subject
// covey:create_task, or covey:create_task:foreign when delegating) — creating a
// task produces work and cost and must be governable.
type RequestCreateTask struct {
	RequestID string `json:"request_id"`
	TaskID    string `json:"task_id,omitempty"` // running task = parent
	Agent     string `json:"agent,omitempty"`   // target agent (slug); empty = myself
	Title     string `json:"title"`
	Body      string `json:"body"`
	Priority  int    `json:"priority,omitempty"`
	// Topic is the topic the agent names for the task (#45); empty means the
	// one its title names (backlog.TopicKey). A request on a topic that
	// already has an open, running or blocked task at the target is routed
	// onto that task as a note instead of creating a second one, so an agent
	// that starts a separate item of the same issue on purpose names its own
	// topic ("#17 part 3").
	// The structured forms "pr:<owner>/<repo>#<n>" and
	// "issue:<owner>/<repo>#<n>" also make a developer's session sticky
	// across rounds on that pull request or issue (#130).
	Topic string `json:"topic,omitempty"`
	// ApprovalID is the approval the daemon's guard-rail check consumed for
	// this request, if a rule required one. The control plane checks the rule
	// again itself and accepts the request only with that approval (#703).
	ApprovalID string `json:"approval_id,omitempty"`
}

// RequestTool/InjectTool are the meta action covey/request_tool: "I am missing
// X, here is the task I was working on when it was missing".
//
// It solves nothing, it reports. A human decides, and what comes of
// it is a line in the Dockerfile of a profile — for every agent in it,
// not for this one home.
type RequestTool struct {
	RequestID string `json:"request_id"`
	TaskID    string `json:"task_id,omitempty"`
	// Tool is what is missing: a package name, a binary name, as precise as the
	// agent knows it.
	Tool string `json:"tool"`
	// Why is the evidence: the command that failed on it, and what it was needed
	// for. Without it the request cannot be decided.
	Why string `json:"why"`
}

// RequestPlatformAction carries one action of a control-plane system. The
// guard-rail decision the daemon fetched travels along as ApprovalID; the
// control plane checks rails, ACCESS and scope again on its own.
type RequestPlatformAction struct {
	RequestID  string          `json:"request_id"`
	TaskID     string          `json:"task_id,omitempty"`
	System     string          `json:"system"`
	Action     string          `json:"action"`
	Params     json.RawMessage `json:"params,omitempty"`
	ApprovalID string          `json:"approval_id,omitempty"`
}

type InjectPlatformAction struct {
	RequestID string          `json:"request_id"`
	OK        bool            `json:"ok"`
	Error     string          `json:"error,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type InjectTool struct {
	RequestID string `json:"request_id"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	// ID is the open point that came of it — so the agent can name it instead of
	// making the same request again on the next run.
	ID string `json:"id,omitempty"`
}

type InjectCreateTask struct {
	RequestID string `json:"request_id"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	TaskID    string `json:"task_id,omitempty"` // the created task, or the one the request was routed onto
	Agent     string `json:"agent,omitempty"`   // resolved target agent (slug)
	// Notice is something the delegating agent should know about a task that
	// was created all the same — that its colleague is stopped, say (#556).
	Notice string `json:"notice,omitempty"`
	// Topic is the topic key the request was matched by and the created task
	// carries ("" for a title that names none), so the agent can name it
	// again or tell it apart (#45).
	Topic string `json:"topic,omitempty"`
	// Routed says that no task was created: the topic already had an open,
	// running or blocked task at the target, and the request was added to it
	// as a note (#45). TaskID is then that task's.
	Routed bool `json:"routed,omitempty"`
	// State is the routed-onto task's state after the note: open when the
	// note resumed it, otherwise the state it was in.
	State string `json:"state,omitempty"`
	// Woken says that the note resumed a blocked task, as a person's reply
	// does.
	Woken bool `json:"woken,omitempty"`
	// Parked says that the task is parked on purpose (a correlation key under
	// "parked:"): the note waits on it until somebody releases the topic.
	Parked bool `json:"parked,omitempty"`
	// WaitingOn is the correlation key a blocked task that was not woken
	// waits on.
	WaitingOn string `json:"waiting_on,omitempty"`
	// Reason is why the task is blocked, where the platform knows it.
	Reason string `json:"reason,omitempty"`
}

// RequestMessage/InjectMessage are the meta action covey/message (#537): the
// agent tells a person of its organisation something, in their direct
// conversation. To names the person — an e-mail address or a display name;
// empty means the agent's supervisor. The running task is attached to the
// message so that the reader can open what it is about.
//
// It goes through the guard rails (subject covey:message): an organisation
// may want an approval in front of what an agent tells people, or forbid it
// for an agent that should only work.
//
// Files (#586) are what the agent hands over with it — read inside the
// sandbox, because the control plane cannot see into it, and carried in this
// one frame under MaxMessageFiles/MaxMessageFileBytes, which keep it inside
// the WebSocket's read limit.
type RequestMessage struct {
	RequestID string        `json:"request_id"`
	TaskID    string        `json:"task_id,omitempty"`
	To        string        `json:"to,omitempty"`
	Text      string        `json:"text"`
	Files     []MessageFile `json:"files,omitempty"`
	// ApprovalID: as on RequestCreateTask (#703).
	ApprovalID string `json:"approval_id,omitempty"`
}

// MessageFile is one file of a covey/message: its base name and its bytes
// (base64 on the wire, as encoding/json writes a []byte).
type MessageFile struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

// The limits of a covey/message's files: at most ten, at most 10 MiB
// together. 10 MiB are 13.4 MiB of base64 — inside the 16 MiB a frame may
// have (httpapi/daemonws.go). chat.MaxAttachments/MaxAttachmentBytes say the
// same on the control plane's side, and a test holds the two together.
const (
	MaxMessageFiles     = 10
	MaxMessageFileBytes = 10 << 20
)

type InjectMessage struct {
	RequestID      string `json:"request_id"`
	OK             bool   `json:"ok"`
	Error          string `json:"error,omitempty"`
	To             string `json:"to,omitempty"`              // the resolved person's display name
	ConversationID string `json:"conversation_id,omitempty"` // where it stands
}

// RequestHiring/InjectHiring are the meta actions an agent runs against the
// platform's own registry. Two halves, unlocked by two different scopes in
// ACCESS.md:
//
//   - DRAFTING a colleague (list_targets, get_agent_config, create_agent,
//     set_agent_config — spec/20, `scope: agents:write`).
//   - REVIEWING one (work_record, read_recording, propose_agent_config —
//     spec/21, `scope: agents:review`), plus get_agent_config, which both
//     halves need.
//
// An agent may hold both scopes; the People department and the improvement
// engineer deliberately do not — neither can do the other's job with the
// other's credentials.
//
// The platform's own target system, so to speak: no external system, no
// credential, executed in the control plane.
//
// Like create_task and unlike the other meta actions they go through the
// guard-rails (subject covey:<op>) — what comes out of them is a colleague, and
// that must be governable centrally rather than in a prompt.
//
// What is deliberately NOT in here: hiring. An agent may draft another agent;
// employing one is a human act (spec/20). There is no op for it, so there is
// nothing to forget to check.
type RequestHiring struct {
	RequestID string `json:"request_id"`
	Op        string `json:"op"` // list_targets | get_agent_config | create_agent | set_agent_config | enable_target | add_egress_host | …
	TaskID    string `json:"task_id,omitempty"`
	// Agent addresses an existing agent by slug (get_agent_config,
	// set_agent_config).
	Agent string `json:"agent,omitempty"`
	// The new colleague (create_agent).
	Slug        string `json:"slug,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Runtime     string `json:"runtime,omitempty"`
	JobTitle    string `json:"job_title,omitempty"`
	Department  string `json:"department,omitempty"` // name, not ID — the model reads names
	Supervisor  string `json:"supervisor,omitempty"` // human's email or display name
	// Files is the config (set_agent_config) or the changed files of a proposal
	// (propose_agent_config): file name → complete content.
	Files map[string]string `json:"files,omitempty"`
	// System is the target system to switch on (enable_target); Host and Note
	// the egress allowlist entry for Agent (add_egress_host).
	System string `json:"system,omitempty"`
	Host   string `json:"host,omitempty"`
	Note   string `json:"note,omitempty"`

	// --- Text as a platform service (style_check, style_apply) ---

	// Text is the draft to measure or revise; Material names the facts a
	// revision may add; MaxIter caps the revision loop (0: the default);
	// Language forces "de"/"en" instead of detecting it.
	Text     string `json:"text,omitempty"`
	Material string `json:"material,omitempty"`
	MaxIter  int    `json:"max_iter,omitempty"`
	Language string `json:"language,omitempty"`

	// --- Correction pairs (spec/24) ---

	// Before and After are the two halves of a pair: what this agent published,
	// and what stands there now after somebody edited it. Action names where it
	// happened ("gitlab:comment"), By names WHO changed it.
	//
	// By is required and is not a formality. Only a person's edit is a
	// correction; if a second agent rewrote the text, that is a handover, and
	// storing it would teach the voice to imitate itself.
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	By     string `json:"by,omitempty"`
	// Where the text stands. Named `where` on the wire because `action` is
	// taken on this side of the protocol by the action proxy's own field.
	Where string `json:"where,omitempty"`

	// --- Review (spec/21), unlocked by `scope: agents:review` ---

	// Task addresses one run (read_recording): the id of the backlog task,
	// which the work record names in its task lines.
	Task string `json:"task,omitempty"`
	// Days is the period of a work record (default 30).
	Days int `json:"days,omitempty"`
	// Title and Rationale carry a proposal: what it changes, and out of which
	// observation. The rationale is what a human reads before the diff.
	Title     string `json:"title,omitempty"`
	Rationale string `json:"rationale,omitempty"`
	// Body is the text of a platform issue (create_issue) — the evidence a
	// maintainer reads. Its own field rather than Rationale's second use: a
	// proposal's rationale is read beside a diff, an issue's body stands
	// alone, and the two are written differently.
	Body string `json:"body,omitempty"`
	// Summary is the review itself (write_review): the assessment a human
	// reads on the employee profile.
	Summary string `json:"summary,omitempty"`
	// Findings and Issues are what the review produced besides a proposal: an
	// assignment only a human can change, and a bug already filed against the
	// platform. They travel with the review because they come out of the same
	// judgement — one call, one moment.
	Findings []ReviewNote `json:"findings,omitempty"`
	Issues   []ReviewNote `json:"issues,omitempty"`

	// --- Services beside the sandbox (spec/16), unlocked by
	// `scope: services:write` ---

	// Compose is the CONTENT of a project's docker-compose.yml, read by the
	// agent out of its own checkout. Sent rather than pointed at: the file
	// exists inside the sandbox, on a host the control plane does not reach
	// into, and a path would have to be resolved by somebody who cannot see it.
	Compose string `json:"compose,omitempty"`
	// Only is an optional filter over the services in that file: bring up the
	// database, leave the rest. Empty = everything the file offers and the
	// organisation allows.
	Only []string `json:"only,omitempty"`

	// --- A voice drafted from a conversation (covey/voice_draft, #458) ---

	// VoiceName, Purpose and Description are the draft; VoiceTexts the texts
	// it is measured from instead of a description. What arrives is a draft
	// in the library and nothing more: a person releases it.
	VoiceName   string      `json:"voice_name,omitempty"`
	Purpose     string      `json:"purpose,omitempty"`
	Description string      `json:"description,omitempty"`
	VoiceTexts  []VoiceText `json:"voice_texts,omitempty"`
}

// VoiceText is one text an agent hands over for a voice draft.
type VoiceText struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// ReviewNote is one finding or one filed issue: a line a human reads, the
// reasoning behind it, and — for an issue — where it now lives.
type ReviewNote struct {
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Link   string `json:"link,omitempty"`
}

// InjectHiring is the answer to a meta action. Pending is the third outcome
// next to OK and Error, and it is the reason this type is not just a boolean:
// a guard rail may put a meta action in front of a human (spec/21). The action
// has then NOT happened — the agent blocks on CorrelationKey and repeats it
// once somebody has decided, exactly as with a target-system action.
type InjectHiring struct {
	RequestID string          `json:"request_id"`
	OK        bool            `json:"ok"`
	Error     string          `json:"error,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`

	Pending        bool   `json:"pending,omitempty"`
	ApprovalID     string `json:"approval_id,omitempty"`
	CorrelationKey string `json:"correlation_key,omitempty"`
}

// Encode builds an envelope; never panics, because all payloads are marshalable.
func Encode(msgType string, payload any) (Message, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Message{}, fmt.Errorf("encode %s: %w", msgType, err)
	}
	return Message{Type: msgType, Payload: raw}, nil
}

// DecodePayload unpacks an envelope's payload in a typed way.
func DecodePayload[T any](m Message) (T, error) {
	var v T
	if len(m.Payload) == 0 {
		return v, fmt.Errorf("%s: empty payload", m.Type)
	}
	if err := json.Unmarshal(m.Payload, &v); err != nil {
		return v, fmt.Errorf("%s: %w", m.Type, err)
	}
	return v, nil
}

// RequestUsage asks the daemon for the engine's own utilisation figure.
//
// Runtime names the engine to ask. The question goes out as soon as the link
// is up, and that is BEFORE the first task brings its inject_config: a daemon
// that looked the engine up in its config found none and answered "not
// supported" within milliseconds — which is all the live path ever filed.
// Empty (a control plane from before the field): the daemon falls back to its
// config.
type RequestUsage struct {
	RequestID string `json:"request_id"`
	Runtime   string `json:"runtime,omitempty"`
}

// UsageReport answers it. Supported=false means the engine could not say —
// then the platform's own estimate stays the source, which for at least one
// engine is the only one there is.
//
// Reason says why, as one of the Usage* reason codes: a code, never text, so
// it can be stored and shown without carrying anything the engine printed.
// Error is the daemon's own description for the log; it is never stored.
type UsageReport struct {
	RequestID string `json:"request_id"`
	Supported bool   `json:"supported"`
	Usage     Usage  `json:"usage"`
	Reason    string `json:"reason,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Why a usage report carries no (fresh) figure.
const (
	// UsageNoRuntimeConfig: the request named no engine and no config had
	// arrived yet — nothing to ask.
	UsageNoRuntimeConfig = "no_runtime_config"
	// UsageNoReporter: the engine cannot ask its provider. A property of the
	// engine, so the control plane stops asking.
	UsageNoReporter = "no_reporter"
	// UsageExecFailed: the engine could ask but the question failed (the
	// binary, the credential, a timeout). Worth asking again later.
	UsageExecFailed = "exec_failed"
	// UsageUnparsed: an answer came back, but no figure could be read from it.
	UsageUnparsed = "unparsed"
	// UsageStale: a figure, but the engine served it from its own cache
	// because the provider's endpoint was rate limited. Supported, flagged.
	UsageStale = "stale"
)
