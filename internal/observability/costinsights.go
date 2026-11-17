package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Cost insights (migrations 0142, 0144): what a run consumed per model, and
// what the provider said about the subscription meanwhile.
//
// Claude Code reports its usage per model only in its `result` lines. Such a
// line is filed as a runtime recording event like every other one, and
// recordings are pruned after their retention — so the split per model is
// copied into model_usage at the moment the event is filed.
//
// EVERY result line carries the session's RUNNING totals (modelUsage,
// total_cost_usd), restored across --resume: a resumed session's line includes
// everything the session did before, in this task and in earlier ones. What
// the line itself added is model_usage_since_last_result (CLI 2.1.x), and that
// is what a row holds (basis since_last). A CLI without the field gets the
// running total minus what the session's earlier rows already hold (basis
// derived). Either way a row is one invocation's own share, so plain SUMs over
// any window are right and every row sits on its own task and time.

// CredentialRef names the credential a run went out on: its place in the
// runtime (RuntimeID/Ord — a Reorder moves values between places), the secret
// value behind it (SecretKey/SecretSlot — that does not move), and its kind
// (api_key | subscription), which says what money means on it. The zero value
// is "not attributable", as for every run from before the runtimes.
type CredentialRef struct {
	RuntimeID  uuid.UUID
	Ord        int
	SecretKey  string
	SecretSlot int
	Kind       string
}

// columns returns the nullable column values: runtime_id, credential_ord,
// secret_key, secret_slot.
func (c CredentialRef) columns() (rt *uuid.UUID, ord *int, key *string, slot *int) {
	if c.RuntimeID != uuid.Nil {
		rt, ord = &c.RuntimeID, &c.Ord
	}
	if c.SecretKey != "" {
		key, slot = &c.SecretKey, &c.SecretSlot
	}
	return
}

// jsonInt reads a whole number out of the model entry (alias m), 0 for
// anything that is not a JSON number. A line is the engine's to shape: a value
// written as 1.2e3 or as a string must not make the insert fail. Numbers go
// through numeric first, so 1.2e3 still lands as 1200.
func jsonInt(field string) string {
	return "CASE WHEN jsonb_typeof(m.value->'" + field + "') = 'number' THEN (m.value->>'" + field + "')::numeric::bigint ELSE 0 END"
}

