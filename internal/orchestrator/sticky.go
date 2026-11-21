package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"covey/internal/agents"
	"covey/internal/backlog"
	"covey/internal/daemon"
	"covey/internal/observability"
)

// Sticky sessions per topic (#130).
//
// Every task used to start a cold runtime session — a developer's second fix
// round on the same pull request included. It read the code and the review
// again, and paid the cache write for its whole context again. A task can now
// name its topic (pr:<owner>/<repo>#<n>, issue:<owner>/<repo>#<n>), and a developer whose policy allows it
// resumes the session of its last finished task on that topic: same working
// directory, same system prompt byte for byte (or the cache would not be read),
// and the new task's title and body as the message.
//
// When it may not — outside the window, over the context cap, another
// credential, the session already continued by another round, stickiness off —
// the task starts fresh, with a handover: the previous report and the latest
// review on the topic. The decision is recorded on the task either way.

// stickyHandoverCap bounds the handover put in front of a fresh task's body.
const stickyHandoverCap = 4000

// sessionPrompt is what a session was started with: the compiled system prompt
// and the memory context. A resume sends the same pair.
type sessionPrompt struct{ prompt, memory, basis string }

// promptBasis is the hash a session's prompt is compared by: the part of the
// compiled prompt that does not depend on the task (configuration, platform
// protocol, target systems, team directory).
func promptBasis(taskIndependent string) string {
	sum := sha256.Sum256([]byte(taskIndependent))
	return hex.EncodeToString(sum[:])
}

// stickyPlan is what the dispatch of one task does about its topic.
type stickyPlan struct {
	// workDirKey: the topic's directory, "" = the task's own.
	workDirKey string
	// decision is recorded on the task; zero = nothing to record.
	decision backlog.StickyDecision
	// pinned replaces the compiled prompt and memory context: the session's
	// own, for a resume of it (sticky, or the task's own after a block or at
	// the turn limit).
	pinned *sessionPrompt
	// record stores the run's prompt as the task's session prompt (a fresh
	// start of a session that later rounds may resume).
	record *sessionPrompt
	// A sticky resume: the session, the message, the fallback.
	sessionID string
	input     string
	fallback  *sessionPrompt
	// handover goes in front of the body of a fresh task (and of the
	// fallback, which is fresh as well).
	handover string
	// A task woken by a routed note (#45) that may not continue its own
	// session: it starts a fresh one (dropResume), and the note that woke it
	// goes into the body (wokenBy). wake records which way it went.
	dropResume bool
	wokenBy    string
	wake       string
	// clearWake: the routed wake is dealt with by continuing the session.
	clearWake bool
}

// effectiveStickyPolicy resolves ab to the arm the topic falls in: odd pull
// requests always, even ones and issues conditional. The arm is a function of
// the number, so a pull request stays in its arm for its whole life.
func effectiveStickyPolicy(policy, topic string) string {
	if policy != agents.StickyAB {
		if policy == "" {
			return agents.StickyFresh
		}
		return policy
	}
	kind, _, number := backlog.TopicParts(topic)
	if kind == "pr" && number != "" && strings.ContainsAny(number[len(number)-1:], "13579") {
		return agents.StickyAlways
	}
	return agents.StickyConditional
}

// topicWorkDir is the topic's directory name under ~/scratch:
// topic-<agent id>-<kind>-<n>-<hash>, the hash the first 12 hex digits of the
// key's SHA-256. The repository is in the hash rather than spelled out: two
// repositories' pull request 12 never share a directory, and no owner or
// repository name (a dot, "a-b/c" against "a/b-c") can shape the path. The
// agent's id and not its slug: a slug can be renamed, and a blocked task
// resuming its session after a rename would look for it elsewhere. The daemon
// accepts no other form (daemon/sticky.go).
func topicWorkDir(agentID uuid.UUID, topic string) string {
	kind, _, number := backlog.TopicParts(topic)
	sum := sha256.Sum256([]byte(topic))
	return "topic-" + agentID.String() + "-" + kind + "-" + number + "-" + hex.EncodeToString(sum[:])[:12]
}

// credentialKey names the pool value a session runs on, for the comparison
// with the one its predecessor ran on: the prompt cache belongs to an account,
// and a resume under another one reads nothing from it.
func credentialKey(s *session) string {
	c := s.credential()
	switch {
	case c.SecretKey != "":
		return fmt.Sprintf("secret:%s:%d", c.SecretKey, c.SecretSlot)
	case c.RuntimeID != uuid.Nil:
		return fmt.Sprintf("runtime:%s:%d", c.RuntimeID, c.Ord)
	}
	return ""
}

