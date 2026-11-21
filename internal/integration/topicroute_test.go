package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/agents"
	"covey/internal/backlog"
	"covey/internal/chat"
	"covey/internal/daemon"
)

// Topic routing (#45): covey/create_task on a topic that already has an open,
// running or blocked task at the target agent creates no task; the request
// becomes a note on that task, and a blocked one resumes with it unless it
// waits on a decision. The tests call the control plane's handler the way a
// daemon reaches it.

// stoppedAgent is a colleague whose open tasks stay open: the kill switch
// keeps the dispatcher off them, and a delegation to it is still accepted.
func (s *stack) stoppedAgent(t *testing.T, slug string) agents.Agent {
	t.Helper()
	a := s.newSupportAgent(slug)
	if err := s.registry.SetKilled(context.Background(), a.ID, true); err != nil {
		t.Fatal(err)
	}
	a.Killed = true
	return a
}

// rehire undoes ohneLaeufe: a draft takes no delegation, so a target set up
// without runs is hired again before it is asked.
func (s *stack) rehire(t *testing.T, agentID uuid.UUID) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `UPDATE agents SET hired_at = now() WHERE id = $1`, agentID); err != nil {
		t.Fatal(err)
	}
}

// caller is the asking agent and the task its run works on. It never runs:
// the handler is called directly, as the daemon of its run would.
func (s *stack) caller(t *testing.T, slug string) (agents.Agent, backlog.Task) {
	t.Helper()
	a := s.newSupportAgent(slug)
	s.ohneLaeufe(a.ID)
	own, err := s.backlog.Create(context.Background(), s.orgID, a.ID, "own work of "+slug, "", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	return a, own
}

func (s *stack) openTask(t *testing.T, agentID uuid.UUID, title string) backlog.Task {
	t.Helper()
	task, err := s.backlog.Create(context.Background(), s.orgID, agentID, title, "", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// blockedOn puts a task of a target without runs straight into blocked on the
// key, as a run that parked on it leaves it.
func (s *stack) blockedOn(t *testing.T, agentID uuid.UUID, title, key string) backlog.Task {
	t.Helper()
	ctx := context.Background()
	task := s.openTask(t, agentID, title)
	if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET state = 'in_progress' WHERE id = $1`, task.ID); err != nil {
		t.Fatal(err)
	}
	task, err := s.backlog.Block(ctx, task.ID, key, "sess-"+task.ID.String(), "waiting")
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func (s *stack) notesOf(t *testing.T, taskID uuid.UUID) []backlog.Note {
	t.Helper()
	notes, err := s.backlog.ListNotes(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	return notes
}

func (s *stack) topicKeyOf(t *testing.T, taskID uuid.UUID) *string {
	t.Helper()
	var key *string
	if err := s.pool.QueryRow(context.Background(), `SELECT topic_key FROM backlog_tasks WHERE id = $1`, taskID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return key
}

func routedTo(t *testing.T, resp daemon.InjectCreateTask, want backlog.Task) {
	t.Helper()
	if !resp.OK || !resp.Routed || resp.TaskID != want.ID.String() {
		t.Fatalf("the request has to be routed onto %s: %+v", want.ID, resp)
	}
}

func created(t *testing.T, resp daemon.InjectCreateTask) {
	t.Helper()
	if !resp.OK || resp.Routed || resp.TaskID == "" {
		t.Fatalf("a task has to be created: %+v", resp)
	}
}

// AC1 and AC9: an addendum with the step's colon, capitals and a full stop
// meets the open task of its topic; no task comes into being, the note
// carries the writer and the body under the title, and the lifecycle record
// names the task it went to.
func TestTopicRoutingAddsToTheOpenTask(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	x := s.stoppedAgent(t, "formelwerk")
	existing := s.openTask(t, x.ID, "formula cost page headline")
	if k := s.topicKeyOf(t, existing.ID); k == nil || *k != "cost page headline" {
		t.Fatalf("a task created by a person carries its topic key: %v", k)
	}
	y, own := s.caller(t, "rechner")

	before := s.taskCount(t)
	resp := s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{
		Agent: "formelwerk", Title: "formula: Cost page headline.", Body: "use the weekly figure"})
	routedTo(t, resp, existing)
	if resp.State != backlog.StateOpen || resp.Woken || resp.Parked || resp.Agent != "formelwerk" {
		t.Fatalf("an open task takes the note and stays open: %+v", resp)
	}
	if got := s.taskCount(t); got != before {
		t.Fatalf("no task may come into being: %d, expected %d", got, before)
	}
	notes := s.notesOf(t, existing.ID)
	if len(notes) != 1 || notes[0].Author != "agent:rechner" ||
		notes[0].Content != "formula: Cost page headline.\n\nuse the weekly figure" {
		t.Fatalf("the note carries the writer, the title and the body: %+v", notes)
	}

	var raw []byte
	if err := s.pool.QueryRow(ctx, `SELECT payload FROM recording_events
		WHERE agent_id=$1 AND kind='lifecycle' AND payload->>'status'='task_routed'`, y.ID).Scan(&raw); err != nil {
		t.Fatalf("a routed call writes a task_routed record: %v", err)
	}
	var rec map[string]any
	_ = json.Unmarshal(raw, &rec)
	if rec["routed_task"] != existing.ID.String() || rec["topic"] != "cost page headline" ||
		rec["target_agent"] != "formelwerk" || rec["woken"] != false {
		t.Fatalf("the record names the task, the topic and the target: %v", rec)
	}
}

// AC2: an issue number is the topic. The running task here predates the
// topic column (no stored key); its key is computed from its title.
func TestTopicRoutingByIssueNumber(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	x := s.newSupportAgent("pruefer")
	s.ohneLaeufe(x.ID)
	review := s.openTask(t, x.ID, "review #45")
	if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET state = 'in_progress', topic_key = NULL WHERE id = $1`, review.ID); err != nil {
		t.Fatal(err)
	}
	s.rehire(t, x.ID)
	y, own := s.caller(t, "entwickler")

	before := s.taskCount(t)
	resp := s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{
		Agent: "pruefer", Title: "issue #45: something", Body: "the scope grew"})
	routedTo(t, resp, review)
	if resp.State != backlog.StateInProgress || resp.Woken {
		t.Fatalf("a running task takes the note and is not woken: %+v", resp)
	}
	if got := s.taskCount(t); got != before {
		t.Fatalf("no task may come into being: %d, expected %d", got, before)
	}
	if st := s.taskState(review.ID); st != backlog.StateInProgress {
		t.Fatalf("the running task stays as it is: %s", st)
	}
}

// AC3: a task blocked on a pull request resumes with the note — blocked to
// open with the note as its resume input, in its own session.
func TestTopicRoutingResumesABlockedTask(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	x := s.newSupportAgent("formelwerk")
	task, err := s.backlog.Create(ctx, s.orgID, x.ID, "formula cost page headline",
		"[mock:block key=github:jmarcone/covey#2 question=Waiting for the review]", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	s.orch.EnsureRunning(x.ID)
	waitFor(t, "the task parks on its pull request", 15*time.Second, func() bool {
		return s.taskState(task.ID) == backlog.StateBlocked
	})
	task = mustTask(t, s, task.ID.String())
	if task.RuntimeSessionID == nil || *task.RuntimeSessionID == "" {
		t.Fatal("a parked task keeps its session")
	}
	session := *task.RuntimeSessionID
	y, own := s.caller(t, "rechner")

	resp := s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{
		Agent: "formelwerk", Title: "amend cost page headline", Body: "use the weekly figure"})
	routedTo(t, resp, task)
	if !resp.Woken || resp.State != backlog.StateOpen || resp.Parked || resp.WaitingOn != "" {
		t.Fatalf("a task blocked on a pull request resumes with the note: %+v", resp)
	}

	input := "Note from agent rechner (task " + own.ID.String() + ") on topic \"cost page headline\":\n" +
		"amend cost page headline\n\nuse the weekly figure"
	waitFor(t, "the resumed task finishes", 15*time.Second, func() bool {
		return s.taskState(task.ID) == backlog.StateDone
	})
	done := mustTask(t, s, task.ID.String())
	if done.RuntimeSessionID == nil || *done.RuntimeSessionID != session {
		t.Fatalf("the resume has to continue session %s: %v", session, done.RuntimeSessionID)
	}
	if done.Result == nil || *done.Result != "Resumed and completed: "+input {
		t.Fatalf("the run has to be resumed with the note as its resume input:\n got %v\nwant %q", textOf(done.Result), input)
	}
	trs, err := s.backlog.Transitions(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	reopened := false
	for _, tr := range trs {
		if tr.FromState == backlog.StateBlocked && tr.ToState == backlog.StateOpen && tr.Note == "answered by agent:rechner" {
			reopened = true
		}
	}
	if !reopened {
		t.Fatalf("the task goes blocked → open on the note: %+v", trs)
	}
}

// AC4: a task parked on purpose, or waiting on a decision, keeps the note
// and stays blocked; the answer says what it waits on. So does a task whose
// agent is stopped, whatever its key.
func TestTopicRoutingLeavesParkedTasksParked(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	x := s.newSupportAgent("formelwerk")
	s.ohneLaeufe(x.ID)
	parked := s.blockedOn(t, x.ID, "formula cost insights", "parked:feat/cost-insights")
	approval := s.blockedOn(t, x.ID, "formula approval flow", "approval:"+uuid.NewString())
	hire := s.blockedOn(t, x.ID, "formula hiring step", "hire:"+uuid.NewString())
	s.rehire(t, x.ID)
	y, own := s.caller(t, "rechner")

	resp := s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{
		Agent: "formelwerk", Title: "accept: cost insights", Body: "after the freeze"})
	routedTo(t, resp, parked)
	if resp.Woken || !resp.Parked || resp.WaitingOn != "parked:feat/cost-insights" || resp.State != backlog.StateBlocked {
		t.Fatalf("a parked task keeps the note and stays parked: %+v", resp)
	}
	for _, c := range []struct {
		title string
		task  backlog.Task
	}{{"accept approval flow", approval}, {"accept hiring step", hire}} {
		resp := s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{Agent: "formelwerk", Title: c.title})
		routedTo(t, resp, c.task)
		if resp.Woken || resp.Parked || resp.WaitingOn != *c.task.CorrelationKey || resp.State != backlog.StateBlocked {
			t.Fatalf("a task waiting on a decision keeps the note and waits: %+v", resp)
		}
	}
	for _, task := range []backlog.Task{parked, approval, hire} {
		if st := s.taskState(task.ID); st != backlog.StateBlocked {
			t.Fatalf("%s has to stay blocked: %s", task.Title, st)
		}
		if notes := s.notesOf(t, task.ID); len(notes) != 1 {
			t.Fatalf("%s has to carry the note: %+v", task.Title, notes)
		}
	}

	// A stopped agent's task is not resumed: the release does not replay it.
	// Stopped first, so the dispatcher does not take the new task before it
	// is blocked.
	if err := s.registry.SetKilled(ctx, x.ID, true); err != nil {
		t.Fatal(err)
	}
	pr := s.blockedOn(t, x.ID, "formula pull request", "github:example/repo#9")
	resp = s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{Agent: "formelwerk", Title: "amend pull request"})
	routedTo(t, resp, pr)
	if resp.Woken || resp.WaitingOn != "github:example/repo#9" || !strings.Contains(resp.Notice, "stopped") {
		t.Fatalf("a stopped agent's task keeps the note, and the agent is told: %+v", resp)
	}
	if st := s.taskState(pr.ID); st != backlog.StateBlocked {
		t.Fatalf("the stopped agent's task stays blocked: %s", st)
	}
}

