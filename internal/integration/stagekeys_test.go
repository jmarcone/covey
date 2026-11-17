package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/backlog"
	"covey/internal/db"
	"covey/migrations"
)

// The three default columns are found by key (open, in_progress, done), not by
// name: the name follows the reader's language until a human renames the
// column, and neither may stop auto-follow or let set_stage open a second
// column of the same meaning.

// stageByKey returns the agent's keyed column.
func stageByKey(t *testing.T, s *stack, agentID uuid.UUID, key string) backlog.Stage {
	t.Helper()
	stages, err := s.backlog.ListStages(context.Background(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range stages {
		if st.Key != nil && *st.Key == key {
			return st
		}
	}
	t.Fatalf("no column with key %s, board: %+v", key, stages)
	return backlog.Stage{}
}

func stageOf(t *testing.T, s *stack, taskID uuid.UUID) uuid.UUID {
	t.Helper()
	tk, err := s.backlog.Get(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if tk.StageID == nil {
		return uuid.Nil
	}
	return *tk.StageID
}

// A fresh board: three keyed, unnamed columns. The API names them in English
// for clients that do not know the key.
func TestFreshBoardIsKeyed(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("frisch")
	if err := s.backlog.SeedDefaultStages(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	admin := login(t, s, "admin@test.local", "admin-passwort")
	resp := admin.do(http.MethodGet, "/api/v1/agents/"+agent.ID.String()+"/stages", nil)
	defer resp.Body.Close()
	var stages []backlog.Stage
	if err := json.NewDecoder(resp.Body).Decode(&stages); err != nil {
		t.Fatal(err)
	}
	want := []struct{ key, name string }{
		{backlog.StageKeyOpen, "Backlog"}, {backlog.StageKeyInProgress, "In progress"}, {backlog.StageKeyDone, "Done"},
	}
	if len(stages) != len(want) {
		t.Fatalf("board: %+v", stages)
	}
	for i, w := range want {
		if stages[i].Key == nil || *stages[i].Key != w.key || stages[i].Name != w.name || stages[i].Renamed {
			t.Errorf("column %d = %+v, want key %s, name %q, not renamed", i, stages[i], w.key, w.name)
		}
	}
}

// A human renames the done column to "Shipped": it keeps its key, the API says
// renamed, and a task that finishes afterwards still lands there. Sending a
// label back (what the board does when it only changes the color) is no rename.
func TestRenamedDoneColumnKeepsAutoFollow(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("umbenannt")
	if err := s.backlog.SeedDefaultStages(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	admin := login(t, s, "admin@test.local", "admin-passwort")
	done := stageByKey(t, s, agent.ID, backlog.StageKeyDone)
	got := admin.expect(http.MethodPatch, "/api/v1/stages/"+done.ID.String(),
		map[string]any{"name": "Shipped", "color": done.Color, "position": done.Position}, http.StatusOK)
	if got["name"] != "Shipped" || got["key"] != backlog.StageKeyDone || got["renamed"] != true {
		t.Fatalf("after the rename: %v", got)
	}

	open := stageByKey(t, s, agent.ID, backlog.StageKeyOpen)
	got = admin.expect(http.MethodPatch, "/api/v1/stages/"+open.ID.String(),
		map[string]any{"name": open.Name, "color": "var(--text-danger)", "position": open.Position}, http.StatusOK)
	if got["renamed"] != false || got["color"] != "var(--text-danger)" {
		t.Fatalf("a color change must not rename the column: %v", got)
	}
	inProgress := stageByKey(t, s, agent.ID, backlog.StageKeyInProgress)
	got = admin.expect(http.MethodPatch, "/api/v1/stages/"+inProgress.ID.String(),
		map[string]any{"name": "In Arbeit", "color": inProgress.Color, "position": inProgress.Position}, http.StatusOK)
	if got["renamed"] != false || got["name"] != "In progress" {
		t.Fatalf("the column's own label in another language is no rename: %v", got)
	}

	task, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Läuft durch", "[mock:result ok]", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task done", 15*time.Second, func() bool {
		return s.taskState(task.ID) == backlog.StateDone
	})
	if stageOf(t, s, task.ID) != done.ID {
		t.Fatalf("a finished task belongs in the renamed done column")
	}
	if renamed := stageByKey(t, s, agent.ID, backlog.StageKeyDone); renamed.Name != "Shipped" || !renamed.Renamed {
		t.Fatalf("done column after the run: %+v", renamed)
	}
}

// set_stage with a label of a default column — in any language, any case, or
// the key — lands in the keyed column; no second column of the same meaning.
func TestSetStageResolvesDefaultLabels(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("etiketten")
	if err := s.backlog.SeedDefaultStages(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	s.ohneLaeufe(agent.ID)
	task, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Wandert", "", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]string{
		"Done": backlog.StageKeyDone, "Erledigt": backlog.StageKeyDone, "done": backlog.StageKeyDone,
		"in progress": backlog.StageKeyInProgress, "In Arbeit": backlog.StageKeyInProgress,
		"Backlog": backlog.StageKeyOpen, "en cours": backlog.StageKeyInProgress,
	} {
		st, err := s.backlog.SetTaskStageByName(ctx, agent.ID, task.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		if st.Key == nil || *st.Key != key {
			t.Errorf("set_stage %q landed in %+v, want key %s", name, st, key)
		}
		if stageOf(t, s, task.ID) != st.ID {
			t.Errorf("set_stage %q did not move the task", name)
		}
	}
	// A custom name still makes a column of its own.
	if st, err := s.backlog.SetTaskStageByName(ctx, agent.ID, task.ID, "Research"); err != nil || st.Key != nil || st.CreatedBy != "agent" {
		t.Fatalf("Research: %+v, %v", st, err)
	}
	stages, _ := s.backlog.ListStages(ctx, agent.ID)
	if len(stages) != 4 {
		t.Fatalf("expected the three default columns plus Research, board: %+v", stages)
	}
}

// The same through a real run: the agent calls set_stage "Done" and finishes.
func TestSetStageDoneInARun(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("lauf")
	if err := s.backlog.SeedDefaultStages(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	task, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Ganz durch",
		`[mock:action covey/set_stage {"stage":"Done"}]
[mock:result done]`, "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task done", 20*time.Second, func() bool {
		return s.taskState(task.ID) == backlog.StateDone
	})
	stages, _ := s.backlog.ListStages(ctx, agent.ID)
	if len(stages) != 3 {
		t.Fatalf("set_stage Done must not add a column, board: %+v", stages)
	}
	if stageOf(t, s, task.ID) != stageByKey(t, s, agent.ID, backlog.StageKeyDone).ID {
		t.Fatalf("the task belongs in the done column")
	}
}

// TestStageKeyMigration takes a board from before 0142 through the migration
// and back:
//
//   - agent A: Backlog / In Arbeit / Erledigt, a custom "Research", and the
//     look-alikes an agent made via set_stage — "Done" and "in progress", with
//     tasks. The defaults get their keys, the look-alikes merge into them
//     without losing a task, Research stays as it was.
//   - agent B: the same defaults plus a "Done" a human made. It stays, with its
//     task: the migration never deletes a human column.
//
// After it, auto-follow runs on the keys. The rollback gives the defaults
// their German names back and the old name-based auto-follow finds them.
func TestStageKeyMigration(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	a := s.newSupportAgent("vorher-a")
	b := s.newSupportAgent("vorher-b")
	s.ohneLaeufe(a.ID)
	s.ohneLaeufe(b.ID)

	if err := db.MigrateDown(ctx, s.pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var top int
	if err := s.pool.QueryRow(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&top); err != nil || top != 141 {
		t.Fatalf("expected to stand at 0141 after the rollback, at %d (%v)", top, err)
	}

	// The board as 0004/SeedDefaultStages and set_stage left it.
	stage := func(agentID uuid.UUID, name string, pos int, color, by string) uuid.UUID {
		id := uuid.New()
		exec(`INSERT INTO agent_stages (id, agent_id, name, position, color, created_by) VALUES ($1,$2,$3,$4,$5,$6)`,
			id, agentID, name, pos, color, by)
		return id
	}
	task := func(agentID, stageID uuid.UUID, title string) uuid.UUID {
		tk, err := s.backlog.Create(ctx, s.orgID, agentID, title, "", "manual", 5)
		if err != nil {
			t.Fatal(err)
		}
		exec("UPDATE backlog_tasks SET stage_id=$2 WHERE id=$1", tk.ID, stageID)
		return tk.ID
	}
	aOpen := stage(a.ID, "Backlog", 0, "var(--text-muted)", "human")
	aWork := stage(a.ID, "In Arbeit", 1, "var(--text-accent)", "human")
	aDone := stage(a.ID, "Erledigt", 2, "var(--text-success)", "human")
	aResearch := stage(a.ID, "Research", 3, "var(--text-pro)", "agent")
	aDoneAgent := stage(a.ID, "Done", 4, "", "agent")
	aWorkAgent := stage(a.ID, "in progress", 5, "", "agent")
	tOpen := task(a.ID, aOpen, "liegt im Backlog")
	tDone := task(a.ID, aDone, "liegt in Erledigt")
	tResearch := task(a.ID, aResearch, "liegt in Research")
	tDoneAgent1 := task(a.ID, aDoneAgent, "liegt in Done 1")
	tDoneAgent2 := task(a.ID, aDoneAgent, "liegt in Done 2")
	tWorkAgent := task(a.ID, aWorkAgent, "liegt in in progress")

	stage(b.ID, "Backlog", 0, "var(--text-muted)", "human")
	stage(b.ID, "In Arbeit", 1, "var(--text-accent)", "human")
	bDone := stage(b.ID, "Erledigt", 2, "var(--text-success)", "human")
	bDoneHuman := stage(b.ID, "Done", 3, "var(--text-warning)", "human")
	tHuman := task(b.ID, bDoneHuman, "liegt im menschlichen Done")

	if n, err := db.MigrateUp(ctx, s.pool, migrations.FS); err != nil || n != 1 {
		t.Fatalf("migrate up: %d applied, %v", n, err)
	}

	// Agent A: keys on the three defaults, unnamed; Research untouched; the
	// look-alikes gone, their tasks in the keyed columns.
	for id, key := range map[uuid.UUID]string{aOpen: "open", aWork: "in_progress", aDone: "done"} {
		var gotKey string
		var name *string
		if err := s.pool.QueryRow(ctx, "SELECT key, name FROM agent_stages WHERE id=$1", id).Scan(&gotKey, &name); err != nil {
			t.Fatal(err)
		}
		if gotKey != key || name != nil {
			t.Errorf("default column %s: key %q, name %v — want key %s, unnamed", id, gotKey, name, key)
		}
	}
	var rName, rColor, rBy string
	var rPos int
	var rKey *string
	if err := s.pool.QueryRow(ctx, "SELECT name, position, color, created_by, key FROM agent_stages WHERE id=$1", aResearch).
		Scan(&rName, &rPos, &rColor, &rBy, &rKey); err != nil {
		t.Fatal(err)
	}
	if rName != "Research" || rPos != 3 || rColor != "var(--text-pro)" || rBy != "agent" || rKey != nil {
		t.Errorf("Research changed: %s %d %s %s %v", rName, rPos, rColor, rBy, rKey)
	}
	var left int
	s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM agent_stages WHERE id = ANY($1)", []uuid.UUID{aDoneAgent, aWorkAgent}).Scan(&left)
	if left != 0 {
		t.Errorf("the agent-made look-alikes must be merged away, %d left", left)
	}
	for tk, want := range map[uuid.UUID]uuid.UUID{
		tOpen: aOpen, tDone: aDone, tResearch: aResearch,
		tDoneAgent1: aDone, tDoneAgent2: aDone, tWorkAgent: aWork,
		tHuman: bDoneHuman,
	} {
		if got := stageOf(t, s, tk); got != want {
			t.Errorf("task %s sits in %s, want %s", tk, got, want)
		}
	}
	if st := stageByKey(t, s, a.ID, backlog.StageKeyDone); st.Name != "Done" || st.Renamed {
		t.Errorf("the API names an unnamed done column in English: %+v", st)
	}

	// Agent B: the human "Done" stays beside the keyed done column.
	var bName, bBy string
	if err := s.pool.QueryRow(ctx, "SELECT name, created_by FROM agent_stages WHERE id=$1", bDoneHuman).Scan(&bName, &bBy); err != nil {
		t.Fatalf("the human Done column must survive: %v", err)
	}
	if bName != "Done" || bBy != "human" {
		t.Errorf("human Done changed: %s %s", bName, bBy)
	}
	if stageByKey(t, s, b.ID, backlog.StageKeyDone).ID != bDone {
		t.Errorf("Erledigt must be B's keyed done column")
	}

	// Auto-follow on the keys: dispatch moves into in_progress, completing
	// and failing into done.
	follow, err := s.backlog.Create(ctx, s.orgID, a.ID, "folgt", "", "manual", 1)
	if err != nil {
		t.Fatal(err)
	}
	if stageOf(t, s, follow.ID) != aOpen {
		t.Errorf("a new task lands in the open column")
	}
	claimed, err := s.backlog.ClaimNext(ctx, a.ID)
	if err != nil || claimed.ID != follow.ID {
		t.Fatalf("claim: %v %v", claimed.ID, err)
	}
	if stageOf(t, s, follow.ID) != aWork {
		t.Errorf("dispatch moves the task into in_progress")
	}
	if _, err := s.backlog.Complete(ctx, follow.ID, backlog.StateDone, "ok", ""); err != nil {
		t.Fatal(err)
	}
	if stageOf(t, s, follow.ID) != aDone {
		t.Errorf("completion moves the task into done")
	}
	failing, _ := s.backlog.Create(ctx, s.orgID, a.ID, "scheitert", "", "manual", 1)
	if _, err := s.backlog.ClaimNext(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.backlog.Complete(ctx, failing.ID, backlog.StateFailed, "", "kaputt"); err != nil {
		t.Fatal(err)
	}
	if stageOf(t, s, failing.ID) != aDone {
		t.Errorf("failing moves the task into done")
	}

	// Since 0142 a human may create "Erledigt" beside the unnamed done column;
	// on the rollback it steps aside for the keyed one.
	bErledigt, err := s.backlog.CreateStage(ctx, b.ID, "Erledigt", "")
	if err != nil {
		t.Fatal(err)
	}

	// Rollback: German names back, the old name-based auto-follow works.
	if err := db.MigrateDown(ctx, s.pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[uuid.UUID]string{
		aOpen: "Backlog", aWork: "In Arbeit", aDone: "Erledigt", aResearch: "Research", bDone: "Erledigt", bDoneHuman: "Done",
	} {
		var name string
		if err := s.pool.QueryRow(ctx, "SELECT name FROM agent_stages WHERE id=$1", id).Scan(&name); err != nil || name != want {
			t.Errorf("after the rollback column %s is %q (%v), want %q", id, name, err, want)
		}
	}
	var stepped string
	s.pool.QueryRow(ctx, "SELECT name FROM agent_stages WHERE id=$1", bErledigt.ID).Scan(&stepped)
	if stepped != "Erledigt ("+bErledigt.ID.String()[:8]+")" {
		t.Errorf("the human Erledigt must step aside, it is called %q", stepped)
	}
	// The name-based auto-follow of the code before 0142, verbatim.
	back, _ := s.backlog.Create(ctx, s.orgID, a.ID, "nach dem Rollback", "", "manual", 1)
	exec("UPDATE backlog_tasks SET stage_id=$2 WHERE id=$1", back.ID, aWork)
	exec(`UPDATE backlog_tasks t SET stage_id = s.id
		FROM agent_stages s
		WHERE t.id=$1 AND s.agent_id=t.agent_id AND s.name=$2
		  AND (t.stage_id IS NULL OR t.stage_id IN (
		      SELECT d.id FROM agent_stages d
		      WHERE d.agent_id=t.agent_id
		        AND (d.name = ANY($3) OR ($4 AND d.created_by='agent'))))`,
		back.ID, "Erledigt", []string{"Backlog", "In Arbeit", "Erledigt"}, true)
	if stageOf(t, s, back.ID) != aDone {
		t.Errorf("after the rollback the old auto-follow must find Erledigt")
	}

	// Leave the database as the stack expects it.
	if _, err := db.MigrateUp(ctx, s.pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
}