// planSticky decides how a task's run starts with regard to its topic. It
// does not write anything; applyStickyPlan records the decision.
func (o *Orchestrator) planSticky(ctx context.Context, agent agents.Agent, task backlog.Task, s *session,
	compiled, memCtx, hits, taskSections, basis string, now time.Time) stickyPlan {
	var plan stickyPlan
	if task.TopicKey == nil || !backlog.IsStickyTopic(*task.TopicKey) {
		return plan
	}
	topic := *task.TopicKey
	state, err := o.Backlog.StickyState(ctx, task.ID)
	if err != nil {
		o.Log.Warn("sticky: task state not readable — starting fresh", "task", task.ID, "err", err)
		return plan
	}

	// The task resumes its OWN session (after a block, or as the
	// continuation at the turn limit): that session lies in the directory it
	// was started in and runs under the prompt it was started with. Nothing
	// to decide.
	if task.RuntimeSessionID != nil && task.ResumeInput != nil {
		// A session prompt is recorded exactly for the sessions started in
		// the topic's directory; a busy round and a fresh-policy agent start
		// theirs in the task's own (orchestrator: ScratchOf).
		if state.Prompt == nil {
			// A session outside the topic's directory (a busy round, a
			// fresh-policy agent): continued as before; a routed wake's
			// flag has nothing to say here.
			plan.clearWake = state.RoutedWake
			return plan
		}
		plan.workDirKey = topicWorkDir(agent.ID, topic)
		if state.RoutedWake {
			// Woken by a note another run routed onto it (#45), not by the
			// answer it parked for: the session continues only if a new
			// round could resume it — inside the window, under the cap, on
			// the same credential, under the same instructions.
			self := backlog.Predecessor{TaskID: task.ID, SessionID: *task.RuntimeSessionID, FinishedAt: now,
				ContextTokens: state.Context, Credential: state.Credential, Prompt: state.Prompt,
				Memory: state.Memory, Basis: state.Basis}
			if state.PausedAt != nil {
				self.FinishedAt = *state.PausedAt
			}
			policy := effectiveStickyPolicy(agent.StickyPolicy, topic)
			recorded := policy
			if state.Policy != "" {
				recorded = state.Policy // the task's A/B arm is the one it was dispatched in
			}
			if reason := stickyVerdict(policy, true, self, now, agent.StickyWindowMinutes, agent.StickyContextCap,
				credentialKey(s), basis); reason != "" {
				plan.wake = "fresh:" + reason
				plan.decision = backlog.StickyDecision{Decision: backlog.DecisionFresh, Reason: reason, Policy: recorded}
				plan.dropResume = true
				plan.wokenBy = *task.ResumeInput
				plan.record = &sessionPrompt{compiled, memCtx, basis}
				if policy == agents.StickyFresh {
					plan.workDirKey = ""
					plan.record = nil
				} else if pred, found, err := o.Backlog.LastOnTopic(ctx, agent.OrgID, agent.ID, topic, task.ID); err == nil && found {
					plan.handover = o.stickyHandover(ctx, agent, topic, pred)
				}
				return plan
			}
			plan.wake = "resumed"
			plan.clearWake = true
		}
		plan.pinned = &sessionPrompt{*state.Prompt, state.Memory, ""}
		return plan
	}
	// A task dispatched again after its run was lost (a requeue) keeps the
	// policy it was dispatched with the first time; the decision is made
	// again (a requeue that now finds a round in play turns busy).
	policy := effectiveStickyPolicy(agent.StickyPolicy, topic)
	if state.Policy != "" {
		policy = state.Policy
	}
	plan.decision.Policy = policy

	pred, found, err := o.Backlog.LastOnTopic(ctx, agent.OrgID, agent.ID, topic, task.ID)
	if err != nil {
		o.Log.Warn("sticky: previous task on the topic not readable — starting fresh", "task", task.ID, "err", err)
		found = false
	}
	if state.WakeDropped {
		// Dispatched again after a routed wake was refused (a requeue): it
		// stays fresh. Its predecessor's session may be the very one that
		// was refused — this task had continued it — and its claim on it
		// would let it take that session back.
		plan.decision.Decision = backlog.DecisionFresh
		plan.decision.Reason = "wake"
		if policy != agents.StickyFresh {
			plan.workDirKey = topicWorkDir(agent.ID, topic)
			plan.record = &sessionPrompt{compiled, memCtx, basis}
			if found {
				plan.handover = o.stickyHandover(ctx, agent, topic, pred)
			}
		}
		return plan
	}
	fresh := func(reason string) stickyPlan {
		plan.decision.Decision = backlog.DecisionFresh
		plan.decision.Reason = reason
		// A busy round runs beside another round still in play on the
		// topic: in its own directory, so it cannot overwrite what the other
		// one will come back to, and without a session the next round could
		// take for the topic's.
		if policy != agents.StickyFresh && reason != backlog.ReasonBusy {
			plan.workDirKey = topicWorkDir(agent.ID, topic)
			plan.record = &sessionPrompt{compiled, memCtx, basis}
		}
		// The reviewer and every other fresh-policy agent start fully fresh,
		// without their own previous report: a review round is a fresh
		// adversarial review, not one anchored on the last.
		if found && policy != agents.StickyFresh {
			plan.handover = o.stickyHandover(ctx, agent, topic, pred)
		}
		return plan
	}
	if found {
		plan.decision.From = &pred.TaskID
		gap := int64(now.Sub(pred.FinishedAt) / time.Second)
		plan.decision.GapS = &gap
		if pred.ContextTokens > 0 {
			c := pred.ContextTokens
			plan.decision.Context = &c
		}
	}
	// Another round on the topic is still in play (blocked on its own
	// session, say): it holds newer work than the predecessor, and the topic
	// directory is where it comes back to. Asked first, whatever else would
	// be decided: a round beside it — resumed or fresh — must neither fork
	// the topic's line of work nor write into that directory.
	if policy != agents.StickyFresh {
		if open, err := o.Backlog.OpenSessionOnTopic(ctx, agent.OrgID, agent.ID, topic, task.ID); err != nil || open {
			if err != nil {
				o.Log.Warn("sticky: open rounds on the topic not readable — starting fresh", "task", task.ID, "err", err)
			}
			return fresh(backlog.ReasonBusy)
		}
	}
	if reason := stickyVerdict(policy, found, pred, now, agent.StickyWindowMinutes, agent.StickyContextCap,
		credentialKey(s), basis); reason != "" {
		return fresh(reason)
	}
	claimed, err := o.Backlog.ClaimSession(ctx, pred.TaskID, task.ID)
	if err != nil || !claimed {
		if err != nil {
			o.Log.Warn("sticky: claiming the session failed — starting fresh", "task", task.ID, "err", err)
		}
		return fresh(backlog.ReasonBusy)
	}

	// Resume: the predecessor's session, under the predecessor's prompt, in
	// the topic's directory. The new task's wiki hits go into the message:
	// in the system prompt they would change the prefix and turn the whole
	// resumed context into a cache write.
	plan.decision.Decision = backlog.DecisionPending
	plan.workDirKey = topicWorkDir(agent.ID, topic)
	plan.pinned = &sessionPrompt{*pred.Prompt, pred.Memory, pred.Basis}
	plan.record = plan.pinned
	plan.sessionID = pred.SessionID
	plan.input = stickyInput(task, hits, taskSections)
	plan.fallback = &sessionPrompt{compiled, memCtx, basis}
	plan.handover = o.stickyHandover(ctx, agent, topic, pred)
	return plan
}

