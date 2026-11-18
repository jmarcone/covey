package integration

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"covey/internal/daemon"
	"covey/internal/observability"
)

// Cost is what an organisation actually decides on: per agent, per model, per
// credential, over time. The report is assembled from four queries, and the
// one thing that must hold across them is that they describe the SAME window —
// a total over thirty days beside a series over seven would not be wrong in
// any one place and would still be a lie.
func TestOrgCostReportOverTheAPI(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("kosten-bericht")

	// Two entries, one of them outside a seven-day window.
	if err := s.obs.AddCost(ctx, agent.ID, nil, 1.25,
		observability.Tokens{Input: 1000, Output: 200}, "claude-opus-5", uuid.Nil, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO cost_entries (agent_id, model, usd, input_tokens, output_tokens, created_at)
		 VALUES ($1, 'claude-opus-5', 9.00, 100, 10, now() - interval '20 days')`,
		agent.ID); err != nil {
		t.Fatal(err)
	}

	all := admin.expect(http.MethodGet, "/api/v1/cost/org?days=30", nil, http.StatusOK)
	total, _ := all["total_usd"].(float64)
	if total < 10 {
		t.Errorf("the thirty-day total is %v, expected both entries", total)
	}

	// The window is what the ?days= says, and the total follows it — otherwise
	// the tiles and the chart would disagree about the same page.
	week := admin.expect(http.MethodGet, "/api/v1/cost/org?days=7", nil, http.StatusOK)
	weekTotal, _ := week["total_usd"].(float64)
	if weekTotal >= total {
		t.Errorf("the seven-day total (%v) is not smaller than the thirty-day one (%v)", weekTotal, total)
	}

	// The buckets are the chart's x-axis. An unknown one falls back rather than
	// producing an empty chart nobody can explain.
	for _, bucket := range []string{"", "day", "week", "month", "erfunden"} {
		admin.expect(http.MethodGet, "/api/v1/cost/org?bucket="+bucket, nil, http.StatusOK)
	}
	// A window beyond the cap is capped rather than refused — somebody asking
	// for five years wants "everything", not an error.
	admin.expect(http.MethodGet, "/api/v1/cost/org?days=100000", nil, http.StatusOK)
	admin.expect(http.MethodGet, "/api/v1/cost/org?days=keine-zahl", nil, http.StatusOK)

	// Per agent: the same numbers, narrowed.
	perAgent := admin.expect(http.MethodGet, "/api/v1/agents/"+agent.ID.String()+"/cost", nil, http.StatusOK)
	if perAgent["total_usd"] == nil {
		t.Errorf("the agent's cost carries no total: %v", perAgent)
	}
	admin.expect(http.MethodGet, "/api/v1/agents/"+agent.ID.String()+"/cost/series?days=7&bucket=day", nil, http.StatusOK)
	admin.expectList(http.MethodGet, "/api/v1/agents/"+agent.ID.String()+"/cost/runs?limit=5", nil, http.StatusOK)
	admin.expectList(http.MethodGet, "/api/v1/cost/runs?limit=5&days=7", nil, http.StatusOK)
	admin.expectList(http.MethodGet, "/api/v1/cost/indicators", nil, http.StatusOK)
	admin.expectList(http.MethodGet, "/api/v1/agents/"+agent.ID.String()+"/cost/indicators", nil, http.StatusOK)
}

// The breakdown answers "who is costing what", so an agent that has never run
// is left out rather than listed at zero — a page of zeroes is a page nobody
// reads, and the agent list next door already says who exists.
func TestCostBreakdownLeavesOutWhatCostNothing(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	idle := s.newSupportAgent("nichtstuer")
	busy := s.newSupportAgent("vielarbeiter")

	if err := s.obs.AddCost(ctx, busy.ID, nil, 2.50,
		observability.Tokens{Input: 500}, "claude-opus-5", uuid.Nil, -1); err != nil {
		t.Fatal(err)
	}

	rep := admin.expect(http.MethodGet, "/api/v1/cost/org", nil, http.StatusOK)
	agentsList, _ := rep["agents"].([]any)
	var sawBusy, sawIdle bool
	for _, a := range agentsList {
		m, _ := a.(map[string]any)
		if m == nil {
			continue
		}
		switch m["slug"] {
		case busy.Slug:
			sawBusy = true
			if usd, _ := m["total_usd"].(float64); usd != 2.50 {
				t.Errorf("the working agent costs %v, expected 2.50", usd)
			}
		case idle.Slug:
			sawIdle = true
		}
	}
	if !sawBusy {
		t.Errorf("the agent that cost something is missing: %v", agentsList)
	}
	if sawIdle {
		t.Error("an agent that never ran was listed at zero")
	}

	// The model breakdown is the other axis of the same total.
	models, _ := rep["models"].([]any)
	if len(models) == 0 {
		t.Errorf("the report names no model: %v", rep)
	}
}

// The recording is what makes a run readable afterwards. Its filters are the
// only way anybody gets at a long one, so they are part of the contract.
func TestRecordingFiltersOverTheAPI(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := login(t, s, "admin@test.local", "admin-passwort")
	agent := s.newSupportAgent("aufzeichnung-agent")
	base := "/api/v1/agents/" + agent.ID.String() + "/recording"

	task, err := s.backlog.Create(ctx, s.orgID, agent.ID, "Aufzeichnung", "[mock:result fertig]", "manual", 3)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task done", 15*time.Second, func() bool {
		return s.taskState(task.ID) == "done"
	})
	/* Und dann noch, bis der Agent wieder schläft.

	   Die fertige Aufgabe ist nicht das letzte, was aufgezeichnet wird: Danach
	   legt sich der Agent hin, und das ist ein `lifecycle`-Ereignis. Wer den
	   Verlauf im Augenblick des `done` abfragt, bekommt ihn manchmal ohne
	   diese letzte Zeile — und `?after=` findet sie dann als eine, die es
	   nicht geben dürfte. Der Test hat damit einen Wettlauf gemessen, keinen
	   Vertrag; unter Last verlor er ihn. */
	waitFor(t, "agent asleep again", 15*time.Second, func() bool {
		return s.agentStatus(agent.ID) == "sleeping"
	})

	all := admin.expectList(http.MethodGet, base, nil, http.StatusOK)
	if len(all) == 0 {
		t.Fatal("a finished run left no recording")
	}

	// Narrowed to one task: the same events, without the rest of the agent's
	// history.
	perTask := admin.expectList(http.MethodGet, base+"?task_id="+task.ID.String(), nil, http.StatusOK)
	if len(perTask) == 0 {
		t.Error("the task's own recording is empty")
	}
	if len(perTask) > len(all) {
		t.Errorf("one task carries more events (%d) than the whole agent (%d)", len(perTask), len(all))
	}

	// ?after= is how the live view follows along: everything since a sequence
	// number it already has. Past the end that is an empty list, not an error —
	// the view polls, and an error would be a red banner on a quiet agent.
	var last float64
	for _, e := range all {
		if seq, ok := e["id"].(float64); ok && seq > last {
			last = seq
		}
	}
	rest := admin.expectList(http.MethodGet, base+"?after="+itoa(int64(last)), nil, http.StatusOK)
	if len(rest) != 0 {
		t.Errorf("?after= the last event returned %d more", len(rest))
	}

	admin.expect(http.MethodGet, base+"?task_id=keine-uuid", nil, http.StatusBadRequest)
	admin.expect(http.MethodGet, "/api/v1/agents/keine-uuid/recording", nil, http.StatusBadRequest)
	admin.expect(http.MethodGet, "/api/v1/recordings/blobs/keine-uuid", nil, http.StatusBadRequest)
	admin.expect(http.MethodGet, "/api/v1/recordings/blobs/"+uuid.NewString(), nil, http.StatusNotFound)
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

// What a single credential of a seat has consumed. It is the figure the
// capacity layer falls back to when the engine reports nothing of its own —
// so a slot that ran must show up, and one that never did must not be
// invented at zero.
func TestRuntimeUsagePerCredential(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("verbrauch-agent")
	rt := uuid.New()

	if err := s.obs.AddCost(ctx, agent.ID, nil, 1.00,
		observability.Tokens{Input: 100, Output: 50, CacheRead: 400}, "m", rt, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.obs.AddCost(ctx, agent.ID, nil, 0.50,
		observability.Tokens{Input: 10, Output: 5}, "m", rt, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.obs.AddCost(ctx, agent.ID, nil, 2.00,
		observability.Tokens{Input: 20, Output: 10}, "m", rt, 1); err != nil {
		t.Fatal(err)
	}
	// A run on no seat at all — the mock engine needs no credential — must not
	// land under any slot.
	if err := s.obs.AddCost(ctx, agent.ID, nil, 9.00,
		observability.Tokens{Input: 1}, "m", uuid.Nil, -1); err != nil {
		t.Fatal(err)
	}

	got, err := s.obs.RuntimeUsage(ctx, rt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d slots, expected the two that ran: %+v", len(got), got)
	}
	if got[0].Ord != 0 || got[1].Ord != 1 {
		t.Errorf("the slots are not in order: %+v", got)
	}
	if got[0].Runs != 2 || got[0].USD != 1.50 {
		t.Errorf("slot 0 = %+v, expected two runs and 1.50", got[0])
	}
	// Every token kind counts towards what a window has been used for — a
	// cache read is read capacity too.
	if got[0].Tokens != 565 {
		t.Errorf("slot 0 counted %d tokens, expected every kind", got[0].Tokens)
	}

	// A window that has already passed holds nothing, which is what makes the
	// figure a WINDOW rather than a running total.
	empty, err := s.obs.RuntimeUsage(ctx, rt, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Errorf("a window of a nanosecond holds %+v", empty)
	}
}

// The input side as a human means it: everything read, cached or not. The
// three kinds are stored apart because they are priced apart, and this is the
// one place they are added back together.
func TestTotalInputAddsEveryKindOfRead(t *testing.T) {
	tok := observability.Tokens{Input: 10, Output: 99, CacheRead: 100, CacheCreation: 1000}
	if got := tok.TotalInput(); got != 1110 {
		t.Errorf("TotalInput = %d, expected the three read kinds", got)
	}
	if got := (observability.Tokens{}).TotalInput(); got != 0 {
		t.Errorf("TotalInput of nothing = %d", got)
	}
}

// A booking names the secret value that paid, not only its place in the
// runtime: a Reorder moves values between places, and the 0047 columns that
// were meant for this stayed empty until 0144. A booking of a Claude Code run
// counts every model's tokens, so a token limit sees the side calls too.
func TestCostBookedAgainstTheSecretValue(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("wert-agent")
	rt, err := s.runtimes.Create(ctx, s.orgID, "claude-code", "Claude", "")
	if err != nil {
		t.Fatal(err)
	}
	a := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "sitz-a", "A")
	b := s.seat(t, rt.ID, daemon.CredSubscription, "claude_code_oauth_token", "sitz-b", "B")

	if err := s.obs.AddCost(ctx, agent.ID, nil, 1.00, observability.Tokens{Output: 5}, "m", rt.ID, b); err != nil {
		t.Fatal(err)
	}
	var key string
	var slot, want int
	if err := s.pool.QueryRow(ctx, `SELECT secret_key, secret_slot FROM cost_entries WHERE agent_id=$1`, agent.ID).Scan(&key, &slot); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT secret_slot FROM runtime_credentials WHERE runtime_id=$1 AND ord=$2`, rt.ID, b).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if key != "claude_code_oauth_token" || slot != want {
		t.Errorf("booked against %s/%d, want claude_code_oauth_token/%d", key, slot, want)
	}

	// A run booked from its lines: the haiku side calls count towards the
	// credential's window as well.
	task := uuid.New()
	cred := observability.CredentialRef{RuntimeID: rt.ID, Ord: a, SecretKey: "claude_code_oauth_token", Kind: daemon.CredSubscription}
	line := session[0].json("wert-sess", 0, true)
	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, cred, json.RawMessage(line)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.obs.BookRun(ctx, observability.CostEntry{AgentID: agent.ID, TaskID: &task, USD: 1.8177047,
		Credential: cred, Basis: observability.BasisLegacy}, true); err != nil {
		t.Fatal(err)
	}
	usd, tokens, err := s.obs.CredentialUsage(ctx, rt.ID, a, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(usd-1.817705) > 1e-6 || tokens != 60+34246+1781843+95179+29681+3552 {
		t.Errorf("credential usage = %v USD, %d tokens — want both models", usd, tokens)
	}
}

// Every bucket is a UTC bucket. The two-argument date_trunc follows the
// session's TimeZone: on a connection that runs with SET TIME ZONE
// 'Europe/Berlin', a run at 23:30 UTC lands on the next day. The same rows,
// read over such a connection, have to fall into the same buckets.
func TestCostBucketsIgnoreSessionTimeZone(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("zeitzone")
	task := uuid.New()
	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, observability.CredentialRef{}, json.RawMessage(resultLine)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.obs.BookRun(ctx, observability.CostEntry{AgentID: agent.ID, TaskID: &task, USD: 1.3991645,
		Basis: observability.BasisInvocation}, false); err != nil {
		t.Fatal(err)
	}
	// 23:30 UTC on the 16th is 00:30 on the 17th in Berlin.
	at := time.Date(2026, 11, 16, 23, 30, 0, 0, time.UTC)
	for _, table := range []string{"model_usage", "cost_entries"} {
		if _, err := s.pool.Exec(ctx, `UPDATE `+table+` SET created_at = $2 WHERE agent_id = $1`, agent.ID, at); err != nil {
			t.Fatal(err)
		}
	}

	cfg := s.pool.Config().Copy()
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, `SET TIME ZONE 'Europe/Berlin'`)
		return err
	}
	berlin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer berlin.Close()
	var tz string
	if err := berlin.QueryRow(ctx, `SHOW TimeZone`).Scan(&tz); err != nil || tz != "Europe/Berlin" {
		t.Fatalf("the connection runs in %q (%v), the test needs Berlin", tz, err)
	}
	obs := observability.NewStore(berlin)
	since := at.Add(-48 * time.Hour)
	day := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

	cells, err := obs.CostCellsFor(ctx, agent.OrgID, "day", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells.Cells) != 1 || !cells.Cells[0].Period.Equal(day) || cells.Cells[0].Reported == 0 || len(cells.Cells[0].Measured) == 0 {
		t.Errorf("cells = %+v, want one on %v with both figures", cells.Cells, day)
	}
	rep, err := obs.OrgCostReport(ctx, agent.OrgID, "day", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Series) != 1 || !rep.Series[0].Period.Equal(day) {
		t.Errorf("org series = %+v, want one bucket on %v", rep.Series, day)
	}
	series, err := obs.CostSeriesByAgent(ctx, agent.ID, "day", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || !series[0].Period.Equal(day) {
		t.Errorf("agent series = %+v, want one bucket on %v", series, day)
	}
}
