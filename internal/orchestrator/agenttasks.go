package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"covey/internal/agents"
	"covey/internal/backlog"
	"covey/internal/daemon"
)

// The agent's own backlog (covey/list_tasks, covey/get_task). Without it an
// agent could not see its own queue or the tasks it handed out: coordinators
// rebuilt the state on every wake from wiki pages and the target systems, and
// an 8-character id quoted in a message was no use for add_note's task_id.
//
// Read-only, so no guard-rail check — like org_chart. Which tasks the agent
// may read is decided here, from the agent the connection belongs to, and
// nothing in the request widens it: its own tasks, and the ones it created
// (backlog/agentview.go). Everything else answers like an id that does not
// exist.
//
// No redaction: a task from a conversation reaches its own assignee in full,
// because the assignee ran it and saw it all. Redaction (spec/28, section 3)
// is for a reader who may not read the conversation; the two sets above hold
// no such task — a delegation does not inherit its parent's conversation.

const (
	// agentTasksDefaultLimit and agentTasksMaxLimit bound list_tasks: a list
	// is a look at the queue, and it goes into the run's context.
	agentTasksDefaultLimit = 50
	agentTasksMaxLimit     = 200
	// agentTaskNotes bounds the notes get_task hands out — the newest ones,
	// with the total beside them.
	agentTaskNotes = 50
	// resultHeadRunes is how much of a result or an error the list shows.
	resultHeadRunes = 200
	// minTaskPrefix is the shortest prefix get_task resolves — the 8
	// characters ids are quoted with.
	minTaskPrefix = 8
)

var taskStates = map[string]bool{
	backlog.StateOpen: true, backlog.StateInProgress: true, backlog.StateBlocked: true,
	backlog.StateDone: true, backlog.StateFailed: true, backlog.StateCancelled: true,
}

// taskPrefix is what get_task takes for an id that is not a full one: the
// start of a uuid, dashes in their places allowed.
var taskPrefix = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{0,4}){0,3}(-[0-9a-f]{0,12})?$`)

// agentTaskEntry is one task in the list.
type agentTaskEntry struct {
	ID           uuid.UUID  `json:"id"`
	Title        string     `json:"title"`
	State        string     `json:"state"`
	Priority     int        `json:"priority"`
	Stage        *string    `json:"stage"`
	Agent        string     `json:"agent"`
	Origin       string     `json:"origin"`
	ParentTaskID *uuid.UUID `json:"parent_task_id"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	// ResultHead is the start of the result, or of the error, on one line.
	ResultHead string `json:"result_head,omitempty"`
	// ArchivedAt is only there for an archived task (archived:true).
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
}

