package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Cost insights (migration 0142): what a run consumed per model, and what the
// provider said about the subscription meanwhile.
//
// Claude Code reports `modelUsage` only in its final `result` line. That line
// is filed as a runtime recording event like every other one, and recordings
// are pruned after their retention — so the split per model is copied into
// model_usage at the moment the event is filed, in the same statement.

// modelUsageSelect turns recording events (alias e) into model_usage rows. The
// migration's backfill uses the same expressions.
const modelUsageSelect = `
SELECT e.org_id, e.agent_id, e.task_id, e.id, COALESCE(e.payload->>'session_id',''), m.key,
    COALESCE((m.value->>'inputTokens')::bigint,0),
    COALESCE((m.value->>'outputTokens')::bigint,0),
    COALESCE((m.value->>'thinkingTokens')::bigint,0),
    COALESCE((m.value->>'cacheReadInputTokens')::bigint,0),
    COALESCE((m.value->>'cacheCreationEphemeral5mInputTokens')::bigint,0),
    COALESCE((m.value->>'cacheCreationEphemeral1hInputTokens')::bigint,0),
    COALESCE((m.value->>'webSearchRequests')::bigint,0),
    COALESCE((m.value->>'costUSD')::numeric,0),
    COALESCE(m.value->>'costBasis',''),
    e.created_at
FROM e
CROSS JOIN LATERAL jsonb_each(e.payload->'modelUsage') AS m(key, value)
WHERE e.payload->>'type' = 'result'
  AND jsonb_typeof(e.payload->'modelUsage') = 'object'
  AND jsonb_typeof(m.value) = 'object'`

// carriesModelUsage is the cheap pre-check: most runtime lines are transcript.
func carriesModelUsage(raw []byte) bool {
	return bytes.Contains(raw, []byte(`"modelUsage"`)) && bytes.Contains(raw, []byte(`"result"`))
}

// RecordRuntime files a runtime event; a Claude Code `result` event also
// leaves one model_usage row per model, in the same statement.
func (s *Store) RecordRuntime(ctx context.Context, orgID, agentID uuid.UUID, taskID *uuid.UUID, payload json.RawMessage) error {
	if !carriesModelUsage(payload) || !json.Valid(payload) {
		return s.Record(ctx, orgID, agentID, taskID, KindRuntime, payload)
	}
	_, err := s.pool.Exec(ctx, `WITH e AS (
		INSERT INTO recording_events (org_id, agent_id, task_id, kind, payload)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, org_id, agent_id, task_id, payload, created_at)
	INSERT INTO model_usage (org_id, agent_id, task_id, event_id, session_id, model,
		input_tokens, output_tokens, thinking_tokens, cache_read_tokens,
		cache_write_5m_tokens, cache_write_1h_tokens, web_search_requests,
		engine_usd, cost_basis, created_at)`+modelUsageSelect+`
	ON CONFLICT (event_id, model) DO NOTHING`, orgID, agentID, taskID, KindRuntime, []byte(payload))
	return err
}

// ModelTokens is what one model consumed, with the cache writes split by TTL.
type ModelTokens struct {
	Input        int64 `json:"input_tokens"`
	Output       int64 `json:"output_tokens"`
	Thinking     int64 `json:"thinking_tokens"`
	CacheRead    int64 `json:"cache_read_tokens"`
	CacheWrite5m int64 `json:"cache_write_5m_tokens"`
	CacheWrite1h int64 `json:"cache_write_1h_tokens"`
	WebSearches  int64 `json:"web_search_requests"`
}

// ModelUsageRow is one agent × model over the window.
type ModelUsageRow struct {
	AgentID   uuid.UUID `json:"agent_id"`
	AgentSlug string    `json:"agent_slug"`
	AgentName string    `json:"agent_name"`
	Model     string    `json:"model"`
	Runs      int64     `json:"runs"`
	ModelTokens
	// EngineUSD is what the engine itself priced (Claude Code's costUSD).
	EngineUSD float64 `json:"engine_usd"`
	// ListUSD is the same tokens priced with covey's own list (nil: model unknown).
	ListUSD *float64 `json:"list_usd"`
}

// ModelUsageReport is the per-model view of the organisation's runs.
type ModelUsageReport struct {
	Rows []ModelUsageRow `json:"rows"`
	// RunsWithout counts runs booked in cost_entries that have no per-model
	// record (cut off before Claude Code wrote its result, or older than the
	// recordings that were still there when 0142 ran).
	RunsWithout int64   `json:"runs_without_breakdown"`
	USDWithout  float64 `json:"usd_without_breakdown"`
}