// modelUsageInsert files the model_usage rows of one recording event ($1).
// $2..$6 are the credential: runtime_id, credential_ord, secret_key,
// secret_slot, kind.
//
// The derived fallback subtracts what the session's EARLIER rows of the same
// model hold: their sum is the running total before this line, whichever basis
// they were filed with. A model that added nothing leaves no row.
var modelUsageInsert = `
WITH e AS (
	SELECT id, org_id, agent_id, task_id, payload, created_at,
		COALESCE(payload->>'session_id', '') AS session_id,
		COALESCE(jsonb_typeof(payload->'model_usage_since_last_result') = 'object', false) AS since_last
	FROM recording_events
	WHERE id = $1 AND payload->>'type' = 'result'),
v AS (
	SELECT e.*, m.key AS model,
		` + jsonInt("inputTokens") + ` AS input_tokens,
		` + jsonInt("outputTokens") + ` AS output_tokens,
		` + jsonInt("thinkingTokens") + ` AS thinking_tokens,
		` + jsonInt("cacheReadInputTokens") + ` AS cache_read_tokens,
		` + jsonInt("cacheCreationEphemeral5mInputTokens") + ` AS cache_write_5m_tokens,
		` + jsonInt("cacheCreationEphemeral1hInputTokens") + ` AS cache_write_1h_tokens,
		` + jsonInt("webSearchRequests") + ` AS web_search_requests,
		CASE WHEN jsonb_typeof(m.value->'costUSD') = 'number' THEN (m.value->>'costUSD')::numeric ELSE 0 END AS engine_usd,
		CASE WHEN jsonb_typeof(m.value->'costBasis') = 'string' THEN m.value->>'costBasis' ELSE '' END AS cost_basis
	FROM e
	CROSS JOIN LATERAL (SELECT CASE WHEN e.since_last THEN e.payload->'model_usage_since_last_result'
		ELSE e.payload->'modelUsage' END AS usage) u
	CROSS JOIN LATERAL jsonb_each(CASE WHEN jsonb_typeof(u.usage) = 'object' THEN u.usage ELSE '{}'::jsonb END) AS m(key, value)
	WHERE jsonb_typeof(m.value) = 'object'),
d AS (
	SELECT v.org_id, v.agent_id, v.task_id, v.id, v.session_id, v.model, v.payload, v.created_at, v.since_last, v.cost_basis,
		v.input_tokens - p.input_tokens AS input_tokens,
		v.output_tokens - p.output_tokens AS output_tokens,
		v.thinking_tokens - p.thinking_tokens AS thinking_tokens,
		v.cache_read_tokens - p.cache_read_tokens AS cache_read_tokens,
		v.cache_write_5m_tokens - p.cache_write_5m_tokens AS cache_write_5m_tokens,
		v.cache_write_1h_tokens - p.cache_write_1h_tokens AS cache_write_1h_tokens,
		v.web_search_requests - p.web_search_requests AS web_search_requests,
		v.engine_usd - p.engine_usd AS engine_usd
	FROM v
	CROSS JOIN LATERAL (
		SELECT COALESCE(SUM(input_tokens),0) AS input_tokens, COALESCE(SUM(output_tokens),0) AS output_tokens,
			COALESCE(SUM(thinking_tokens),0) AS thinking_tokens, COALESCE(SUM(cache_read_tokens),0) AS cache_read_tokens,
			COALESCE(SUM(cache_write_5m_tokens),0) AS cache_write_5m_tokens,
			COALESCE(SUM(cache_write_1h_tokens),0) AS cache_write_1h_tokens,
			COALESCE(SUM(web_search_requests),0) AS web_search_requests, COALESCE(SUM(engine_usd),0) AS engine_usd
		FROM model_usage p
		WHERE NOT v.since_last AND v.session_id <> ''
		  AND p.session_id = v.session_id AND p.model = v.model AND p.event_id < v.id) p)
INSERT INTO model_usage (org_id, agent_id, task_id, event_id, session_id, model,
	input_tokens, output_tokens, thinking_tokens, cache_read_tokens,
	cache_write_5m_tokens, cache_write_1h_tokens, web_search_requests,
	engine_usd, cost_basis, created_at, basis, started_at,
	runtime_id, credential_ord, secret_key, secret_slot, credential_kind,
	service_tier, speed, inference_geo, line_uuid)
SELECT d.org_id, d.agent_id, d.task_id, d.id, d.session_id, d.model,
	d.input_tokens, d.output_tokens, d.thinking_tokens, d.cache_read_tokens,
	d.cache_write_5m_tokens, d.cache_write_1h_tokens, d.web_search_requests,
	d.engine_usd, d.cost_basis, d.created_at,
	CASE WHEN d.since_last THEN 'since_last' ELSE 'derived' END,
	CASE WHEN jsonb_typeof(d.payload->'duration_ms') = 'number'
		THEN d.created_at - ((d.payload->>'duration_ms')::numeric::float8 * interval '1 millisecond') END,
	$2::uuid, $3::smallint, $4::text, $5::smallint, $6::text,
	COALESCE(d.payload->'usage'->>'service_tier', ''),
	COALESCE(d.payload->'usage'->>'speed', ''),
	COALESCE(d.payload->'usage'->>'inference_geo', ''),
	CASE WHEN jsonb_typeof(d.payload->'uuid') = 'string' THEN d.payload->>'uuid' END
FROM d
WHERE d.input_tokens <> 0 OR d.output_tokens <> 0 OR d.thinking_tokens <> 0 OR d.cache_read_tokens <> 0
   OR d.cache_write_5m_tokens <> 0 OR d.cache_write_1h_tokens <> 0 OR d.web_search_requests <> 0 OR d.engine_usd <> 0
ON CONFLICT DO NOTHING`

// carriesModelUsage is the cheap pre-check: most runtime lines are transcript.
func carriesModelUsage(raw []byte) bool {
	return (bytes.Contains(raw, []byte(`"modelUsage"`)) || bytes.Contains(raw, []byte(`"model_usage_since_last_result"`))) &&
		bytes.Contains(raw, []byte(`"result"`))
}

