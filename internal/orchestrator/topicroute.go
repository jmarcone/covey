package orchestrator

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"covey/internal/agents"
	"covey/internal/backlog"
	"covey/internal/daemon"
	"covey/internal/observability"
)

// Correlation keys of a blocked task that a routed note leaves waiting (#45).
// Each names something other than more input that the task waits for:
//
//   - parked: the topic is parked on purpose (a freeze, a branch held back);
//     a note is kept for when it is released, and waking the task would undo
//     the decision to park it.
//   - approval: a person's decision (OnApprovalDecidedWithText); a note in
//     its place would resume the task without it, and the decision would
//     find nobody waiting.
//   - hire: a hiring step, which waits for a decision the same way.
//
// A denylist, unlike the allowlist an agent's answer goes by (answerKeyPrefix
// in note.go): a request routed here is an addendum to the task's own work —
// a scope change on a pull request it waits on, a correction to the formula
// it waits to have accepted — and the task should take it up now rather than
// after the event it was waiting for, which may come much later or never.
//
// A conversation task is never woken either, whatever its key; the store
// decides that under the row lock (backlog.Store.RouteNote).
var routeKeepsParked = []string{"parked:", "approval:", "hire:"}

// routeCreateTask adds a covey/create_task request to the task that already
// holds its topic at the target agent, instead of creating a second task with
// a session of its own (#45). The title stands on the note's first line, so
// the note reads as the request it was.
//
// A blocked task is resumed with the note as its resume input — the edge of
// a person's reply, so it continues its own session — unless it waits on a
// key in routeKeepsParked, it is a conversation task, or its agent is
// stopped. An open or running task only takes the note. The same note again
// by the same agent is a repeat and changes nothing.
//
// The answer names the task, its state after the note and whether it woke,
// so the asking agent can tell an addendum from a new task. ok is false when
// the task left play between the lookup and the note: nothing was written,
// and the caller creates the task instead.
func (o *Orchestrator) routeCreateTask(ctx context.Context, agent agents.Agent, taskID uuid.UUID,
	targetAgent agents.Agent, match backlog.Task, topic, title string, req daemon.RequestCreateTask) (daemon.InjectCreateTask, bool) {
	author := "agent:" + agent.Slug
	content := title
	if body := strings.TrimSpace(req.Body); body != "" {
		content += "\n\n" + body
	}

	// The kill switch is read before the note, so that the store decides the
	// wake with everything else under the row lock.
	mayWake, stopNotice := true, ""
	stopped, err := o.isKilled(ctx, agents.Agent{ID: match.AgentID})
	switch {
	case err != nil:
		o.Log.Warn("create_task: the kill switch could not be read", "task", match.ID, "err", err)
		mayWake = false
		stopNotice = "the kill switch of the task's agent could not be checked, so the task was not resumed; " +
			"your note stands on it"
	case stopped:
		// As with a person's reply: the release does not replay what came
		// while the agent was stopped.
		mayWake = false
		stopNotice = "the task's agent is stopped (kill switch); your note stands on the task, but it does not " +
			"resume it, not after the release either (the same note again counts as a repeat) — if it cannot wait, " +
			"tell your manager (covey/message)"
	}

	input := "Note from agent " + agent.Slug + " (task " + taskID.String() + ") on topic \"" + topic + "\":\n" + content
	r, err := o.Backlog.RouteNote(ctx, match.ID, taskID, author, content, input, routeKeepsParked, mayWake)
	if errors.Is(err, backlog.ErrNotRoutable) {
		return daemon.InjectCreateTask{}, false
	}
	if err != nil {
		// The store's own words name tables and SQL states; the agent gets
		// the fact, the log keeps the cause.
		o.Log.Warn("create_task: the routed note was not written", "task", match.ID, "err", err)
		return daemon.InjectCreateTask{RequestID: req.RequestID, OK: false, Error: "the note was not written"}, true
	}

	t := r.Task
	resp := daemon.InjectCreateTask{RequestID: req.RequestID, OK: true, TaskID: t.ID.String(),
		Agent: targetAgent.Slug, Topic: topic, Routed: true, State: t.State, Woken: r.Woken}
	if t.State == backlog.StateBlocked {
		key := ""
		if t.CorrelationKey != nil {
			key = *t.CorrelationKey
		}
		resp.WaitingOn = key
		resp.Parked = strings.HasPrefix(key, "parked:")
		switch {
		case r.Repeat || keepsParked(key):
		case t.ConversationID != nil || t.ChatAnswer:
			resp.Notice = "the task waits on a conversation with a person; your note stands on it, " +
				"but only the person's reply resumes it"
		case !mayWake:
			resp.Notice = stopNotice
		}
	}
	if r.Repeat {
		resp.Notice = "the same note is already on the task; nothing added"
	}
	if r.Woken && t.AgentID != agent.ID {
		o.EnsureRunning(t.AgentID)
	}

	_ = o.Obs.Record(ctx, agent.OrgID, agent.ID, &taskID, observability.KindLifecycle, map[string]any{
		"status": "task_routed", "routed_task": t.ID.String(), "target_agent": targetAgent.Slug,
		"topic": topic, "title": title, "state": t.State, "woken": r.Woken, "repeat": r.Repeat})
	o.publishTask(t.ID, targetAgent)
	return resp, true
}

func keepsParked(key string) bool {
	for _, p := range routeKeepsParked {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}
