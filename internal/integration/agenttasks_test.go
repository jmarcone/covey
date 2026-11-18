package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"covey/internal/agents"
	"covey/internal/backlog"
	"covey/internal/daemon"
	"covey/internal/guardrails"
)

// listedTask is what covey/list_tasks hands out per task.
type listedTask struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	State        string     `json:"state"`
	Priority     int        `json:"priority"`
	Stage        *string    `json:"stage"`
	Agent        string     `json:"agent"`
	Origin       string     `json:"origin"`
	ParentTaskID *string    `json:"parent_task_id"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ResultHead   string     `json:"result_head"`
	ArchivedAt   *time.Time `json:"archived_at"`
}

// readTask is what covey/get_task hands out.
type readTask struct {
	listedTask
	Body        string  `json:"body"`
	Result      *string `json:"result"`
	Error       *string `json:"error"`
	ResumeInput *string `json:"resume_input"`
	Notes       []struct {
		Author    string    `json:"author"`
		Content   string    `json:"content"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"notes"`
	NotesTotal  int `json:"notes_total"`
	Transitions []struct {
		From string `json:"from"`
		To   string `json:"to"`
		Note string `json:"note"`
	} `json:"transitions"`
	Children []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Agent string `json:"agent"`
		State string `json:"state"`
	} `json:"children"`
}

func listTasks(t *testing.T, s *stack, caller agents.Agent, req daemon.RequestTasks) []listedTask {
	t.Helper()
	req.Op = "list"
	resp := s.orch.AgentTasks(context.Background(), caller, req)
	if !resp.OK {
		t.Fatalf("list_tasks %+v: %s", req, resp.Error)
	}
	var out []listedTask
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("list_tasks answer unreadable: %v (%s)", err, resp.Data)
	}
	return out
}

func getTask(t *testing.T, s *stack, caller agents.Agent, id string) readTask {
	t.Helper()
	resp := s.orch.AgentTasks(context.Background(), caller, daemon.RequestTasks{Op: "get", ID: id})
	if !resp.OK {
		t.Fatalf("get_task %q: %s", id, resp.Error)
	}
	var out readTask
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("get_task answer unreadable: %v (%s)", err, resp.Data)
	}
	return out
}

func titlesOf(list []listedTask) []string {
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.Title)
	}
	return out
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, g := range got {
		seen[g]++
	}
	for _, w := range want {
		if seen[w] == 0 {
			return false
		}
		seen[w]--
	}
	return true
}

// TestAgentReadsOwnAndCreatedTasks covers covey/list_tasks and covey/get_task
// in the control plane: which tasks an agent may see (its own, and the ones
// it created with covey/create_task — one level), the filters, the prefix
// lookup, and that everything else answers exactly like an id that does not
// exist.
func TestAgentReadsOwnAndCreatedTasks(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	lead := s.newSupportAgent("lead")
	dev := s.newSupportAgent("dev")
	s.ohneLaeufe(lead.ID)
	s.ohneLaeufe(dev.ID)
	if err := s.backlog.SeedDefaultStages(ctx, lead.ID); err != nil {
		t.Fatal(err)
	}

	mk := func(agent agents.Agent, title string, prio int) backlog.Task {
		t.Helper()
		task, err := s.backlog.Create(ctx, s.orgID, agent.ID, title, "body of "+title, "manual", prio)
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	child := func(parent backlog.Task, to agents.Agent, title, origin string) backlog.Task {
		t.Helper()
		task, err := s.backlog.CreateChild(ctx, parent.ID, backlog.ChildSpec{
			AgentID: to.ID, Title: title, Body: "body of " + title, Origin: origin, Priority: 4})
		if err != nil {
			t.Fatal(err)
		}
		return task
	}

	plan := mk(lead, "Plan", 3)
	report := mk(lead, "Report", 5)
	// A long result over several lines: the list carries one line of it.
	longResult := "first line\n" + strings.Repeat("ä", 300)
	if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET state='done', result=$2 WHERE id=$1`, report.ID, longResult); err != nil {
		t.Fatal(err)
	}
	old := mk(lead, "Old", 5)
	if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET state='done', archived_at=now() WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	delegated := child(plan, dev, "Delegated", "agent:lead")
	subtask := child(plan, lead, "Subtask", "agent:lead")
	continued := child(plan, lead, "Continued", "continuation:"+plan.ID.String())
	// What the colleague did with the delegation is the colleague's: its own
	// subtask stays out of sight, a task it handed back is the caller's own.
	grandchild := child(delegated, dev, "Dev's own subtask", "agent:dev")
	handedBack := child(delegated, lead, "Handed back", "agent:dev")
	devOwn := mk(dev, "Dev's private work", 2)

	// A conversation task of the caller: it ran it, so it reads it in full.
	conv := direkt(t, s, s.adminID, lead.ID)
	chatTask, err := s.backlog.CreateIn(ctx, s.orgID, lead.ID, "Question from the chat", "what the person wrote", "chat:admin@test.local", 6, &conv)
	if err != nil {
		t.Fatal(err)
	}

	// Another organisation with a task of its own.
	otherOrg := uuid.New()
	if _, err := s.pool.Exec(ctx, `INSERT INTO organizations (id, name) VALUES ($1,'Other Ltd')`, otherOrg); err != nil {
		t.Fatal(err)
	}
	foreign, err := s.registry.Create(ctx, otherOrg, "foreign", "Foreign", "mock", nil)
	if err != nil {
		t.Fatal(err)
	}
	foreignTask, err := s.backlog.Create(ctx, otherOrg, foreign.ID, "Foreign secret", "confidential", "manual", 1)
	if err != nil {
		t.Fatal(err)
	}

	// --- list_tasks ---
	all := listTasks(t, s, lead, daemon.RequestTasks{})
	want := []string{"Plan", "Report", "Delegated", "Subtask", "Continued", "Handed back", "Question from the chat"}
	if !sameSet(titlesOf(all), want) {
		t.Fatalf("default list: got %v, want %v", titlesOf(all), want)
	}
	// Open work first, then by priority; the done Report comes last.
	if all[0].Title != "Plan" || all[len(all)-1].Title != "Report" {
		t.Fatalf("order: open work by priority first, finished last — got %v", titlesOf(all))
	}
	for _, e := range all {
		if e.Title == "Delegated" {
			if e.Agent != "dev" || e.Origin != "agent:lead" || e.ParentTaskID == nil || *e.ParentTaskID != plan.ID.String() {
				t.Fatalf("delegation entry: %+v", e)
			}
			if e.ID != delegated.ID.String() || e.State != backlog.StateOpen || e.Priority != 4 || e.CreatedAt.IsZero() || e.UpdatedAt.IsZero() {
				t.Fatalf("delegation entry fields: %+v", e)
			}
		}
		if e.Title == "Report" {
			if strings.Contains(e.ResultHead, "\n") || utf8.RuneCountInString(e.ResultHead) != 200 ||
				!strings.HasPrefix(e.ResultHead, "first line ä") {
				t.Fatalf("result_head has to be one line of 200 characters, got %q", e.ResultHead)
			}
		}
		if e.Title == "Plan" && (e.Stage == nil || *e.Stage == "") {
			t.Fatalf("the list names the stage, got %+v", e)
		}
	}
	raw, _ := json.Marshal(all)
	if strings.Contains(string(raw), `"body"`) || strings.Contains(string(raw), `"notes"`) {
		t.Fatalf("the list carries neither body nor notes: %s", raw)
	}

	own := listTasks(t, s, lead, daemon.RequestTasks{Scope: "own"})
	if !sameSet(titlesOf(own), []string{"Plan", "Report", "Subtask", "Continued", "Handed back", "Question from the chat"}) {
		t.Fatalf("scope own: %v", titlesOf(own))
	}
	created := listTasks(t, s, lead, daemon.RequestTasks{Scope: "created"})
	if !sameSet(titlesOf(created), []string{"Delegated", "Subtask"}) {
		t.Fatalf("scope created: delegations and subtasks, no continuations — got %v", titlesOf(created))
	}
	done := listTasks(t, s, lead, daemon.RequestTasks{States: []string{"done"}})
	if !sameSet(titlesOf(done), []string{"Report"}) {
		t.Fatalf("state done: %v", titlesOf(done))
	}
	doneArchived := listTasks(t, s, lead, daemon.RequestTasks{States: []string{"done", "open"}, Scope: "own", Archived: true})
	if !sameSet(titlesOf(doneArchived), []string{"Plan", "Report", "Old", "Subtask", "Continued", "Handed back", "Question from the chat"}) {
		t.Fatalf("archived=true with two states: %v", titlesOf(doneArchived))
	}
	if one := listTasks(t, s, lead, daemon.RequestTasks{Limit: 1}); len(one) != 1 || one[0].Title != "Plan" {
		t.Fatalf("limit 1: %v", titlesOf(one))
	}
	for _, bad := range []daemon.RequestTasks{
		{Op: "list", Scope: "team"},
		{Op: "list", States: []string{"open", "finished"}},
	} {
		if resp := s.orch.AgentTasks(ctx, lead, bad); resp.OK || resp.Error == "" {
			t.Fatalf("%+v has to be refused, got %+v", bad, resp)
		}
	}

	// --- get_task ---
	for i := 0; i < 55; i++ {
		if _, err := s.backlog.AddNote(ctx, delegated.ID, "dev", fmt.Sprintf("note %02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.backlog.Cancel(ctx, delegated.ID, "no longer needed"); err != nil {
		t.Fatal(err)
	}
	got := getTask(t, s, lead, delegated.ID.String())
	if got.Body != "body of Delegated" || got.Agent != "dev" || got.State != backlog.StateCancelled {
		t.Fatalf("get_task by uuid: %+v", got.listedTask)
	}
	if got.NotesTotal != 55 || len(got.Notes) != 50 || got.Notes[0].Content != "note 05" || got.Notes[49].Content != "note 54" {
		t.Fatalf("notes: the newest 50 in order and the total, got %d of %d (%v … %v)",
			len(got.Notes), got.NotesTotal, got.Notes[0].Content, got.Notes[len(got.Notes)-1].Content)
	}
	if len(got.Transitions) != 1 || got.Transitions[0].From != backlog.StateOpen ||
		got.Transitions[0].To != backlog.StateCancelled || got.Transitions[0].Note != "no longer needed" {
		t.Fatalf("transitions: %+v", got.Transitions)
	}
	// The children of a delegation that the caller may read: the one handed
	// back to it, not the colleague's own subtask.
	if len(got.Children) != 1 || got.Children[0].ID != handedBack.ID.String() || got.Children[0].Agent != "lead" {
		t.Fatalf("children of the delegation: %+v", got.Children)
	}

	byPrefix := getTask(t, s, lead, plan.ID.String()[:8])
	if byPrefix.ID != plan.ID.String() || byPrefix.Body != "body of Plan" {
		t.Fatalf("get_task by an 8-character prefix: %+v", byPrefix.listedTask)
	}
	kids := map[string]bool{}
	for _, c := range byPrefix.Children {
		kids[c.ID] = true
	}
	if len(byPrefix.Children) != 2 || !kids[delegated.ID.String()] || !kids[subtask.ID.String()] || kids[continued.ID.String()] {
		t.Fatalf("the children of Plan are what it created, without the continuation: %+v", byPrefix.Children)
	}
	if up := getTask(t, s, lead, strings.ToUpper(plan.ID.String()[:13])); up.ID != plan.ID.String() {
		t.Fatalf("a prefix with the dash and in upper case: %s", up.ID)
	}

	chat := getTask(t, s, lead, chatTask.ID.String())
	if chat.Title != "Question from the chat" || chat.Body != "what the person wrote" {
		t.Fatalf("its own conversation task reaches the assignee unredacted: %+v", chat.listedTask)
	}

	// Two of the caller's tasks share a prefix, a third task with the same
	// prefix belongs to the colleague and must not show up as a candidate.
	shared := []uuid.UUID{
		uuid.MustParse("abcdef01-0000-4000-8000-000000000001"),
		uuid.MustParse("abcdef01-0000-4000-8000-000000000002"),
		uuid.MustParse("abcdef01-0000-4000-8000-000000000003"),
	}
	for i, id := range shared {
		owner := lead.ID
		if i == 2 {
			owner = dev.ID
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO backlog_tasks (id, org_id, agent_id, title, body, origin, priority)
			VALUES ($1,$2,$3,$4,'','manual',5)`, id, s.orgID, owner, fmt.Sprintf("Shared %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	amb := s.orch.AgentTasks(ctx, lead, daemon.RequestTasks{Op: "get", ID: "abcdef01"})
	if amb.OK || !strings.Contains(amb.Error, shared[0].String()) || !strings.Contains(amb.Error, shared[1].String()) ||
		strings.Contains(amb.Error, shared[2].String()) {
		t.Fatalf("an ambiguous prefix names the caller's candidates and nobody else's: %+v", amb)
	}
	if one := getTask(t, s, lead, "abcdef01-0000-4000-8000-000000000002"); one.Title != "Shared 1" {
		t.Fatalf("the full id resolves: %+v", one.listedTask)
	}

	// Everything outside the two sets answers like an unknown id: no title,
	// no state, the same sentence.
	unknown := s.orch.AgentTasks(ctx, lead, daemon.RequestTasks{Op: "get", ID: uuid.NewString()})
	if unknown.OK || unknown.Error == "" {
		t.Fatalf("an unknown id has to be an error: %+v", unknown)
	}
	for name, id := range map[string]string{
		"another agent's task":             devOwn.ID.String(),
		"another agent's task by prefix":   devOwn.ID.String()[:8],
		"a subtask of a delegation":        grandchild.ID.String(),
		"another organisation's task":      foreignTask.ID.String(),
		"another organisation's by prefix": foreignTask.ID.String()[:8],
		"a shared prefix of someone else":  shared[2].String(),
	} {
		resp := s.orch.AgentTasks(ctx, lead, daemon.RequestTasks{Op: "get", ID: id})
		if resp.OK || len(resp.Data) > 0 {
			t.Fatalf("%s: must not be readable, got %+v", name, resp)
		}
		for _, leak := range []string{"Dev's", "Foreign", "Shared 2", "open", "confidential"} {
			if strings.Contains(resp.Error, leak) {
				t.Fatalf("%s: the error reveals %q: %s", name, leak, resp.Error)
			}
		}
		if strings.Replace(resp.Error, id, "<id>", 1) != strings.Replace(unknown.Error, unknownID(unknown.Error), "<id>", 1) {
			t.Fatalf("%s: answered differently from an unknown id:\n%s\n%s", name, resp.Error, unknown.Error)
		}
	}
	for _, bad := range []string{"", "abc", "abcdef0", "not-a-uuid-at-all", "abcdef01%"} {
		if resp := s.orch.AgentTasks(ctx, lead, daemon.RequestTasks{Op: "get", ID: bad}); resp.OK {
			t.Fatalf("get_task %q has to be refused", bad)
		}
	}

	// The colleague sees the delegation as its own task, and not the caller's plan.
	devAll := listTasks(t, s, dev, daemon.RequestTasks{})
	if !sameSet(titlesOf(devAll), []string{"Delegated", "Dev's own subtask", "Dev's private work", "Handed back", "Shared 2"}) {
		t.Fatalf("the colleague's view: %v", titlesOf(devAll))
	}
}

// unknownID pulls the quoted id out of the error for an unknown id, so two
// errors can be compared with the id taken out.
func unknownID(msg string) string {
	_, rest, ok := strings.Cut(msg, `"`)
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, `"`)
	return id
}

// TestListTasksLimit: 50 by default, never more than 200.
func TestListTasksLimit(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("busy")
	s.ohneLaeufe(agent.ID)
	if _, err := s.pool.Exec(ctx, `INSERT INTO backlog_tasks (id, org_id, agent_id, title, body, origin, priority)
		SELECT gen_random_uuid(), $1, $2, 'Task ' || n, '', 'manual', 5 FROM generate_series(1, 205) n`,
		s.orgID, agent.ID); err != nil {
		t.Fatal(err)
	}
	if got := listTasks(t, s, agent, daemon.RequestTasks{}); len(got) != 50 {
		t.Fatalf("default limit: %d", len(got))
	}
	if got := listTasks(t, s, agent, daemon.RequestTasks{Limit: 1000}); len(got) != 200 {
		t.Fatalf("limit above 200 is capped: %d", len(got))
	}
}

// TestListAndGetTasksThroughTheProxy runs both actions the way a run calls
// them: through the action proxy in the sandbox, over the daemon protocol, to
// the control plane — and checks the audit events and that neither action
// asks the guard rails.
func TestListAndGetTasksThroughTheProxy(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("reader")
	other := s.newSupportAgent("other")
	s.ohneLaeufe(other.ID)

	earlier, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Earlier", "", "manual", 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.backlog.Cancel(ctx, earlier.ID, "test"); err != nil {
		t.Fatal(err)
	}
	notMine, err := s.backlog.Create(ctx, s.orgID, other.ID, "Not mine", "", "manual", 5)
	if err != nil {
		t.Fatal(err)
	}
	// Rules that would stop both actions if they asked: they are read-only
	// and do not ask, like org_chart.
	for _, subject := range []string{"covey:list_tasks", "covey:get_task"} {
		if _, err := s.rails.Create(ctx, railRule(s.orgID, guardrails.RuleDenyAction, subject)); err != nil {
			t.Fatal(err)
		}
	}

	reads, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Read my tasks",
		`[mock:action covey/list_tasks {"scope":"own","state":["cancelled","in_progress"]}]
[mock:action covey/get_task {"id":"`+earlier.ID.String()[:8]+`"}]
[mock:result read]`, "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the reading task is done", 20*time.Second, func() bool {
		return s.taskState(reads.ID) == backlog.StateDone
	})
	var listed, gotID string
	if err := s.pool.QueryRow(ctx, `SELECT payload->>'count' FROM recording_events
		WHERE task_id=$1 AND kind='action' AND payload->>'action'='covey:list_tasks'`, reads.ID).Scan(&listed); err != nil {
		t.Fatalf("list_tasks writes an audit event: %v", err)
	}
	if listed != "2" {
		t.Fatalf("the run listed its cancelled and its running task, audit says %s", listed)
	}
	if err := s.pool.QueryRow(ctx, `SELECT payload->>'id' FROM recording_events
		WHERE task_id=$1 AND kind='action' AND payload->>'action'='covey:get_task' AND (payload->>'ok')::bool`,
		reads.ID).Scan(&gotID); err != nil {
		t.Fatalf("get_task writes an audit event: %v", err)
	}
	if gotID != earlier.ID.String()[:8] {
		t.Fatalf("the audit event names the id asked for, got %q", gotID)
	}

	// A task that is not the caller's fails the run like any action error.
	snoop, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Read another's task",
		`[mock:action covey/get_task {"id":"`+notMine.ID.String()+`"}]
[mock:result should not get here]`, "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the snooping task fails", 20*time.Second, func() bool {
		return s.taskState(snoop.ID) == backlog.StateFailed
	})
	failed, err := s.backlog.Get(ctx, snoop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Error == nil || !strings.Contains(*failed.Error, "action covey/get_task") || strings.Contains(*failed.Error, "Not mine") {
		t.Fatalf("the refusal reaches the run without the title: %v", failed.Error)
	}
	var denied int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM recording_events WHERE task_id=$1 AND kind='action'
		AND payload->>'action'='covey:get_task' AND NOT (payload->>'ok')::bool`, snoop.ID).Scan(&denied); err != nil || denied != 1 {
		t.Fatalf("a refused get_task is recorded as well: %d (%v)", denied, err)
	}
}
