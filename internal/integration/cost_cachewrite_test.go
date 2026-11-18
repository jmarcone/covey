package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"covey/internal/observability"
)

// A line from a CLI without the cache-write split (only
// cacheCreationInputTokens): the write is filed as 5-minute writes — the
// API's default lifetime — rather than dropped, so the rows, the list price
// and the booking from the lines see the same tokens the daemon counted.
func TestModelUsageCacheWriteWithoutSplit(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	agent := s.newSupportAgent("ohne-split")
	task := uuid.New()
	line := `{"type":"result","subtype":"success","session_id":"` + uuid.NewString() + `","total_cost_usd":0.1,
		"modelUsage":{"claude-opus-5-5":{"costUSD":0.1,"outputTokens":10,"cacheCreationInputTokens":400}},
		"model_usage_since_last_result":{"claude-opus-5-5":{"costUSD":0.1,"outputTokens":10,"cacheCreationInputTokens":400}}}`
	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task, observability.CredentialRef{}, json.RawMessage(line)); err != nil {
		t.Fatal(err)
	}
	var w5m, w1h int64
	if err := s.pool.QueryRow(ctx, `SELECT cache_write_5m_tokens, cache_write_1h_tokens FROM model_usage WHERE task_id=$1`, task).Scan(&w5m, &w1h); err != nil {
		t.Fatal(err)
	}
	if w5m != 400 || w1h != 0 {
		t.Errorf("cache writes 5m %d / 1h %d, want the plain 400 as 5-minute writes", w5m, w1h)
	}
	b, err := s.obs.BookRun(ctx, observability.CostEntry{AgentID: agent.ID, TaskID: &task, USD: 0.9}, true)
	if err != nil {
		t.Fatal(err)
	}
	var cw int64
	_ = s.pool.QueryRow(ctx, `SELECT cache_creation_tokens FROM cost_entries WHERE id=$1`, b.ID).Scan(&cw)
	if cw != 400 {
		t.Errorf("the booking from the lines has %d cache-write tokens, want 400", cw)
	}

	// With the split, the split counts, never the plain sum on top.
	task2 := uuid.New()
	split := `{"type":"result","subtype":"success","session_id":"` + uuid.NewString() + `",
		"model_usage_since_last_result":{"claude-opus-5-5":{"costUSD":0.1,"cacheCreationInputTokens":400,
		"cacheCreationEphemeral5mInputTokens":100,"cacheCreationEphemeral1hInputTokens":300}}}`
	if err := s.obs.RecordRuntime(ctx, agent.OrgID, agent.ID, &task2, observability.CredentialRef{}, json.RawMessage(split)); err != nil {
		t.Fatal(err)
	}
	_ = s.pool.QueryRow(ctx, `SELECT cache_write_5m_tokens, cache_write_1h_tokens FROM model_usage WHERE task_id=$1`, task2).Scan(&w5m, &w1h)
	if w5m != 100 || w1h != 300 {
		t.Errorf("with the split: 5m %d / 1h %d, want 100 / 300", w5m, w1h)
	}
}
