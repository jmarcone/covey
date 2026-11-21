package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
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
	unreported            float64
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
	_ = s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(usd),0)::float8, COALESCE(SUM(usd) FILTER (WHERE basis='unreported'),0)::float8
		FROM cost_entries WHERE agent_id=$1`, agentID).Scan(&g.booked, &g.unreported)
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

// A line whose filing failed (a transient error in the per-model rows, the
// gap or its booking — one transaction) still stands in the recording, and
// its run is booked by the daemon all the same. The session's next line has
// to measure its gap from that line, not from the last line that left a row:
// otherwise the failed line's share is booked a second time, as a gap.
func TestCostGapAfterAFailedFiling(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("gap-nach-fehler")
	task := uuid.New()
	sid := uuid.NewString()
	none := observability.CredentialRef{}
	at := func(usd float64, out int64) ccUsage { return ccUsage{USD: usd, Out: out} }
	lines := []recordedLine{
		{0, 1000, 0.2, map[string]ccUsage{opus: at(0.2, 200)}, map[string]ccUsage{opus: at(0.2, 200)}},
		// Its filing fails: recorded, booked by the daemon, no row.
		{1, 1000, 1.0, map[string]ccUsage{opus: at(1.0, 1000)}, map[string]ccUsage{opus: at(0.8, 800)}},
		// The next resume after a process WITH a result line: no gap.
		{2, 1000, 1.5, map[string]ccUsage{opus: at(1.5, 1500)}, map[string]ccUsage{opus: at(0.5, 500)}},
		// A process died without a line (0.3), then this resume (0.4): the
		// 0.3 is a gap, measured from line 2.
		{3, 1000, 2.2, map[string]ccUsage{opus: at(2.2, 2200)}, map[string]ccUsage{opus: at(0.4, 400)}},
	}
	for n, l := range lines {
		raw := json.RawMessage(l.json(sid, n, true))
		if n == 1 {
			// What RecordRuntime leaves when fileModelUsage fails: the
			// recording row and nothing derived from it.
			if err := s.obs.Record(ctx, agent.OrgID, agent.ID, &task, observability.KindRuntime, raw); err != nil {
				t.Fatal(err)
			}
		} else if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, none, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := s.obs.BookRun(ctx, observability.CostEntry{AgentID: agent.ID, TaskID: &task, USD: l.since[opus].USD,
			Model: opus, Basis: observability.BasisInvocation, SessionID: sid}, false); err != nil {
			t.Fatal(err)
		}
		if n == 2 {
			if g := readGap(t, s, agent.ID); g.rows != 0 || g.entries != 0 || math.Abs(g.booked-1.5) > 1e-6 {
				t.Fatalf("the failed line's 0.8 is booked by its run; the next line must not book it again as a gap: %+v", g)
			}
		}
	}
	g := readGap(t, s, agent.ID)
	if g.rows != 1 || g.entries != 1 || math.Abs(g.entryUSD-0.3) > 1e-6 || g.out != 300 {
		t.Fatalf("the dead process's 0.3 is one gap, measured from the line before it: %+v", g)
	}
	if math.Abs(g.booked-2.2) > 1e-6 {
		t.Errorf("booked %v, want the session's 2.2", g.booked)
	}
}

// A line without a uuid that arrives again after a later line (L1, L2, L1')
// is filed again, and it is the session's latest line by arrival. Its running
// total is older than L2's: the next line (L3) has to measure from the
// largest running total the session's lines report, not from L1'.
func TestCostGapAfterALateLineWithoutUUID(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("gap-spaete-zeile")
	task := uuid.New()
	sid := uuid.NewString()
	none := observability.CredentialRef{}
	at := func(usd float64, out int64) ccUsage { return ccUsage{USD: usd, Out: out} }
	lines := []recordedLine{
		{0, 1000, 0.5, map[string]ccUsage{opus: at(0.5, 500)}, map[string]ccUsage{opus: at(0.5, 500)}},
		{1, 1000, 1.2, map[string]ccUsage{opus: at(1.2, 1200)}, map[string]ccUsage{opus: at(0.7, 700)}},
		{2, 1000, 1.5, map[string]ccUsage{opus: at(1.5, 1500)}, map[string]ccUsage{opus: at(0.3, 300)}},
	}
	file := func(n int) {
		t.Helper()
		var line map[string]any
		if err := json.Unmarshal([]byte(lines[n].json(sid, n, true)), &line); err != nil {
			t.Fatal(err)
		}
		delete(line, "uuid")
		raw, _ := json.Marshal(line)
		if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, none, raw); err != nil {
			t.Fatal(err)
		}
	}
	file(0)
	file(1)
	file(0) // re-sent late
	file(2)
	if g := readGap(t, s, agent.ID); g.rows != 0 || g.entries != 0 {
		t.Fatalf("no process died: the late copy of the first line must not make L2's 0.7 a gap: %+v", g)
	}
}

// A session outlives its run: task A's session files a line and blocks, and
// is resumed after the agent has worked on other things. Other sessions write
// in between — task B's, and a second one in task A itself (a run started
// afresh rather than resumed is a session of its own) — each with larger
// totals for the same model. When A's resumed process dies and the next one
// writes its line, the gap is A's own: measured from A's line, booked once.
func TestCostGapOfAResumedSessionAmongOthers(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("gap-unter-anderen")
	taskA, taskB := uuid.New(), uuid.New()
	sidA, sidB, sidC := uuid.NewString(), uuid.NewString(), uuid.NewString()
	none := observability.CredentialRef{}
	at := func(usd float64, out int64) ccUsage { return ccUsage{USD: usd, Out: out} }
	file := func(task uuid.UUID, sid string, n int, l recordedLine) {
		t.Helper()
		if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, none, json.RawMessage(l.json(sid, n, true))); err != nil {
			t.Fatal(err)
		}
		if _, err := s.obs.BookRun(ctx, observability.CostEntry{AgentID: agent.ID, TaskID: &task, USD: l.since[opus].USD,
			Model: opus, Basis: observability.BasisInvocation, SessionID: sid}, false); err != nil {
			t.Fatal(err)
		}
	}
	// A's first line, then A blocks.
	file(taskA, sidA, 0, recordedLine{0, 1000, 0.5, map[string]ccUsage{opus: at(0.5, 500)}, map[string]ccUsage{opus: at(0.5, 500)}})
	// Task B in its own session.
	file(taskB, sidB, 0, recordedLine{0, 1000, 3.0, map[string]ccUsage{opus: at(3.0, 3000)}, map[string]ccUsage{opus: at(3.0, 3000)}})
	// A second session in task A.
	file(taskA, sidC, 0, recordedLine{0, 1000, 4.0, map[string]ccUsage{opus: at(4.0, 4000)}, map[string]ccUsage{opus: at(4.0, 4000)}})
	if g := readGap(t, s, agent.ID); g.rows != 0 {
		t.Fatalf("every session so far has one line: no gap yet: %+v", g)
	}
	// A resumes; that process dies (0.3), the next one writes its line (0.4).
	file(taskA, sidA, 1, recordedLine{0, 1000, 1.2, map[string]ccUsage{opus: at(1.2, 1200)}, map[string]ccUsage{opus: at(0.4, 400)}})
	g := readGap(t, s, agent.ID)
	if g.rows != 1 || g.entries != 1 || math.Abs(g.entryUSD-0.3) > 1e-6 || g.out != 300 || g.gapTask == nil || *g.gapTask != taskA {
		t.Fatalf("A's dead process is one gap of 0.3 on task A, measured from A's own line: %+v", g)
	}
	if math.Abs(g.booked-8.2) > 1e-6 {
		t.Errorf("booked %v, want the four lines' 7.9 and the gap's 0.3", g.booked)
	}
}

// A session whose earlier lines are known nowhere — neither filed nor in the
// recording (before 0142, pruned) — has a running total that holds its whole
// history. Its resumed line files its own share; without a known earlier line
// there is nothing to measure the gap from, so none is booked.
func TestCostGapNeedsAnEarlierLine(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("gap-ohne-vorgaenger")
	task := uuid.New()
	sid := uuid.NewString()
	l := recordedLine{0, 1000, 2.0, map[string]ccUsage{opus: {USD: 2.0, Out: 2000}}, map[string]ccUsage{opus: {USD: 0.4, Out: 400}}}
	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, observability.CredentialRef{}, json.RawMessage(l.json(sid, 1, true))); err != nil {
		t.Fatal(err)
	}
	g := readGap(t, s, agent.ID)
	if g.rows != 0 || g.entries != 0 {
		t.Fatalf("no earlier line of the session is known: its 1.6 of history is no gap: %+v", g)
	}
	if math.Abs(g.measured-0.4) > 1e-6 {
		t.Errorf("measured %v, want the line's own 0.4", g.measured)
	}
}

// A turn-limit continuation carries the session into a child task. Its first
// line there fails to file, so the child task holds none of the session's
// rows yet; the next line in the child task still has to find it.
func TestCostGapAfterAFailedFilingInAContinuation(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("gap-fortsetzung")
	parent, child := uuid.New(), uuid.New()
	sid := uuid.NewString()
	at := func(usd float64, out int64) ccUsage { return ccUsage{USD: usd, Out: out} }
	lines := []struct {
		task uuid.UUID
		recordedLine
	}{
		{parent, recordedLine{0, 1000, 0.5, map[string]ccUsage{opus: at(0.5, 500)}, map[string]ccUsage{opus: at(0.5, 500)}}},
		// The continuation's line, whose filing fails.
		{child, recordedLine{1, 1000, 1.3, map[string]ccUsage{opus: at(1.3, 1300)}, map[string]ccUsage{opus: at(0.8, 800)}}},
		// A process died (0.3), then the next line in the child task (0.4).
		{child, recordedLine{1, 1000, 2.0, map[string]ccUsage{opus: at(2.0, 2000)}, map[string]ccUsage{opus: at(0.4, 400)}}},
	}
	for n, l := range lines {
		raw := json.RawMessage(l.json(sid, n, true))
		var err error
		if n == 1 {
			err = s.obs.Record(ctx, agent.OrgID, agent.ID, &l.task, observability.KindRuntime, raw)
		} else {
			err = s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &l.task, observability.CredentialRef{}, raw)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	g := readGap(t, s, agent.ID)
	if g.rows != 1 || math.Abs(g.rowsUSD-0.3) > 1e-6 || g.out != 300 {
		t.Fatalf("the dead process's 0.3 is the gap, not the failed line's 0.8 with it: %+v", g)
	}
}

// gapLine is a one-model result line: its running total and its share.
func gapLine(total, share float64) recordedLine {
	at := func(usd float64, out int64) ccUsage { return ccUsage{USD: usd, Out: out} }
	return recordedLine{0, 1000, total, map[string]ccUsage{opus: at(total, int64(total*1000))},
		map[string]ccUsage{opus: at(share, int64(share*1000+0.5))}}
}

// gapRun is one run of a session's task, as the orchestrator leaves it in
// the recording and the booking: the lifecycle record "working" at its start,
// its result lines (some of whose filing fails), and what its end booked.
type gapRun struct {
	task   uuid.UUID
	lines  []recordedLine
	failed map[int]bool // indexes into lines whose filing fails
	// stage: indexes into lines after which the task moves to a stage
	// (set_stage) — a lifecycle record inside the run that starts none.
	stage map[int]bool
	// resent: indexes into lines that re-send an earlier line of the
	// session, without a uuid; the run's report does not hold their share.
	resent map[int]bool
	noUUID bool
	// derived: the lines carry no model_usage_since_last_result (a CLI
	// before the field); their rows are the running total's difference.
	// derivedAt: the same for single lines (a session that mixes both).
	derived   bool
	derivedAt map[int]bool
	// end: "booked" (the daemon's invocation report, the lines' shares),
	// "killed" (no report, as after a kill or a budget pause; a BookRun that
	// failed leaves the same) or "" (the run goes on). Either end is followed
	// by what the orchestrator does after every run: FileFailedLines, then
	// BookUnreported.
	end string
}

func playGapRuns(t *testing.T, s *stack, agentID, orgID uuid.UUID, sid string, runs []gapRun) {
	t.Helper()
	ctx := context.Background()
	n := 0
	for _, r := range runs {
		task := r.task
		if err := s.obs.Record(ctx, orgID, agentID, &task, observability.KindLifecycle, map[string]string{"status": "working"}); err != nil {
			t.Fatal(err)
		}
		usd := 0.0
		for i, l := range r.lines {
			raw := []byte(l.json(sid, n, !r.derived && !r.derivedAt[i]))
			n++
			if r.noUUID || r.resent[i] {
				var line map[string]any
				if err := json.Unmarshal(raw, &line); err != nil {
					t.Fatal(err)
				}
				delete(line, "uuid")
				raw, _ = json.Marshal(line)
			}
			var err error
			if r.failed[i] {
				// What RecordRuntime leaves when fileModelUsage fails.
				err = s.obs.Record(ctx, orgID, agentID, &task, observability.KindRuntime, json.RawMessage(raw))
			} else {
				err = s.obs.RecordRuntime(ctx, orgID, agentID, &task, observability.CredentialRef{}, raw)
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.stage[i] {
				if err := s.obs.Record(ctx, orgID, agentID, &task, observability.KindLifecycle,
					map[string]string{"status": "stage", "stage": "Review"}); err != nil {
					t.Fatal(err)
				}
			}
			if r.resent[i] {
				continue
			}
			for _, u := range l.since {
				usd += u.USD
			}
		}
		if r.end == "booked" {
			if _, err := s.obs.BookRun(ctx, observability.CostEntry{AgentID: agentID, TaskID: &task, USD: usd,
				Model: opus, Basis: observability.BasisInvocation, SessionID: sid}, false); err != nil {
				t.Fatal(err)
			}
		}
		if r.end != "" {
			// Orchestrator.bookUnreported, at the end of every run: the
			// failed lines of a run that did not report are filed once
			// more, then its unlinked rows are booked.
			if _, err := s.obs.FileFailedLines(ctx, agentID, task, observability.CredentialRef{}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.obs.BookUnreported(ctx, agentID, task); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// A failed line is booked by its run's report where the run reported: the
// daemon's invocation entry holds its share. A run killed (kill switch, budget
// pause, its sandbox gone) never reports, nor does one whose BookRun failed;
// its end files the failed line once more (FileFailedLines), and its share is
// booked with the run's unreported entry, not as the next line's gap. Where the
// run did report, nothing is filed again and the next line books no gap; nor
// where the failed line's run is the next line's own, whose report is still to
// come (the turn-limit handover after a failed main line).
func TestCostGapAfterAFailedLineOfAKilledRun(t *testing.T) {
	s := newStack(t)
	line := gapLine
	for _, c := range []struct {
		name string
		runs func(task uuid.UUID) []gapRun
		gap  float64
		// unreported: what the killed run's entry books.
		unreported float64
	}{
		{"killed", func(task uuid.UUID) []gapRun {
			return []gapRun{
				{task: task, lines: []recordedLine{line(0.5, 0.5)}, end: "booked"},
				{task: task, lines: []recordedLine{line(1.3, 0.8)}, failed: map[int]bool{0: true}, end: "killed"},
				{task: task, lines: []recordedLine{line(1.7, 0.4)}, end: "booked"},
			}
		}, 0, 0.8},
		{"booked", func(task uuid.UUID) []gapRun {
			return []gapRun{
				{task: task, lines: []recordedLine{line(0.5, 0.5)}, end: "booked"},
				{task: task, lines: []recordedLine{line(1.3, 0.8)}, failed: map[int]bool{0: true}, end: "booked"},
				{task: task, lines: []recordedLine{line(1.7, 0.4)}, end: "booked"},
			}
		}, 0, 0},
		{"same run", func(task uuid.UUID) []gapRun {
			return []gapRun{
				{task: task, lines: []recordedLine{line(0.5, 0.5)}, end: "booked"},
				// The main line fails, the handover's line is filed; the
				// run's report comes after both and holds both.
				{task: task, lines: []recordedLine{line(1.3, 0.8), line(1.7, 0.4)}, failed: map[int]bool{0: true}, end: "booked"},
			}
		}, 0, 0},
		{"same run, moved to a stage", func(task uuid.UUID) []gapRun {
			return []gapRun{
				{task: task, lines: []recordedLine{line(0.5, 0.5)}, end: "booked"},
				// A lifecycle record between the two lines (set_stage, as
				// add_note or task_created leave one too) starts no run:
				// the failed line is still the handover line's own run's.
				{task: task, lines: []recordedLine{line(1.3, 0.8), line(1.7, 0.4)}, failed: map[int]bool{0: true},
					stage: map[int]bool{0: true}, end: "booked"},
			}
		}, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			agent := s.newSupportAgent("gap-gekillt-" + strings.ReplaceAll(c.name, " ", "-"))
			task := uuid.New()
			sid := uuid.NewString()
			playGapRuns(t, s, agent.ID, agent.OrgID, sid, c.runs(task))
			g := readGap(t, s, agent.ID)
			if math.Abs(g.entryUSD-c.gap) > 1e-6 || math.Abs(g.rowsUSD-c.gap) > 1e-6 || math.Abs(g.booked-1.7) > 1e-6 ||
				math.Abs(g.unreported-c.unreported) > 1e-6 || g.unlinked != 0 {
				t.Fatalf("want a gap of %v, %v unreported and the session's 1.7 booked: %+v", c.gap, c.unreported, g)
			}
			// The next line finds nothing again.
			playGapRuns(t, s, agent.ID, agent.OrgID, sid, []gapRun{{task: task, lines: []recordedLine{line(2.1, 0.4)}, end: "booked"}})
			if g2 := readGap(t, s, agent.ID); g2.rows != g.rows || math.Abs(g2.booked-2.1) > 1e-6 {
				t.Errorf("the failed line's share is caught once; the next line books no gap: %+v", g2)
			}
		})
	}
}

// A turn-limit continuation in the middle of the chain usually holds just
// its main line and the handover's, seconds apart: one failed filing leaves
// the task without a filed line. Its run booked the share all the same; the
// next continuation must find that line through the chain of tasks, not book
// it again as a gap.
func TestCostGapAfterAFailedFilingInTheMiddleOfTheChain(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("gap-kettenmitte")
	s.ohneLaeufe(agent.ID)
	sid := uuid.NewString()
	parent, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Sylius bench", "work", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	ids := []uuid.UUID{parent.ID}
	for i := 1; i < 3; i++ {
		child, err := s.backlog.CreateChild(ctx, ids[i-1], backlog.ChildSpec{Title: "Sylius bench", Body: "carry on",
			Origin: "continuation:" + ids[i-1].String(), SessionID: sid, ResumeInput: "carry on"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, child.ID)
	}
	at := func(usd float64, out int64) ccUsage { return ccUsage{USD: usd, Out: out} }
	playGapRuns(t, s, agent.ID, agent.OrgID, sid, []gapRun{
		{task: ids[0], lines: []recordedLine{{0, 1000, 0.5, map[string]ccUsage{opus: at(0.5, 500)}, map[string]ccUsage{opus: at(0.5, 500)}}}, end: "booked"},
		{task: ids[1], lines: []recordedLine{{1, 1000, 1.3, map[string]ccUsage{opus: at(1.3, 1300)}, map[string]ccUsage{opus: at(0.8, 800)}}},
			failed: map[int]bool{0: true}, end: "booked"},
		{task: ids[2], lines: []recordedLine{{2, 1000, 1.7, map[string]ccUsage{opus: at(1.7, 1700)}, map[string]ccUsage{opus: at(0.4, 400)}}}, end: "booked"},
	})
	if g := readGap(t, s, agent.ID); g.rows != 0 || g.entries != 0 || math.Abs(g.booked-1.7) > 1e-6 {
		t.Fatalf("the middle task's 0.8 is booked by its run; the last task must not book it again: %+v", g)
	}
}

// The failed lines are looked for after the filed line with the largest
// running total, not after the latest filed one: a late copy without a uuid
// is filed after the lines that followed its original, and a failed line
// among those would fall out of the window.
func TestCostGapWindowFromTheLargestTotal(t *testing.T) {
	s := newStack(t)
	agent := s.newSupportAgent("gap-groesste-summe")
	task := uuid.New()
	sid := uuid.NewString()
	line := gapLine
	playGapRuns(t, s, agent.ID, agent.OrgID, sid, []gapRun{
		{task: task, lines: []recordedLine{line(0.2, 0.2)}, noUUID: true, end: "booked"},
		{task: task, lines: []recordedLine{line(1.0, 0.8)}, noUUID: true, end: "booked"},
		{task: task, lines: []recordedLine{line(1.5, 0.5)}, noUUID: true, end: "booked"},
		{task: task, lines: []recordedLine{line(2.0, 0.5)}, noUUID: true, failed: map[int]bool{0: true}, end: "booked"},
		// The second line arrives again, then the run's own line.
		{task: task, lines: []recordedLine{line(1.0, 0.8), line(2.4, 0.4)}, noUUID: true, end: "booked"},
	})
	if g := readGap(t, s, agent.ID); g.rows != 0 || g.entries != 0 {
		t.Fatalf("no process died: the failed line's booked 0.5 must not come back as a gap: %+v", g)
	}
}

// Two filed lines can hold the largest total: a late copy without a uuid
// (L1, then the failed and booked F, then L1' again). The window starts at
// the earliest of them; from the copy, F would fall out of it and its booked
// share would come back as a gap.
func TestCostGapWindowFromTheEarliestLargestTotal(t *testing.T) {
	s := newStack(t)
	agent := s.newSupportAgent("gap-gleiche-summe")
	task := uuid.New()
	sid := uuid.NewString()
	playGapRuns(t, s, agent.ID, agent.OrgID, sid, []gapRun{
		{task: task, lines: []recordedLine{gapLine(0.5, 0.5)}, noUUID: true, end: "booked"},
		// L1, F (failed, this run's), L1' re-sent, then L2.
		{task: task, lines: []recordedLine{gapLine(1.0, 0.5), gapLine(1.5, 0.5), gapLine(1.0, 0.5), gapLine(1.9, 0.4)},
			failed: map[int]bool{1: true}, noUUID: true},
	})
	if g := readGap(t, s, agent.ID); g.rows != 0 || g.entries != 0 {
		t.Fatalf("no process died: F's 0.5 is its run's, not a gap: %+v", g)
	}
}

// A run's report vouches only for the failed lines of that run. The run after
// a killed one can fail its filing too (a persistent error, a payload the
// insert rejects) and report: its entry is after the killed run's failed
// line as well, but after the next "working" record, so it does not hold
// that line's share. The killed run's end books it, once; the next run's
// failed line is its report's, and no line after them books a gap.
func TestCostGapAfterAKilledRunWhoseNextRunFailsToo(t *testing.T) {
	s := newStack(t)
	agent := s.newSupportAgent("gap-gekillt-dann-fehler")
	task := uuid.New()
	sid := uuid.NewString()
	playGapRuns(t, s, agent.ID, agent.OrgID, sid, []gapRun{
		{task: task, lines: []recordedLine{gapLine(0.5, 0.5)}, end: "booked"},
		{task: task, lines: []recordedLine{gapLine(1.3, 0.8)}, failed: map[int]bool{0: true}, end: "killed"},
		{task: task, lines: []recordedLine{gapLine(1.7, 0.4)}, failed: map[int]bool{0: true}, end: "booked"},
		{task: task, lines: []recordedLine{gapLine(2.1, 0.4)}, end: "booked"},
	})
	g := readGap(t, s, agent.ID)
	if g.rows != 0 || g.entries != 0 || math.Abs(g.unreported-0.8) > 1e-6 || math.Abs(g.booked-2.1) > 1e-6 {
		t.Fatalf("the killed run's 0.8 is its unreported entry, and the session's 2.1 is booked: %+v", g)
	}
}

// A failed line that arrives twice, and a late failed copy of a line older
// than the window's bound, are subtracted from the gap at most once and not
// at all: otherwise a dead process's spend after them is booked too little.
func TestCostGapAfterACopiedFailedLine(t *testing.T) {
	s := newStack(t)
	for _, c := range []struct {
		name string
		runs func(task uuid.UUID) []gapRun
	}{
		{"sent twice", func(task uuid.UUID) []gapRun {
			return []gapRun{
				{task: task, lines: []recordedLine{gapLine(0.5, 0.5)}, end: "booked"},
				// F (1.3, 0.8) fails, its copy fails as well; the report
				// holds 0.8 once.
				{task: task, lines: []recordedLine{gapLine(1.3, 0.8), gapLine(1.3, 0.8)},
					failed: map[int]bool{0: true, 1: true}, resent: map[int]bool{1: true}, end: "booked"},
				// A process died (0.3), then L (2.0, 0.4).
				{task: task, lines: []recordedLine{gapLine(2.0, 0.4)}, end: "booked"},
			}
		}},
		{"late copy of an old line", func(task uuid.UUID) []gapRun {
			return []gapRun{
				{task: task, lines: []recordedLine{gapLine(0.5, 0.5)}, end: "booked"},
				// L1 is filed, then L0 arrives again and its filing fails.
				{task: task, lines: []recordedLine{gapLine(1.0, 0.5), gapLine(0.5, 0.5)},
					failed: map[int]bool{1: true}, resent: map[int]bool{1: true}, end: "booked"},
				// A process died (0.3), then L2 (1.7, 0.4).
				{task: task, lines: []recordedLine{gapLine(1.7, 0.4)}, end: "booked"},
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			agent := s.newSupportAgent("gap-kopie-" + strings.ReplaceAll(c.name, " ", "-"))
			playGapRuns(t, s, agent.ID, agent.OrgID, uuid.NewString(), c.runs(uuid.New()))
			g := readGap(t, s, agent.ID)
			if g.rows != 1 || math.Abs(g.rowsUSD-0.3) > 1e-6 || math.Abs(g.entryUSD-0.3) > 1e-6 {
				t.Fatalf("the dead process's 0.3 is the gap: %+v", g)
			}
		})
	}
}

// A failed line of a run that never reports, with no later line of its
// session (#113): L0 is filed, F's filing fails, the run is killed. Nothing
// after it would find F, so the run's end files F once more and books it with
// the run's rows; the session's next line counts F as filed, not as failed.
// Where the run reports, its entry holds F and nothing is filed again; where
// a later line of the same run was filed, that line's gap subtracted F as its
// run's, and the killed run's end books it.
func TestCostOfAFailedLineOfAKilledRun(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		runs func(task uuid.UUID) []gapRun
		// booked is the task's total, unreported what the run's end booked.
		booked, unreported float64
		// f is F's running total; refiled: F has rows, linked to the
		// unreported entry.
		f       float64
		refiled bool
		// gap: what a process without a line spent, booked once as a gap.
		gap float64
	}{
		{"killed", func(task uuid.UUID) []gapRun {
			return []gapRun{{task: task, lines: []recordedLine{gapLine(0.5, 0.5), gapLine(0.8, 0.3)},
				failed: map[int]bool{1: true}, end: "killed"}}
		}, 0.8, 0.8, 0.8, true, 0},
		{"reported", func(task uuid.UUID) []gapRun {
			return []gapRun{{task: task, lines: []recordedLine{gapLine(0.5, 0.5), gapLine(0.8, 0.3)},
				failed: map[int]bool{1: true}, end: "booked"}}
		}, 0.8, 0, 0.8, false, 0},
		{"followed by a filed line", func(task uuid.UUID) []gapRun {
			return []gapRun{
				{task: task, lines: []recordedLine{gapLine(0.5, 0.5)}, end: "booked"},
				{task: task, lines: []recordedLine{gapLine(1.3, 0.8), gapLine(1.7, 0.4)},
					failed: map[int]bool{0: true}, end: "killed"},
			}
		}, 1.7, 1.2, 1.3, true, 0},
		{"sent twice", func(task uuid.UUID) []gapRun {
			return []gapRun{{task: task, lines: []recordedLine{gapLine(0.5, 0.5), gapLine(0.8, 0.3), gapLine(0.8, 0.3)},
				failed: map[int]bool{1: true, 2: true}, resent: map[int]bool{2: true}, end: "killed"}}
		}, 0.8, 0.8, 0.8, true, 0},
		{"derived", func(task uuid.UUID) []gapRun {
			return []gapRun{{task: task, lines: []recordedLine{gapLine(0.5, 0.5), gapLine(0.8, 0.3)},
				failed: map[int]bool{1: true}, derived: true, end: "killed"}}
		}, 0.8, 0.8, 0.8, true, 0},
		{"derived, followed by a filed line", func(task uuid.UUID) []gapRun {
			// L2's derived row is its total minus L0's row: F's share is in
			// it, and filing F would book it twice.
			return []gapRun{
				{task: task, lines: []recordedLine{gapLine(0.5, 0.5)}, derived: true, end: "booked"},
				{task: task, lines: []recordedLine{gapLine(1.3, 0.8), gapLine(1.7, 0.4)},
					failed: map[int]bool{0: true}, derived: true, end: "killed"},
			}
		}, 1.7, 1.2, 1.3, false, 0},
		// F carries model_usage_since_last_result, the filed line after it
		// does not: L2's derived row is its total minus L0's row, F's share
		// included. Filing F books it twice (2.5).
		{"followed by a derived line", func(task uuid.UUID) []gapRun {
			return []gapRun{
				{task: task, lines: []recordedLine{gapLine(0.5, 0.5)}, end: "booked"},
				{task: task, lines: []recordedLine{gapLine(1.3, 0.8), gapLine(1.7, 0.4)},
					failed: map[int]bool{0: true}, derivedAt: map[int]bool{1: true}, end: "killed"},
			}
		}, 1.7, 1.2, 1.3, false, 0},
		{"followed by a derived line in one run", func(task uuid.UUID) []gapRun {
			return []gapRun{{task: task, lines: []recordedLine{gapLine(0.5, 0.5), gapLine(1.3, 0.8), gapLine(1.7, 0.4)},
				failed: map[int]bool{1: true}, derivedAt: map[int]bool{2: true}, end: "killed"}}
		}, 1.7, 1.7, 1.3, false, 0},
		// A line without a uuid whose filing failed, sent again and filed:
		// the copy's rows are F's share. Filing F books it twice (2.1). With
		// a uuid the line's unique index stops the second insert.
		{"a filed copy without a uuid", func(task uuid.UUID) []gapRun {
			return []gapRun{
				{task: task, lines: []recordedLine{gapLine(0.5, 0.5)}, end: "booked"},
				{task: task, lines: []recordedLine{gapLine(1.3, 0.8), gapLine(1.3, 0.8)},
					failed: map[int]bool{0: true}, resent: map[int]bool{1: true}, noUUID: true, end: "killed"},
			}
		}, 1.3, 0.8, 1.3, false, 0},
		// L1 fails, F fails, then L1's late copy is filed and becomes the
		// bound of L2: L2's gap (1.7 - 0.9 - 0.4) is F's share. Filing F
		// as followed books it twice (2.1).
		{"a late copy of an earlier line before f's bound", func(task uuid.UUID) []gapRun {
			return []gapRun{
				{task: task, lines: []recordedLine{gapLine(0.5, 0.5)}, end: "booked"},
				{task: task, lines: []recordedLine{gapLine(0.9, 0.4), gapLine(1.3, 0.4), gapLine(0.9, 0.4), gapLine(1.7, 0.4)},
					failed: map[int]bool{0: true, 1: true}, resent: map[int]bool{2: true}, end: "killed"},
			}
		}, 1.7, 0.8, 1.3, false, 0.4},
		// A copy without a uuid of L0 after F moves no bound (L0 stays L2's,
		// the lower id wins a tie), so L2's gap subtracted F and F is
		// refiled. The copy's own 0.5 is booked twice, as on main: a copy
		// without a uuid is not this PR's.
		{"a late copy that moves no bound", func(task uuid.UUID) []gapRun {
			return []gapRun{
				{task: task, lines: []recordedLine{gapLine(0.5, 0.5)}, end: "booked"},
				{task: task, lines: []recordedLine{gapLine(1.3, 0.8), gapLine(0.5, 0.5), gapLine(1.7, 0.4)},
					failed: map[int]bool{0: true}, resent: map[int]bool{1: true}, end: "killed"},
			}
		}, 2.2, 1.7, 1.3, true, 0},
		// A process without a line before F (0.2 of 1.5 - 0.5 - 0.8). Where
		// a later line of F's session was filed, its gap measured over F from
		// the same bound and holds the 0.2: F is filed without a gap of its
		// own, or the 0.2 is booked twice.
		{"dead process before f, a later line filed", func(task uuid.UUID) []gapRun {
			return []gapRun{
				{task: task, lines: []recordedLine{gapLine(0.5, 0.5)}, end: "booked"},
				{task: task, lines: []recordedLine{gapLine(1.5, 0.8), gapLine(1.9, 0.4)},
					failed: map[int]bool{0: true}, end: "killed"},
			}
		}, 1.9, 1.2, 1.5, true, 0.2},
		{"dead process before f, a later line filed in one run", func(task uuid.UUID) []gapRun {
			return []gapRun{{task: task, lines: []recordedLine{gapLine(0.5, 0.5), gapLine(1.5, 0.8), gapLine(1.9, 0.4)},
				failed: map[int]bool{1: true}, end: "killed"}}
		}, 1.9, 1.7, 1.5, true, 0.2},
		{"dead process before f, f last", func(task uuid.UUID) []gapRun {
			// No later line: F's own gap is the only one that finds the 0.2.
			return []gapRun{{task: task, lines: []recordedLine{gapLine(0.5, 0.5), gapLine(1.5, 0.8)},
				failed: map[int]bool{1: true}, end: "killed"}}
		}, 1.5, 1.3, 1.5, true, 0.2},
		{"dead process before f, resumed", func(task uuid.UUID) []gapRun {
			return []gapRun{
				{task: task, lines: []recordedLine{gapLine(0.5, 0.5)}, end: "booked"},
				{task: task, lines: []recordedLine{gapLine(1.5, 0.8), gapLine(1.9, 0.4)},
					failed: map[int]bool{0: true}, end: "killed"},
				{task: task, lines: []recordedLine{gapLine(2.3, 0.4)}, end: "booked"},
			}
		}, 2.3, 1.2, 1.5, true, 0.2},
	} {
		t.Run(c.name, func(t *testing.T) {
			agent := s.newSupportAgent("gap-unberichtet-" + strings.ReplaceAll(strings.ReplaceAll(c.name, " ", "-"), ",", ""))
			task := uuid.New()
			sid := uuid.NewString()
			playGapRuns(t, s, agent.ID, agent.OrgID, sid, c.runs(task))
			g := readGap(t, s, agent.ID)
			if c.gap == 0 && (g.rows != 0 || g.entries != 0) || g.unlinked != 0 ||
				math.Abs(g.rowsUSD-c.gap) > 1e-6 || math.Abs(g.entryUSD-c.gap) > 1e-6 ||
				math.Abs(g.booked-c.booked) > 1e-6 || math.Abs(g.unreported-c.unreported) > 1e-6 {
				t.Fatalf("want %v booked, %v of it unreported, a gap of %v: %+v", c.booked, c.unreported, c.gap, g)
			}
			// F's own rows, if any (not its gap's): the first line with F's
			// running total.
			var rows int
			var unreported bool
			if err := s.pool.QueryRow(ctx, `SELECT count(mu.id), COALESCE(bool_and(c.basis = 'unreported'), false)
				FROM model_usage mu LEFT JOIN cost_entries c ON c.id = mu.cost_entry_id
				WHERE mu.basis <> 'gap' AND mu.event_id = (SELECT min(r.id) FROM recording_events r
					WHERE r.agent_id = $1 AND r.kind = 'runtime' AND (r.payload->>'total_cost_usd')::numeric = $2)`,
				agent.ID, c.f).Scan(&rows, &unreported); err != nil {
				t.Fatal(err)
			}
			if c.refiled && (rows == 0 || !unreported) || !c.refiled && rows != 0 {
				t.Errorf("F filed again: want %v, F has %d rows (all on the unreported entry: %v)", c.refiled, rows, unreported)
			}
		})
	}
}

// A failure ends the refiling of its own session only. A sub-run's line (a
// session of its own) whose filing fails again leaves the main session's
// failed line to be filed; the sub-run's later line stays unfiled, or it
// would subtract the failed one as its run's and lose it.
func TestFileFailedLinesGoesOnWithTheOtherSessions(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("nachgereicht-sitzungen")
	task := uuid.New()
	sub, main := uuid.NewString(), uuid.NewString()
	record := func(raw string) int64 {
		t.Helper()
		if err := s.obs.Record(ctx, agent.OrgID, agent.ID, &task, observability.KindRuntime, json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := s.pool.QueryRow(ctx, `SELECT max(id) FROM recording_events WHERE agent_id=$1`, agent.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if err := s.obs.Record(ctx, agent.OrgID, agent.ID, &task, observability.KindLifecycle, map[string]string{"status": "working"}); err != nil {
		t.Fatal(err)
	}
	// A token count beyond bigint: its rows cannot be filed, again.
	var line map[string]any
	if err := json.Unmarshal([]byte(gapLine(0.3, 0.3).json(sub, 0, true)), &line); err != nil {
		t.Fatal(err)
	}
	line["model_usage_since_last_result"].(map[string]any)[opus].(map[string]any)["outputTokens"] = 1e30
	broken, _ := json.Marshal(line)
	record(string(broken))
	later := record(gapLine(0.6, 0.3).json(sub, 1, true))
	other := record(gapLine(0.8, 0.8).json(main, 0, true))

	n, err := s.obs.FileFailedLines(ctx, agent.ID, task, observability.CredentialRef{})
	if err == nil || n != 1 {
		t.Fatalf("want the main session's line filed and the sub-run's failure returned: %d filed, err %v", n, err)
	}
	rows := func(id int64) int {
		var n int
		_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM model_usage WHERE event_id=$1`, id).Scan(&n)
		return n
	}
	if rows(other) != 1 || rows(later) != 0 {
		t.Errorf("the main session's line has %d rows (want 1), the sub-run's later line %d (want 0)", rows(other), rows(later))
	}
}

