package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"covey/internal/backlog"
	"covey/internal/daemon"
)

// runningTask gives the agent (without runs) one task in progress through
// a real dispatch edge, as the dispatcher leaves it while its run is going.
func (s *stack) runningTask(t *testing.T, agentID uuid.UUID, title string) backlog.Task {
	t.Helper()
	ctx := context.Background()
	task, err := s.backlog.Create(ctx, s.orgID, agentID, title, "", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.backlog.ClaimNext(ctx, agentID)
	if err != nil || claimed.ID != task.ID {
		t.Fatalf("claim: %v %v", claimed.ID, err)
	}
	return claimed
}

func (s *stack) heldAnswers(t *testing.T, taskID uuid.UUID) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM task_held_answers WHERE task_id = $1`, taskID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestAnAnswerBeforeTheParkResumesAtThePark: the asker delegates and parks
// only when its run ends, while covey/create_task wakes the colleague at
// once. An answer inside that window used to stay a note, and the park after
// it waited for an answer that had already come. Now the answer is held for
// the running run, and its park on an answer: key resumes the task at once
// with the labelled text, in the same session.
func TestAnAnswerBeforeTheParkResumesAtThePark(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	asker := s.newSupportAgent("kollegin")
	s.ohneLaeufe(asker.ID)
	running := s.runningTask(t, asker.ID, "Rollout")

	writer := s.newSupportAgent("schreiber")
	own, err := s.backlog.Create(ctx, s.orgID, writer.ID, "Freigabe weitergeben", "", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	resp := s.orch.NoteOnTask(ctx, writer, own.ID, daemon.RequestNote{Target: running.ID.String(), Content: "go", Answer: true})
	if !resp.OK || resp.Woken || !strings.Contains(resp.Reason, "held") {
		t.Fatalf("an answer to a running task is held for its park: %+v", resp)
	}
	if st := s.taskState(running.ID); st != backlog.StateInProgress {
		t.Fatalf("the running task is not touched: %s", st)
	}
	notes, err := s.backlog.ListNotes(ctx, running.ID)
	if err != nil || len(notes) != 1 || notes[0].Author != "agent:schreiber" || notes[0].Content != "go" {
		t.Fatalf("the answer stands as the writer's note: %+v %v", notes, err)
	}

	parked, err := s.backlog.Block(ctx, running.ID, "answer:rollout", "sess-1", "Go or hold?")
	if err != nil {
		t.Fatal(err)
	}
	want := "Answer from agent schreiber (task " + own.ID.String() + "):\ngo"
	if parked.State != backlog.StateOpen || textOf(parked.ResumeInput) != want {
		t.Fatalf("the park takes the held answer and resumes at once:\n state %s\n got %q\nwant %q",
			parked.State, textOf(parked.ResumeInput), want)
	}
	if parked.RuntimeSessionID == nil || *parked.RuntimeSessionID != "sess-1" || parked.Priority != 1 {
		t.Fatalf("the resume continues the parked session at the front: %v %d", parked.RuntimeSessionID, parked.Priority)
	}
	if n := s.heldAnswers(t, running.ID); n != 0 {
		t.Fatalf("a taken answer is gone: %d held", n)
	}
	// The history says what happened: it parked, and the answer ended it.
	var notesOfEdges []string
	rows, err := s.pool.Query(ctx, `SELECT note FROM task_transitions WHERE task_id = $1 ORDER BY id`, running.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		notesOfEdges = append(notesOfEdges, n)
	}
	rows.Close()
	if got := strings.Join(notesOfEdges, " | "); got != "dispatch | blocked: Go or hold? | answered by agent:schreiber" {
		t.Fatalf("the edges: %s", got)
	}
}

// TestAHeldAnswerStaysWithItsRun: a held answer belongs to the run it came
// in. A park on another key does not take it — an approval waits for its
// decision — and a run that ended without parking leaves it behind: the next
// run's park on an answer: key does not wake on an answer to an earlier
// question.
func TestAHeldAnswerStaysWithItsRun(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	asker := s.newSupportAgent("kollegin")
	s.ohneLaeufe(asker.ID)
	writer := s.newSupportAgent("schreiber")
	own, err := s.backlog.Create(ctx, s.orgID, writer.ID, "eigene", "", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	answer := func(id uuid.UUID) {
		t.Helper()
		if resp := s.orch.NoteOnTask(ctx, writer, own.ID, daemon.RequestNote{Target: id.String(), Content: "go", Answer: true}); !resp.OK || resp.Woken {
			t.Fatalf("held: %+v", resp)
		}
	}

	// A park on an approval.
	approval := s.runningTask(t, asker.ID, "Freigabe")
	answer(approval.ID)
	if got, err := s.backlog.Block(ctx, approval.ID, "approval:"+uuid.NewString(), "sess-a", "approve?"); err != nil || got.State != backlog.StateBlocked {
		t.Fatalf("a park on an approval waits for its decision: %s %v", got.State, err)
	}

	// A run that ended without parking (a kill, the budget stop), then the
	// next run parks on an answer: key.
	left := s.runningTask(t, asker.ID, "Rollout")
	answer(left.ID)
	if _, err := s.backlog.Reopen(ctx, left.ID, "kill"); err != nil {
		t.Fatal(err)
	}
	if again, err := s.backlog.ClaimNext(ctx, asker.ID); err != nil || again.ID != left.ID {
		t.Fatalf("claim: %v %v", again.ID, err)
	}
	got, err := s.backlog.Block(ctx, left.ID, "answer:next", "sess-b", "Next question?")
	if err != nil || got.State != backlog.StateBlocked {
		t.Fatalf("an answer held for an earlier run does not end a later park: %s %v", got.State, err)
	}
	if n := s.heldAnswers(t, left.ID); n != 0 {
		t.Fatalf("the earlier run's answer goes at the park: %d held", n)
	}
}

// TestAnAgentsAnswerAnswersTheQuestionInTheChat: a park's question goes to
// people (DueQuestions, here the supervisor's direct conversation). A
// colleague's answer resumes the task, and it has to stand under the
// question there as well — otherwise the people read an open question that
// is no longer open, and whoever answers it later answers whatever the task
// waits for by then (#620). A question an agent answered before it went out
// does not go out at all.
func TestAnAgentsAnswerAnswersTheQuestionInTheChat(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	asker := s.newSupportAgent("kollegin")
	s.ohneLaeufe(asker.ID)
	if _, err := s.pool.Exec(ctx, `UPDATE agents SET supervisor_id=$2 WHERE id=$1`, asker.ID, s.adminID); err != nil {
		t.Fatal(err)
	}
	writer := s.newSupportAgent("schreiber")
	own, err := s.backlog.Create(ctx, s.orgID, writer.ID, "eigene", "", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	answer := func(id uuid.UUID, text string) daemon.InjectNote {
		t.Helper()
		resp := s.orch.NoteOnTask(ctx, writer, own.ID, daemon.RequestNote{Target: id.String(), Content: text, Answer: true})
		if !resp.OK {
			t.Fatalf("answer: %+v", resp)
		}
		return resp
	}
	questions := func(id uuid.UUID) map[string]bool {
		t.Helper()
		rows, err := s.pool.Query(ctx, `SELECT text FROM conversation_messages WHERE task_id = $1 AND kind = 'question'`, id)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var q string
			if err := rows.Scan(&q); err != nil {
				t.Fatal(err)
			}
			out[q] = true
		}
		return out
	}

	// The question went out, then the colleague answers.
	task := s.runningTask(t, asker.ID, "Rollout")
	if _, err := s.backlog.Block(ctx, task.ID, "answer:rollout", "sess-1", "Go or hold?"); err != nil {
		t.Fatal(err)
	}
	s.srv.Nacherzaehlen(ctx)
	var questionID, convID uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT id, conversation_id FROM conversation_messages
		WHERE task_id = $1 AND kind = 'question'`, task.ID).Scan(&questionID, &convID); err != nil {
		t.Fatalf("the question has to reach the supervisor first: %v", err)
	}
	if resp := answer(task.ID, "go"); !resp.Woken {
		t.Fatalf("the parked task wakes: %+v", resp)
	}
	var (
		authorKind string
		authorID   uuid.UUID
		text       string
		answerTo   string
		underQ     string
	)
	if err := s.pool.QueryRow(ctx, `SELECT author_kind, author_id, text, meta->>'answer_to', meta->>'question'
		FROM conversation_messages WHERE conversation_id = $1 AND kind = 'text' AND task_id = $2`, convID, task.ID).
		Scan(&authorKind, &authorID, &text, &answerTo, &underQ); err != nil {
		t.Fatalf("the answer has to stand in the question's conversation: %v", err)
	}
	want := "Answer from agent schreiber (task " + own.ID.String() + "):\ngo"
	if authorKind != "agent" || authorID != asker.ID || text != want || answerTo != task.ID.String() || underQ != questionID.String() {
		t.Fatalf("the answer stands under the question, written by the task's agent, naming the colleague:\n %s %s %q %s %s",
			authorKind, authorID, text, answerTo, underQ)
	}

	// Answered before the question went out: it does not go out.
	if again, err := s.backlog.ClaimNext(ctx, asker.ID); err != nil || again.ID != task.ID {
		t.Fatalf("claim: %v %v", again.ID, err)
	}
	if _, err := s.backlog.Block(ctx, task.ID, "answer:second", "sess-1", "And the second?"); err != nil {
		t.Fatal(err)
	}
	if resp := answer(task.ID, "yes"); !resp.Woken {
		t.Fatalf("the parked task wakes: %+v", resp)
	}
	// Held for the run and taken at the park: the same.
	if again, err := s.backlog.ClaimNext(ctx, asker.ID); err != nil || again.ID != task.ID {
		t.Fatalf("claim: %v %v", again.ID, err)
	}
	answer(task.ID, "also yes")
	if got, err := s.backlog.Block(ctx, task.ID, "answer:third", "sess-1", "And the third?"); err != nil || got.State != backlog.StateOpen {
		t.Fatalf("the park takes the held answer: %s %v", got.State, err)
	}
	s.srv.Nacherzaehlen(ctx)
	if q := questions(task.ID); len(q) != 1 || !q["Go or hold?"] {
		t.Fatalf("only the question that was open when it went out is in the conversation: %v", q)
	}

	// A person's answer is not an agent's: the question of a park a person
	// answered in the task view still goes out (antwortZustellen puts the
	// answer beside it).
	if again, err := s.backlog.ClaimNext(ctx, asker.ID); err != nil || again.ID != task.ID {
		t.Fatalf("claim: %v %v", again.ID, err)
	}
	if _, err := s.backlog.Block(ctx, task.ID, "answer:fourth", "sess-1", "And the fourth?"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.backlog.Answer(ctx, task.ID, "admin@test.local", "no"); err != nil {
		t.Fatal(err)
	}
	s.srv.Nacherzaehlen(ctx)
	if q := questions(task.ID); !q["And the fourth?"] {
		t.Fatalf("a person's answer leaves the delivery as it was: %v", q)
	}
}
