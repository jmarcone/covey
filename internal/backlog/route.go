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
	// Stopped: the task's agent is stopped (its own kill switch or the
	// organisation's), so the note did not resume it.
	Stopped bool
	// Park is the blocked edge the wake ended (Woken), as AnswerAsAgent
	// reports it: the question of that park may have gone out to people, and
	// the caller answers it there.
	Park int64
}

// RouteNote writes an addendum covey/create_task routed onto an existing task
// (#45) and, where that is right, resumes the task with it — in one
// transaction, behind the task's row lock, so that what decides the wake is
// the task as it stands and not as it stood at the lookup. A task that parked
// on a decision in between stays parked. TopicTx.RouteNote is the same inside
// the transaction of the lookup.
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
// resumeInput — only when it is blocked, its correlation key starts with none
// of keep, it does not talk to a person (TalksToSomebody), and its agent is
// not stopped. A task that talks to a person waits for them — their reply or
// an event of their channel; resumed by a colleague's note, its run would
// answer the person on the strength of that note, and the person's own reply
// would then find nobody waiting (as with the keys an agent's answer may not
// end, see AnswerKeyPrefix). A stopped agent is not resumed because the
// release does not replay what came while it was stopped, as with a
// person's reply.
func (s *Store) RouteNote(ctx context.Context, taskID, routedFrom uuid.UUID, author, content, resumeInput string,
	keep []string) (Routed, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Routed{}, err
	}
	defer tx.Rollback(ctx)
	r, err := routeNote(ctx, tx, taskID, routedFrom, author, content, resumeInput, keep)
	if err != nil {
		return Routed{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Routed{}, err
	}
	if r.Woken {
		s.notify(ctx, r.Task.AgentID)
	}
	return r, nil
}

