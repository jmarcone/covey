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

	agent := s.agentBelowSchema("vor-0144")
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

	// Down: everything as it was. Up applied what came after 0144 too; down
	// goes back past all of it.
	for version() > 143 {
		if err := db.MigrateDown(ctx, s.pool, migrations.FS); err != nil {
			t.Fatal(err)
		}
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

// Migration 0145 keys the meter readings on the secret and files the in-band
// readings the recordings still hold: one per window of every
// rate_limit_event, on the credential the agent's phase was brokered. Running
// it twice adds nothing; down gives the readings back as they were.
func TestMigration0145MeterReadings(t *testing.T) {
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
	for version() > 144 {
		if err := db.MigrateDown(ctx, s.pool, migrations.FS); err != nil {
			t.Fatal(err)
		}
	}

	agent := s.agentBelowSchema("vor-0145")
	stranger := s.agentBelowSchema("ohne-sitz")
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	ord := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "sitz", "A")
	base := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	event := func(agentID uuid.UUID, kind, payload string, at time.Time) {
		exec(`INSERT INTO recording_events (org_id, agent_id, kind, payload, created_at)
			VALUES ($1,$2,$3,$4::jsonb,$5)`, s.orgID, agentID, kind, payload, at)
	}
	// The phase's credential, then two lines of the run; and a line of an
	// agent no credential was ever brokered to.
	event(agent.ID, "credential", fmt.Sprintf(`{"system":"anthropic","granted":true,"proactive":true,"runtime":%q,"ord":%d,"label":""}`, rt.ID, ord), base)
	event(agent.ID, "runtime", event9802, base.Add(time.Minute))
	event(agent.ID, "runtime", twoWindows, base.Add(2*time.Minute))
	event(stranger.ID, "runtime", event9802, base.Add(3*time.Minute))
	// The readings as 0142 kept them: one figure, one answer without.
	exec(`INSERT INTO usage_readings (org_id, runtime_id, credential_ord, supported, window_percent, week_percent,
		window_resets, week_resets, stale, fetched_at) VALUES
		($1,$2,$3,true,4,9,'','Nov 20',false,$4), ($1,$2,$3,false,NULL,NULL,'','',false,$5)`,
		s.orgID, rt.ID, ord, base.Add(-time.Hour), base.Add(5*time.Minute))
	// And one reading filed twice, as the doubled backfill left them on the
	// instance this was written for: the same place, figure and moment. Both
	// get the same secret, source and window, which the unique index of 0145
	// would not take.
	exec(`INSERT INTO usage_readings (org_id, runtime_id, credential_ord, supported, window_percent, week_percent,
		window_resets, week_resets, stale, fetched_at) VALUES
		($1,$2,$3,true,NULL,11,'','',false,$4), ($1,$2,$3,true,NULL,11,'','',false,$4)`,
		s.orgID, rt.ID, ord, base.Add(10*time.Minute))

	snapshot := func() string {
		t.Helper()
		var out string
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(string_agg(concat_ws('|', id, runtime_id, credential_ord, supported,
			window_percent, week_percent, window_resets, week_resets, stale, fetched_at), E'\n' ORDER BY id), '')
			FROM usage_readings WHERE org_id = $1`, s.orgID).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := snapshot()

	if _, err := db.MigrateUp(ctx, s.pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	count := func() (all, inband int) {
		t.Helper()
		if err := s.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE source = 'inband')
			FROM usage_readings WHERE org_id = $1`, s.orgID).Scan(&all, &inband); err != nil {
			t.Fatal(err)
		}
		return
	}
	all, inband := count()
	if all != 6 || inband != 3 {
		t.Fatalf("after 0145: %d readings, %d in band — want the 3 kept (one copy of the twice-filed one) and 3 windows (the stranger's line has no credential)", all, inband)
	}
	var keyed int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM usage_readings WHERE org_id = $1 AND secret_key = 'claude_code_oauth_token'
		AND secret_slot = 0 AND credential_kind = 'subscription'`, s.orgID).Scan(&keyed)
	if keyed != 6 {
		t.Errorf("%d of 6 readings carry the secret", keyed)
	}
	var aside int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM usage_readings_0145_duplicates WHERE org_id = $1`, s.orgID).Scan(&aside); err != nil || aside != 1 {
		t.Errorf("copies moved aside: %d (%v), want 1", aside, err)
	}
	var week float64
	var resets time.Time
	var status string
	if err := s.pool.QueryRow(ctx, `SELECT week_percent, resets_at, status FROM usage_readings
		WHERE org_id = $1 AND source = 'inband' AND fetched_at = $2`, s.orgID, base.Add(time.Minute)).Scan(&week, &resets, &status); err != nil {
		t.Fatal(err)
	}
	if week != 13 || !resets.Equal(time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC)) || status != "allowed" {
		t.Errorf("event 9802 filed as %v %% resetting %v (%s)", week, resets, status)
	}
	var five float64
	if err := s.pool.QueryRow(ctx, `SELECT window_percent FROM usage_readings
		WHERE org_id = $1 AND source = 'inband' AND window_kind = 'five_hour'`, s.orgID).Scan(&five); err != nil || five != 41 {
		t.Errorf("the five-hour window: %v (%v)", five, err)
	}

	// The whole file once more: nothing is added twice.
	up, err := migrations.FS.ReadFile("0145_meter_readings.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(string(up))
	if a, i := count(); a != all || i != inband {
		t.Errorf("0145 run twice: %d readings (%d in band), want %d (%d)", a, i, all, inband)
	}

	// A plan price entered by hand: the down migration must not lose it.
	exec(`INSERT INTO credential_plans (org_id, secret_key, secret_slot, plan_name, amount, period, valid_from)
		VALUES ($1, 'claude_code_oauth_token', 0, 'Max', 200, 'month', $2)`, s.orgID, base)

	// Down: the readings as they were, the plans kept.
	for version() > 144 {
		if err := db.MigrateDown(ctx, s.pool, migrations.FS); err != nil {
			t.Fatal(err)
		}
	}
	if after := snapshot(); after != before {
		t.Errorf("usage_readings after down differ:\n%s", diffLines(before, after))
	}
	plans := func(when string) {
		t.Helper()
		var amount float64
		if err := s.pool.QueryRow(ctx, `SELECT amount::float8 FROM credential_plans WHERE org_id = $1`, s.orgID).
			Scan(&amount); err != nil || amount != 200 {
			t.Errorf("credential_plans %s: %v (%v), want the plan entered by hand", when, amount, err)
		}
	}
	plans("after the down migration")
	var aside0145 bool
	_ = s.pool.QueryRow(ctx, `SELECT to_regclass('usage_readings_0145_duplicates') IS NOT NULL`).Scan(&aside0145)
	if aside0145 {
		t.Error("usage_readings_0145_duplicates survived the down migration")
	}
	if _, err := db.MigrateUp(ctx, s.pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	plans("after up again")
}

// Migration 0151 adds the cost bases and the usage charges. Their rows are
// entered by hand and found nowhere else, so the down keeps both tables as
// 0145 kept credential_plans: up → down → up loses none of them, and the up
// file run twice adds nothing.
func TestMigration0151CostBasisKeepsRows(t *testing.T) {
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
	if version() < 151 {
		t.Fatalf("schema at %d, want 0151 applied", version())
	}
	at := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	exec(`INSERT INTO credential_cost_bases (org_id, secret_key, secret_slot, basis, valid_from, valid_to)
		VALUES ($1, 'claude_code_oauth_token', 0, 'seat', $2, $3), ($1, 'claude_code_oauth_token', 0, 'usage', $3, NULL)`,
		s.orgID, at.Add(-48*time.Hour), at)
	exec(`INSERT INTO credential_usage_charges (id, org_id, secret_key, secret_slot, amount, currency, period_from, period_to,
		fx_to_usd, fx_date, note) VALUES ($1, $2, 'claude_code_oauth_token', 0, 180, 'EUR', $3, $4, 1.08, '2026-11-18', 'measured')`,
		uuid.New(), s.orgID, at, at.Add(24*time.Hour))
	snapshot := func() string {
		t.Helper()
		var bases, charges string
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(string_agg(concat_ws('|', secret_key, secret_slot, basis, valid_from, valid_to),
			E'\n' ORDER BY valid_from), '') FROM credential_cost_bases WHERE org_id = $1`, s.orgID).Scan(&bases); err != nil {
			t.Fatal(err)
		}
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(string_agg(concat_ws('|', id, amount, currency, period_from, period_to,
			fx_to_usd, fx_date, note), E'\n' ORDER BY period_from), '') FROM credential_usage_charges WHERE org_id = $1`, s.orgID).Scan(&charges); err != nil {
			t.Fatal(err)
		}
		return bases + "\n--\n" + charges
	}
	before := snapshot()
	if !strings.Contains(before, "usage") || !strings.Contains(before, "measured") {
		t.Fatalf("the rows were not stored: %s", before)
	}

	for version() > 150 {
		if err := db.MigrateDown(ctx, s.pool, migrations.FS); err != nil {
			t.Fatal(err)
		}
	}
	if after := snapshot(); after != before {
		t.Errorf("after the down migration:\n%s", diffLines(before, after))
	}
	if _, err := db.MigrateUp(ctx, s.pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if version() < 151 {
		t.Fatalf("up again stopped at %d", version())
	}
	if after := snapshot(); after != before {
		t.Errorf("after up again:\n%s", diffLines(before, after))
	}
	up, err := migrations.FS.ReadFile("0151_credential_cost_basis.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(string(up))
	if after := snapshot(); after != before {
		t.Errorf("0151 run twice:\n%s", diffLines(before, after))
	}
	// The checks are still there: a rate without its date is refused.
	if _, err := s.pool.Exec(ctx, `INSERT INTO credential_usage_charges (id, org_id, secret_key, amount, currency, period_from, period_to,
		fx_to_usd) VALUES ($1, $2, 'k', 1, 'EUR', $3, $4, 1.1)`, uuid.New(), s.orgID, at, at.Add(time.Hour)); err == nil {
		t.Error("a rate without its date was stored after up → down → up")
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
