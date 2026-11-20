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
// The same note as the latest one the same author left on the task is a
// repeat: no note, no wake. A heartbeat that files the same request every
// tick would otherwise resume a blocked task on every tick — the loop the
// identical-title refusal used to stop. Notes of others in between do not
// break the match. The price: an agent that means the very same words a
// second time is told it is a repeat, and has to say what is new.
//
// The task is resumed — the blocked → open edge of a reply (Answer), with
// resumeInput — only when mayWake, the task is blocked, its correlation key
// starts with none of keep, and it does not talk to somebody outside
// (TalksToSomebody). Such a task waits for the person on the other end —
// their reply or an event of their channel; resumed by a colleague's note,
// its run would answer the person on the strength of that note, and the
// person's own reply would then find nobody waiting (as with the keys an
// agent's answer may not end, see AnswerKeyPrefix).
//
// The lock is FOR NO KEY UPDATE: the note's foreign key on routed_from takes
// a key-share lock on the asking task, and two agents routing onto each
// other's running task at the same moment would deadlock under FOR UPDATE.
// It still conflicts with every other writer of the row (Block included).
func (s *Store) RouteNote(ctx context.Context, taskID, routedFrom uuid.UUID, author, content, resumeInput string,
	keep []string, mayWake bool) (Routed, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Routed{}, err
	}
	defer tx.Rollback(ctx)

	t, err := scanTask(tx.QueryRow(ctx, "SELECT "+taskCols+" FROM backlog_tasks WHERE id=$1 FOR NO KEY UPDATE", taskID))
	if err != nil {
		return Routed{}, err
	}
	if t.ArchivedAt != nil || (t.State != StateOpen && t.State != StateInProgress && t.State != StateBlocked) {
		return Routed{}, ErrNotRoutable
	}

	var lastContent string
	err = tx.QueryRow(ctx, `SELECT content FROM task_notes WHERE task_id=$1 AND author=$2
		ORDER BY created_at DESC, id DESC LIMIT 1`, taskID, author).Scan(&lastContent)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Routed{}, err
	}
	if err == nil && lastContent == content {
		return Routed{Task: t, Repeat: true}, nil
	}

	if _, err := tx.Exec(ctx, `INSERT INTO task_notes (id, task_id, author, content, routed_from)
		VALUES ($1,$2,$3,$4,$5)`, uuid.New(), taskID, author, content, routedFrom); err != nil {
		return Routed{}, err
	}

	wake := mayWake && t.State == StateBlocked && !t.TalksToSomebody()
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
		// Committed: the note stands and the wake happened. The task as it
		// was locked, moved along the edge, is the best answer left.
		after = t
		if wake {
			after.State = StateOpen
			after.ResumeInput = &resumeInput
		}
	}
	return Routed{Task: after, Woken: wake}, nil
}

// TalksToSomebody says whether the task is a conversation with somebody
// outside the agents (#45): opened by a conversation or a chat message, or
// by an event of a channel (origin "webhook:<system>" — a Teams message, a
// ticket, a trigger). Such a task, blocked, waits for that party, and a
// colleague's routed note must not resume it in their place.
func (t Task) TalksToSomebody() bool {
	return t.ConversationID != nil || t.ChatAnswer || strings.HasPrefix(t.Origin, "webhook:")
}
