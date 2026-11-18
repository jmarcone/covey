package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/backlog"
)

// TestSetStageMovesOnlyTheCallersOwnTasks pins the reach of covey/set_stage
// with a task_id (#66): a board column belongs to its agent, so the action
// moves the calling agent's own tasks and nothing else. A colleague's task and
// a task of another organisation stay where their owners put them, and the
// refused call leaves no column behind on the caller's board either — exactly
// like an id that does not exist.
func TestSetStageMovesOnlyTheCallersOwnTasks(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()

	caller := s.newSupportAgent("stage-caller")
	colleague := s.newSupportAgent("stage-colleague")
	s.ohneLaeufe(colleague.ID)
	if err := s.backlog.SeedDefaultStages(ctx, colleague.ID); err != nil {
		t.Fatal(err)
	}
	theirs, err := s.backlog.Create(ctx, s.orgID, colleague.ID, "The colleague's", "", "manual", 5)
	if err != nil {
		t.Fatal(err)
	}
	theirStage, err := s.backlog.SetTaskStageByName(ctx, colleague.ID, theirs.ID, "Waiting")
	if err != nil {
		t.Fatal(err)
	}

	otherOrg := uuid.New()
	if _, err := s.pool.Exec(ctx, `INSERT INTO organizations (id, name) VALUES ($1,'Other Ltd')`, otherOrg); err != nil {
		t.Fatal(err)
	}
	foreign, err := s.registry.Create(ctx, otherOrg, "foreign", "Foreign", "mock", nil)
	if err != nil {
		t.Fatal(err)
	}
	foreignTask, err := s.backlog.Create(ctx, otherOrg, foreign.ID, "Foreign", "", "manual", 5)
	if err != nil {
		t.Fatal(err)
	}
	foreignStage, err := s.backlog.SetTaskStageByName(ctx, foreign.ID, foreignTask.ID, "Waiting")
	if err != nil {
		t.Fatal(err)
	}

	// An own task that is not the running one: cancelled, so no run picks it
	// up and the terminal transition of the running task does not touch it.
	earlier, err := s.backlog.Create(ctx, s.orgID, caller.ID, "Earlier", "", "manual", 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.backlog.Cancel(ctx, earlier.ID, "test"); err != nil {
		t.Fatal(err)
	}

	run, err := s.backlog.Create(ctx, s.orgID, caller.ID, "Move cards",
		`[mock:action covey/set_stage {"stage":"Taken","task_id":"`+theirs.ID.String()+`"}]
[mock:action covey/set_stage {"stage":"Taken abroad","task_id":"`+foreignTask.ID.String()+`"}]
[mock:action covey/set_stage {"stage":"Nowhere","task_id":"`+uuid.NewString()+`"}]
[mock:action covey/set_stage {"stage":"Mine","task_id":"`+earlier.ID.String()+`"}]
[mock:result moved]`, "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run is done", 20*time.Second, func() bool {
		return s.taskState(run.ID) == backlog.StateDone
	})

	for _, c := range []struct {
		name  string
		id    uuid.UUID
		stage uuid.UUID
	}{
		{"the colleague's task", theirs.ID, theirStage.ID},
		{"the other organisation's task", foreignTask.ID, foreignStage.ID},
	} {
		got, err := s.backlog.Get(ctx, c.id)
		if err != nil {
			t.Fatal(err)
		}
		if got.StageID == nil || *got.StageID != c.stage {
			t.Errorf("%s has to stay in its owner's column, stage_id=%v", c.name, got.StageID)
		}
	}

	stages, err := s.backlog.ListStages(ctx, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	var mine *backlog.Stage
	for i, st := range stages {
		switch st.Name {
		case "Taken", "Taken abroad", "Nowhere":
			t.Errorf("a refused set_stage must not leave a column on the caller's board: %q", st.Name)
		case "Mine":
			mine = &stages[i]
		}
	}
	if mine == nil {
		t.Fatalf("the caller's own task moves into the named column, board: %+v", stages)
	}
	got, err := s.backlog.Get(ctx, earlier.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.StageID == nil || *got.StageID != mine.ID {
		t.Fatalf("the caller's own task has to sit in 'Mine', stage_id=%v", got.StageID)
	}
}

// TestSetTaskStageByNameRefusesAnotherAgentsTask checks the rule at the store:
// another agent's task is not found for the caller, and the refusal comes
// before the column is ensured.
func TestSetTaskStageByNameRefusesAnotherAgentsTask(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	caller := s.newSupportAgent("store-caller")
	owner := s.newSupportAgent("store-owner")
	s.ohneLaeufe(caller.ID)
	s.ohneLaeufe(owner.ID)
	theirs, err := s.backlog.Create(ctx, s.orgID, owner.ID, "Theirs", "", "manual", 5)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []uuid.UUID{theirs.ID, uuid.New()} {
		if _, err := s.backlog.SetTaskStageByName(ctx, caller.ID, id, "Taken"); !errors.Is(err, backlog.ErrNotFound) {
			t.Fatalf("a task that is not the caller's reads as not found, got %v", err)
		}
	}
	got, err := s.backlog.Get(ctx, theirs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.StageID != nil {
		t.Fatalf("the owner's task must not move, stage_id=%v", got.StageID)
	}
	stages, err := s.backlog.ListStages(ctx, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 0 {
		t.Fatalf("a refused move must not create a column, board: %+v", stages)
	}
}