// RecordRuntime files a runtime event; a Claude Code `result` event also
// leaves one model_usage row per model, booked against cred.
//
// Two statements on purpose. The recording is the record, the per-model rows
// are derived from it: a line the engine shaped unexpectedly must never cost
// the recording its line, which is what one statement did when a cast failed.
// The second statement's error is returned (wrapped) for the caller to log;
// the line is filed either way.
func (s *Store) RecordRuntime(ctx context.Context, orgID, agentID uuid.UUID, taskID *uuid.UUID, cred CredentialRef, payload json.RawMessage) error {
	if !carriesModelUsage(payload) || !json.Valid(payload) {
		return s.Record(ctx, orgID, agentID, taskID, KindRuntime, payload)
	}
	var id int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO recording_events (org_id, agent_id, task_id, kind, payload)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`, orgID, agentID, taskID, KindRuntime, []byte(payload)).Scan(&id); err != nil {
		return err
	}
	rt, ord, key, slot := cred.columns()
	if _, err := s.pool.Exec(ctx, modelUsageInsert, id, rt, ord, key, slot, cred.Kind); err != nil {
		return fmt.Errorf("model usage of recording event %d: %w", id, err)
	}
	return nil
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

// ModelUsageRow is one agent × model × credential kind over the window.
type ModelUsageRow struct {
	AgentID   uuid.UUID `json:"agent_id"`
	AgentSlug string    `json:"agent_slug"`
	AgentName string    `json:"agent_name"`
	Model     string    `json:"model"`
	// CredentialKind is what the runs went out on (api_key | subscription),
	// empty where the run was not attributable. It says what money means here:
	// on a subscription seat EngineUSD and ListUSD are what the work would have
	// cost through the API, not what was paid.
	CredentialKind string `json:"credential_kind"`
	// Runs counts tasks (a line without a task counts on its own).
	Runs int64 `json:"runs"`
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
	// record (cut off before Claude Code wrote its result, an engine without
	// result lines, or older than the recordings that were still there when
	// 0142 ran).
	RunsWithout int64   `json:"runs_without_breakdown"`
	USDWithout  float64 `json:"usd_without_breakdown"`
}

// CredentialFilter narrows a cost read to one secret value. An empty
// SecretKey means every credential.
type CredentialFilter struct {
	SecretKey  string
	SecretSlot int
}

// ModelUsage sums model_usage per agent, model and credential kind since
// `since`. The rows are deltas, so this is a plain SUM over an index range.
func (s *Store) ModelUsage(ctx context.Context, orgID uuid.UUID, since time.Time, f CredentialFilter) (ModelUsageReport, error) {
	rep := ModelUsageReport{Rows: []ModelUsageRow{}}
	rows, err := s.pool.Query(ctx, `SELECT mu.agent_id, a.slug, a.display_name, mu.model, mu.credential_kind,
		COUNT(DISTINCT COALESCE(mu.task_id::text, mu.event_id::text)),
		SUM(mu.input_tokens), SUM(mu.output_tokens), SUM(mu.thinking_tokens), SUM(mu.cache_read_tokens),
		SUM(mu.cache_write_5m_tokens), SUM(mu.cache_write_1h_tokens), SUM(mu.web_search_requests),
		SUM(mu.engine_usd)::float8
		FROM model_usage mu JOIN agents a ON a.id = mu.agent_id
		WHERE mu.org_id = $1 AND mu.created_at >= $2
		  AND ($3::text = '' OR (mu.secret_key = $3 AND mu.secret_slot = $4::smallint))
		GROUP BY 1,2,3,4,5 ORDER BY SUM(mu.engine_usd) DESC`, orgID, since, f.SecretKey, f.SecretSlot)
	if err != nil {
		return rep, err
	}
	defer rows.Close()
	for rows.Next() {
		var r ModelUsageRow
		if err := rows.Scan(&r.AgentID, &r.AgentSlug, &r.AgentName, &r.Model, &r.CredentialKind, &r.Runs,
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
	// A booking without a breakdown is one no line was linked to (0144).
	err = s.pool.QueryRow(ctx, `SELECT COUNT(DISTINCT ce.task_id), COALESCE(SUM(ce.usd),0)::float8
		FROM cost_entries ce JOIN agents a ON a.id = ce.agent_id
		WHERE a.org_id = $1 AND ce.created_at >= $2 AND ce.task_id IS NOT NULL
		  AND ($3::text = '' OR (ce.secret_key = $3 AND ce.secret_slot = $4::smallint))
		  AND NOT EXISTS (SELECT 1 FROM model_usage mu WHERE mu.cost_entry_id = ce.id)`,
		orgID, since, f.SecretKey, f.SecretSlot).Scan(&rep.RunsWithout, &rep.USDWithout)
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
