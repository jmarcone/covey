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
var routeKeepsParked = []string{"parked:", "approval:", "hire:"}

// routeCreateTask adds a covey/create_task request to the task that already
// holds its topic at the target agent, instead of creating a second task with
// a session of its own (#45). The title stands on the note's first line, so
// the note reads as the request it was.
//
// A blocked task is resumed with the note as its resume input — the same
// store call as a person's reply (Backlog.Answer), so it continues its own
// session — unless it waits on a key in routeKeepsParked, or its agent is
// stopped. An open or running task only takes the note.
//
// The answer names the task, its state after the note and whether it woke,
// so the asking agent can tell an addendum from a new task. The note carries
// the asking run's task (routed_from), which the width limit counts.
func (o *Orchestrator) routeCreateTask(ctx context.Context, agent agents.Agent, taskID uuid.UUID,
	targetAgent agents.Agent, match backlog.Task, topic, title string, req daemon.RequestCreateTask) daemon.InjectCreateTask {
	author := "agent:" + agent.Slug
	content := title
	if body := strings.TrimSpace(req.Body); body != "" {
		content += "\n\n" + body
	}
	if _, err := o.Backlog.AddRoutedNote(ctx, match.ID, taskID, author, content); err != nil {
		// The store's own words name tables and SQL states; the agent gets
		// the fact, the log keeps the cause.
		o.Log.Warn("create_task: the routed note was not written", "task", match.ID, "err", err)
		return daemon.InjectCreateTask{RequestID: req.RequestID, OK: false, Error: "the note was not written"}
	}

	resp := daemon.InjectCreateTask{RequestID: req.RequestID, OK: true, TaskID: match.ID.String(),
		Agent: targetAgent.Slug, Routed: true, State: match.State}
	if match.State == backlog.StateBlocked {
		key := ""
		if match.CorrelationKey != nil {
			key = *match.CorrelationKey
		}
		resp.WaitingOn = key
		switch {
		case strings.HasPrefix(key, "parked:"):
			resp.Parked = true
		case keepsParked(key):
			// Waits on a decision: the note stands until it comes.
		default:
			resp.Woken, resp.Notice = o.resumeRouted(ctx, agent, taskID, match, topic, content)
			if resp.Woken {
				resp.State = backlog.StateOpen
				resp.WaitingOn = ""
				if match.AgentID != agent.ID {
					o.EnsureRunning(match.AgentID)
				}
			}
		}
	}

	_ = o.Obs.Record(ctx, agent.OrgID, agent.ID, &taskID, observability.KindLifecycle, map[string]any{
		"status": "task_routed", "routed_task": match.ID.String(), "target_agent": targetAgent.Slug,
		"topic": topic, "title": title, "state": resp.State, "woken": resp.Woken})
	o.publishTask(match.ID, targetAgent)
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

// resumeRouted resumes a blocked task with a routed note. It answers whether
// the task woke and, when it did not for a reason the asking agent can act
// on, a notice saying why. The note is written already; nothing here takes
// it back.
func (o *Orchestrator) resumeRouted(ctx context.Context, agent agents.Agent, taskID uuid.UUID,
	match backlog.Task, topic, content string) (bool, string) {
	stopped, err := o.isKilled(ctx, agents.Agent{ID: match.AgentID})
	if err != nil {
		o.Log.Warn("create_task: the kill switch could not be read", "task", match.ID, "err", err)
		return false, "the kill switch of the task's agent could not be checked, so the task was not resumed; " +
			"your note stands on it"
	}
	if stopped {
		// As with a person's reply: the release does not replay what came
		// while the agent was stopped.
		return false, "the task's agent is stopped (kill switch); your note stands on the task, but it does not " +
			"resume it, not after the release either — send it again then, or tell your manager (covey/message)"
	}
	input := "Note from agent " + agent.Slug + " (task " + taskID.String() + ") on topic \"" + topic + "\":\n" + content
	if _, err := o.Backlog.Answer(ctx, match.ID, "agent:"+agent.Slug, input); err != nil {
		if !errors.Is(err, backlog.ErrInvalidTransition) {
			o.Log.Warn("create_task: the routed note did not resume the task", "task", match.ID, "err", err)
		}
		// ErrInvalidTransition: the task left blocked meanwhile (a reply, an
		// event); it reads the note on its own course.
		return false, ""
	}
	return true, ""
}
