package orchestrator

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"covey/internal/agents"
	"covey/internal/backlog"
	"covey/internal/chat"
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
const answerKeyPrefix = backlog.AnswerKeyPrefix

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
//   - With Answer, a target parked on a key under answerKeyPrefix is
//     resumed — the same edge as a person's POST /tasks/{id}/reply — and a
//     running one keeps the answer for its park (Backlog.AnswerAsAgent); both
//     only while its agent is not stopped (the kill switch, as gestoppt holds
//     it for a person's reply). The run reads the text under the writer's
//     name, so it does not take a colleague's words for a person's, and a
//     question of the park that went to people gets the answer under it. A
//     target that is not woken keeps the note, and Reason says why.
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
	t, target, denied, reason := o.noteTarget(ctx, agent, taskID, req.Target)
	switch {
	case denied:
		return deny(reason)
	case reason != "":
		return fail(reason)
	}
	if reason := o.enforceRails(ctx, agent, taskID, "covey:add_note", req.ApprovalID); reason != "" {
		return deny(reason)
	}

	var err error
	resp := daemon.InjectNote{RequestID: req.RequestID, OK: true}
	if req.Answer {
		resp.Woken, resp.Reason, err = o.answerTask(ctx, agent, taskID, t, text)
	} else {
		_, err = o.Backlog.AddNote(ctx, target, "agent:"+agent.Slug, text)
	}
	if err != nil {
		// The store's own words name tables and SQL states; the agent gets
		// the fact, the log keeps the cause.
		o.Log.Warn("add_note: the note was not written", "task", target, "err", err)
		return fail("the note was not written")
	}
	_ = o.Obs.Record(ctx, agent.OrgID, agent.ID, &taskID, observability.KindLifecycle,
		map[string]any{"status": "note", "scope": "task", "task_id": target.String(), "woken": resp.Woken})
	o.publishTask(target, agents.Agent{ID: t.AgentID, OrgID: t.OrgID})
	return resp
}

// noteTarget resolves the task a note names and holds it to the agent's
// organisation. It is the one place that check lives: decideAction asks it
// before the guard rails, so a person is not asked to approve a note that
// cannot land, and NoteOnTask asks it again before it writes, since the
// request can also reach it without the daemon's check.
//
// A task of another organisation and an id that names no task read the same
// and are recorded as a guard-rail event of the rule "organisation": denied
// is true and reason says so. A reason with denied false is a failure that
// is not a refusal (the id is no task id, the store could not be read).
func (o *Orchestrator) noteTarget(ctx context.Context, agent agents.Agent, taskID uuid.UUID, raw string) (t backlog.Task, target uuid.UUID, denied bool, reason string) {
	target, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return t, target, false, "task_id is not a task id"
	}
	t, err = o.Backlog.Get(ctx, target)
	if err != nil || t.OrgID != agent.OrgID {
		if err != nil && !errors.Is(err, backlog.ErrNotFound) {
			o.Log.Warn("add_note: the task could not be read", "task", target, "err", err)
			return t, target, false, "the task could not be read"
		}
		_ = o.Obs.Record(ctx, agent.OrgID, agent.ID, &taskID, observability.KindGuardrail,
			map[string]any{"rule": "organisation", "action": "covey:add_note", "decision": "denied"})
		return t, target, true, "no task " + target.String() + " in this organisation"
	}
	return t, target, false, ""
}

// answerTask writes an agent's answer onto a colleague's task and resumes
// the task with it, if it is one an agent may end (see answerKeyPrefix and
// NoteOnTask), or holds it for the task's park while the task still runs. It
// answers whether the task woke, and why not; an error means the note was not
// written.
func (o *Orchestrator) answerTask(ctx context.Context, agent agents.Agent, taskID uuid.UUID, t backlog.Task, text string) (bool, string, error) {
	author := "agent:" + agent.Slug
	if stopped, err := o.isKilled(ctx, agents.Agent{ID: t.AgentID}); err != nil || stopped {
		// The note stands either way; only the wake waits for the release,
		// as a person's reply does — and nothing holds it until then.
		if _, err := o.Backlog.AddNote(ctx, t.ID, author, text); err != nil {
			return false, "", err
		}
		if err != nil {
			o.Log.Warn("add_note: the kill switch could not be read", "task", t.ID, "err", err)
			return false, "the kill switch could not be checked, so the task was not resumed; " +
				"the note stands — answer again later", nil
		}
		return false, "the task's agent is stopped (kill switch); the note stands, but it does not " +
			"resume the task after the release — answer again then", nil
	}
	input := "Answer from agent " + agent.Slug + " (task " + taskID.String() + "):\n" + text
	a, err := o.Backlog.AnswerAsAgent(ctx, t.ID, answerKeyPrefix, author, text, input)
	if err != nil {
		return false, "", err
	}
	switch a.Outcome {
	case backlog.AnswerWoke:
		o.answerUnderQuestion(ctx, t, a.Park, input)
		return true, "", nil
	case backlog.AnswerHeld:
		return false, "the task is still running; the answer is held for this run and resumes the task " +
			"as soon as it parks on a correlation key starting with " + answerKeyPrefix, nil
	case backlog.AnswerOtherKey:
		return false, "the task waits for something other than a colleague's answer; " +
			"only a task parked on a correlation key starting with " + answerKeyPrefix + " is resumed this way", nil
	default:
		return false, "the task is not waiting for an answer", nil
	}
}

// answerUnderQuestion posts an agent's answer into the conversation the
// park's question went to, under it — as a person's answer is posted there
// (httpapi antwortZustellen, #620). Otherwise the people would still read an
// open question that a colleague has answered and that has resumed, and the
// one who answers it later would answer whatever the task waits for by then.
// The task's own agent writes it: the question is its own, and it is a member
// of that conversation; the text names the colleague it is from.
//
// A question that has not gone out yet does not go out any more
// (chat.DueQuestions skips a park an agent answered). Nothing here fails the
// answer: the note and the wake stand, and a message that cannot be written
// is logged.
func (o *Orchestrator) answerUnderQuestion(ctx context.Context, t backlog.Task, park int64, input string) {
	if o.Chat == nil || park == 0 {
		return
	}
	q, err := o.Chat.Message(ctx, chat.QuestionID(park))
	if errors.Is(err, chat.ErrNotFound) {
		return
	}
	if err != nil {
		o.Log.Warn("add_note: the question could not be read", "task", t.ID, "err", err)
		return
	}
	agentID, tid := t.AgentID, t.ID
	if _, _, err := o.Chat.Post(ctx, chat.Message{
		ConversationID: q.ConversationID, AuthorKind: chat.MemberAgent, AuthorID: &agentID,
		Text: input, Kind: chat.MessageText, TaskID: &tid,
		Meta: map[string]string{"answer_to": t.ID.String(), "question": q.ID.String()},
	}); err != nil {
		o.Log.Warn("add_note: the answer was not written into the conversation", "task", t.ID, "err", err)
		return
	}
	o.events.Publish(Event{Type: "chat", AgentID: t.AgentID.String(), OrgID: t.OrgID,
		Data: map[string]string{"state": "said", "conversation_id": q.ConversationID.String(), "task_id": t.ID.String()}})
}