// stickyInput is the message a resumed session gets: the new task, the
// sections of the prompt that belong to this task rather than to the agent
// (a stopped colleague it names, its conversation, tone and audience — the
// session's prompt carries the first task's, not these), and the wiki pages
// its text found.
func stickyInput(task backlog.Task, hits, taskSections string) string {
	in := "New task on the same topic — your earlier work on it is above in this session.\n\n" +
		task.Title + "\n\n" + task.Body
	if strings.TrimSpace(taskSections) != "" {
		in += "\n\n## For this task\n\n" + strings.TrimSpace(taskSections)
	}
	if strings.TrimSpace(hits) != "" {
		in += "\n\n## Wiki pages that match this task\n\n" + strings.TrimSpace(hits)
	}
	return in
}

// stickyHandover is what a fresh task on the topic is told of the previous
// one: its final report, and on a pull request the latest review on it.
// Capped, and marked as a handover so the run does not take it for its own
// memory of the work.
func (o *Orchestrator) stickyHandover(ctx context.Context, agent agents.Agent, topic string, pred backlog.Predecessor) string {
	report := strings.TrimSpace(pred.Result)
	review := ""
	if kind, repo, number := backlog.TopicParts(topic); kind == "pr" {
		// The review comment as it was posted on the pull request, from the
		// recording; else the reviewer's own report on the topic.
		review = o.postedReview(ctx, agent.OrgID, repo, number, pred.FinishedAt)
		if review == "" {
			var err error
			if review, err = o.Backlog.LatestReview(ctx, agent.OrgID, agent.ID, topic, pred.FinishedAt); err != nil {
				o.Log.Warn("sticky: latest review not readable", "topic", topic, "err", err)
			}
		}
	}
	if report == "" && review == "" {
		return ""
	}
	head := "## Handover (" + topic + ")\n\n" +
		"This run starts a fresh session. What follows is a handover from your previous task on " + topic +
		", not your own memory of it — check what it says against the code.\n\n"
	const tail = "\n\n## The new task\n\n"
	var parts []string
	if report != "" {
		parts = append(parts, "### Your previous report\n\n"+report)
	}
	if review != "" {
		parts = append(parts, "### The latest review\n\n"+review)
	}
	// Counted in characters, the separators between the parts included.
	budget := stickyHandoverCap - utf8.RuneCountInString(head) - utf8.RuneCountInString(tail) - 2*(len(parts)-1)
	// Each part gets an even share of what is left; a short one leaves its
	// rest to the other. truncateRunes appends " …", hence the 2 kept back.
	out := make([]string, 0, len(parts))
	for i, p := range parts {
		share := budget / (len(parts) - i)
		if utf8.RuneCountInString(p) > share {
			p = truncateRunes(p, max(share-2, 0))
		}
		budget -= utf8.RuneCountInString(p)
		out = append(out, p)
	}
	return head + strings.Join(out, "\n\n") + tail
}

