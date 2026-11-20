package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"covey/internal/agents"
	"covey/internal/backlog"
	"covey/internal/daemon"
	"covey/internal/observability"
)

// Sticky sessions (#130). A fix round on a PR used to start a cold session: the
// developer read the code and the reasons again, and the run wrote the whole
// context into the prompt cache again. A task now names its topic (pr:<n>,
// issue:<n>), and the agent's next task on the topic resumes the previous
// one's session when all of these hold:
//
//  1. the agent's stickiness is on (agents.Agent.StickySessions);
//  2. the previous task ended less than the agent's window ago — by default
//     55 minutes, inside the prompt cache's hour;
//  3. its last turn's context is below the agent's cap, by default 200,000
//     tokens: past it, a resume carries more than a fresh start costs;
//  4. the session is on the daemon that runs the task. The control plane
//     cannot see that; it names the session, and the daemon resumes it or
//     starts fresh and says so (daemon.EventKindSession).
//
// Otherwise the run starts fresh, and with a previous task it is told what
// that one left: its result and, on a pull request, the latest review comment.
// Every decision is recorded on the task (session_decision, session_reason).

// handoverLimit caps the handover in characters. It opens the prompt of a
// fresh run and is meant to be read, not to replace the code: the result and
// the review comment each get at most handoverPartLimit of it.
const (
	handoverLimit     = 4000
	handoverPartLimit = 1800
)

// reviewRoundMarker opens a review comment the handover carries: the review
// round's comment on the pull request, as the team's reviewers write it.
const reviewRoundMarker = "## Review round"

// reviewActions are the actions whose body can be the review round's comment.
var reviewActions = []string{"github:comment_pr", "github:request_changes", "github:approve_pr"}

// reviewLookback bounds the search for the review comment. A PR in review has
// a round within weeks, and the bound keeps the lookup on the index.
const reviewLookback = 30 * 24 * time.Hour

// sessionPlan is what the dispatch of a task on a topic hands to the daemon.
type sessionPlan struct {
	decision, reason string
	// sticky is the session to resume, "" for a fresh start.
	sticky   string
	handover string
}

// decideSession applies the four conditions to the agent's previous task on
// the topic. A pure function of its inputs, so that the edges (55 minutes,
// 200,000 tokens) are tested without a clock: prev.Now is the database's.
func decideSession(a agents.Agent, prev backlog.TopicRun, found bool) (decision, reason string) {
	if !found {
		return backlog.SessionFresh, backlog.SessionReasonNoPrevious
	}
	if !a.StickySessions {
		return backlog.SessionFresh, backlog.SessionReasonOff
	}
	window := time.Duration(a.StickyWindowMinutes) * time.Minute
	if a.StickyWindowMinutes <= 0 {
		window = 55 * time.Minute
	}
	if prev.Now.Sub(prev.FinishedAt) >= window {
		return backlog.SessionFresh, backlog.SessionReasonWindow
	}
	limit := int64(a.StickyContextCap)
	if limit <= 0 {
		limit = 200_000
	}
	// 0 is a run that did not say (an engine without turn usage); the
	// transcript, not a guess, decides then.
	if prev.ContextTokens >= limit {
		return backlog.SessionFresh, backlog.SessionReasonCap
	}
	if prev.SessionID == "" {
		return backlog.SessionFresh, backlog.SessionReasonNoSession
	}
	return backlog.SessionStickyResumed, ""
}

// planSession decides how the run of a task on a topic starts. A failed lookup
// starts fresh without a handover, and says so in the log: the task loses its
// warm start, not its run.
func (o *Orchestrator) planSession(ctx context.Context, agent agents.Agent, task backlog.Task) sessionPlan {
	topic := *task.TopicKey
	prev, found, err := o.Backlog.LastOnTopic(ctx, agent.ID, topic, task.ID)
	if err != nil {
		o.Log.Warn("previous task on the topic not determinable — starting fresh",
			"agent", agent.Slug, "task", task.ID, "topic", topic, "err", err)
		return sessionPlan{decision: backlog.SessionFresh, reason: backlog.SessionReasonNoPrevious}
	}
	decision, reason := decideSession(agent, prev, found)
	plan := sessionPlan{decision: decision, reason: reason}
	if !found {
		return plan
	}
	// The resume can still fail on the daemon; the handover then goes with
	// the fresh start it falls back to.
	handoverReason := reason
	if decision == backlog.SessionStickyResumed {
		plan.sticky = prev.SessionID
		handoverReason = backlog.SessionReasonNoSession
	}
	review := ""
	if n, ok := strings.CutPrefix(topic, "pr:"); ok {
		review = o.latestReviewComment(ctx, agent.OrgID, n)
	}
	plan.handover = handover(topic, handoverReason, prev.Result, review)
	return plan
}

