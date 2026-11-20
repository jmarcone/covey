package integration

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"covey/internal/observability"

	"github.com/google/uuid"
)

// The fix-round figures (#130, AC 8) against Postgres: which tasks are fix
// rounds, that their $ comes from model_usage (the Measured source) and not
// from the booked cost entries, and the rounds per PR — and the same over the API.
func TestFixRoundsFromPostgres(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()

	dev, err := s.registry.Create(ctx, s.orgID, "fix-dev", "Fix Dev", "mock", &s.adminID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.registry.Create(ctx, s.orgID, "fix-other", "Fix Other", "mock", &s.adminID)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	var event int64 = 9_000_000
	task := func(agent uuid.UUID, title, topic string, created, finished time.Time, decision, policy string) uuid.UUID {
		t.Helper()
		tk, err := s.backlog.Create(ctx, s.orgID, agent, title, "", "manual", 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET state = 'done', topic_key = $2, created_at = $3,
			session_finished_at = $4, sticky_decision = NULLIF($5, ''), sticky_policy = NULLIF($6, '')
			WHERE id = $1`, tk.ID, topic, created, finished, decision, policy); err != nil {
			t.Fatal(err)
		}
		return tk.ID
	}
	usage := func(agent, task uuid.UUID, model string, input, w5m, w1h int64, engine float64) {
		t.Helper()
		event++
		if _, err := s.pool.Exec(ctx, `INSERT INTO model_usage (org_id, agent_id, task_id, event_id, model,
			input_tokens, cache_write_5m_tokens, cache_write_1h_tokens, engine_usd)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, s.orgID, agent, task, event, model, input, w5m, w1h, engine); err != nil {
			t.Fatal(err)
		}
	}

	// pr:42 — the first task (no fix round), then two fix rounds in the window.
	first := task(dev.ID, "PR 42", "pr:42", now.Add(-5*time.Hour), now.Add(-4*time.Hour), "fresh", "conditional")
	resumed := task(dev.ID, "PR 42 round 2", "pr:42", now.Add(-3*time.Hour), now.Add(-2*time.Hour), "sticky_resumed", "conditional")
	fresh := task(dev.ID, "PR 42 round 3", "pr:42", now.Add(-90*time.Minute), now.Add(-time.Hour), "fresh", "conditional")
	if _, err := s.pool.Exec(ctx, `UPDATE backlog_tasks SET first_turn_cache_read = 150000,
		first_turn_cache_write_5m = 1000, first_turn_cache_write_1h = 2000, first_turn_input = 5 WHERE id = $1`, resumed); err != nil {
		t.Fatal(err)
	}
	// $4 + $1 (1h write: 125,000 × $8 / M) for the resumed round, two runs.
	usage(dev.ID, resumed, "claude-opus-5-5", 1_000_000, 0, 0, 77)
	usage(dev.ID, resumed, "claude-opus-5-5", 0, 0, 125_000, 77)
	// An unknown model is priced at the engine's figure.
	usage(dev.ID, fresh, "some-model", 1, 0, 0, 0.25)
	usage(dev.ID, first, "claude-opus-5-5", 1_000_000, 0, 0, 4)
	// The booked side says something else entirely: it must not be read.
	if err := s.obs.AddCost(ctx, dev.ID, &resumed, 1000, observability.Tokens{Input: 1}, "claude-opus-5-5", uuid.Nil, 0); err != nil {
		t.Fatal(err)
	}

	// Not fix rounds: another agent's first task on the same PR, a title-derived
	// topic, a round finished before the window.
	task(other.ID, "PR 42 other", "pr:42", now.Add(-2*time.Hour), now.Add(-time.Hour), "fresh", "fresh")
	task(dev.ID, "routed", "topic-from-title", now.Add(-3*time.Hour), now.Add(-2*time.Hour), "", "")
	task(dev.ID, "routed 2", "topic-from-title", now.Add(-2*time.Hour), now.Add(-time.Hour), "", "")
	task(dev.ID, "old 1", "pr:7", now.Add(-30*24*time.Hour), now.Add(-29*24*time.Hour), "", "always")
	task(dev.ID, "old 2", "pr:7", now.Add(-20*24*time.Hour), now.Add(-19*24*time.Hour), "sticky_resumed", "always")

	rep, err := s.obs.FixRounds(ctx, s.orgID, now.Add(-7*24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	var res, fr observability.FixRoundGroup
	for _, g := range rep.ByDecision {
		switch g.Key {
		case "sticky_resumed":
			res = g
		case "fresh":
			fr = g
		}
	}
	if res.Rounds != 1 || fr.Rounds != 1 {
		t.Fatalf("one resumed and one fresh fix round, got %+v", rep.ByDecision)
	}
	if res.USD.N != 1 || math.Abs(*res.USD.Mean-5) > 1e-6 {
		t.Fatalf("resumed $ = %+v, want $5 measured (not the $1000 booked)", res.USD)
	}
	if math.Abs(*fr.USD.Mean-0.25) > 1e-6 {
		t.Fatalf("fresh $ = %+v, want the engine's $0.25 for an unknown model", fr.USD)
	}
	if res.FirstTurnCacheWrite.N != 1 || *res.FirstTurnCacheWrite.Mean != 3000 || *res.FirstTurnCacheRead.Mean != 150000 {
		t.Fatalf("first turn: %+v", res.FixRoundFigures)
	}
	if fr.FirstTurnCacheWrite.N != 0 || fr.FirstTurnCacheWrite.Mean != nil {
		t.Fatalf("an unknown first turn is no data: %+v", fr.FirstTurnCacheWrite)
	}

	// Rounds per PR: pr:42 has three done tasks of fix-dev, one of fix-other;
	// pr:7 had no done task in the window.
	got := map[string]int{}
	for _, p := range rep.PRs {
		got[p.Slug] += p.Rounds
		if p.PR != 42 {
			t.Fatalf("only pr:42 had a round in the window: %+v", rep.PRs)
		}
	}
	if got["fix-dev"] != 3 || got["fix-other"] != 1 {
		t.Fatalf("rounds per PR: %+v", rep.PRs)
	}

	// Over the API, with the window as RFC 3339.
	admin := login(t, s, "admin@test.local", "admin-passwort")
	q := "/api/v1/cost/fix-rounds?from=" + now.Add(-7*24*time.Hour).Format(time.RFC3339) + "&to=" + now.Add(time.Minute).Format(time.RFC3339)
	resp := admin.do(http.MethodGet, q, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET fix-rounds: %d", resp.StatusCode)
	}
	var api observability.FixRoundReport
	if err := json.NewDecoder(resp.Body).Decode(&api); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(api.ByDecision) != 3 || api.ByDecision[0].Rounds != 1 {
		t.Fatalf("api report: %+v", api.ByDecision)
	}
	bad := admin.do(http.MethodGet, "/api/v1/cost/fix-rounds?from=yesterday", nil)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("a malformed window is a 400, got %d", bad.StatusCode)
	}
}