// applyStickyPlan records the decision and the session prompt on the task.
// Best effort: a record that fails must not cost the run.
func (o *Orchestrator) applyStickyPlan(ctx context.Context, agent agents.Agent, taskID uuid.UUID, plan stickyPlan, s *session) {
	if plan.decision.Decision != "" {
		if err := o.Backlog.RecordSticky(ctx, taskID, plan.decision); err != nil {
			o.Log.Warn("sticky: decision not recorded", "task", taskID, "err", err)
		}
		fields := map[string]string{"status": plan.decision.Decision, "policy": plan.decision.Policy}
		if plan.decision.Decision == backlog.DecisionFresh {
			fields = map[string]string{"status": "sticky_fresh", "reason": plan.decision.Reason, "policy": plan.decision.Policy}
		}
		if plan.decision.From != nil {
			fields["from_task"] = plan.decision.From.String()
		}
		_ = o.Obs.Record(ctx, agent.OrgID, agent.ID, &taskID, observability.KindLifecycle, fields)
	}
	if plan.wake != "" {
		fields := map[string]string{"status": "sticky_wake", "wake": "routed", "outcome": plan.wake}
		_ = o.Obs.Record(ctx, agent.OrgID, agent.ID, &taskID, observability.KindLifecycle, fields)
	}
	keepPrompt := false
	if plan.dropResume {
		// One write, before the run: a dispatch after a lost run must
		// start fresh again, not resume the session the checks refused.
		if err := o.Backlog.DropSessionForFreshStart(ctx, taskID, wokenByBody(plan.wokenBy)); err != nil {
			o.Log.Warn("sticky: refused session not dropped", "task", taskID, "err", err)
			// The refused session is still on the task: its prompt stays
			// as it was, neither replaced (a later resume of it would pass
			// the prompt check under instructions it never ran with) nor
			// cleared (its resume would look for it in the wrong place).
			keepPrompt = true
		}
	} else if plan.clearWake {
		if err := o.Backlog.ClearRoutedWake(ctx, taskID); err != nil {
			o.Log.Warn("sticky: routed wake not cleared", "task", taskID, "err", err)
		}
	}
	switch {
	case keepPrompt:
	case plan.record != nil:
		if err := o.Backlog.SetSessionPrompt(ctx, taskID, plan.record.prompt, plan.record.memory, plan.record.basis); err != nil {
			o.Log.Warn("sticky: session prompt not recorded", "task", taskID, "err", err)
		}
	case plan.decision.Decision != "":
		if err := o.Backlog.ClearSessionPrompt(ctx, taskID); err != nil {
			o.Log.Warn("sticky: stale session prompt not cleared", "task", taskID, "err", err)
		}
	}
	s.stickyFallback = plan.fallback
}

