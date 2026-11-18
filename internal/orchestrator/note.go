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

// answerKeyPrefix marks a parked task that waits for a colleague's answer:
// the one kind of park an agent's add_note with "answer" may end. Every other
// key names something that will still arrive — an approval's decision
// ("approval:<id>", OnApprovalDecidedWithText), a hiring step ("hire:<id>"),
// an event of a target system (a ticket id, "teams:conversation:<id>") — and
// an agent that resumed such a task first would leave that event to find
// nobody waiting. The task would go on with the agent's words in place of the
// decision, and the decision would be lost.
//
// An allowlist and not a list of the keys to refuse: every blocked task
// carries a key (the runtime fails a park without one), and the keys of
// target systems are the plugins' to choose, so no list of them is complete.
const answerKeyPrefix = "answer:"

// NoteOnTask is the control-plane side of covey/add_note with a task_id: an
// agent adds to a colleague's open task, or answers a parked one, instead of
// opening a new task beside it that starts its own session.
//
// The note used to be written onto whatever id the agent named — a task of
// another organisation as well — under the bare author "agent", and it never
// woke anything. Now:
//
//   - The target has to belong to the agent's organisation. A task of another
//     one is refused with the same words as a task that does not exist, so
//     the answer tells nothing about ids outside. This comes before the guard
//     rails, so a mistyped id does not use up a granted approval.
//   - The guard rails decide (subject covey:add_note), here and not only in
//     the daemon (#703).
//   - The author is agent:<slug>, so the task's own run and the people
//     reading it can tell who wrote it.
//   - With Answer, a blocked target is resumed — the same store call as a
//     person's POST /tasks/{id}/reply, Backlog.Answer — but only one parked
//     on a key under answerKeyPrefix, and only while its agent is not stopped
//     (the kill switch, as gestoppt holds it for a person's reply). The run
//     reads the text under the writer's name, so it does not take a
//     colleague's words for a person's. A target that is not woken keeps the
//     note, and Reason says why.
func (o *Orchestrator) NoteOnTask(ctx context.Context, agent agents.Agent, taskID uuid.UUID, req daemon.RequestNote) daemon.InjectNote {
	fail := func(m string) daemon.InjectNote {
		return daemon.InjectNote{RequestID: req.RequestID, OK: false, Error: m}
	}
	deny := func(m string) daemon.InjectNote {
		return daemon.InjectNote{RequestID: req.RequestID, OK: false, Denied: true, Error: m}
	}
	text := strings.TrimSpace(req.Content)
	if text == "" {
		return fail("content is missing")
	}
	target, err := uuid.Parse(strings.TrimSpace(req.Target))
	if err != nil {
		return fail("task_id is not a task id")
	}
	t, err := o.Backlog.Get(ctx, target)
	if err != nil || t.OrgID != agent.OrgID {
		if err != nil && !errors.Is(err, backlog.ErrNotFound) {
			o.Log.Warn("add_note: the task could not be read", "task", target, "err", err)
			return fail("the task could not be read")
		}
		_ = o.Obs.Record(ctx, agent.OrgID, agent.ID, &taskID, observability.KindGuardrail,
			map[string]any{"rule": "organisation", "action": "covey:add_note", "decision": "denied"})
		return deny("no task " + target.String() + " in this organisation")
	}
	if reason := o.enforceRails(ctx, agent, taskID, "covey:add_note", req.ApprovalID); reason != "" {
		return deny(reason)
	}

	author := "agent:" + agent.Slug
	if _, err := o.Backlog.AddNote(ctx, target, author, text); err != nil {
		// The store's own words name tables and SQL states; the agent gets
		// the fact, the log keeps the cause.
		o.Log.Warn("add_note: the note was not written", "task", target, "err", err)
		return fail("the note was not written")
	}
	resp := daemon.InjectNote{RequestID: req.RequestID, OK: true}
	if req.Answer {
		resp.Woken, resp.Reason = o.answerTask(ctx, agent, taskID, t, text)
	}
	_ = o.Obs.Record(ctx, agent.OrgID, agent.ID, &taskID, observability.KindLifecycle,
		map[string]any{"status": "note", "scope": "task", "task_id": target.String(), "woken": resp.Woken})
	o.publishTask(target, agents.Agent{ID: t.AgentID, OrgID: t.OrgID})
	return resp
}

// answerTask resumes a colleague's parked task with an agent's answer, if it
// is one an agent may end (see answerKeyPrefix and NoteOnTask). It answers
// whether the task woke, and why not.
func (o *Orchestrator) answerTask(ctx context.Context, agent agents.Agent, taskID uuid.UUID, t backlog.Task, text string) (bool, string) {
	const otherKey = "the task waits for something other than a colleague's answer; " +
		"only a task parked on a correlation key starting with " + answerKeyPrefix + " is resumed this way"
	if t.State != backlog.StateBlocked {
		return false, "the task is not waiting for an answer"
	}
	if t.CorrelationKey == nil || !strings.HasPrefix(*t.CorrelationKey, answerKeyPrefix) {
		return false, otherKey
	}
	if stopped, err := o.isKilled(ctx, agents.Agent{ID: t.AgentID}); err != nil || stopped {
		if err != nil {
			o.Log.Warn("add_note: the kill switch could not be read", "task", t.ID, "err", err)
		}
		return false, "the task's agent is stopped (kill switch)"
	}
	input := "Answer from agent " + agent.Slug + " (task " + taskID.String() + "):\n" + text
	switch _, err := o.Backlog.AnswerParkedOn(ctx, t.ID, answerKeyPrefix, "agent:"+agent.Slug, input); {
	case err == nil:
		return true, ""
	case errors.Is(err, backlog.ErrParkedOnOtherKey):
		// it resumed and parked again on another key since it was read
		return false, otherKey
	case errors.Is(err, backlog.ErrInvalidTransition):
		// it moved on since it was read — nobody is waiting any more
		return false, "the task is not waiting for an answer"
	default:
		o.Log.Warn("add_note: the task could not be resumed", "task", t.ID, "err", err)
		return false, "the task could not be resumed"
	}
}
