package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/agents"
	"covey/internal/backlog"
	"covey/internal/db"
	"covey/migrations"
)

// The three default columns are found by key (open, in_progress, done), not by
// name: the board shows them in the reader's language until a human renames
// them, and neither may stop auto-follow or let set_stage open a second column
// of the same meaning.

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

// A fresh board: three keyed columns under their old German names, recorded as
// 'seed' rather than 'human'. The names stay so that an instance of the code
// before the key, sharing the database during a rolling start, still reads a
// name and finds its default columns by it. The API says not renamed, so the
// interface shows the reader's language; the test reads the raw JSON the
// interface gets, not the Go struct.
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
	var stages []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&stages); err != nil {
		t.Fatal(err)
	}
	want := []struct{ key, name string }{
		{backlog.StageKeyOpen, "Backlog"}, {backlog.StageKeyInProgress, "In Arbeit"}, {backlog.StageKeyDone, "Erledigt"},
	}
	if len(stages) != len(want) {
		t.Fatalf("board: %v", stages)
	}
	for i, w := range want {
		if stages[i]["key"] != w.key || stages[i]["name"] != w.name || stages[i]["renamed"] != false ||
			stages[i]["created_by"] != "seed" {
			t.Errorf("column %d = %v, want key %s, name %q, renamed false, by seed", i, stages[i], w.key, w.name)
		}
	}
}