// settleSticky takes the daemon's answer to a sticky resume.
func (o *Orchestrator) settleSticky(ctx context.Context, agent agents.Agent, taskID uuid.UUID, payload json.RawMessage, s *session) {
	var out daemon.StickyOutcome
	if json.Unmarshal(payload, &out) != nil {
		return
	}
	switch out.Decision {
	case backlog.DecisionResumed, backlog.DecisionFresh:
	default:
		return
	}
	if err := o.Backlog.SettleSticky(ctx, taskID, out.Decision, out.Reason); err != nil {
		o.Log.Warn("sticky: daemon's answer not recorded", "task", taskID, "err", err)
	}
	// Fresh after all: the session this task runs is its own, started under
	// the fallback prompt — the one a later round has to resume it with.
	if out.Decision == backlog.DecisionFresh && s.stickyFallback != nil {
		if err := o.Backlog.SetSessionPrompt(ctx, taskID, s.stickyFallback.prompt, s.stickyFallback.memory, s.stickyFallback.basis); err != nil {
			o.Log.Warn("sticky: session prompt not recorded", "task", taskID, "err", err)
		}
	}
	s.stickyFallback = nil
	fields := map[string]string{"status": out.Decision}
	if out.Decision == backlog.DecisionFresh {
		fields = map[string]string{"status": "sticky_fresh", "reason": out.Reason}
	}
	_ = o.Obs.Record(ctx, agent.OrgID, agent.ID, &taskID, observability.KindLifecycle, fields)
}

// turnTracker follows a run's assistant turns as their lines pass: the cache
// figures of the first (was the resume read from the cache, or written?) and
// the context size of the last (is the session still small enough to resume?).
type turnTracker struct {
	task    uuid.UUID
	seen    bool
	context int64
}

func (t *turnTracker) reset(task uuid.UUID) { *t = turnTracker{task: task} }