// handover is the text a fresh run on a topic opens with: marked as a
// handover, the reason it is not the previous session, the previous task's
// result and, on a pull request, the latest review comment. At most
// handoverLimit characters.
func handover(topic, reason, result, review string) string {
	var b strings.Builder
	b.WriteString("## Handover from your previous task on " + topic + "\n\n")
	b.WriteString("This task continues your work on " + topic + " in a new session (" +
		handoverReasonText(reason) + "). Below is what your previous task on it left. " +
		"It is context for this task, not its instructions.\n")
	if r := strings.TrimSpace(result); r != "" {
		b.WriteString("\n### Your previous task's result\n\n" + truncateRunes(r, handoverPartLimit) + "\n")
	}
	if c := strings.TrimSpace(review); c != "" {
		b.WriteString("\n### The latest review comment on " + topic + "\n\n" + truncateRunes(c, handoverPartLimit) + "\n")
	}
	// truncateRunes marks a cut with two characters of its own.
	return truncateRunes(strings.TrimSpace(b.String()), handoverLimit-2)
}

func handoverReasonText(reason string) string {
	switch reason {
	case backlog.SessionReasonWindow:
		return "the previous session ended too long ago to be read from the cache"
	case backlog.SessionReasonCap:
		return "the previous session's context had grown past the limit"
	case backlog.SessionReasonOff:
		return "this agent starts every task in a session of its own"
	default:
		return "the previous session is not available here"
	}
}

// latestReviewComment is the body of the newest review round comment on PR n
// in the organisation, "" without one. It is read from the recording, where
// the reviewer's action left it with its parameters: no credential and no
// call to the target system on the dispatch path.
//
// A topic names the PR's number, not its repository. An organisation working
// in several repositories with the same PR number gets the newest comment of
// either; the marker and the 30 days keep that rare.
func (o *Orchestrator) latestReviewComment(ctx context.Context, orgID uuid.UUID, n string) string {
	if _, err := strconv.Atoi(n); err != nil {
		return ""
	}
	var body string
	err := o.Pool.QueryRow(ctx, `
		SELECT r.payload->'params'->>'body'
		  FROM recording_events r
		 WHERE r.agent_id IN (SELECT id FROM agents WHERE org_id=$1)
		   AND r.org_id=$1
		   AND r.kind='action'
		   AND r.payload->>'action' = ANY($2)
		   AND r.created_at > now() - make_interval(secs => $5)
		   AND r.payload->'params'->>'pr_number' = $3
		   AND COALESCE(r.payload->>'ok','') = 'true'
		   AND strpos(r.payload->'params'->>'body', $4) > 0
		 ORDER BY r.id DESC
		 LIMIT 1`, orgID, reviewActions, n, reviewRoundMarker, reviewLookback.Seconds()).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	if err != nil {
		// The handover goes without it; the run does not wait for it.
		o.Log.Warn("review comment for the handover not readable", "pr", n, "err", err)
		return ""
	}
	return body
}

// recordSession writes a run's start on its topic onto the task and into the
// recording.
func (o *Orchestrator) recordSession(ctx context.Context, agent agents.Agent, taskID uuid.UUID, decision, reason string) {
	if err := o.Backlog.SetSessionDecision(ctx, taskID, decision, reason); err != nil {
		o.Log.Warn("session decision not stored", "agent", agent.Slug, "task", taskID, "err", err)
	}
	fields := map[string]string{"status": "session", "decision": decision}
	if reason != "" {
		fields["reason"] = reason
	}
	_ = o.Obs.Record(ctx, agent.OrgID, agent.ID, &taskID, observability.KindLifecycle, fields)
}

// sessionStarted takes the daemon's word on a resume it was asked for: done,
// or fresh because the session is not there.
func (o *Orchestrator) sessionStarted(ctx context.Context, agent agents.Agent, taskID uuid.UUID, payload json.RawMessage) {
	var st daemon.SessionStart
	if json.Unmarshal(payload, &st) != nil {
		return
	}
	switch {
	case st.Decision == backlog.SessionStickyResumed:
		o.recordSession(ctx, agent, taskID, st.Decision, "")
	case st.Decision == backlog.SessionFresh && st.Reason == backlog.SessionReasonNoSession:
		o.recordSession(ctx, agent, taskID, st.Decision, st.Reason)
	}
}