// routeNote is RouteNote inside tx; the caller commits and wakes the agent
// after the commit.
//
// The lock is FOR NO KEY UPDATE: the note's foreign key on routed_from takes
// a key-share lock on the asking task, and two agents routing onto each
// other's running task at the same moment would deadlock under FOR UPDATE.
// It still conflicts with every other writer of the row (Block included).
func routeNote(ctx context.Context, tx pgx.Tx, taskID, routedFrom uuid.UUID, author, content, resumeInput string,
	keep []string) (Routed, error) {
	t, err := scanTask(tx.QueryRow(ctx, "SELECT "+taskCols+" FROM backlog_tasks WHERE id=$1 FOR NO KEY UPDATE", taskID))
	if err != nil {
		return Routed{}, err
	}
	if t.ArchivedAt != nil || (t.State != StateOpen && t.State != StateInProgress && t.State != StateBlocked) {
		return Routed{}, ErrNotRoutable
	}

	// Read in the transaction, not locked: a stop that commits after this
	// read leaves a task open that would have stayed blocked, and it waits
	// harmlessly — runAgent does nothing for a stopped agent.
	var killed, fleetKilled bool
	if err := tx.QueryRow(ctx, `SELECT a.killed, org.fleet_killed FROM agents a
		JOIN organizations org ON org.id=a.org_id WHERE a.id=$1`, t.AgentID).Scan(&killed, &fleetKilled); err != nil {
		return Routed{}, err
	}
	stopped := killed || fleetKilled

	var lastContent string
	err = tx.QueryRow(ctx, `SELECT content FROM task_notes WHERE task_id=$1 AND author=$2
		ORDER BY created_at DESC, id DESC LIMIT 1`, taskID, author).Scan(&lastContent)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Routed{}, err
	}
	if err == nil && lastContent == content {
		return Routed{Task: t, Repeat: true, Stopped: stopped}, nil
	}

	if _, err := tx.Exec(ctx, `INSERT INTO task_notes (id, task_id, author, content, routed_from)
		VALUES ($1,$2,$3,$4,$5)`, uuid.New(), taskID, author, content, routedFrom); err != nil {
		return Routed{}, err
	}

	wake := !stopped && t.State == StateBlocked && !t.TalksToSomebody()
	if wake && t.CorrelationKey != nil {
		for _, p := range keep {
			if strings.HasPrefix(*t.CorrelationKey, p) {
				wake = false
				break
			}
		}
	}
	out := Routed{Task: t, Stopped: stopped}
	if !wake {
		return out, nil
	}
	// The park this wake ends, read before the edge, as AnswerAsAgent does.
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM task_transitions
		WHERE task_id=$1 AND to_state='blocked'`, taskID).Scan(&out.Park); err != nil {
		return Routed{}, err
	}
	if err := applyTransition(ctx, tx, t, StateOpen, "answered by "+author,
		"resume_input=$3, priority=1", resumeInput); err != nil {
		return Routed{}, err
	}
	if out.Task, err = scanTask(tx.QueryRow(ctx, "SELECT "+taskCols+" FROM backlog_tasks WHERE id=$1", taskID)); err != nil {
		return Routed{}, err
	}
	out.Woken = true
	return out, nil
}

// TopicTx is the transaction covey/create_task decides in (#45): the topic
// lookup, the identical-title check, and the note or the new task, all on one
// connection behind one advisory lock per target agent and topic
// (WithTopicLock). Two runs that ask for the same new topic at the same moment
// would otherwise both miss in the lookup and both create a task.
//
// Everything it does goes through the transaction: a statement on another
// connection of the pool while this one holds the lock can deadlock a small
// pool (#241).
type TopicTx struct {
	tx   pgx.Tx
	wake []uuid.UUID
}

// WithTopicLock runs fn in a transaction that holds the advisory lock for the
// agent and lockKey — the topic, or "title:" and the title for a request that
// names no topic — commits it, and only then wakes the agents fn created work
// for: a NOTIFY before the commit could send the dispatcher looking for a row
// it cannot see yet.
func (s *Store) WithTopicLock(ctx context.Context, agentID uuid.UUID, lockKey string, fn func(tt *TopicTx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
		agentID.String()+":"+lockKey); err != nil {
		return err
	}
	tt := &TopicTx{tx: tx}
	if err := fn(tt); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, id := range tt.wake {
		s.notify(ctx, id)
	}
	return nil
}

// OpenByTopic is Store.OpenByTopic in the transaction.
func (tt *TopicTx) OpenByTopic(ctx context.Context, orgID, agentID uuid.UUID, topic string, exclude uuid.UUID) (Task, bool, error) {
	return openByTopic(ctx, tt.tx, orgID, agentID, topic, exclude)
}

// OpenWithTitle is Store.OpenWithTitle in the transaction.
func (tt *TopicTx) OpenWithTitle(ctx context.Context, agentID uuid.UUID, title string) (uuid.UUID, bool, error) {
	return openWithTitle(ctx, tt.tx, agentID, title)
}

// CreateChild is Store.CreateChild in the transaction; its agent is woken
// after the commit.
func (tt *TopicTx) CreateChild(ctx context.Context, parentID uuid.UUID, spec ChildSpec) (Task, error) {
	t, err := createChild(ctx, tt.tx, parentID, spec)
	if err != nil {
		return t, err
	}
	tt.wake = append(tt.wake, t.AgentID)
	return t, nil
}

// RouteNote is Store.RouteNote in the transaction; a woken task's agent is
// woken after the commit.
func (tt *TopicTx) RouteNote(ctx context.Context, taskID, routedFrom uuid.UUID, author, content, resumeInput string,
	keep []string) (Routed, error) {
	r, err := routeNote(ctx, tt.tx, taskID, routedFrom, author, content, resumeInput, keep)
	if err != nil {
		return r, err
	}
	if r.Woken {
		tt.wake = append(tt.wake, r.Task.AgentID)
	}
	return r, nil
}

// personChannels are the event sources on whose other end a person waits: a
// chat, a mail, a support ticket. A task one of them opened talks to that
// person (TalksToSomebody). The other sources — github, gitlab, jira, a
// trigger and the like — are events of a system; a task blocked on one of
// them is resumed by a routed note, which is what #45 (criterion 3) is for: a
// scope change to a pull request the task waits on.
var personChannels = []string{"teamsapp", "teams", "email", "m365", "zammad", "zendesk"}

// TalksToSomebody says whether the task is a conversation with a person
// outside the agents (#45): opened by a conversation or a chat message, or by
// an event of a person-facing channel (personChannels; origin
// "webhook:<channel>"). Such a task, blocked, waits for that person, and a
// colleague's routed note must not resume it in their place.
func (t Task) TalksToSomebody() bool {
	if t.ConversationID != nil || t.ChatAnswer {
		return true
	}
	source, ok := strings.CutPrefix(t.Origin, "webhook:")
	if !ok {
		return false
	}
	for _, c := range personChannels {
		if source == c {
			return true
		}
	}
	return false
}
