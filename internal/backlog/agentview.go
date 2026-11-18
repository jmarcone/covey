package backlog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The agent's view of the backlog (covey/list_tasks, covey/get_task). An agent
// reads two sets of tasks and nothing else: its own (agent_id is the agent),
// and the ones it created with covey/create_task — subtasks and delegations,
// one level down. A created task hangs off one of the agent's tasks and
// carries the origin "agent:<slug>"; a continuation hangs there too, but it is
// the same work carried on and the agent's own task anyway.
//
// The rule lives in one SQL predicate, used for the list, the lookup and the
// children alike, so that no route reads wider than the others.

// The scopes of the agent's view.
const (
	ScopeOwn     = "own"
	ScopeCreated = "created"
	ScopeAll     = "all"
)

// ErrUnknownScope is a scope the view does not have.
var ErrUnknownScope = errors.New("unknown scope")

// ownBy and createdBy are the two sets, over the alias t, with the agent's id
// as $1.
const (
	ownBy     = `t.agent_id = $1`
	createdBy = `(t.origin LIKE 'agent:%' AND t.parent_task_id IN (SELECT id FROM backlog_tasks WHERE agent_id = $1))`
)

func readableBy(scope string) (string, error) {
	switch scope {
	case ScopeOwn:
		return ownBy, nil
	case ScopeCreated:
		return createdBy, nil
	case ScopeAll, "":
		return "(" + ownBy + " OR " + createdBy + ")", nil
	}
	return "", fmt.Errorf("%w %q", ErrUnknownScope, scope)
}

// taskColsT is taskCols over the alias t, for the joins below.
var taskColsT = func() string {
	cols := strings.Split(taskCols, ",")
	for i, c := range cols {
		cols[i] = "t." + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}()

// AgentTask is a task as the agent reads it: with the name of its column and
// the slug of whoever it is assigned to.
type AgentTask struct {
	Task
	StageName *string
	AgentSlug string
}

const agentTaskFrom = ` FROM backlog_tasks t
	JOIN agents a ON a.id = t.agent_id
	LEFT JOIN agent_stages st ON st.id = t.stage_id`

func scanAgentTasks(rows pgx.Rows) ([]AgentTask, error) {
	defer rows.Close()
	var out []AgentTask
	for rows.Next() {
		var at AgentTask
		if err := rows.Scan(append(at.scanDest(), &at.StageName, &at.AgentSlug)...); err != nil {
			return nil, err
		}
		out = append(out, at)
	}
	return out, rows.Err()
}

// AgentTaskFilter narrows ListForAgent. Empty States means any state;
// Archived adds the archive. Limit must be positive — the caller sets the
// default and the cap.
type AgentTaskFilter struct {
	Scope    string
	States   []string
	Archived bool
	Limit    int
}

// ListForAgent lists what the agent may read of the backlog. Work that is
// still going comes first (open, in progress, blocked), then by priority,
// then what moved last.
func (s *Store) ListForAgent(ctx context.Context, agentID uuid.UUID, f AgentTaskFilter) ([]AgentTask, error) {
	where, err := readableBy(f.Scope)
	if err != nil {
		return nil, err
	}
	args := []any{agentID}
	if !f.Archived {
		where += " AND t.archived_at IS NULL"
	}
	if len(f.States) > 0 {
		args = append(args, f.States)
		where += fmt.Sprintf(" AND t.state = ANY($%d)", len(args))
	}
	args = append(args, f.Limit)
	rows, err := s.pool.Query(ctx, "SELECT "+taskColsT+", st.name, a.slug"+agentTaskFrom+`
		WHERE `+where+`
		ORDER BY t.state NOT IN ('open','in_progress','blocked'), t.priority, t.updated_at DESC, t.id
		LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, err
	}
	return scanAgentTasks(rows)
}

// AmbiguousError is a prefix that matches more than one task the agent may
// read. IDs are those tasks (at most maxCandidates).
type AmbiguousError struct {
	Prefix string
	IDs    []uuid.UUID
}

func (e *AmbiguousError) Error() string {
	ids := make([]string, len(e.IDs))
	for i, id := range e.IDs {
		ids[i] = id.String()
	}
	return fmt.Sprintf("%q matches more than one of your tasks: %s", e.Prefix, strings.Join(ids, ", "))
}

// maxCandidates bounds the ids an ambiguous prefix names.
const maxCandidates = 10

// GetForAgent finds one task the agent may read, by its full id or by a
// prefix of it. A task outside the agent's view is ErrNotFound, exactly like
// one that does not exist: the answer must not tell the two apart.
func (s *Store) GetForAgent(ctx context.Context, agentID uuid.UUID, ref string) (AgentTask, error) {
	where, _ := readableBy(ScopeAll)
	var rows pgx.Rows
	var err error
	if id, perr := uuid.Parse(ref); perr == nil {
		rows, err = s.pool.Query(ctx, "SELECT "+taskColsT+", st.name, a.slug"+agentTaskFrom+`
			WHERE t.id = $2 AND `+where, agentID, id)
	} else {
		prefix := likeEscaper.Replace(strings.ToLower(ref)) + "%"
		rows, err = s.pool.Query(ctx, "SELECT "+taskColsT+", st.name, a.slug"+agentTaskFrom+`
			WHERE t.id::text LIKE $2 ESCAPE '\' AND `+where+`
			ORDER BY t.id LIMIT $3`, agentID, prefix, maxCandidates+1)
	}
	if err != nil {
		return AgentTask{}, err
	}
	found, err := scanAgentTasks(rows)
	if err != nil {
		return AgentTask{}, err
	}
	switch {
	case len(found) == 0:
		return AgentTask{}, ErrNotFound
	case len(found) > 1:
		amb := &AmbiguousError{Prefix: ref}
		for i, t := range found {
			if i == maxCandidates {
				break
			}
			amb.IDs = append(amb.IDs, t.ID)
		}
		return AgentTask{}, amb
	}
	return found[0], nil
}

// CreatedChildrenForAgent lists the tasks a task created (subtasks and
// delegations, no continuations) that the agent may read, oldest first.
func (s *Store) CreatedChildrenForAgent(ctx context.Context, agentID, parentID uuid.UUID) ([]AgentTask, error) {
	where, _ := readableBy(ScopeAll)
	rows, err := s.pool.Query(ctx, "SELECT "+taskColsT+", st.name, a.slug"+agentTaskFrom+`
		WHERE t.parent_task_id = $2 AND t.origin LIKE 'agent:%' AND `+where+`
		ORDER BY t.created_at, t.id`, agentID, parentID)
	if err != nil {
		return nil, err
	}
	return scanAgentTasks(rows)
}

// RecentNotes returns a task's newest notes, at most limit of them, in
// chronological order — and how many the task has in all.
func (s *Store) RecentNotes(ctx context.Context, taskID uuid.UUID, limit int) ([]Note, int, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, task_id, author, content, created_at, count(*) OVER ()
		FROM task_notes WHERE task_id=$1 ORDER BY created_at DESC, id DESC LIMIT $2`, taskID, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Note
	total := 0
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.TaskID, &n.Author, &n.Content, &n.CreatedAt, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, total, nil
}