type agentTaskNote struct {
	Author    string    `json:"author"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type agentTaskTransition struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

type agentTaskChild struct {
	ID    uuid.UUID `json:"id"`
	Title string    `json:"title"`
	Agent string    `json:"agent"`
	State string    `json:"state"`
}

// agentTaskDetail is one task in full.
type agentTaskDetail struct {
	agentTaskEntry
	Body        string                `json:"body"`
	Result      *string               `json:"result"`
	Error       *string               `json:"error"`
	ResumeInput *string               `json:"resume_input"`
	Notes       []agentTaskNote       `json:"notes"`
	NotesTotal  int                   `json:"notes_total"`
	Transitions []agentTaskTransition `json:"transitions"`
	Children    []agentTaskChild      `json:"children"`
}

func entryOf(t backlog.AgentTask) agentTaskEntry {
	return agentTaskEntry{
		ID: t.ID, Title: t.Title, State: t.State, Priority: t.Priority, Stage: t.StageName,
		Agent: t.AgentSlug, Origin: t.Origin, ParentTaskID: t.ParentTaskID,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, ResultHead: resultHead(t.Task),
		ArchivedAt: t.ArchivedAt,
	}
}

// resultHead is the first resultHeadRunes characters of the result — or of
// the error when there is no result — with the line breaks folded.
func resultHead(t backlog.Task) string {
	text := ""
	switch {
	case t.Result != nil && strings.TrimSpace(*t.Result) != "":
		text = *t.Result
	case t.Error != nil:
		text = *t.Error
	}
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= resultHeadRunes {
		return text
	}
	return string([]rune(text)[:resultHeadRunes])
}

// AgentTasks serves covey/list_tasks (Op "list") and covey/get_task (Op
// "get") for the agent the connection belongs to.
func (o *Orchestrator) AgentTasks(ctx context.Context, agent agents.Agent, req daemon.RequestTasks) daemon.InjectTasks {
	fail := func(m string) daemon.InjectTasks {
		return daemon.InjectTasks{RequestID: req.RequestID, OK: false, Error: m}
	}
	var data any
	var err error
	switch req.Op {
	case "list":
		data, err = o.listAgentTasks(ctx, agent, req)
	case "get":
		data, err = o.getAgentTask(ctx, agent, req.ID)
	default:
		return fail(fmt.Sprintf("unknown op %q", req.Op))
	}
	if err != nil {
		return fail(err.Error())
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fail(err.Error())
	}
	return daemon.InjectTasks{RequestID: req.RequestID, OK: true, Data: raw}
}

func (o *Orchestrator) listAgentTasks(ctx context.Context, agent agents.Agent, req daemon.RequestTasks) ([]agentTaskEntry, error) {
	switch req.Scope {
	case "", backlog.ScopeAll, backlog.ScopeOwn, backlog.ScopeCreated:
	default:
		return nil, fmt.Errorf("unknown scope %q — own, created or all", req.Scope)
	}
	for _, st := range req.States {
		if !taskStates[st] {
			return nil, fmt.Errorf("unknown state %q — open, in_progress, blocked, done, failed or cancelled", st)
		}
	}
	limit := req.Limit
	if limit <= 0 {
		limit = agentTasksDefaultLimit
	}
	limit = min(limit, agentTasksMaxLimit)
	tasks, err := o.Backlog.ListForAgent(ctx, agent.ID, backlog.AgentTaskFilter{
		Scope: req.Scope, States: req.States, Archived: req.Archived, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]agentTaskEntry, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, entryOf(t))
	}
	return out, nil
}

func (o *Orchestrator) getAgentTask(ctx context.Context, agent agents.Agent, ref string) (agentTaskDetail, error) {
	ref = strings.TrimSpace(ref)
	if _, err := uuid.Parse(ref); err != nil && !taskPrefix.MatchString(strings.ToLower(ref)) {
		return agentTaskDetail{}, fmt.Errorf("id %q is neither a task id nor a prefix of at least %d hex characters", ref, minTaskPrefix)
	}
	t, err := o.Backlog.GetForAgent(ctx, agent.ID, ref)
	if errors.Is(err, backlog.ErrNotFound) {
		// The same sentence for a task that does not exist and for one that
		// is not the agent's to read.
		return agentTaskDetail{}, fmt.Errorf("no task %q among your own or created tasks", ref)
	}
	if err != nil {
		return agentTaskDetail{}, err
	}
	d := agentTaskDetail{agentTaskEntry: entryOf(t), Body: t.Body, Result: t.Result, Error: t.Error,
		ResumeInput: t.ResumeInput, Notes: []agentTaskNote{}, Transitions: []agentTaskTransition{},
		Children: []agentTaskChild{}}
	notes, total, err := o.Backlog.RecentNotes(ctx, t.ID, agentTaskNotes)
	if err != nil {
		return agentTaskDetail{}, err
	}
	for _, n := range notes {
		d.Notes = append(d.Notes, agentTaskNote{Author: n.Author, Content: n.Content, CreatedAt: n.CreatedAt})
	}
	d.NotesTotal = total
	trs, err := o.Backlog.Transitions(ctx, t.ID)
	if err != nil {
		return agentTaskDetail{}, err
	}
	for _, tr := range trs {
		d.Transitions = append(d.Transitions, agentTaskTransition{From: tr.FromState, To: tr.ToState, Note: tr.Note, CreatedAt: tr.CreatedAt})
	}
	children, err := o.Backlog.CreatedChildrenForAgent(ctx, agent.ID, t.ID)
	if err != nil {
		return agentTaskDetail{}, err
	}
	for _, c := range children {
		d.Children = append(d.Children, agentTaskChild{ID: c.ID, Title: c.Title, Agent: c.AgentSlug, State: c.State})
	}
	return d, nil
}
