package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/backlog"
	"covey/internal/daemon"
)

// parkedTask runs a task of the agent until it parks on a question, and
// returns it blocked, with the session its resume continues.
func (s *stack) parkedTask(t *testing.T, agentID uuid.UUID, title string) backlog.Task {
	t.Helper()
	ctx := context.Background()
	task, err := s.backlog.Create(ctx, s.orgID, agentID, title,
		"[mock:block key=frage:"+title+" question=Go or hold?]", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	s.orch.EnsureRunning(agentID)
	waitFor(t, "the task parks on its question", 15*time.Second, func() bool {
		return s.taskState(task.ID) == backlog.StateBlocked
	})
	task = mustTask(t, s, task.ID.String())
	if task.RuntimeSessionID == nil || *task.RuntimeSessionID == "" {
		t.Fatal("a parked task keeps its session")
	}
	return task
}

func (s *stack) taskCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM backlog_tasks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestAddNoteAnswersAColleaguesParkedTask: covey/add_note with the task_id of
// a colleague's parked task and "answer": true resumes that task in its own
// session with the note's text — through the action proxy, as an agent calls
// it — and no task is created beside it. The note carries the writer's name.
func TestAddNoteAnswersAColleaguesParkedTask(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	colleague := s.newSupportAgent("kollegin")
	parked := s.parkedTask(t, colleague.ID, "Rollout")
	session := *parked.RuntimeSessionID

	writer := s.newSupportAgent("schreiber")
	before := s.taskCount(t)
	if _, err := s.backlog.Create(ctx, s.orgID, writer.ID, "Freigabe weitergeben",
		fmt.Sprintf(`[mock:action covey/add_note {"task_id":"%s","content":"go: the rollout may start","answer":true}]`, parked.ID),
		"manual", 3); err != nil {
		t.Fatal(err)
	}
	s.orch.EnsureRunning(writer.ID)

	waitForOr(t, "the parked task resumes and finishes", 15*time.Second, func() bool {
		return s.taskState(parked.ID) == backlog.StateDone
	}, func() string { return s.recordingSummary(writer.ID) })
	done := mustTask(t, s, parked.ID.String())
	if done.RuntimeSessionID == nil || *done.RuntimeSessionID != session {
		t.Fatalf("the resume has to continue session %s: %v", session, done.RuntimeSessionID)
	}
	if done.Result == nil || *done.Result != "Resumed and completed: go: the rollout may start" {
		t.Fatalf("the run has to be resumed with the note's text: %v", done.Result)
	}
	if got := s.taskCount(t); got != before+1 {
		t.Fatalf("only the writer's own task may be new: %d tasks, expected %d", got, before+1)
	}

	admin := login(t, s, "admin@test.local", "admin-passwort")
	notes := admin.expectList(http.MethodGet, "/api/v1/tasks/"+parked.ID.String()+"/notes", nil, http.StatusOK)
	var author string
	for _, n := range notes {
		if n["content"] == "go: the rollout may start" {
			author, _ = n["author"].(string)
		}
	}
	if author != "agent:schreiber" {
		t.Fatalf("the note has to name its writer: %q in %v", author, notes)
	}
}

// TestAddNoteWithoutAnswerLeavesTheTaskParked: the same note without
// "answer" is a note and nothing more — and the agent is told nobody woke.
func TestAddNoteWithoutAnswerLeavesTheTaskParked(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	colleague := s.newSupportAgent("kollegin")
	s.ohneLaeufe(colleague.ID)
	parked, err := s.backlog.Create(ctx, s.orgID, colleague.ID, "Rollout", "", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.backlog.ClaimNext(ctx, colleague.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.backlog.Block(ctx, parked.ID, "frage:rollout", "sess-1", "Go or hold?"); err != nil {
		t.Fatal(err)
	}
	writer := s.newSupportAgent("schreiber")
	own, err := s.backlog.Create(ctx, s.orgID, writer.ID, "eigene Aufgabe", "", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}

	resp := s.orch.NoteOnTask(ctx, writer, own.ID, daemon.RequestNote{Target: parked.ID.String(), Content: "fyi: the customer called"})
	if !resp.OK || resp.Woken {
		t.Fatalf("a note without answer is written and wakes nothing: %+v", resp)
	}
	if st := s.taskState(parked.ID); st != backlog.StateBlocked {
		t.Fatalf("the task has to stay parked: %s", st)
	}

	// answer on a task that is not parked: the note stands, woken stays false.
	open, err := s.backlog.Create(ctx, s.orgID, colleague.ID, "offen", "", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	resp = s.orch.NoteOnTask(ctx, writer, own.ID, daemon.RequestNote{Target: open.ID.String(), Content: "hold", Answer: true})
	if !resp.OK || resp.Woken {
		t.Fatalf("an open task takes the note and is not woken: %+v", resp)
	}
	notes, err := s.backlog.ListNotes(ctx, open.ID)
	if err != nil || len(notes) != 1 || notes[0].Author != "agent:schreiber" {
		t.Fatalf("the note has to stand on the open task: %+v %v", notes, err)
	}
}

// TestAddNoteStaysInTheOrganisation: a task_id of another organisation is
// refused — through the action proxy with the "denied" a guard rail gives,
// and at the control plane directly — and nothing is written onto it.
func TestAddNoteStaysInTheOrganisation(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	fremdeOrg, fremderAgent := nachbar(t, s)
	fremd, err := s.backlog.Create(ctx, fremdeOrg, fremderAgent.ID, "fremde Aufgabe", "", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}

	writer := s.newSupportAgent("schreiber")
	own, err := s.backlog.Create(ctx, s.orgID, writer.ID, "Notiz woanders",
		fmt.Sprintf(`[mock:action covey/add_note {"task_id":"%s","content":"from outside","answer":true}]`, fremd.ID),
		"manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	s.orch.EnsureRunning(writer.ID)
	waitForOr(t, "the writer's run ends", 15*time.Second, func() bool {
		return s.taskState(own.ID) == backlog.StateDone
	}, func() string { return s.recordingSummary(writer.ID) })
	var outcome string
	if err := s.pool.QueryRow(ctx, `SELECT coalesce(payload->>'text', '') FROM recording_events
		WHERE agent_id=$1 AND payload->>'text' LIKE 'Action covey/add_note%'`, writer.ID).Scan(&outcome); err != nil {
		t.Fatalf("the action's outcome is not in the recording: %v — %s", err, s.recordingSummary(writer.ID))
	}
	if outcome != "Action covey/add_note → denied" {
		t.Fatalf("the proxy has to answer denied: %q", outcome)
	}

	resp := s.orch.NoteOnTask(ctx, writer, own.ID, daemon.RequestNote{Target: fremd.ID.String(), Content: "from outside"})
	if resp.OK || !resp.Denied || !strings.Contains(resp.Error, "in this organisation") {
		t.Fatalf("a task of another organisation has to be refused: %+v", resp)
	}
	// An id that names no task at all reads the same — the refusal tells
	// nothing about ids outside.
	resp = s.orch.NoteOnTask(ctx, writer, own.ID, daemon.RequestNote{Target: uuid.NewString(), Content: "x"})
	if !resp.Denied || !strings.Contains(resp.Error, "in this organisation") {
		t.Fatalf("an unknown id has to read like a foreign one: %+v", resp)
	}

	notes, err := s.backlog.ListNotes(ctx, fremd.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("nothing may be written onto another organisation's task: %+v", notes)
	}
}

// TestAddNoteOnAnotherTaskAsksTheGuardRails: a deny on covey:add_note holds
// at the control plane, without the daemon's check.
func TestAddNoteOnAnotherTaskAsksTheGuardRails(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	colleague := s.newSupportAgent("kollegin")
	s.ohneLaeufe(colleague.ID)
	target, err := s.backlog.Create(ctx, s.orgID, colleague.ID, "Rollout", "", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	writer := s.newSupportAgent("schreiber")
	own, err := s.backlog.Create(ctx, s.orgID, writer.ID, "eigene", "", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.rails.Create(ctx, railRule(s.orgID, "deny_action", "covey:add_note")); err != nil {
		t.Fatal(err)
	}
	resp := s.orch.NoteOnTask(ctx, writer, own.ID, daemon.RequestNote{Target: target.ID.String(), Content: "go"})
	if resp.OK || !resp.Denied || !strings.Contains(resp.Error, "guard rail") {
		t.Fatalf("a deny_action rule has to hold: %+v", resp)
	}
	if notes, _ := s.backlog.ListNotes(ctx, target.ID); len(notes) != 0 {
		t.Fatalf("a refused note is not written: %+v", notes)
	}
}
