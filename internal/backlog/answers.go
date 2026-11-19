package backlog

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AnswerKeyPrefix marks a parked task that waits for a colleague's answer:
// the one kind of park an agent's answer may end (AnswerAsAgent). Every other
// key names something that will still arrive — an approval's decision, a
// hiring step, an event of a target system — and is that event's to end.
const AnswerKeyPrefix = "answer:"

// AnswerOutcome is what an agent's answer did to the task it went to.
type AnswerOutcome int

const (
	// AnswerNotWaiting: the task is neither parked nor running (open, or
	// finished). The note stands, nothing else happens.
	AnswerNotWaiting AnswerOutcome = iota
	// AnswerWoke: the task was parked on a key under the prefix and resumes
	// with the answer.
	AnswerWoke
	// AnswerHeld: the task is running. The answer waits for that run's park
	// on a key under AnswerKeyPrefix (Block) and is dropped if the run ends
	// any other way.
	AnswerHeld
	// AnswerOtherKey: the task is parked on a key an agent may not end.
	AnswerOtherKey
)

// Answered is AnswerAsAgent's result.
type Answered struct {
	Outcome AnswerOutcome
	Note    Note
	// Park is the blocked edge the answer ended (AnswerWoke): the question
	// that went to people, if it went, stands on it (chat.QuestionID).
	Park int64
}

// AnswerAsAgent writes an agent's answer onto a task as a note and, in the
// same transaction behind the task's lock, does with it what the task's state
// allows:
//
//   - parked on a key under keyPrefix: the task resumes with resumeInput,
//     the same edge as Answer;
//   - running: the answer is held for this run, and the run's park on such
//     a key takes it (Block). The asker delegates and parks when its run
//     ends, while the colleague it delegated to is woken at once — an answer
//     inside that window would otherwise be a note nobody waits for;
//   - parked on any other key, or neither parked nor running: the note
//     stands, and nothing else happens.
//
// Note, state and hold are one transaction, so a park and an answer that
// come at the same moment meet one way or the other, never past each other.
func (s *Store) AnswerAsAgent(ctx context.Context, id uuid.UUID, keyPrefix, author, note, resumeInput string) (Answered, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Answered{}, err
	}
	defer tx.Rollback(ctx)

	t, err := scanTask(tx.QueryRow(ctx, "SELECT "+taskCols+" FROM backlog_tasks WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		return Answered{}, err
	}
	var out Answered
	if err := tx.QueryRow(ctx, `INSERT INTO task_notes (id, task_id, author, content)
		VALUES ($1,$2,$3,$4) RETURNING id, task_id, author, content, created_at`,
		uuid.New(), id, author, note).
		Scan(&out.Note.ID, &out.Note.TaskID, &out.Note.Author, &out.Note.Content, &out.Note.CreatedAt); err != nil {
		return Answered{}, err
	}
	switch {
	case t.State == StateInProgress:
		if _, err := tx.Exec(ctx, `INSERT INTO task_held_answers (task_id, run, author, input)
			VALUES ($1, (`+currentRun+`), $2, $3)`, id, author, resumeInput); err != nil {
			return Answered{}, err
		}
		out.Outcome = AnswerHeld
	case t.State != StateBlocked:
		out.Outcome = AnswerNotWaiting
	case t.CorrelationKey == nil || !strings.HasPrefix(*t.CorrelationKey, keyPrefix):
		out.Outcome = AnswerOtherKey
	default:
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM task_transitions
			WHERE task_id=$1 AND to_state='blocked'`, id).Scan(&out.Park); err != nil {
			return Answered{}, err
		}
		if err := applyTransition(ctx, tx, t, StateOpen, "answered by "+author,
			"resume_input=$3, priority=1", resumeInput); err != nil {
			return Answered{}, err
		}
		out.Outcome = AnswerWoke
	}
	if err := tx.Commit(ctx); err != nil {
		return Answered{}, err
	}
	if out.Outcome == AnswerWoke {
		s.notify(ctx, t.AgentID)
	}
	return out, nil
}

// currentRun is the task's ($1) latest edge into in_progress: the run that
// is going. 0 for a task that was put there without one.
const currentRun = `SELECT coalesce(max(id), 0) FROM task_transitions WHERE task_id=$1 AND to_state='in_progress'`

// takeHeldAnswers takes the answers held for the task's current run, oldest
// first, joined into one resume input under their authors. Answers held for
// an earlier run go with them: that run ended without parking for them.
func takeHeldAnswers(ctx context.Context, tx pgx.Tx, taskID uuid.UUID) (authors, input string, err error) {
	rows, err := tx.Query(ctx, `DELETE FROM task_held_answers h
		WHERE h.task_id=$1
		RETURNING h.id, h.run = (`+currentRun+`), h.author, h.input`, taskID)
	if err != nil {
		return "", "", err
	}
	type held struct {
		id      int64
		current bool
		author  string
		input   string
	}
	var all []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.id, &h.current, &h.author, &h.input); err != nil {
			rows.Close()
			return "", "", err
		}
		all = append(all, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	// RETURNING keeps no order; the ids are the order the answers came in.
	sort.Slice(all, func(i, j int) bool { return all[i].id < all[j].id })
	var who, texts []string
	for _, h := range all {
		if !h.current {
			continue
		}
		texts = append(texts, h.input)
		if !slices.Contains(who, h.author) {
			who = append(who, h.author)
		}
	}
	return strings.Join(who, ", "), strings.Join(texts, "\n\n"), nil
}
