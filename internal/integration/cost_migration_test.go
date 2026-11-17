package integration

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"covey/internal/daemon"
	"covey/internal/db"
	"covey/migrations"
)

// Migration 0144 turns what 0142/0143 kept into what the code now writes:
// model_usage from running totals into deltas, and the bookings the old daemon
// made from those running totals into the sum of their lines. The data is
// shaped like the instance it was written for: one session over three tasks
// with two turn-limit handovers, booked 4.37 + 10.58 + 6.45 for 6.45 of work;
// a run without a session; a booking without any line. Down has to give every
// figure back exactly.
func TestMigration0144CostDeltas(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	version := func() int {
		var v int
		if err := s.pool.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	for version() > 143 {
		if err := db.MigrateDown(ctx, s.pool, migrations.FS); err != nil {
			t.Fatal(err)
		}
	}

	agent := s.newSupportAgent("vor-0144")
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	ord := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "A")
	tasks := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	single, bare := uuid.New(), uuid.New()
	base := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	sessionID := "0b3bc4ce-03fb-488c-918a-440c18e73f20"

	// The recording, as the CLI wrote it (running totals and the per-line
	// field), and model_usage as 0142 filed it: running totals, one row per
	// line and model.
	at := []time.Duration{0, 12 * time.Second, 7 * time.Minute, 7*time.Minute + 11*time.Second, 11 * time.Minute}
	for n, l := range session {
		exec(`INSERT INTO recording_events (org_id, agent_id, task_id, kind, payload, created_at)
			VALUES ($1,$2,$3,'runtime',$4::jsonb,$5)`, s.orgID, agent.ID, tasks[l.task], l.json(sessionID, n, true), base.Add(at[n]))
	}
	exec(`INSERT INTO recording_events (org_id, agent_id, task_id, kind, payload, created_at)
		VALUES ($1,$2,$3,'runtime',$4::jsonb,$5)`, s.orgID, agent.ID, single,
		`{"type":"result","modelUsage":{"claude-sonnet-5-5":{"costUSD":0.5,"outputTokens":50000}}}`, base.Add(-10*time.Minute))
	exec(`INSERT INTO model_usage (org_id, agent_id, task_id, event_id, session_id, model,
		input_tokens, output_tokens, thinking_tokens, cache_read_tokens,
		cache_write_5m_tokens, cache_write_1h_tokens, web_search_requests,
		engine_usd, cost_basis, created_at)
	SELECT e.org_id, e.agent_id, e.task_id, e.id, COALESCE(e.payload->>'session_id', ''), m.key,
		COALESCE((m.value->>'inputTokens')::bigint, 0),
		COALESCE((m.value->>'outputTokens')::bigint, 0),
		COALESCE((m.value->>'thinkingTokens')::bigint, 0),
		COALESCE((m.value->>'cacheReadInputTokens')::bigint, 0),
		COALESCE((m.value->>'cacheCreationEphemeral5mInputTokens')::bigint, 0),
		COALESCE((m.value->>'cacheCreationEphemeral1hInputTokens')::bigint, 0),
		COALESCE((m.value->>'webSearchRequests')::bigint, 0),
		COALESCE((m.value->>'costUSD')::numeric, 0),
		COALESCE(m.value->>'costBasis', ''),
		e.created_at
	FROM recording_events e
	CROSS JOIN LATERAL jsonb_each(e.payload->'modelUsage') AS m(key, value)
	WHERE e.agent_id = $1 AND e.payload->>'type' = 'result'`, agent.ID)

	// The bookings: what the old daemon sent once each run (and its handover)
	// had ended — the running totals, tokens of the main model only.
	type booking struct {
		task             uuid.UUID
		after            time.Duration
		usd              float64
		out, read, write int64
	}
	for _, b := range []booking{
		{tasks[0], 13 * time.Second, bookedBefore[0], 35324, 1781843, 183992},
		{tasks[1], 7*time.Minute + 12*time.Second, bookedBefore[1], 31705, 3276449, 244117},
		{tasks[2], 11*time.Minute + time.Second, bookedBefore[2], 12213, 1154586, 22937},
		{single, -10*time.Minute + time.Second, 0.5, 50000, 0, 0},
		{bare, 20 * time.Minute, 2.5, 10, 0, 0},
	} {
		exec(`INSERT INTO cost_entries (agent_id, task_id, usd, output_tokens, cache_read_tokens, cache_creation_tokens,
			model, runtime_id, credential_ord, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,'claude-opus-5-5',$7,$8,$9)`,
			agent.ID, b.task, b.usd, b.out, b.read, b.write, rt.ID, ord, base.Add(b.after))
	}

	snapshot := func() (mu, ce string) {
		t.Helper()
		var a, b string
		if err := s.pool.QueryRow(ctx, `SELECT string_agg(concat_ws('|', event_id, model, task_id, session_id,
			input_tokens, output_tokens, thinking_tokens, cache_read_tokens, cache_write_5m_tokens,
			cache_write_1h_tokens, web_search_requests, engine_usd, cost_basis, created_at), E'\n' ORDER BY event_id, model)
			FROM model_usage WHERE agent_id = $1`, agent.ID).Scan(&a); err != nil {
			t.Fatal(err)
		}
		if err := s.pool.QueryRow(ctx, `SELECT string_agg(concat_ws('|', id, task_id, usd, input_tokens, output_tokens,
			cache_read_tokens, cache_creation_tokens, created_at), E'\n' ORDER BY id)
			FROM cost_entries WHERE agent_id = $1`, agent.ID).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return a, b
	}
	muBefore, ceBefore := snapshot()
	var trueTotal float64
	if err := s.pool.QueryRow(ctx, `SELECT SUM(engine_usd)::float8 FROM model_usage_latest WHERE agent_id = $1`, agent.ID).Scan(&trueTotal); err != nil {
		t.Fatal(err)
	}
	if math.Abs(trueTotal-(sessionTotal+0.5)) > 1e-9 {
		t.Fatalf("fixture: the 0143 view sums %v, want %v", trueTotal, sessionTotal+0.5)
	}

	// Up.
	if _, err := db.MigrateUp(ctx, s.pool, migrations.FS); err != nil {
		t.Fatal(err)
	}

	var total float64
	_ = s.pool.QueryRow(ctx, `SELECT SUM(engine_usd)::float8 FROM model_usage WHERE agent_id = $1`, agent.ID).Scan(&total)
	if math.Abs(total-trueTotal) > 1e-9 {
		t.Errorf("SUM(engine_usd) after 0144 = %v, want the view's %v", total, trueTotal)
	}
	perTask := map[uuid.UUID]float64{tasks[0]: ownShare[0], tasks[1]: ownShare[1], tasks[2]: ownShare[2], single: 0.5}
	for task, want := range perTask {
		var got, booked float64
		var prior *float64
		var basis string
		_ = s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(engine_usd),0)::float8 FROM model_usage WHERE task_id = $1`, task).Scan(&got)
		if err := s.pool.QueryRow(ctx, `SELECT usd::float8, booked_usd::float8, basis FROM cost_entries WHERE task_id = $1`, task).
			Scan(&booked, &prior, &basis); err != nil {
			t.Fatal(err)
		}
		// The migration subtracts running totals that were each stored to
		// 1e-6, so a task's share can sit one unit off the per-line figure;
		// the session's sum (checked above) is exact.
		if math.Abs(got-want) > 2e-6 || math.Abs(booked-got) > 1e-9 || basis != "corrected" || prior == nil {
			t.Errorf("task %s: lines %v, booked %v (%s, before %v) — want %v", task, got, booked, basis, prior, want)
		}
	}
	var over, unlinked, haikuRows, untouched, attributed, starts, uuids int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE usd > COALESCE(booked_usd, usd) + 0.000001),
		count(*) FILTER (WHERE booked_usd IS NULL AND basis = '' AND usd = 2.5),
		count(*) FILTER (WHERE secret_key = 'claude_code_oauth_token')
		FROM cost_entries WHERE agent_id = $1`, agent.ID).Scan(&over, &untouched, &attributed)
	if over != 0 || untouched != 1 || attributed != 5 {
		t.Errorf("entries above their booking %d, the one without lines untouched %d, with the secret %d", over, untouched, attributed)
	}
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE cost_entry_id IS NULL),
		count(*) FILTER (WHERE model = 'claude-haiku-5-5'),
		count(*) FILTER (WHERE started_at IS NOT NULL), count(*) FILTER (WHERE line_uuid IS NOT NULL)
		FROM model_usage WHERE agent_id = $1`, agent.ID).Scan(&unlinked, &haikuRows, &starts, &uuids)
	// Seven rows: five opus lines, the first line's haiku calls, the run
	// without a session (which wrote neither a duration nor a uuid).
	if unlinked != 0 || haikuRows != 1 || starts != 6 || uuids != 6 {
		t.Errorf("after 0144: %d lines unlinked, %d haiku rows (want the first only), %d starts, %d line uuids",
			unlinked, haikuRows, starts, uuids)
	}
	var kinds string
	_ = s.pool.QueryRow(ctx, `SELECT string_agg(DISTINCT concat_ws('/', secret_key, secret_slot, credential_kind), ',')
		FROM model_usage WHERE agent_id = $1`, agent.ID).Scan(&kinds)
	if kinds != "claude_code_oauth_token/0/subscription" {
		t.Errorf("credential of the lines = %q", kinds)
	}
	var tokens string
	_ = s.pool.QueryRow(ctx, `SELECT concat_ws('/', output_tokens, cache_creation_tokens, booked_tokens->>'output_tokens')
		FROM cost_entries WHERE task_id = $1`, tasks[0]).Scan(&tokens)
	if want := fmt.Sprintf("%d/%d/%d", 34246+3552+1078, 95179+88813, 35324); tokens != want {
		t.Errorf("first task's tokens = %s, want %s (every model; the booked ones kept)", tokens, want)
	}
	var started time.Time
	_ = s.pool.QueryRow(ctx, `SELECT started_at FROM model_usage WHERE task_id = $1 AND model = 'claude-opus-5-5'
		ORDER BY event_id LIMIT 1`, tasks[0]).Scan(&started)
	if want := base.Add(-360607 * time.Millisecond); !started.Equal(want) {
		t.Errorf("started_at = %v, want %v", started, want)
	}

	// Down: everything as it was.
	if err := db.MigrateDown(ctx, s.pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	muAfter, ceAfter := snapshot()
	if muAfter != muBefore {
		t.Errorf("model_usage after down differs:\n%s", diffLines(muBefore, muAfter))
	}
	if ceAfter != ceBefore {
		t.Errorf("cost_entries after down differ:\n%s", diffLines(ceBefore, ceAfter))
	}
	if _, err := db.MigrateUp(ctx, s.pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
}

// diffLines names the lines that differ between two snapshots.
func diffLines(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, c string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			c = g[i]
		}
		if a != c {
			fmt.Fprintf(&b, "- %s\n+ %s\n", a, c)
		}
	}
	return b.String()
}