// A human renames the done column to "Shipped": it keeps its key, the API says
// renamed, and a task that finishes afterwards still lands there. Sending the
// name back (what the board does when it only changes the color) is no rename.
// A name is stored as it is typed, a label of the column's own in another
// language too; default_name gives the column its own name back.
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
	if got["renamed"] != false || got["name"] != "Backlog" || got["color"] != "var(--text-danger)" {
		t.Fatalf("a color change must not rename the column: %v", got)
	}

	inProgress := stageByKey(t, s, agent.ID, backlog.StageKeyInProgress)
	got = admin.expect(http.MethodPatch, "/api/v1/stages/"+inProgress.ID.String(),
		map[string]any{"name": "En cours", "color": inProgress.Color, "position": inProgress.Position}, http.StatusOK)
	if got["renamed"] != true || got["name"] != "En cours" {
		t.Fatalf("a typed name is stored as typed, a label of another language too: %v", got)
	}
	got = admin.expect(http.MethodPatch, "/api/v1/stages/"+inProgress.ID.String(),
		map[string]any{"default_name": true, "color": inProgress.Color, "position": inProgress.Position}, http.StatusOK)
	if got["renamed"] != false || got["name"] != "In Arbeit" {
		t.Fatalf("default_name gives the column its own name back: %v", got)
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

// A human cannot make a second column of a default column's meaning: "Done",
// "erledigt", "done" beside the done column are refused, on creating and on
// renaming, also while the done column is called "Shipped". The name a column
// had before is free again once it is renamed, as long as it means nothing.
func TestNoSecondColumnOfADefaultMeaning(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("doppelt")
	if err := s.backlog.SeedDefaultStages(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	s.ohneLaeufe(agent.ID)
	admin := login(t, s, "admin@test.local", "admin-passwort")
	stagesURL := "/api/v1/agents/" + agent.ID.String() + "/stages"
	for _, name := range []string{"Done", "erledigt", "done", "Terminé", "In progress", "Erledigt"} {
		admin.expect(http.MethodPost, stagesURL, map[string]string{"name": name}, http.StatusConflict)
	}
	research := admin.expect(http.MethodPost, stagesURL, map[string]string{"name": "Research"}, http.StatusCreated)
	for _, name := range []string{"Done", "Backlog", "Research2"} {
		want := http.StatusConflict
		if name == "Research2" {
			want = http.StatusOK
		}
		admin.expect(http.MethodPatch, "/api/v1/stages/"+research["id"].(string),
			map[string]any{"name": name, "position": 3}, want)
	}
	// The open column may not take the done column's label either.
	open := stageByKey(t, s, agent.ID, backlog.StageKeyOpen)
	admin.expect(http.MethodPatch, "/api/v1/stages/"+open.ID.String(),
		map[string]any{"name": "Done", "position": open.Position}, http.StatusConflict)

	done := stageByKey(t, s, agent.ID, backlog.StageKeyDone)
	admin.expect(http.MethodPatch, "/api/v1/stages/"+done.ID.String(),
		map[string]any{"name": "Shipped", "position": done.Position}, http.StatusOK)
	admin.expect(http.MethodPost, stagesURL, map[string]string{"name": "Done"}, http.StatusConflict)
	admin.expect(http.MethodPost, stagesURL, map[string]string{"name": "Erledigt"}, http.StatusConflict)
	admin.expect(http.MethodPost, stagesURL, map[string]string{"name": "Shipped"}, http.StatusConflict)

	stages, _ := s.backlog.ListStages(ctx, agent.ID)
	if len(stages) != 4 {
		t.Fatalf("expected the three default columns plus Research2, board: %+v", stages)
	}
	// A column without a key has no name to fall back to.
	admin.expect(http.MethodPatch, "/api/v1/stages/"+research["id"].(string),
		map[string]any{"default_name": true, "position": 3}, http.StatusBadRequest)
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

// A column of exactly the name set_stage gives wins over the key: on a board
// that has a human's "Done" beside the done column (made before 0148, which
// keeps such columns), set_stage "Done" lands in the human's column — the one
// the board shows under that name. Another spelling of it means the default
// column and lands in the keyed one.
func TestSetStagePrefersTheExactName(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("genau")
	if err := s.backlog.SeedDefaultStages(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	s.ohneLaeufe(agent.ID)
	// CreateStage refuses "Done" beside the done column now; a board from
	// before 0148 can still have one.
	human := uuid.New()
	if _, err := s.pool.Exec(ctx, `INSERT INTO agent_stages (id, agent_id, name, position, created_by)
		VALUES ($1, $2, 'Done', 3, 'human')`, human, agent.ID); err != nil {
		t.Fatal(err)
	}
	done := stageByKey(t, s, agent.ID, backlog.StageKeyDone)
	task, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Wohin", "", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]uuid.UUID{
		"Done": human, "done": done.ID, "DONE": done.ID, "Erledigt": done.ID,
	} {
		st, err := s.backlog.SetTaskStageByName(ctx, agent.ID, task.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		if st.ID != want || stageOf(t, s, task.ID) != want {
			t.Errorf("set_stage %q landed in %s (%s), want %s", name, st.ID, st.Name, want)
		}
	}
	stages, _ := s.backlog.ListStages(ctx, agent.ID)
	if len(stages) != 4 {
		t.Fatalf("set_stage must not add a column, board: %+v", stages)
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

// set_stage "Done" while the task still runs. On main the name did not match
// "Erledigt", so the call made a column "Done" of its own; the running task sat
// in it beside an empty "Erledigt" until the task ended — then the terminal
// transition moved it out and the cleanup deleted the empty column, so such a
// column never stays in the database to be found afterwards. Now the running
// task sits in the keyed done column and the board keeps its three columns,
// during the run and after it.
func TestSetStageDoneWhileInProgress(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("mittendrin")
	if err := s.backlog.SeedDefaultStages(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	s.ohneLaeufe(agent.ID)
	task, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Läuft noch", "", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.backlog.ClaimNext(ctx, agent.ID); err != nil || claimed.ID != task.ID {
		t.Fatalf("claim: %v %v", claimed.ID, err)
	}
	done := stageByKey(t, s, agent.ID, backlog.StageKeyDone)
	board := func(when string) {
		t.Helper()
		stages, _ := s.backlog.ListStages(ctx, agent.ID)
		if len(stages) != 3 {
			t.Fatalf("%s: set_stage Done must not add a column, board: %+v", when, stages)
		}
		if stageOf(t, s, task.ID) != done.ID {
			t.Fatalf("%s: the task belongs in the keyed done column", when)
		}
	}
	if _, err := s.backlog.SetTaskStageByName(ctx, agent.ID, task.ID, "Done"); err != nil {
		t.Fatal(err)
	}
	if s.taskState(task.ID) != backlog.StateInProgress {
		t.Fatalf("set_stage is display only, the task must still run")
	}
	board("while running")
	if _, err := s.backlog.Complete(ctx, task.ID, backlog.StateDone, "ok", ""); err != nil {
		t.Fatal(err)
	}
	board("after the run")
}

// TestStageKeyMigration takes boards from before 0148 through the migration and
// back:
//
//   - agent A: Backlog / In Arbeit / Erledigt, a custom "Research", and the
//     look-alikes an agent made via set_stage — "Done" and "in progress", with
//     tasks. The defaults get their keys, the look-alikes merge into them
//     without losing a task, Research stays as it was.
//   - agent B: the same defaults plus a "Done" a human made. It stays, with its
//     task: the migration never deletes a human column, and set_stage "Done"
//     still lands in it.
//   - agent C: "Backlog" and "In Arbeit" seeded with the agent; its seeded
//     "Erledigt" is gone, and a human made a column of that name a day later.
//     It carries the default name, so it took part in auto-follow before and
//     keeps it: it is C's done column, and the agent's "Done" merges into it.
//   - agents D and E: boards 0004 backfilled; on E a human renamed two of the
//     columns since, and those stay custom columns.
//   - agent F: only an "Erledigt" is left of 0004's backfill.
//   - agent G: an "Erledigt" an agent made via set_stage after its seeded one
//     was deleted. It is G's done column now, and stays the agent's.
//
// The names stay as they are, so the code from before the key still works on
// the migrated database. The rollback puts the merged look-alikes back with
// their tasks, and leaves created_by as it was.
func TestStageKeyMigration(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	ag := map[string]uuid.UUID{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		ag[n] = s.newSupportAgent("vorher-" + n).ID
		s.ohneLaeufe(ag[n])
	}
	a, b, c, f, g := ag["a"], ag["b"], ag["c"], ag["f"], ag["g"]

	// belowStageKeys rolls back through 0148 and whatever came after it, and
	// no further: whatever main has below it stays.
	version := func() int {
		t.Helper()
		var v int
		if err := s.pool.QueryRow(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	belowStageKeys := func() {
		t.Helper()
		for version() >= 148 {
			if err := db.MigrateDown(ctx, s.pool, migrations.FS); err != nil {
				t.Fatal(err)
			}
		}
		if v := version(); v != 147 {
			t.Fatalf("expected to stand right below 0148 after the rollback, at %d", v)
		}
	}
	belowStageKeys()

	// The boards as 0004/SeedDefaultStages and set_stage left them.
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
	backfilled := func(agentID uuid.UUID) {
		exec(`UPDATE agent_stages s SET created_at = a.created_at + interval '1 day'
			FROM agents a WHERE a.id = s.agent_id AND s.agent_id = $1`, agentID)
	}
	aOpen := stage(a, "Backlog", 0, "var(--text-muted)", "human")
	aWork := stage(a, "In Arbeit", 1, "var(--text-accent)", "human")
	aDone := stage(a, "Erledigt", 2, "var(--text-success)", "human")
	aResearch := stage(a, "Research", 3, "var(--text-pro)", "agent")
	aDoneAgent := stage(a, "Done", 4, "var(--text-warning)", "agent")
	aWorkAgent := stage(a, "in progress", 5, "", "agent")
	tOpen := task(a, aOpen, "liegt im Backlog")
	tDone := task(a, aDone, "liegt in Erledigt")
	tResearch := task(a, aResearch, "liegt in Research")
	tDoneAgent1 := task(a, aDoneAgent, "liegt in Done 1")
	tDoneAgent2 := task(a, aDoneAgent, "liegt in Done 2")
	tWorkAgent := task(a, aWorkAgent, "liegt in in progress")

	stage(b, "Backlog", 0, "var(--text-muted)", "human")
	stage(b, "In Arbeit", 1, "var(--text-accent)", "human")
	bDone := stage(b, "Erledigt", 2, "var(--text-success)", "human")
	bDoneHuman := stage(b, "Done", 3, "var(--text-warning)", "human")
	tHuman := task(b, bDoneHuman, "liegt im menschlichen Done")

	cOpen := stage(c, "Backlog", 0, "var(--text-muted)", "human")
	cWork := stage(c, "In Arbeit", 1, "var(--text-accent)", "human")
	cLater := stage(c, "Erledigt", 2, "var(--text-warning)", "human")
	exec(`UPDATE agent_stages s SET created_at = a.created_at + interval '1 day'
		FROM agents a WHERE a.id = s.agent_id AND s.id = $1`, cLater)
	cDoneAgent := stage(c, "Done", 3, "", "agent")
	tLater := task(c, cLater, "liegt im späteren Erledigt")
	tDoneAgentC := task(c, cDoneAgent, "liegt in Cs Done")

	dOpen := stage(ag["d"], "Backlog", 0, "var(--text-muted)", "human")
	dWork := stage(ag["d"], "In Arbeit", 1, "var(--text-accent)", "human")
	dDone := stage(ag["d"], "Erledigt", 2, "var(--text-success)", "human")
	backfilled(ag["d"])
	eOpen := stage(ag["e"], "Backlog", 0, "var(--text-muted)", "human")
	eSprint := stage(ag["e"], "Sprint", 1, "var(--text-accent)", "human")
	eShipped := stage(ag["e"], "Shipped", 2, "var(--text-success)", "human")
	backfilled(ag["e"])

	fDone := stage(f, "Erledigt", 0, "var(--text-success)", "human")
	backfilled(f)

	gOpen := stage(g, "Backlog", 0, "var(--text-muted)", "human")
	gDone := stage(g, "Erledigt", 1, "", "agent")
	exec(`UPDATE agent_stages s SET created_at = a.created_at + interval '3 days'
		FROM agents a WHERE a.id = s.agent_id AND s.id = $1`, gDone)

	if n, err := db.MigrateUp(ctx, s.pool, migrations.FS); err != nil || n < 1 || version() < 148 {
		t.Fatalf("migrate up: %d applied, %v", n, err)
	}

	// Every column of a default name is keyed, with its name and its origin
	// as they were.
	type row struct {
		key  *string
		name string
		by   string
	}
	read := func(id uuid.UUID) (row, error) {
		var r row
		err := s.pool.QueryRow(ctx, "SELECT key, name, created_by FROM agent_stages WHERE id=$1", id).Scan(&r.key, &r.name, &r.by)
		return r, err
	}
	for id, want := range map[uuid.UUID][3]string{
		aOpen: {"open", "Backlog", "human"}, aWork: {"in_progress", "In Arbeit", "human"}, aDone: {"done", "Erledigt", "human"},
		bDone: {"done", "Erledigt", "human"},
		cOpen: {"open", "Backlog", "human"}, cWork: {"in_progress", "In Arbeit", "human"}, cLater: {"done", "Erledigt", "human"},
		dOpen: {"open", "Backlog", "human"}, dWork: {"in_progress", "In Arbeit", "human"}, dDone: {"done", "Erledigt", "human"},
		eOpen: {"open", "Backlog", "human"}, fDone: {"done", "Erledigt", "human"},
		gOpen: {"open", "Backlog", "human"}, gDone: {"done", "Erledigt", "agent"},
	} {
		r, err := read(id)
		if err != nil {
			t.Fatal(err)
		}
		if r.key == nil || *r.key != want[0] || r.name != want[1] || r.by != want[2] {
			t.Errorf("default column %s: key %v, name %q, by %s — want key %s, %q, by %s", id, r.key, r.name, r.by, want[0], want[1], want[2])
		}
	}
	// Research, B's human "Done" and E's renamed columns: untouched, no key.
	for id, want := range map[uuid.UUID][2]string{
		aResearch: {"Research", "agent"}, bDoneHuman: {"Done", "human"},
		eSprint: {"Sprint", "human"}, eShipped: {"Shipped", "human"},
	} {
		r, err := read(id)
		if err != nil {
			t.Fatalf("column %s must survive: %v", id, err)
		}
		if r.key != nil || r.name != want[0] || r.by != want[1] {
			t.Errorf("column %s: key %v, %q, by %s — want no key, %q, by %s", id, r.key, r.name, r.by, want[0], want[1])
		}
	}
	var left int
	s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM agent_stages WHERE id = ANY($1)", []uuid.UUID{aDoneAgent, aWorkAgent, cDoneAgent}).Scan(&left)
	if left != 0 {
		t.Errorf("the agent-made look-alikes must be merged away, %d left", left)
	}
	for tk, want := range map[uuid.UUID]uuid.UUID{
		tOpen: aOpen, tDone: aDone, tResearch: aResearch,
		tDoneAgent1: aDone, tDoneAgent2: aDone, tWorkAgent: aWork,
		tHuman: bDoneHuman, tLater: cLater, tDoneAgentC: cLater,
	} {
		if got := stageOf(t, s, tk); got != want {
			t.Errorf("task %s sits in %s, want %s", tk, got, want)
		}
	}
	if st := stageByKey(t, s, a, backlog.StageKeyDone); st.Name != "Erledigt" || st.Renamed {
		t.Errorf("an unrenamed done column keeps its name and says not renamed: %+v", st)
	}

	// The code before the key, sharing the database during a rolling start:
	// name is still NOT NULL, so it reads into a string; its ensureStage finds
	// "Erledigt" by name rather than making a second one; its name-based
	// auto-follow finds the column.
	var nullable string
	if err := s.pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns
		WHERE table_name='agent_stages' AND column_name='name'`).Scan(&nullable); err != nil || nullable != "NO" {
		t.Errorf("agent_stages.name must stay NOT NULL in this release: %q, %v", nullable, err)
	}
	var found uuid.UUID
	if err := s.pool.QueryRow(ctx, "SELECT id FROM agent_stages WHERE agent_id=$1 AND name=$2", a, "Erledigt").Scan(&found); err != nil || found != aDone {
		t.Errorf("the old ensureStage must find A's done column by name: %s, %v", found, err)
	}
	oldFollow := func(taskID uuid.UUID, target string) {
		t.Helper()
		exec(`UPDATE backlog_tasks t SET stage_id = s.id
			FROM agent_stages s
			WHERE t.id=$1 AND s.agent_id=t.agent_id AND s.name=$2
			  AND (t.stage_id IS NULL OR t.stage_id IN (
			      SELECT d.id FROM agent_stages d
			      WHERE d.agent_id=t.agent_id
			        AND (d.name = ANY($3) OR ($4 AND d.created_by='agent'))))`,
			taskID, target, []string{"Backlog", "In Arbeit", "Erledigt"}, true)
	}
	byOld, _ := s.backlog.Create(ctx, s.orgID, a, "vom alten Code", "", "manual", 9)
	oldFollow(byOld.ID, "Erledigt")
	if stageOf(t, s, byOld.ID) != aDone {
		t.Errorf("the old auto-follow must find Erledigt on the migrated board")
	}

	// Auto-follow on the keys: dispatch moves into in_progress, completing
	// and failing into done — on A, on C (whose done column a human made),
	// and on F (one column left of the backfill).
	follow, err := s.backlog.Create(ctx, s.orgID, a, "folgt", "", "manual", 1)
	if err != nil {
		t.Fatal(err)
	}
	if stageOf(t, s, follow.ID) != aOpen {
		t.Errorf("a new task lands in the open column")
	}
	claimed, err := s.backlog.ClaimNext(ctx, a)
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
	failing, _ := s.backlog.Create(ctx, s.orgID, a, "scheitert", "", "manual", 1)
	if _, err := s.backlog.ClaimNext(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.backlog.Complete(ctx, failing.ID, backlog.StateFailed, "", "kaputt"); err != nil {
		t.Fatal(err)
	}
	if stageOf(t, s, failing.ID) != aDone {
		t.Errorf("failing moves the task into done")
	}
	for agentID, doneCol := range map[uuid.UUID]uuid.UUID{c: cLater, f: fDone} {
		tk, err := s.backlog.Create(ctx, s.orgID, agentID, "folgt auch", "", "manual", 1)
		if err != nil {
			t.Fatal(err)
		}
		if claimed, err := s.backlog.ClaimNext(ctx, agentID); err != nil || claimed.ID != tk.ID {
			t.Fatalf("claim on %s: %v %v", agentID, claimed.ID, err)
		}
		if _, err := s.backlog.Complete(ctx, tk.ID, backlog.StateDone, "ok", ""); err != nil {
			t.Fatal(err)
		}
		if stageOf(t, s, tk.ID) != doneCol {
			t.Errorf("agent %s: completion moves the task into its done column %s, it sits in %s", agentID, doneCol, stageOf(t, s, tk.ID))
		}
	}

	// set_stage on B: "Done" is the human's column of that name, any other
	// spelling of done is the keyed column.
	bTask, _ := s.backlog.Create(ctx, s.orgID, b, "auf B", "", "manual", 9)
	for name, want := range map[string]uuid.UUID{"Done": bDoneHuman, "done": bDone, "Erledigt": bDone} {
		if st, err := s.backlog.SetTaskStageByName(ctx, b, bTask.ID, name); err != nil || st.ID != want {
			t.Errorf("set_stage %q on B landed in %s (%v), want %s", name, st.ID, err, want)
		}
	}
	// B's "Done" stays a column of its own, so a human can recolour and move
	// it under its name. A rename to another spelling of done is still a
	// second done column. The colour and position go back for the rollback.
	if st, err := s.backlog.UpdateStage(ctx, bDoneHuman, "Done", "var(--text-accent)", 5); err != nil || st.Color != "var(--text-accent)" || st.Position != 5 || st.Key != nil {
		t.Errorf("recolouring B's Done: %+v, %v", st, err)
	}
	if _, err := s.backlog.UpdateStage(ctx, bDoneHuman, "done", "var(--text-accent)", 5); !errors.Is(err, backlog.ErrStageNameTaken) {
		t.Errorf("B's Done renamed to done: %v, want ErrStageNameTaken", err)
	}
	if _, err := s.backlog.UpdateStage(ctx, bDoneHuman, "Done", "var(--text-warning)", 3); err != nil {
		t.Fatal(err)
	}

	// After the migration: a task leaves a merged column's target for
	// Research, and a board is seeded new (created_by 'seed').
	exec("UPDATE backlog_tasks SET stage_id=$2 WHERE id=$1", tDoneAgent2, aResearch)
	fresh := s.newSupportAgent("nachher")
	s.ohneLaeufe(fresh.ID)
	if err := s.backlog.SeedDefaultStages(ctx, fresh.ID); err != nil {
		t.Fatal(err)
	}

	// Rollback: the look-alikes come back as they were, with the tasks still
	// in the column they were merged into; created_by is what it was.
	belowStageKeys()
	for id, want := range map[uuid.UUID][4]string{
		aDoneAgent: {"Done", "4", "var(--text-warning)", "agent"},
		aWorkAgent: {"in progress", "5", "", "agent"},
		cDoneAgent: {"Done", "3", "", "agent"},
		gDone:      {"Erledigt", "1", "", "agent"},
		aDone:      {"Erledigt", "2", "var(--text-success)", "human"},
		cLater:     {"Erledigt", "2", "var(--text-warning)", "human"},
		bDoneHuman: {"Done", "3", "var(--text-warning)", "human"},
	} {
		var name, color, by string
		var pos int
		if err := s.pool.QueryRow(ctx, "SELECT name, position, color, created_by FROM agent_stages WHERE id=$1", id).
			Scan(&name, &pos, &color, &by); err != nil {
			t.Errorf("after the rollback column %s is missing: %v", id, err)
			continue
		}
		if got := [4]string{name, strconv.Itoa(pos), color, by}; got != want {
			t.Errorf("after the rollback column %s is %v, want %v", id, got, want)
		}
	}
	for tk, want := range map[uuid.UUID]uuid.UUID{
		tDoneAgent1: aDoneAgent, tDoneAgent2: aResearch, tWorkAgent: aWorkAgent, tDoneAgentC: cDoneAgent,
		tDone: aDone, tLater: cLater, tHuman: bDoneHuman,
	} {
		if got := stageOf(t, s, tk); got != want {
			t.Errorf("after the rollback task %s sits in %s, want %s", tk, got, want)
		}
	}
	var seeded, freshHuman int
	s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM agent_stages WHERE created_by='seed'").Scan(&seeded)
	s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM agent_stages WHERE agent_id=$1 AND created_by='human' AND name = ANY($2)",
		fresh.ID, []string{"Backlog", "In Arbeit", "Erledigt"}).Scan(&freshHuman)
	if seeded != 0 || freshHuman != 3 {
		t.Errorf("the code before 0148 knows no 'seed': %d still carry it, the new board has %d of 3 as 'human'", seeded, freshHuman)
	}
	var merged *string
	s.pool.QueryRow(ctx, "SELECT to_regclass('stage_keys_merged')::text").Scan(&merged)
	if merged != nil {
		t.Errorf("the rollback must drop stage_keys_merged")
	}
	// The name-based auto-follow of the code before 0148, verbatim.
	back, _ := s.backlog.Create(ctx, s.orgID, a, "nach dem Rollback", "", "manual", 1)
	exec("UPDATE backlog_tasks SET stage_id=$2 WHERE id=$1", back.ID, aWork)
	oldFollow(back.ID, "Erledigt")
	if stageOf(t, s, back.ID) != aDone {
		t.Errorf("after the rollback the old auto-follow must find Erledigt")
	}

	// Leave the database as the stack expects it.
	if _, err := db.MigrateUp(ctx, s.pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
}

// importBoard exports a fresh agent, replaces the board in the bundle with
// stages and imports it under slug. It returns the imported board and the
// import's warnings.
func importBoard(t *testing.T, s *stack, slug string, stages []map[string]string) ([]backlog.Stage, string) {
	t.Helper()
	c := login(t, s, "admin@test.local", "admin-passwort")
	src := c.expect(http.MethodPost, "/api/v1/agents",
		map[string]string{"slug": slug + "-quelle", "display_name": "Quelle", "runtime": "mock"}, http.StatusCreated)
	resp := c.do(http.MethodGet, "/api/v1/agents/"+src["id"].(string)+"/export", nil)
	var bundle map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&bundle); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	bundle["stages"] = stages
	imported := c.expect(http.MethodPost, "/api/v1/agents/import?slug="+slug, bundle, http.StatusCreated)
	id, err := uuid.Parse(imported["agent"].(map[string]any)["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	board, err := s.backlog.ListStages(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	warnings, _ := json.Marshal(imported["warnings"])
	return board, string(warnings)
}

// boardOf writes a board as "name:key" per column, in order.
func boardOf(stages []backlog.Stage) []string {
	var out []string
	for _, st := range stages {
		k := ""
		if st.Key != nil {
			k = *st.Key
		}
		out = append(out, st.Name+":"+k)
	}
	return out
}

// A bundle with keys is taken by its keys: the done column is the one that
// says so ("Shipped"), not an "Erledigt" before it, which is a look-alike of
// it and is not created.
func TestImportTakesExplicitKeysFirst(t *testing.T) {
	s := newStack(t)
	board, warnings := importBoard(t, s, "mit-schluesseln", []map[string]string{
		{"name": "Erledigt"},
		{"name": "Backlog", "key": "open"},
		{"name": "In Arbeit", "key": "in_progress"},
		{"name": "Shipped", "key": "done"},
		{"name": "Review"},
	})
	want := []string{"Backlog:open", "In Arbeit:in_progress", "Shipped:done", "Review:"}
	if got := boardOf(board); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("imported board %v, want %v", got, want)
	}
	if !strings.Contains(warnings, "Erledigt") {
		t.Errorf("the skipped column must be named in a warning: %s", warnings)
	}
}

// A keyed column keeps its key, but not under a name that means another
// default column: "Done" keyed open would read as the done column, and
// set_stage "Done" would land in it (exact name first) while set_stage "done"
// lands in the real one. It is imported under its own old German name, as
// UpdateStage refuses such a rename on the board, and named in a warning.
func TestImportKeepsAKeyedColumnOffAnotherMeaning(t *testing.T) {
	s := newStack(t)
	board, warnings := importBoard(t, s, "fremder-name", []map[string]string{
		{"name": "Done", "key": "open"},
		{"name": "In Arbeit", "key": "in_progress"},
		{"name": "Erledigt"},
	})
	want := []string{"Backlog:open", "In Arbeit:in_progress", "Erledigt:done"}
	if got := boardOf(board); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("imported board %v, want %v", got, want)
	}
	if !strings.Contains(warnings, "Done") {
		t.Errorf("the renamed column must be named in a warning: %s", warnings)
	}
}

// A bundle from before the key: its default columns are the ones with the old
// German names. An agent-made look-alike ("Done") is merged as 0148 merges
// it — there are no tasks in a bundle, so it is simply not created — and a
// name that appears twice is created once, instead of failing halfway and
// leaving a half-imported draft.
func TestImportOfABundleWithoutKeys(t *testing.T) {
	s := newStack(t)
	board, warnings := importBoard(t, s, "ohne-schluessel", []map[string]string{
		{"name": "Backlog"}, {"name": "In Arbeit"}, {"name": "Done"},
		{"name": "Erledigt"}, {"name": "Research"}, {"name": "Research"},
	})
	want := []string{"Backlog:open", "In Arbeit:in_progress", "Erledigt:done", "Research:"}
	if got := boardOf(board); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("imported board %v, want %v", got, want)
	}
	for _, st := range board {
		if st.Key != nil && (st.Renamed || st.CreatedBy != "seed") {
			t.Errorf("an imported default column is the platform's and not renamed: %+v", st)
		}
	}
	if !strings.Contains(warnings, "Done") || !strings.Contains(warnings, "Research") {
		t.Errorf("the skipped columns must be named in a warning: %s", warnings)
	}
}

// A bundle without keys and without "Erledigt" can still carry two columns of
// the done meaning ("Done", "done", "Hecho"). The first becomes the done
// column, as CreateStage makes it; the others are look-alikes of it and are not
// created. Before, the planner let each through, and the second CreateStage
// answered 409 after the agent was written, leaving a half-imported draft.
func TestImportOfTwoUnkeyedColumnsOfOneMeaning(t *testing.T) {
	s := newStack(t)
	board, warnings := importBoard(t, s, "eine-bedeutung", []map[string]string{
		{"name": "Backlog"}, {"name": "Done"}, {"name": "done"}, {"name": "Hecho"}, {"name": "Research"},
	})
	want := []string{"Backlog:open", "Done:done", "Research:"}
	if got := boardOf(board); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("imported board %v, want %v", got, want)
	}
	if !strings.Contains(warnings, `done means`) || !strings.Contains(warnings, "Hecho") {
		t.Errorf("the skipped columns must be named in a warning: %s", warnings)
	}
}

// oldCodeBoard lays out an agent's board the way the code before 0148 seeds
// it — CreateStage's INSERT verbatim, without a key, recorded as 'human'. An
// instance of that code may share the database during a rolling start, after
// 0148 ran, so such boards keep coming.
func oldCodeBoard(t *testing.T, s *stack, agentID uuid.UUID) map[string]uuid.UUID {
	t.Helper()
	ids := map[string]uuid.UUID{}
	for _, name := range []string{"Backlog", "In Arbeit", "Erledigt"} {
		id := uuid.New()
		if _, err := s.pool.Exec(context.Background(), `INSERT INTO agent_stages (id, agent_id, name, color, position, created_by)
			VALUES ($1,$2,$3,'',(SELECT COALESCE(MAX(position),-1)+1 FROM agent_stages WHERE agent_id=$2),'human')`,
			id, agentID, name); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	return ids
}

// A board the old code seeded after 0148 gets its keys where it is used: a
// task that runs to done lands in "Erledigt", set_stage "Done" lands there
// too, and a human cannot add a "Done" beside it. On 8f4fd1a the task stayed
// in Backlog and set_stage "Done" opened a second done column.
func TestOldCodeBoardGetsItsKeys(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()

	t.Run("auto-follow", func(t *testing.T) {
		agent := s.newSupportAgent("altbrett-lauf")
		board := oldCodeBoard(t, s, agent.ID)
		task, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Läuft durch", "[mock:result ok]", "manual", 3)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "task done", 15*time.Second, func() bool {
			return s.taskState(task.ID) == backlog.StateDone
		})
		if got := stageOf(t, s, task.ID); got != board["Erledigt"] {
			t.Fatalf("a finished task belongs in Erledigt, it sits in %s", got)
		}
		if st := stageByKey(t, s, agent.ID, backlog.StageKeyDone); st.ID != board["Erledigt"] || st.Renamed {
			t.Fatalf("Erledigt must be the done column, not renamed: %+v", st)
		}
		if st := stageByKey(t, s, agent.ID, backlog.StageKeyInProgress); st.ID != board["In Arbeit"] {
			t.Fatalf("the run passed through In Arbeit, it must be keyed: %+v", st)
		}
	})

	t.Run("set_stage", func(t *testing.T) {
		agent := s.newSupportAgent("altbrett-stage")
		s.ohneLaeufe(agent.ID)
		board := oldCodeBoard(t, s, agent.ID)
		task, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Wohin", "", "manual", 3)
		if err != nil {
			t.Fatal(err)
		}
		st, err := s.backlog.SetTaskStageByName(ctx, agent.ID, task.ID, "Done")
		if err != nil {
			t.Fatal(err)
		}
		if st.ID != board["Erledigt"] || st.Key == nil || *st.Key != backlog.StageKeyDone || stageOf(t, s, task.ID) != st.ID {
			t.Fatalf("set_stage Done landed in %+v, want the keyed Erledigt", st)
		}
		// The exact old name finds it as the keyed column as well.
		if st, err := s.backlog.EnsureStage(ctx, agent.ID, "In Arbeit"); err != nil || st.ID != board["In Arbeit"] ||
			st.Key == nil || *st.Key != backlog.StageKeyInProgress {
			t.Fatalf("set_stage In Arbeit: %+v, %v", st, err)
		}
		if stages, _ := s.backlog.ListStages(ctx, agent.ID); len(stages) != 3 {
			t.Fatalf("set_stage must not add a column, board: %+v", stages)
		}
	})

	t.Run("column editor", func(t *testing.T) {
		agent := s.newSupportAgent("altbrett-editor")
		s.ohneLaeufe(agent.ID)
		board := oldCodeBoard(t, s, agent.ID)
		admin := login(t, s, "admin@test.local", "admin-passwort")
		admin.expect(http.MethodPost, "/api/v1/agents/"+agent.ID.String()+"/stages",
			map[string]string{"name": "Done"}, http.StatusConflict)
		if st := stageByKey(t, s, agent.ID, backlog.StageKeyDone); st.ID != board["Erledigt"] {
			t.Fatalf("Erledigt must be the done column: %+v", st)
		}
	})
}

// A deleted default column comes back with its key. A human who deleted the
// done column and creates "Erledigt" again gets the done column, and a task
// that finishes lands there; the same for a column renamed to "Done". An
// agent's set_stage "Done" on a board without one makes it keyed too, under
// the old German name, and the cleanup leaves it. On 8f4fd1a each of them was
// an ordinary column and auto-follow did not come back.
func TestADeletedDefaultColumnComesBack(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	withoutDone := func(slug string) agents.Agent {
		t.Helper()
		agent := s.newSupportAgent(slug)
		if err := s.backlog.SeedDefaultStages(ctx, agent.ID); err != nil {
			t.Fatal(err)
		}
		done := stageByKey(t, s, agent.ID, backlog.StageKeyDone)
		admin.expect(http.MethodDelete, "/api/v1/stages/"+done.ID.String(), nil, http.StatusNoContent)
		return agent
	}

	t.Run("created by a human", func(t *testing.T) {
		agent := withoutDone("zurueck-mensch")
		got := admin.expect(http.MethodPost, "/api/v1/agents/"+agent.ID.String()+"/stages",
			map[string]string{"name": "Erledigt"}, http.StatusCreated)
		if got["key"] != backlog.StageKeyDone || got["renamed"] != false || got["created_by"] != "human" {
			t.Fatalf("Erledigt made again: %v", got)
		}
		task, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Läuft durch", "[mock:result ok]", "manual", 3)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "task done", 15*time.Second, func() bool {
			return s.taskState(task.ID) == backlog.StateDone
		})
		if stageOf(t, s, task.ID).String() != got["id"] {
			t.Fatalf("a finished task belongs in the done column made again")
		}
	})

	t.Run("renamed by a human", func(t *testing.T) {
		agent := withoutDone("zurueck-umbenannt")
		s.ohneLaeufe(agent.ID)
		research := admin.expect(http.MethodPost, "/api/v1/agents/"+agent.ID.String()+"/stages",
			map[string]string{"name": "Research"}, http.StatusCreated)
		if research["key"] != nil {
			t.Fatalf("Research means no default column: %v", research)
		}
		// A keyed column keeps its key on a rename, so the open column
		// renamed to "Done" would read done and still be filled as open.
		open := stageByKey(t, s, agent.ID, backlog.StageKeyOpen)
		admin.expect(http.MethodPatch, "/api/v1/stages/"+open.ID.String(),
			map[string]any{"name": "Done", "position": open.Position}, http.StatusConflict)
		if st := stageByKey(t, s, agent.ID, backlog.StageKeyOpen); st.Name != open.Name {
			t.Fatalf("the refused rename changed the open column: %+v", st)
		}
		got := admin.expect(http.MethodPatch, "/api/v1/stages/"+research["id"].(string),
			map[string]any{"name": "Done", "position": 2}, http.StatusOK)
		if got["key"] != backlog.StageKeyDone || got["name"] != "Done" || got["renamed"] != true {
			t.Fatalf("Research renamed to Done: %v", got)
		}
	})

	t.Run("set_stage", func(t *testing.T) {
		agent := withoutDone("zurueck-agent")
		s.ohneLaeufe(agent.ID)
		task, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Wohin", "", "manual", 3)
		if err != nil {
			t.Fatal(err)
		}
		if claimed, err := s.backlog.ClaimNext(ctx, agent.ID); err != nil || claimed.ID != task.ID {
			t.Fatalf("claim: %v %v", claimed.ID, err)
		}
		st, err := s.backlog.SetTaskStageByName(ctx, agent.ID, task.ID, "Done")
		if err != nil {
			t.Fatal(err)
		}
		if st.Key == nil || *st.Key != backlog.StageKeyDone || st.Name != "Erledigt" || st.Renamed || st.CreatedBy != "agent" {
			t.Fatalf("set_stage Done on a board without a done column: %+v", st)
		}
		// The task leaves it; a keyed column is no agent column to clean up.
		if _, err := s.backlog.SetTaskStageByName(ctx, agent.ID, task.ID, "Research"); err != nil {
			t.Fatal(err)
		}
		if got := stageByKey(t, s, agent.ID, backlog.StageKeyDone); got.ID != st.ID {
			t.Fatalf("the done column must stay: %+v", got)
		}
		if _, err := s.backlog.Complete(ctx, task.ID, backlog.StateDone, "ok", ""); err != nil {
			t.Fatal(err)
		}
		if stageOf(t, s, task.ID) != st.ID {
			t.Fatalf("a finished task leaves Research for the done column")
		}
	})
}
