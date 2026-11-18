package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/backlog"
	"covey/internal/db"
	"covey/internal/observability"
	"covey/migrations"
)

// gapFigures is what the gap rows and entries of one agent hold.
type gapFigures struct {
	rows, entries         int
	rowsUSD, entryUSD     float64
	booked, measured      float64
	unlinked              int
	gapTask               *uuid.UUID
	out, cacheRead, write int64
}

func readGap(t *testing.T, s *stack, agentID uuid.UUID) gapFigures {
	t.Helper()
	ctx := context.Background()
	var g gapFigures
	if err := s.pool.QueryRow(ctx, `SELECT count(*), COALESCE(SUM(engine_usd),0)::float8,
			COALESCE(SUM(output_tokens),0), COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_1h_tokens),0)
		FROM model_usage WHERE agent_id=$1 AND basis='gap'`, agentID).Scan(&g.rows, &g.rowsUSD, &g.out, &g.cacheRead, &g.write); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*), COALESCE(SUM(usd),0)::float8, MAX(task_id::text)::uuid
		FROM cost_entries WHERE agent_id=$1 AND basis='gap'`, agentID).Scan(&g.entries, &g.entryUSD, &g.gapTask); err != nil {
		t.Fatal(err)
	}
	_ = s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(usd),0)::float8 FROM cost_entries WHERE agent_id=$1`, agentID).Scan(&g.booked)
	_ = s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(engine_usd),0)::float8, count(*) FILTER (WHERE cost_entry_id IS NULL)
		FROM model_usage WHERE agent_id=$1`, agentID).Scan(&g.measured, &g.unlinked)
	return g
}

// Session 5eeb07c2 (task f0c6b4f5): the first process wrote its result line
// (0.157228), the second died without one, the third resumed and wrote a line
// whose share is 0.8071344 — and whose running total is 2.3384671. Booked
// were the two shares, $0.9644 of $2.3385. The 1.3741047 nobody reported is
// in the third line's running total; it has to be filed and booked once, as a
// gap of its own, and nothing after it may find it again.
func TestCostGapOfAProcessWithoutResult(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("ohne-ergebnis")
	task := uuid.New()
	seat := observability.CredentialRef{}
	const sid = "5eeb07c2-0000-4000-8000-000000000000"
	sonnet := "claude-sonnet-5-5"
	at := func(usd float64, out, read, w1h int64) ccUsage {
		return ccUsage{USD: usd, Out: out, Read: read, W1h: w1h}
	}
	lines := []recordedLine{
		{0, 1000, 0.157228, map[string]ccUsage{sonnet: at(0.157228, 3044, 20000, 9000)},
			map[string]ccUsage{sonnet: at(0.157228, 3044, 20000, 9000)}},
		// The process in between died: no line. Then the resume:
		{1, 1000, 2.3384671, map[string]ccUsage{sonnet: at(2.3384671, 53801, 900000, 120000)},
			map[string]ccUsage{sonnet: at(0.8071344, 2547, 300000, 40000)}},
		// A further resume after a process that did write its line.
		{2, 1000, 2.5, map[string]ccUsage{sonnet: at(2.5, 54801, 950000, 121000)},
			map[string]ccUsage{sonnet: at(0.1615329, 1000, 50000, 1000)}},
	}
	file := func(n int) {
		t.Helper()
		if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, seat, json.RawMessage(lines[n].json(sid, n, true))); err != nil {
			t.Fatal(err)
		}
	}
	book := func(n int) observability.Booked {
		t.Helper()
		usd := 0.0
		for _, u := range lines[n].since {
			usd += u.USD
		}
		b, err := s.obs.BookRun(ctx, observability.CostEntry{AgentID: agent.ID, TaskID: &task, USD: usd,
			Model: sonnet, Basis: observability.BasisInvocation, SessionID: sid}, false)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	file(0)
	book(0)
	if g := readGap(t, s, agent.ID); g.rows != 0 || g.entries != 0 {
		t.Fatalf("a fresh session's first line has no gap: %+v", g)
	}

	file(1)
	b := book(1)
	g := readGap(t, s, agent.ID)
	if g.rows != 1 || g.entries != 1 || math.Abs(g.rowsUSD-1.3741047) > 1e-6 || math.Abs(g.entryUSD-g.rowsUSD) > 1e-9 {
		t.Fatalf("the dead process's 1.3741047 has to be one gap row and one gap entry: %+v", g)
	}
	if g.out != 53801-3044-2547 || g.cacheRead != 900000-20000-300000 || g.write != 120000-9000-40000 {
		t.Errorf("the gap's tokens are the running total's minus the rows' and the line's: %+v", g)
	}
	if g.gapTask == nil || *g.gapTask != task {
		t.Errorf("the gap is booked on the task of the line that revealed it, got %v", g.gapTask)
	}
	if b.Lines != 1 || math.Abs(b.LinesUSD-0.807134) > 1e-6 {
		t.Errorf("the run's own entry links its own line only, not the gap: %+v", b)
	}
	if math.Abs(g.booked-2.3384671) > 1e-5 || math.Abs(g.measured-2.3384671) > 1e-5 || g.unlinked != 0 {
		t.Errorf("booked %v and measured %v must both reach the session's 2.3385, %d rows unlinked", g.booked, g.measured, g.unlinked)
	}

	// The same line filed again (a retried event): no row, no gap.
	file(1)
	// The next resume after a process WITH a result: no new gap.
	file(2)
	book(2)
	// A line whose running total is BELOW what the rows hold: never a
	// negative gap.
	low := recordedLine{3, 1000, 2.4, map[string]ccUsage{sonnet: at(2.4, 54900, 960000, 121000)},
		map[string]ccUsage{sonnet: at(0.01, 99, 10000, 0)}}
	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, seat, json.RawMessage(low.json(sid, 3, true))); err != nil {
		t.Fatal(err)
	}
	g = readGap(t, s, agent.ID)
	if g.rows != 1 || g.entries != 1 || math.Abs(g.rowsUSD-1.3741047) > 1e-6 {
		t.Fatalf("later lines must not find the gap again, nor book a negative one: %+v", g)
	}
	if math.Abs(g.measured-(2.5+0.01)) > 1e-5 {
		t.Errorf("measured %v, want the session's 2.5 plus the last line's own 0.01", g.measured)
	}

	// The gap is a kind of its own on the cost page's booking check.
	cells, err := s.obs.CostCellsFor(ctx, agent.OrgID, "day", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var gapEntries int64
	for _, bb := range cells.Bookings {
		if bb.Basis == observability.BasisGap {
			gapEntries += bb.Entries
		}
	}
	if gapEntries != 1 {
		t.Errorf("the cost cells' bookings show %d gap entries, want 1: %+v", gapEntries, cells.Bookings)
	}
}

// The turn-limit handover dies before its line (timeout, kill): the main
// run's line is booked, the handover's spend sits in the session's running
// total. The continuation that resumes the session reveals it — once.
func TestCostGapOfAKilledHandover(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("uebergabe-tot")
	main, cont := uuid.New(), uuid.New()
	sid := uuid.NewString()
	none := observability.CredentialRef{}
	at := func(usd float64, out int64) ccUsage { return ccUsage{USD: usd, Out: out} }
	mainLine := recordedLine{0, 1000, 1.0, map[string]ccUsage{opus: at(1.0, 900)}, map[string]ccUsage{opus: at(1.0, 900)}}
	// The handover spent 0.3 and died; the continuation's own share is 0.7.
	contLine := recordedLine{1, 1000, 2.0, map[string]ccUsage{opus: at(2.0, 2000)}, map[string]ccUsage{opus: at(0.7, 800)}}
	for _, l := range []struct {
		task uuid.UUID
		line recordedLine
		n    int
	}{{main, mainLine, 0}, {cont, contLine, 1}} {
		if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &l.task, none, json.RawMessage(l.line.json(sid, l.n, true))); err != nil {
			t.Fatal(err)
		}
		if _, err := s.obs.BookRun(ctx, observability.CostEntry{AgentID: agent.ID, TaskID: &l.task, USD: l.line.since[opus].USD,
			Basis: observability.BasisInvocation, SessionID: sid}, false); err != nil {
			t.Fatal(err)
		}
	}
	g := readGap(t, s, agent.ID)
	if g.rows != 1 || math.Abs(g.entryUSD-0.3) > 1e-6 || g.out != 300 || g.gapTask == nil || *g.gapTask != cont {
		t.Fatalf("the killed handover's 0.3 is one gap on the continuation: %+v", g)
	}
	if math.Abs(g.booked-2.0) > 1e-6 || math.Abs(g.measured-2.0) > 1e-6 {
		t.Errorf("booked %v, measured %v — want the session's 2.0 on both sides", g.booked, g.measured)
	}
}

// Through the real orchestrator: the evening's session, but the continuation
// (task 1) died before its lines — it reported nothing. The second
// continuation's line holds task 1's spend in its running total. Booked and
// measured have to reach the session's last total, the dead task's share
// standing as a gap.
func TestCostGapThroughTheOrchestrator(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("gap-kette")
	sid := uuid.NewString()
	bodies := []string{
		mockLines(sid, 0, true) + fmt.Sprintf(`[mock:cost {"usd":%v,"basis":"invocation"}]`, sinceUSD(0)),
		// Died without a line: nothing to report.
		`[mock:cost {"usd":0}]`,
		mockLines(sid, 2, true) + fmt.Sprintf(`[mock:cost {"usd":%v,"basis":"invocation"}]`, sinceUSD(2)),
	}
	var ids []uuid.UUID
	for i, body := range bodies {
		var (
			task backlog.Task
			err  error
		)
		if i == 0 {
			task, err = s.backlog.Create(ctx, s.orgID, agent.ID, "Sylius bench", body, "manual", 3)
		} else {
			task, err = s.backlog.CreateChild(ctx, ids[i-1], backlog.ChildSpec{Title: "Sylius bench", Body: body,
				Origin: "continuation:" + ids[i-1].String(), SessionID: sid, ResumeInput: "carry on"})
		}
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "task done", 20*time.Second, func() bool { return s.taskState(task.ID) == backlog.StateDone })
		ids = append(ids, task.ID)
	}
	g := readGap(t, s, agent.ID)
	if g.entries != 1 || math.Abs(g.entryUSD-ownShare[1]) > 2e-6 || g.gapTask == nil || *g.gapTask != ids[2] {
		t.Fatalf("the dead continuation's %v has to stand as one gap on the task that revealed it: %+v", ownShare[1], g)
	}
	if math.Abs(g.booked-sessionTotal) > 1e-5 || math.Abs(g.measured-sessionTotal) > 1e-5 || g.unlinked != 0 {
		t.Errorf("booked %v, measured %v (%d unlinked) — want the session's %v", g.booked, g.measured, g.unlinked, sessionTotal)
	}
}

// Migration 0147 files the gaps of the lines filed before it, with the same
// statements the code runs per line, and books them; down takes them away
// again. The fixture is session 5eeb07c2 as it was filed before 0147: two
// since_last rows, $0.9644 booked of $2.3385.
func TestMigration0147CostGap(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	version := func() int {
		var v int
		if err := s.pool.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	down := func() {
		t.Helper()
		for version() > 146 {
			if err := db.MigrateDown(ctx, s.pool, migrations.FS); err != nil {
				t.Fatal(err)
			}
		}
	}
	down()

	agent := s.newSupportAgent("vor-0147")
	task := uuid.New()
	sid := uuid.NewString()
	sonnet := "claude-sonnet-5-5"
	at := func(usd float64, out int64) ccUsage { return ccUsage{USD: usd, Out: out} }
	lines := []recordedLine{
		{0, 1000, 0.157228, map[string]ccUsage{sonnet: at(0.157228, 3044)}, map[string]ccUsage{sonnet: at(0.157228, 3044)}},
		{1, 1000, 2.3384671, map[string]ccUsage{sonnet: at(2.3384671, 53801)}, map[string]ccUsage{sonnet: at(0.8071344, 2547)}},
	}
	for n, l := range lines {
		// Filed by today's code against the schema before 0147: the gap row
		// collides with the line's own row on (event_id, model) and is not
		// filed — what the instance holds before the migration.
		if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, observability.CredentialRef{}, json.RawMessage(l.json(sid, n, true))); err != nil {
			t.Fatal(err)
		}
		if _, err := s.obs.BookRun(ctx, observability.CostEntry{AgentID: agent.ID, TaskID: &task, USD: l.since[sonnet].USD,
			Basis: observability.BasisInvocation, SessionID: sid}, false); err != nil {
			t.Fatal(err)
		}
	}
	if g := readGap(t, s, agent.ID); g.rows != 0 || math.Abs(g.booked-0.964362) > 1e-6 {
		t.Fatalf("fixture: before 0147 nothing knows the gap: %+v", g)
	}

	if _, err := db.MigrateUp(ctx, s.pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	g := readGap(t, s, agent.ID)
	if g.rows != 1 || g.entries != 1 || math.Abs(g.entryUSD-1.3741047) > 1e-6 || g.out != 53801-3044-2547 {
		t.Fatalf("0147 has to file and book the gap the second line reveals: %+v", g)
	}
	if math.Abs(g.booked-2.3384671) > 1e-5 || math.Abs(g.measured-2.3384671) > 1e-5 || g.unlinked != 0 {
		t.Errorf("after 0147 booked %v, measured %v (%d unlinked) — want the session's 2.3385", g.booked, g.measured, g.unlinked)
	}
	var sameTime bool
	_ = s.pool.QueryRow(ctx, `SELECT ce.created_at = mu.created_at FROM cost_entries ce JOIN model_usage mu ON mu.cost_entry_id = ce.id
		WHERE ce.agent_id = $1 AND ce.basis = 'gap'`, agent.ID).Scan(&sameTime)
	if !sameTime {
		t.Error("the backfilled gap entry has to stand at the time of the line that revealed it")
	}

	down()
	if g := readGap(t, s, agent.ID); g.rows != 0 || g.entries != 0 || math.Abs(g.booked-0.964362) > 1e-6 {
		t.Errorf("down has to take the gaps away again: %+v", g)
	}
	if _, err := db.MigrateUp(ctx, s.pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
}
