package backlog

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrNotRoutable: the task a request was to be routed onto has left play
// (finished, failed, cancelled or archived) since it was looked up. Nothing
// was written; the caller creates the task as if there had been no match, so
// the addendum is not lost on a task nobody will read again.
var ErrNotRoutable = errors.New("the task is no longer open, running or blocked")

// Routed is the outcome of RouteNote.
type Routed struct {
	// Task is the task as it stands after the call.
	Task Task
	// Woken: the note resumed the blocked task.
	Woken bool
	// Repeat: the task's latest note is this one, by the same author; nothing
	// was written and nothing woke.
	Repeat bool
}

// RouteNote writes an addendum covey/create_task routed onto an existing task
// (#45) and, where that is right, resumes the task with it — in one
// transaction, behind the task's row lock, so that what decides the wake is
// the task as it stands and not as it stood at the lookup. A task that parked
// on a decision in between stays parked.
//
// routedFrom is the asking run's task; the width limit of that run counts
// the note (CountRouted).
//
// The same note as the task's latest, by the same author, is a repeat: no
// note, no wake. A heartbeat that files the same request every tick would
// otherwise resume a blocked task on every tick — the loop the identical-title
// refusal used to stop.
//
// The task is resumed — the blocked → open edge of a reply (Answer), with
// resumeInput — only when mayWake, the task is blocked, its correlation key
// starts with none of keep, and it is not a conversation task. A conversation
// task (a conversation id, or a chat answer) waits for the person it talks to
// — their reply or an event of their channel; resumed by a colleague's note,
// its run would answer the person on the strength of that note, and the
// person's own reply would then find nobody waiting (as with the keys an
// agent's answer may not end, see AnswerKeyPrefix).
func (s *Store) RouteNote(ctx context.Context, taskID, routedFrom uuid.UUID, author, content, resumeInput string,
	keep []string, mayWake bool) (Routed, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Routed{}, err
	}
	defer tx.Rollback(ctx)

	t, err := scanTask(tx.QueryRow(ctx, "SELECT "+taskCols+" FROM backlog_tasks WHERE id=$1 FOR UPDATE", taskID))
	if err != nil {
		return Routed{}, err
	}
	if t.ArchivedAt != nil || (t.State != StateOpen && t.State != StateInProgress && t.State != StateBlocked) {
		return Routed{}, ErrNotRoutable
	}

	var lastAuthor, lastContent string
	err = tx.QueryRow(ctx, `SELECT author, content FROM task_notes WHERE task_id=$1
		ORDER BY created_at DESC, id DESC LIMIT 1`, taskID).Scan(&lastAuthor, &lastContent)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Routed{}, err
	}
	if err == nil && lastAuthor == author && lastContent == content {
		return Routed{Task: t, Repeat: true}, nil
	}

	if _, err := tx.Exec(ctx, `INSERT INTO task_notes (id, task_id, author, content, routed_from)
		VALUES ($1,$2,$3,$4,$5)`, uuid.New(), taskID, author, content, routedFrom); err != nil {
		return Routed{}, err
	}

	wake := mayWake && t.State == StateBlocked && t.ConversationID == nil && !t.ChatAnswer
	if wake && t.CorrelationKey != nil {
		for _, p := range keep {
			if strings.HasPrefix(*t.CorrelationKey, p) {
				wake = false
				break
			}
		}
	}
	if wake {
		if err := applyTransition(ctx, tx, t, StateOpen, "answered by "+author,
			"resume_input=$3, priority=1", resumeInput); err != nil {
			return Routed{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Routed{}, err
	}
	if wake {
		s.notify(ctx, t.AgentID)
	}
	after, err := s.Get(ctx, taskID)
	if err != nil {
		return Routed{}, err
	}
	return Routed{Task: after, Woken: wake}, nil
}
