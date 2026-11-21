package integration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"covey/internal/agents"
	"covey/internal/backlog"
	"covey/internal/daemon"
)

// More of #45: what the review of the first version found — event tasks of a
// system are woken, a woken park's question is answered where it went out,
// the lookup and the insert are one decision, and a task that leaves play
// under the request does not swallow it.

// A task a system's event opened (a pull request, an issue) is no
// conversation with a person: blocked on that system, a routed note resumes
// it — criterion 3 of #45 on a task GitHub started.
func TestTopicRoutingWakesASystemEventTask(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	x := s.newSupportAgent("pruefer")
	s.ohneLaeufe(x.ID)
	task, err := s.backlog.Create(ctx, s.orgID, x.ID, "review PR #2 cost page", "", "webhook:github", 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET state = 'in_progress' WHERE id = $1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.backlog.Block(ctx, task.ID, "github:example/repo#2", "sess-pr", "waiting for the push"); err != nil {
		t.Fatal(err)
	}
	s.rehire(t, x.ID)
	y, own := s.caller(t, "entwickler")

	resp := s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{
		Agent: "pruefer", Title: "amend #2 scope", Body: "the export moves to the next PR"})
	routedTo(t, resp, task)
	if !resp.Woken || resp.State != backlog.StateOpen || resp.Notice != "" {
		t.Fatalf("a task blocked on a system's event resumes with the note: %+v", resp)
	}
	if st := s.taskState(task.ID); st == backlog.StateBlocked {
		t.Fatalf("the event task has to leave blocked: %s", st)
	}
}

// A woken park's question that went out to people gets the note under it,
// as an agent's answer does (#620): otherwise the people read an open
// question, and the one who answers it answers whatever the task waits for
// by then.
func TestTopicRoutingAnswersTheDeliveredQuestion(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	x := s.newSupportAgent("formelwerk")
	s.ohneLaeufe(x.ID)
	if _, err := s.pool.Exec(ctx, `UPDATE agents SET supervisor_id=$2 WHERE id=$1`, x.ID, s.adminID); err != nil {
		t.Fatal(err)
	}
	task := s.runningTask(t, x.ID, "formula cost page headline")
	if _, err := s.backlog.Block(ctx, task.ID, "github:example/repo#2", "sess-1", "Waiting for the review"); err != nil {
		t.Fatal(err)
	}
	s.srv.Nacherzaehlen(ctx)
	var questionID, convID uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT id, conversation_id FROM conversation_messages
		WHERE task_id = $1 AND kind = 'question'`, task.ID).Scan(&questionID, &convID); err != nil {
		t.Fatalf("the question has to reach the supervisor first: %v", err)
	}
	s.rehire(t, x.ID)
	y, own := s.caller(t, "rechner")

	resp := s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{
		Agent: "formelwerk", Title: "amend cost page headline", Body: "use the weekly figure"})
	routedTo(t, resp, task)
	if !resp.Woken {
		t.Fatalf("the parked task wakes: %+v", resp)
	}
	// Looked up by the question it answers: the resumed run may report into
	// the same conversation.
	var text, answerTo string
	if err := s.pool.QueryRow(ctx, `SELECT text, meta->>'answer_to' FROM conversation_messages
		WHERE conversation_id = $1 AND kind = 'text' AND task_id = $2 AND meta->>'question' = $3`,
		convID, task.ID, questionID.String()).Scan(&text, &answerTo); err != nil {
		t.Fatalf("the note has to stand under the question in its conversation: %v", err)
	}
	want := "Note from agent rechner (task " + own.ID.String() + ") on topic \"cost page headline\":\n" +
		"amend cost page headline\n\nuse the weekly figure"
	if text != want || answerTo != task.ID.String() {
		t.Fatalf("the note stands under the question:\n got %q (answer to %s)\nwant %q", text, answerTo, want)
	}
}

// Two runs asking for the same new topic at the same target at the same
// moment: one creates the task, the other is routed onto it.
func TestTopicRoutingConcurrentRequestsMakeOneTask(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	x := s.stoppedAgent(t, "formelwerk")
	y, yOwn := s.caller(t, "rechner")
	z, zOwn := s.caller(t, "planer")

	callers := []struct {
		agent agents.Agent
		own   backlog.Task
	}{{y, yOwn}, {z, zOwn}}
	start := make(chan struct{})
	var wg sync.WaitGroup
	resps := make([]daemon.InjectCreateTask, len(callers))
	for i, c := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resps[i] = s.orch.CreateAgentTask(ctx, c.agent, c.own.ID, daemon.RequestCreateTask{
				Agent: "formelwerk", Title: "formula brand new headline", Body: "from " + c.agent.Slug})
		}()
	}
	close(start)
	wg.Wait()

	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM backlog_tasks
		WHERE agent_id = $1 AND topic_key = 'brand new headline'`, x.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("one topic, one task: %d tasks, answers %+v", n, resps)
	}
	created, routed := 0, 0
	var createdID string
	for _, r := range resps {
		if !r.OK {
			t.Fatalf("both requests succeed: %+v", resps)
		}
		if r.Routed {
			routed++
		} else {
			created++
			createdID = r.TaskID
		}
	}
	if created != 1 || routed != 1 {
		t.Fatalf("one creates, one is routed: %+v", resps)
	}
	for _, r := range resps {
		if r.TaskID != createdID {
			t.Fatalf("the routed one lands on the created task: %+v", resps)
		}
	}
}