// assistantUsage is the usage of a main-thread assistant line; ok is false for
// any other line, and for a sub-agent's line (parent_tool_use_id set), whose
// context is the sub-agent's and not the session's.
func assistantUsage(payload []byte) (u backlog.FirstTurn, ok bool) {
	var line struct {
		Type   string           `json:"type"`
		Parent *json.RawMessage `json:"parent_tool_use_id"`
		// covey_sub_agent marks the lines of a sub-run the daemon started
		// itself (daemon/subagent.go): its context is not the session's.
		Sub *json.RawMessage `json:"covey_sub_agent"`
		Msg struct {
			Usage *struct {
				Input         int64 `json:"input_tokens"`
				CacheRead     int64 `json:"cache_read_input_tokens"`
				CacheCreation int64 `json:"cache_creation_input_tokens"`
				Split         *struct {
					W5m int64 `json:"ephemeral_5m_input_tokens"`
					W1h int64 `json:"ephemeral_1h_input_tokens"`
				} `json:"cache_creation"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(payload, &line) != nil || line.Type != "assistant" || line.Msg.Usage == nil {
		return u, false
	}
	if line.Parent != nil && string(*line.Parent) != "null" || line.Sub != nil && string(*line.Sub) != "null" {
		return u, false
	}
	us := line.Msg.Usage
	// Without the split the write is known in total only: booking it as a
	// 5-minute write would put a 1-hour write in the wrong column of the
	// split the window default is tuned from (AC 9).
	u = backlog.FirstTurn{Input: us.Input, CacheRead: us.CacheRead, CacheWrite: us.CacheCreation}
	if us.Split != nil && (us.Split.W5m+us.Split.W1h > 0 || us.CacheCreation == 0) {
		w5m, w1h := us.Split.W5m, us.Split.W1h
		u.CacheWrite5m, u.CacheWrite1h = &w5m, &w1h
		if w5m+w1h > 0 {
			u.CacheWrite = w5m + w1h
		}
	}
	return u, true
}

// observeTurn takes one runtime line of the current run.
func (o *Orchestrator) observeTurn(ctx context.Context, taskID uuid.UUID, payload []byte, s *session) {
	u, ok := assistantUsage(payload)
	if !ok {
		return
	}
	t := &s.turns
	if t.task != taskID {
		t.reset(taskID)
	}
	t.context = u.Input + u.CacheRead + u.CacheWrite
	if !t.seen {
		t.seen = true
		if err := o.Backlog.RecordFirstTurn(ctx, taskID, u); err != nil {
			o.Log.Warn("sticky: first turn not recorded", "task", taskID, "err", err)
		}
	}
}

// lastContext is the current run's last-turn context, 0 when none was seen.
func (s *session) lastContext(taskID uuid.UUID) int64 {
	if s.turns.task != taskID {
		return 0
	}
	return s.turns.context
}

// stickyVerdict is the rule, without the store: "" when the predecessor's
// session may be resumed, otherwise the reason for a fresh start. The order is
// the order of the questions — is stickiness on, is there anything to resume,
// is it recent and small enough, was it the same account, is the session
// there at all. Whether another round already continues it (busy) is asked
// of the store afterwards, atomically.
func stickyVerdict(policy string, found bool, pred backlog.Predecessor, now time.Time,
	windowMinutes int, contextCap int64, credential, basis string) string {
	switch {
	case policy == agents.StickyFresh || policy == "":
		return backlog.ReasonOff
	case !found:
		return backlog.ReasonNoPrevious
	}
	window := time.Duration(windowMinutes) * time.Minute
	if window <= 0 {
		window = agents.DefaultStickyWindowMinutes * time.Minute
	}
	if contextCap <= 0 {
		contextCap = agents.DefaultStickyContextCap
	}
	if policy == agents.StickyConditional {
		// An unknown size (0: a daemon or engine that reported no turn) is
		// not held against the session; the window still bounds it.
		if now.Sub(pred.FinishedAt) >= window {
			return backlog.ReasonWindow
		}
		if pred.ContextTokens >= contextCap {
			return backlog.ReasonCap
		}
	}
	// The prompt cache belongs to an account: under another credential a
	// resume reads nothing from it, and the conversation would be carried
	// to an account it was not held under.
	if pred.Credential != "" && credential != "" && pred.Credential != credential {
		return backlog.ReasonCredential
	}
	// A predecessor whose own resume was never answered ran on a daemon that
	// predates sticky sessions: fresh, under a prompt pinned for a resume
	// that did not happen. Its session is not one to build on.
	if pred.SessionID == "" || pred.Prompt == nil || pred.Decision == backlog.DecisionPending {
		return backlog.ReasonNoSession
	}
	// The agent's instructions changed since the session started (SOUL,
	// ACCESS, the platform protocol after a deploy): a resume would carry
	// the old ones on, and miss the cache anyway.
	if pred.Basis != "" && basis != "" && pred.Basis != basis {
		return backlog.ReasonPromptChanged
	}
	return ""
}

// reviewActions are the github plugin's actions that post a review on a pull
// request; their params carry pr_number and body.
var reviewActions = []string{"github:comment_pr", "github:request_changes", "github:approve_pr"}

// postedReview is the latest "## Review round" comment an agent of the
// organisation posted on pull request number of repository repo (owner/repo,
// lower case) after since, from its "## Review
// round" heading on; "" when the recording holds none (a recording level that
// keeps no params, a review posted outside covey). Idea and query from #205.
func (o *Orchestrator) postedReview(ctx context.Context, orgID uuid.UUID, repo, number string, since time.Time) string {
	if o.Pool == nil || number == "" || repo == "" {
		return ""
	}
	var body string
	err := o.Pool.QueryRow(ctx, `SELECT r.payload->'params'->>'body' FROM recording_events r
		WHERE r.agent_id IN (SELECT id FROM agents WHERE org_id=$1) AND r.org_id=$1
		  AND r.kind='action' AND r.created_at > $5
		  AND r.payload->>'action' = ANY($2)
		  AND r.payload->'params'->>'pr_number' = $3
		  AND lower(btrim(r.payload->'params'->>'repo', ' /')) = $6
		  AND COALESCE(r.payload->>'ok','') = 'true'
		  AND r.payload->'params'->>'body' ~ $4
		ORDER BY r.id DESC LIMIT 1`, orgID, reviewActions, number, `(^|\n)## Review round`, since, repo).Scan(&body)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			o.Log.Warn("sticky: posted review not readable", "pr", number, "err", err)
		}
		return ""
	}
	if i := strings.LastIndex("\n"+body, "\n## Review round"); i >= 0 {
		body = body[i:]
	}
	return strings.TrimSpace(body)
}

// wokenByBody is what a routed wake's fresh start adds to the task's body:
// the note that woke it.
func wokenByBody(note string) string {
	if strings.TrimSpace(note) == "" {
		return ""
	}
	return "\n\n## What woke this task\n\n" + note
}