// AC5: no task of the topic in play at the target — none, only a finished
// one, or only one at another agent — and the task is created as before.
func TestTopicRoutingCreatesWithoutAMatch(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	x := s.stoppedAgent(t, "formelwerk")
	z := s.stoppedAgent(t, "anderswo")
	finished := s.openTask(t, x.ID, "formula gamma delta")
	if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET state = 'done' WHERE id = $1`, finished.ID); err != nil {
		t.Fatal(err)
	}
	s.openTask(t, z.ID, "formula epsilon zeta")
	y, own := s.caller(t, "rechner")

	for _, title := range []string{"formula: alpha beta", "formula gamma delta", "formula epsilon zeta"} {
		resp := s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{Agent: "formelwerk", Title: title})
		created(t, resp)
		id, _ := uuid.Parse(resp.TaskID)
		if got := mustTask(t, s, resp.TaskID); got.AgentID != x.ID || got.Title != title {
			t.Fatalf("the task is created at the target: %+v", got)
		}
		if k := s.topicKeyOf(t, id); k == nil || *k != strings.TrimPrefix(strings.TrimPrefix(title, "formula: "), "formula ") {
			t.Fatalf("the created task carries its topic key: %v", k)
		}
	}
}

// AC6: an explicit topic starts a separate item of the same issue, and a
// later request on that topic finds it; without a topic the issue number
// still routes onto the task of the issue.
func TestTopicRoutingExplicitTopic(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	x := s.stoppedAgent(t, "entwickler")
	part2 := s.openTask(t, x.ID, "issue #17: part 2")
	y, own := s.caller(t, "planer")

	resp := s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{
		Agent: "entwickler", Title: "issue #17: part 3", Topic: "#17 part 3"})
	created(t, resp)
	if resp.Topic != "#17 part 3" {
		t.Fatalf("the answer names the topic the task carries: %+v", resp)
	}
	part3 := mustTask(t, s, resp.TaskID)
	if k := s.topicKeyOf(t, part3.ID); k == nil || *k != "#17 part 3" {
		t.Fatalf("the explicit topic is stored: %v", k)
	}

	resp = s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{
		Agent: "entwickler", Title: "issue #17: part 3, the tests", Topic: "#17  Part 3."})
	routedTo(t, resp, part3)

	resp = s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{
		Agent: "entwickler", Title: "issue #17: one more thing"})
	routedTo(t, resp, part2)
}

// AC7: a title with a single word after the step names no topic and never
// routes; an identical open title is refused as before, naming the task.
func TestTopicRoutingNeedsATopic(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	x := s.stoppedAgent(t, "formelwerk")
	existing := s.openTask(t, x.ID, "formula: go")
	y, own := s.caller(t, "rechner")

	for _, title := range []string{"formula go", "go"} {
		created(t, s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{Agent: "formelwerk", Title: title}))
	}
	resp := s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{Agent: "formelwerk", Title: "formula: go"})
	if resp.OK || !strings.Contains(resp.Error, existing.ID.String()) || !strings.Contains(resp.Error, "covey/add_note") {
		t.Fatalf("an identical open title is refused with the task's id: %+v", resp)
	}
	if notes := s.notesOf(t, existing.ID); len(notes) != 0 {
		t.Fatalf("a request without a topic leaves no note: %+v", notes)
	}
}

// AC8: the guard rails decide before anything is routed, and a refused call
// leaves no note; routed requests count towards the run's width.
func TestTopicRoutingRunsTheLimitsFirst(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	x := s.stoppedAgent(t, "formelwerk")
	existing := s.openTask(t, x.ID, "formula cost page headline")
	y, own := s.caller(t, "rechner")

	rule, err := s.rails.Create(ctx, railRule(s.orgID, "deny_action", "covey:create_task:foreign"))
	if err != nil {
		t.Fatal(err)
	}
	resp := s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{Agent: "formelwerk", Title: "amend cost page headline"})
	if resp.OK || !strings.Contains(resp.Error, "guard rail") {
		t.Fatalf("a denied delegation is refused: %+v", resp)
	}
	if notes := s.notesOf(t, existing.ID); len(notes) != 0 {
		t.Fatalf("a denied call adds no note: %+v", notes)
	}
	if err := s.rails.Delete(ctx, s.orgID, rule.ID); err != nil {
		t.Fatal(err)
	}

	// Each with its own body: the same note twice is a repeat, which adds
	// nothing and counts nothing.
	for i := 0; i < 10; i++ {
		routedTo(t, s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{
			Agent: "formelwerk", Title: "amend cost page headline", Body: fmt.Sprintf("addendum %d", i)}), existing)
	}
	resp = s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{
		Agent: "formelwerk", Title: "amend cost page headline", Body: "addendum 10"})
	if resp.OK || !strings.Contains(resp.Error, "limit") {
		t.Fatalf("routed requests count towards the width of the run: %+v", resp)
	}
	if notes := s.notesOf(t, existing.ID); len(notes) != 10 {
		t.Fatalf("the refused call adds no note: %d notes", len(notes))
	}
}

// M1: a conversation task waits for the person it talks to. A routed note
// lands on it, but does not resume it — whatever its key — or its run would
// answer the person on a colleague's word, and the person's reply would find
// nobody waiting.
func TestTopicRoutingNeverWakesAConversationTask(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	x := s.newSupportAgent("formelwerk")
	s.ohneLaeufe(x.ID)
	conv, err := chat.New(s.pool).Direct(ctx, s.orgID, chat.Agent(x.ID), chat.Human(s.adminID), &s.adminID)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.backlog.CreateIn(ctx, s.orgID, x.ID, "answer cost page headline", "", "chat", 3, &conv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET state = 'in_progress' WHERE id = $1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.backlog.Block(ctx, task.ID, "github:example/repo#3", "sess-conv", "waiting"); err != nil {
		t.Fatal(err)
	}
	s.rehire(t, x.ID)
	y, own := s.caller(t, "rechner")

	resp := s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{
		Agent: "formelwerk", Title: "amend cost page headline", Body: "use the weekly figure"})
	routedTo(t, resp, task)
	if resp.Woken || resp.State != backlog.StateBlocked || resp.WaitingOn != "github:example/repo#3" ||
		!strings.Contains(resp.Notice, "person") {
		t.Fatalf("a conversation task keeps the note and waits for its person: %+v", resp)
	}
	if st := s.taskState(task.ID); st != backlog.StateBlocked {
		t.Fatalf("the conversation task stays blocked: %s", st)
	}
	if notes := s.notesOf(t, task.ID); len(notes) != 1 || notes[0].Author != "agent:rechner" {
		t.Fatalf("the note stands on the conversation task: %+v", notes)
	}
}

// A task a channel opened (a Teams message, a ticket) waits on the person at
// the other end just as a chat task does; a colleague's note does not resume
// it in their place, whatever key it parked on.
func TestTopicRoutingNeverWakesAChannelTask(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	x := s.newSupportAgent("formelwerk")
	s.ohneLaeufe(x.ID)
	task, err := s.backlog.Create(ctx, s.orgID, x.ID, "Teams message from Ada Example", "", "webhook:teamsapp", 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET state = 'in_progress' WHERE id = $1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.backlog.Block(ctx, task.ID, "teamsapp:conversation:c-1", "sess-teams", "waiting"); err != nil {
		t.Fatal(err)
	}
	s.rehire(t, x.ID)
	y, own := s.caller(t, "rechner")

	resp := s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{
		Agent: "formelwerk", Title: "reply: message from Ada Example", Body: "tell her it ships Friday"})
	routedTo(t, resp, task)
	if resp.Woken || resp.State != backlog.StateBlocked || !strings.Contains(resp.Notice, "person") {
		t.Fatalf("a channel task keeps the note and waits for its person: %+v", resp)
	}
	if st := s.taskState(task.ID); st != backlog.StateBlocked {
		t.Fatalf("the channel task stays blocked: %s", st)
	}
}

// The store's half: the note and the wake are decided together behind the
// row lock. A repeat of the latest note wakes nothing — a recurring run
// cannot resume a task that parked again on every tick — and a task that
// left play since the lookup takes no note at all (create_task then creates
// the task instead).
func TestRouteNoteRepeatAndTerminal(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	x := s.newSupportAgent("formelwerk")
	s.ohneLaeufe(x.ID)
	task := s.blockedOn(t, x.ID, "formula cost page headline", "github:example/repo#2")
	_, own := s.caller(t, "rechner")
	keep := []string{"parked:", "approval:", "hire:"}

	r, err := s.backlog.RouteNote(ctx, task.ID, own.ID, "agent:rechner", "amend it", "resume with it", keep)
	if err != nil || !r.Woken || r.Repeat || r.Task.State != backlog.StateOpen ||
		r.Task.ResumeInput == nil || *r.Task.ResumeInput != "resume with it" {
		t.Fatalf("the first note resumes the blocked task with its input: %+v %v", r, err)
	}

	// Parked again on the same pull request, as the next run would leave it.
	if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET state = 'in_progress' WHERE id = $1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.backlog.Block(ctx, task.ID, "github:example/repo#2", "sess-again", "waiting"); err != nil {
		t.Fatal(err)
	}
	r, err = s.backlog.RouteNote(ctx, task.ID, own.ID, "agent:rechner", "amend it", "resume with it", keep)
	if err != nil || r.Woken || !r.Repeat || r.Task.State != backlog.StateBlocked {
		t.Fatalf("the same note again wakes nothing: %+v %v", r, err)
	}
	if notes := s.notesOf(t, task.ID); len(notes) != 1 {
		t.Fatalf("a repeat writes no note: %+v", notes)
	}
	if n, err := s.backlog.CountRouted(ctx, own.ID); err != nil || n != 1 {
		t.Fatalf("a repeat does not count towards the width: %d %v", n, err)
	}
	// A note of somebody else in between does not make the same words new.
	if _, err := s.backlog.AddNote(ctx, task.ID, "user:admin", "looked at it"); err != nil {
		t.Fatal(err)
	}
	r, err = s.backlog.RouteNote(ctx, task.ID, own.ID, "agent:rechner", "amend it", "resume with it", keep)
	if err != nil || r.Woken || !r.Repeat {
		t.Fatalf("the same note after another's is still a repeat: %+v %v", r, err)
	}

	// The same request through create_task: routed, not woken, and said so.
	s.rehire(t, x.ID)
	y, yOwn := s.caller(t, "schreiber")
	if _, err := s.backlog.AddNote(ctx, task.ID, "agent:schreiber", "amend cost page headline\n\nuse the weekly figure"); err != nil {
		t.Fatal(err)
	}
	resp := s.orch.CreateAgentTask(ctx, y, yOwn.ID, daemon.RequestCreateTask{
		Agent: "formelwerk", Title: "amend cost page headline", Body: "use the weekly figure"})
	routedTo(t, resp, task)
	if resp.Woken || resp.State != backlog.StateBlocked || !strings.Contains(resp.Notice, "already") {
		t.Fatalf("a repeated request is routed, wakes nothing and says so: %+v", resp)
	}
	if st := s.taskState(task.ID); st != backlog.StateBlocked {
		t.Fatalf("the repeat leaves the task blocked: %s", st)
	}

	// Finished, or archived, since the lookup: nothing is written.
	before := len(s.notesOf(t, task.ID))
	if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET state = 'done' WHERE id = $1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.backlog.RouteNote(ctx, task.ID, own.ID, "agent:rechner", "late", "late", keep); !errors.Is(err, backlog.ErrNotRoutable) {
		t.Fatalf("a finished task is not routable: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET state = 'blocked', archived_at = now() WHERE id = $1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.backlog.RouteNote(ctx, task.ID, own.ID, "agent:rechner", "late", "late", keep); !errors.Is(err, backlog.ErrNotRoutable) {
		t.Fatalf("an archived task is not routable: %v", err)
	}
	if got := len(s.notesOf(t, task.ID)); got != before {
		t.Fatalf("a task out of play takes no note: %d, expected %d", got, before)
	}
}