// The task found for a topic finishes between the lookup and the note: the
// request goes to an older open task of the topic if there is one, and is
// created otherwise — not lost on the finished task.
func TestTopicRoutingFallsThroughAFinishedMatch(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	x := s.stoppedAgent(t, "formelwerk")
	older := s.openTask(t, x.ID, "formula cost page headline")
	newer := s.openTask(t, x.ID, "accept cost page headline")
	y, own := s.caller(t, "rechner")
	finish := func(id uuid.UUID) {
		if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET state = 'done' WHERE id = $1`, id); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { s.orch.BeforeRouteHook = nil })

	var seen []uuid.UUID
	s.orch.BeforeRouteHook = func(id uuid.UUID) {
		seen = append(seen, id)
		if id == newer.ID {
			finish(id)
		}
	}
	resp := s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{
		Agent: "formelwerk", Title: "amend cost page headline", Body: "first"})
	routedTo(t, resp, older)
	if len(seen) != 2 || seen[0] != newer.ID {
		t.Fatalf("the newest match first, then the older one: %v", seen)
	}
	if notes := s.notesOf(t, newer.ID); len(notes) != 0 {
		t.Fatalf("the finished task takes no note: %+v", notes)
	}

	// The last one finishes too: the request is created.
	s.orch.BeforeRouteHook = finish
	resp = s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{
		Agent: "formelwerk", Title: "amend cost page headline", Body: "second"})
	created(t, resp)
	if notes := s.notesOf(t, older.ID); len(notes) != 1 {
		t.Fatalf("the finished task takes no second note: %+v", notes)
	}
}

// An explicit topic that names nothing is refused rather than turning
// routing off.
func TestTopicRoutingRefusesAnEmptyTopic(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	s.stoppedAgent(t, "formelwerk")
	y, own := s.caller(t, "rechner")
	before := s.taskCount(t)
	resp := s.orch.CreateAgentTask(ctx, y, own.ID, daemon.RequestCreateTask{
		Agent: "formelwerk", Title: "formula cost page headline", Topic: " ... "})
	if resp.OK || !strings.Contains(resp.Error, "topic names nothing") {
		t.Fatalf("an empty topic is refused: %+v", resp)
	}
	if got := s.taskCount(t); got != before {
		t.Fatalf("nothing is created: %d, expected %d", got, before)
	}
}

// AC8: the depth and the chain limit refuse before anything is routed, and
// the refused call leaves no note.
func TestTopicRoutingDepthAndChainComeFirst(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	x := s.stoppedAgent(t, "formelwerk")
	existing := s.openTask(t, x.ID, "formula cost page headline")
	y, own := s.caller(t, "rechner")

	chainOf := func(n int, origin func(i int) string) uuid.UUID {
		t.Helper()
		id := own.ID
		for i := 0; i < n; i++ {
			child, err := s.backlog.CreateChild(ctx, id, backlog.ChildSpec{
				Title: fmt.Sprintf("link %d", i), Origin: origin(i)})
			if err != nil {
				t.Fatal(err)
			}
			id = child.ID
		}
		return id
	}
	ask := func(from uuid.UUID) daemon.InjectCreateTask {
		return s.orch.CreateAgentTask(ctx, y, from, daemon.RequestCreateTask{
			Agent: "formelwerk", Title: "amend cost page headline", Body: "use the weekly figure"})
	}

	deep := chainOf(3, func(int) string { return "agent:rechner" })
	if resp := ask(deep); resp.OK || !strings.Contains(resp.Error, "chain limit") {
		t.Fatalf("the depth limit refuses first: %+v", resp)
	}
	long := chainOf(12, func(i int) string { return fmt.Sprintf("agent:station-%d", i) })
	if resp := ask(long); resp.OK || !strings.Contains(resp.Error, "too long") {
		t.Fatalf("the chain limit refuses first: %+v", resp)
	}
	if notes := s.notesOf(t, existing.ID); len(notes) != 0 {
		t.Fatalf("a refused call adds no note: %+v", notes)
	}
}