// A followed line whose refiling fails again does not end its session: it is
// refiled without a gap and subtracts nothing, so the next followed line of
// the session loses nothing by being filed after it. L0 filed, F1 (cannot be
// filed: a duration beyond an interval) and F2 failed, L3 filed, the run
// killed: F2 is filed, F1's failure is returned.
func TestFileFailedLinesGoesOnAfterAFollowedLine(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("nachgereicht-gefolgt")
	task := uuid.New()
	sid := uuid.NewString()
	lastID := func() int64 {
		t.Helper()
		var id int64
		if err := s.pool.QueryRow(ctx, `SELECT max(id) FROM recording_events WHERE agent_id=$1`, agent.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if err := s.obs.Record(ctx, agent.OrgID, agent.ID, &task, observability.KindLifecycle, map[string]string{"status": "working"}); err != nil {
		t.Fatal(err)
	}
	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, observability.CredentialRef{},
		[]byte(gapLine(0.5, 0.5).json(sid, 0, true))); err != nil {
		t.Fatal(err)
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(gapLine(1.0, 0.5).json(sid, 1, true)), &line); err != nil {
		t.Fatal(err)
	}
	line["duration_ms"] = 1e30
	broken, _ := json.Marshal(line)
	if err := s.obs.Record(ctx, agent.OrgID, agent.ID, &task, observability.KindRuntime, json.RawMessage(broken)); err != nil {
		t.Fatal(err)
	}
	if err := s.obs.Record(ctx, agent.OrgID, agent.ID, &task, observability.KindRuntime,
		json.RawMessage(gapLine(1.5, 0.5).json(sid, 2, true))); err != nil {
		t.Fatal(err)
	}
	f2 := lastID()
	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, observability.CredentialRef{},
		[]byte(gapLine(1.9, 0.4).json(sid, 3, true))); err != nil {
		t.Fatal(err)
	}

	n, err := s.obs.FileFailedLines(ctx, agent.ID, task, observability.CredentialRef{})
	if err == nil || n != 1 {
		t.Fatalf("want F2 filed and F1's failure returned: %d filed, err %v", n, err)
	}
	var rows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM model_usage WHERE event_id=$1 AND basis <> 'gap'`, f2).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("F2 has %d rows, want 1: a followed line's failure must not end its session", rows)
	}
}
