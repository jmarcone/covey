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
//     the answer tells nothing about ids outside.
//   - The guard rails decide (subject covey:add_note), here and not only in
//     the daemon (#703).
//   - The author is agent:<slug>, so the task's own run and the people
//     reading it can tell who wrote it.
//   - With Answer, a blocked target is resumed with the note's text — the
//     same two store calls as a person's POST /tasks/{id}/reply: the note,
//     then Backlog.Answer. A target that is not blocked keeps the note and
//     is not woken.
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
	if reason := o.enforceRails(ctx, agent, taskID, "covey:add_note", req.ApprovalID); reason != "" {
		return deny(reason)
	}
	t, err := o.Backlog.Get(ctx, target)
	if err != nil || t.OrgID != agent.OrgID {
		if err != nil && !errors.Is(err, backlog.ErrNotFound) {
			return fail("the task could not be read")
		}
		_ = o.Obs.Record(ctx, agent.OrgID, agent.ID, &taskID, observability.KindGuardrail,
			map[string]any{"rule": "organisation", "action": "covey:add_note", "decision": "denied"})
		return deny("no task " + target.String() + " in this organisation")
	}

	author := "agent:" + agent.Slug
	if _, err := o.Backlog.AddNote(ctx, target, author, text); err != nil {
		return fail("the note was not written: " + err.Error())
	}
	resp := daemon.InjectNote{RequestID: req.RequestID, OK: true}
	if req.Answer {
		switch _, err := o.Backlog.Answer(ctx, target, author, text); {
		case err == nil:
			resp.Woken = true
		case errors.Is(err, backlog.ErrInvalidTransition):
			// nobody was waiting — the note stands
		default:
			o.Log.Warn("add_note: the task could not be resumed", "task", target, "err", err)
		}
	}
	_ = o.Obs.Record(ctx, agent.OrgID, agent.ID, &taskID, observability.KindLifecycle,
		map[string]any{"status": "note", "scope": "task", "task_id": target.String(), "woken": resp.Woken})
	o.publishTask(target, agents.Agent{ID: t.AgentID, OrgID: t.OrgID})
	return resp
}
