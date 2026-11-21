package orchestrator

import (
	"context"
	"fmt"
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
// A task that talks to a person is never woken either, whatever its key, nor
// one whose agent is stopped; the store decides that under the row lock
// (backlog.Store.RouteNote).
var routeKeepsParked = []string{"parked:", "approval:", "hire:"}

// routedAnswer does what follows a create_task request that CreateAgentTask
// added to the task already holding its topic (#45), once the note is
// committed, and builds the answer: the task, its state after the note,
// whether it woke, and — when it did not — what it waits on, so the asking
// agent can tell an addendum from a new task.
//
// A woken task is resumed in its own session with input, the edge of a
// person's reply; the question its park put to people, if it went out, gets
// the note under it as an agent's answer does (answerUnderQuestion), or a
// person's later reply to it would answer whatever the task waits for next.
func (o *Orchestrator) routedAnswer(ctx context.Context, agent agents.Agent, taskID uuid.UUID,
	targetAgent agents.Agent, r backlog.Routed, topic, title, input string, req daemon.RequestCreateTask) daemon.InjectCreateTask {
	t := r.Task
	resp := daemon.InjectCreateTask{RequestID: req.RequestID, OK: true, TaskID: t.ID.String(),
		Agent: targetAgent.Slug, Topic: topic, Routed: true, State: t.State, Woken: r.Woken}
	key := ""
	if t.State == backlog.StateBlocked && t.CorrelationKey != nil {
		key = *t.CorrelationKey
	}
	if t.State == backlog.StateBlocked {
		resp.WaitingOn = key
		resp.Parked = strings.HasPrefix(key, "parked:")
	}
	switch {
	case r.Repeat:
		resp.Notice = "the same note is already on the task; nothing added"
	case r.Stopped && t.State == backlog.StateBlocked:
		// As with a person's reply: the release does not replay what came
		// while the agent was stopped.
		resp.Notice = fmt.Sprintf("%s is stopped (kill switch); your note stands on the task, but it does not "+
			"resume it, not after the release either (the same note again counts as a repeat) — if it cannot "+
			"wait, tell your manager (covey/message)", targetAgent.Slug)
	case r.Stopped:
		// The same word the create path gives (#556): the note is delivered,
		// but nobody works on it until a person releases the agent.
		resp.Notice = fmt.Sprintf("%s is stopped (kill switch): your note stands on the task, which waits in their "+
			"backlog until a person releases them. If the work cannot wait, hand it to another colleague or tell "+
			"your manager (covey/message).", targetAgent.Slug)
	case t.State == backlog.StateBlocked && !keepsParked(key) && t.TalksToSomebody():
		resp.Notice = "the task waits on a person (a chat, a mail or a ticket); your note stands on it, " +
			"but only their reply resumes it"
	}
	if r.Woken {
		o.answerUnderQuestion(ctx, t, r.Park, input)
		if t.AgentID != agent.ID {
			o.EnsureRunning(t.AgentID)
		}
	}

	_ = o.Obs.Record(ctx, agent.OrgID, agent.ID, &taskID, observability.KindLifecycle, map[string]any{
		"status": "task_routed", "routed_task": t.ID.String(), "target_agent": targetAgent.Slug,
		"topic": topic, "title": title, "state": t.State, "woken": r.Woken, "repeat": r.Repeat})
	o.publishTask(t.ID, targetAgent)
	return resp
}

func keepsParked(key string) bool {
	for _, p := range routeKeepsParked {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}