// ModelUsage aggregates model_usage per agent and model since `since`.
func (s *Store) ModelUsage(ctx context.Context, orgID uuid.UUID, since time.Time) (ModelUsageReport, error) {
	rep := ModelUsageReport{Rows: []ModelUsageRow{}}
	rows, err := s.pool.Query(ctx, `SELECT mu.agent_id, a.slug, a.display_name, mu.model,
		COUNT(DISTINCT mu.event_id),
		SUM(mu.input_tokens), SUM(mu.output_tokens), SUM(mu.thinking_tokens), SUM(mu.cache_read_tokens),
		SUM(mu.cache_write_5m_tokens), SUM(mu.cache_write_1h_tokens), SUM(mu.web_search_requests),
		SUM(mu.engine_usd)::float8
		FROM model_usage_latest mu JOIN agents a ON a.id = mu.agent_id
		WHERE mu.org_id = $1 AND mu.created_at >= $2
		GROUP BY 1,2,3,4 ORDER BY SUM(mu.engine_usd) DESC`, orgID, since)
	if err != nil {
		return rep, err
	}
	defer rows.Close()
	for rows.Next() {
		var r ModelUsageRow
		if err := rows.Scan(&r.AgentID, &r.AgentSlug, &r.AgentName, &r.Model, &r.Runs,
			&r.Input, &r.Output, &r.Thinking, &r.CacheRead, &r.CacheWrite5m, &r.CacheWrite1h, &r.WebSearches,
			&r.EngineUSD); err != nil {
			return rep, err
		}
		if p, ok := ListPrice(r.Model); ok {
			v := p.Cost(r.ModelTokens)
			r.ListUSD = &v
		}
		rep.Rows = append(rep.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return rep, err
	}
	err = s.pool.QueryRow(ctx, `SELECT COUNT(DISTINCT ce.task_id), COALESCE(SUM(ce.usd),0)::float8
		FROM cost_entries ce JOIN agents a ON a.id = ce.agent_id
		WHERE a.org_id = $1 AND ce.created_at >= $2 AND ce.task_id IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM model_usage mu WHERE mu.task_id = ce.task_id)`,
		orgID, since).Scan(&rep.RunsWithout, &rep.USDWithout)
	return rep, err
}

// UsageReading is one answer of the provider about a credential's utilisation.
type UsageReading struct {
	RuntimeID     uuid.UUID `json:"runtime_id"`
	CredentialOrd int       `json:"credential_ord"`
	WindowPercent *float64  `json:"window_percent"`
	WeekPercent   *float64  `json:"week_percent"`
	WindowResets  string    `json:"window_resets,omitempty"`
	WeekResets    string    `json:"week_resets,omitempty"`
	Stale         bool      `json:"stale"`
	FetchedAt     time.Time `json:"fetched_at"`
	// CostUSD is the organisation's booked list-price cost from the start of
	// the requested window up to this reading — the figure the meter is read against.
	CostUSD float64 `json:"cost_usd"`
}

// AddUsageReading keeps a provider answer. Negative percentages mean "not reported".
func (s *Store) AddUsageReading(ctx context.Context, orgID, runtimeID uuid.UUID, ord int, supported bool,
	windowPct, weekPct float64, windowResets, weekResets string, stale bool, fetched time.Time) error {
	pct := func(v float64) *float64 {
		if v < 0 {
			return nil
		}
		return &v
	}
	if fetched.IsZero() {
		fetched = time.Now()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO usage_readings (org_id, runtime_id, credential_ord, supported,
		window_percent, week_percent, window_resets, week_resets, stale, fetched_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		orgID, runtimeID, ord, supported, pct(windowPct), pct(weekPct),
		strings.TrimSpace(windowResets), strings.TrimSpace(weekResets), stale, fetched)
	return err
}

// UsageReadings returns the supported readings since `since`, oldest first,
// each with the cumulative booked cost up to it.
func (s *Store) UsageReadings(ctx context.Context, orgID uuid.UUID, since time.Time) ([]UsageReading, error) {
	rows, err := s.pool.Query(ctx, `SELECT r.runtime_id, r.credential_ord, r.window_percent, r.week_percent,
		r.window_resets, r.week_resets, r.stale, r.fetched_at,
		(SELECT COALESCE(SUM(ce.usd),0)::float8 FROM cost_entries ce JOIN agents a ON a.id = ce.agent_id
		  WHERE a.org_id = r.org_id AND ce.created_at >= $2 AND ce.created_at <= r.fetched_at)
		FROM usage_readings r
		WHERE r.org_id = $1 AND r.fetched_at >= $2 AND r.supported
		ORDER BY r.fetched_at`, orgID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageReading{}
	for rows.Next() {
		var u UsageReading
		if err := rows.Scan(&u.RuntimeID, &u.CredentialOrd, &u.WindowPercent, &u.WeekPercent,
			&u.WindowResets, &u.WeekResets, &u.Stale, &u.FetchedAt, &u.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
