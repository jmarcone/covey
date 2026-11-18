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

// The subscription meter (migrations 0142, 0145): the provider's own reading
// of a credential's utilisation, from two sources covey can tell apart.
//
//   - usage_cmd: `claude -p /usage`, asked by covey once per credential and
//     cached. Prose, whole percents, a rate limit of its own; a rate-limited
//     answer is the engine's cached one (stale), up to an hour old.
//   - inband: Claude Code's rate_limit_event in the run's own stream — every
//     window the provider's headers name, its utilisation and its reset as
//     an epoch. Free, but only there while a run runs.
//
// Both are keyed on the secret value (a Reorder moves places, not values), and
// both read an ACCOUNT-wide meter: whatever else uses the account — a person
// in claude.ai, another machine — moves it too.

// Reading sources and the window the plan's price is spread over.
const (
	SourceUsageCmd = "usage_cmd"
	SourceInband   = "inband"
	SourceManual   = "manual"
	// WindowSevenDay is the weekly all-models window. Other weekly windows
	// (seven_day_opus, …) are sub-limits of the same allowance: their points
	// are shown, never priced — that would count the same work twice.
	WindowSevenDay = "seven_day"
)

// MeterReading is one reading to file.
type MeterReading struct {
	Credential CredentialRef
	Source     string
	WindowKind string
	Supported  bool
	// WindowPercent/WeekPercent: negative means "not reported".
	WindowPercent, WeekPercent float64
	// WindowResets/WeekResets: the reset as the engine printed it.
	WindowResets, WeekResets string
	// ResetsAt is the reset of the reading's window as an instant (zero:
	// unknown).
	ResetsAt time.Time
	// Status is the provider's (allowed, allowed_warning, rejected), Reason
	// covey's code for a reading without a fresh figure.
	Status, Reason string
	Stale          bool
	// FetchedAt is when the figure was read from the provider; zero means
	// "when it arrived", which is what received_at says anyway.
	FetchedAt time.Time
}

// secret returns the nullable secret columns.
func (c CredentialRef) secret() (key *string, slot *int) {
	if c.SecretKey == "" {
		return nil, nil
	}
	return &c.SecretKey, &c.SecretSlot
}

// AddUsageReading keeps a provider answer. A reading for a credential without
// a place in a runtime is not kept: nobody could attribute it.
func (s *Store) AddUsageReading(ctx context.Context, orgID uuid.UUID, r MeterReading) error {
	if r.Credential.RuntimeID == uuid.Nil || r.Credential.Ord < 0 {
		return nil
	}
	pct := func(v float64) *float64 {
		if v < 0 {
			return nil
		}
		return &v
	}
	var fetched, resets *time.Time
	if !r.FetchedAt.IsZero() {
		fetched = &r.FetchedAt
	}
	if !r.ResetsAt.IsZero() {
		resets = &r.ResetsAt
	}
	if r.Source == "" {
		r.Source = SourceUsageCmd
	}
	if r.WindowKind == "" {
		r.WindowKind = WindowSevenDay
	}
	key, slot := r.Credential.secret()
	_, err := s.pool.Exec(ctx, `INSERT INTO usage_readings (org_id, runtime_id, credential_ord,
		secret_key, secret_slot, credential_kind, source, window_kind, supported,
		window_percent, week_percent, window_resets, week_resets, resets_at, status, reason, stale,
		fetched_at, received_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,COALESCE($18::timestamptz, now()), now())
		ON CONFLICT DO NOTHING`,
		orgID, r.Credential.RuntimeID, r.Credential.Ord, key, slot, r.Credential.Kind, r.Source, r.WindowKind,
		r.Supported, pct(r.WindowPercent), pct(r.WeekPercent),
		strings.TrimSpace(r.WindowResets), strings.TrimSpace(r.WeekResets), resets,
		r.Status, r.Reason, r.Stale, fetched)
	return err
}

// IsRateLimitEvent is the cheap pre-check for a Claude Code rate_limit_event.
func IsRateLimitEvent(raw []byte) bool {
	return bytes.Contains(raw, []byte(`"rate_limit_event"`)) && bytes.Contains(raw, []byte(`"rate_limit_info"`))
}

// inbandInsert files a rate_limit_event's windows ($7) as readings of the
// credential $2..$6 (runtime_id, ord, secret_key, secret_slot, kind) — the
// expression of the backfill in 0145: one row per window, five-hour windows in
// window_percent, the weekly ones in week_percent, the utilisation in percent.
// A line from before unifiedWindows names its one window in rateLimitType.
const inbandInsert = `
INSERT INTO usage_readings (org_id, runtime_id, credential_ord, secret_key, secret_slot, credential_kind,
	source, window_kind, supported, window_percent, week_percent, resets_at, status, stale, fetched_at, received_at)
SELECT $1::uuid, $2::uuid, $3::int, $4::text, $5::smallint, $6::text, 'inband', w.key, true,
	CASE WHEN w.key LIKE 'five_hour%' THEN w.pct END,
	CASE WHEN w.key NOT LIKE 'five_hour%' THEN w.pct END,
	w.resets_at, w.status, false, now(), now()
FROM (SELECT $7::jsonb AS payload) e
CROSS JOIN LATERAL (
	SELECT m.key,
		round((m.value->>'utilization')::numeric * 100, 4)::float8 AS pct,
		CASE WHEN jsonb_typeof(m.value->'resetsAt') = 'number'
			THEN to_timestamp((m.value->>'resetsAt')::float8) END AS resets_at,
		CASE WHEN jsonb_typeof(m.value->'status') = 'string' THEN m.value->>'status' ELSE '' END AS status
	FROM jsonb_each(CASE
		WHEN jsonb_typeof(e.payload->'rate_limit_info'->'unifiedWindows') = 'object'
			THEN e.payload->'rate_limit_info'->'unifiedWindows'
		WHEN jsonb_typeof(e.payload->'rate_limit_info'->'rateLimitType') = 'string'
			THEN jsonb_build_object(e.payload->'rate_limit_info'->>'rateLimitType', e.payload->'rate_limit_info')
		ELSE '{}'::jsonb END) AS m(key, value)
	WHERE jsonb_typeof(m.value) = 'object' AND jsonb_typeof(m.value->'utilization') = 'number') w
WHERE e.payload->>'type' = 'rate_limit_event'
ON CONFLICT DO NOTHING`

// AddInbandReadings files a Claude Code rate_limit_event as one reading per
// window it names (five_hour, seven_day, …), source inband, against the
// credential the run went out on. A line of another type, or a run without a
// credential, files nothing.
func (s *Store) AddInbandReadings(ctx context.Context, orgID uuid.UUID, cred CredentialRef, payload json.RawMessage) error {
	if cred.RuntimeID == uuid.Nil || cred.Ord < 0 || !IsRateLimitEvent(payload) || !json.Valid(payload) {
		return nil
	}
	key, slot := cred.secret()
	_, err := s.pool.Exec(ctx, inbandInsert, orgID, cred.RuntimeID, cred.Ord, key, slot, cred.Kind, []byte(payload))
	return err
}

// UsageReading is one reading of the provider's meter.
type UsageReading struct {
	RuntimeID      uuid.UUID `json:"runtime_id"`
	CredentialOrd  int       `json:"credential_ord"`
	SecretKey      *string   `json:"secret_key"`
	SecretSlot     *int      `json:"secret_slot"`
	CredentialKind string    `json:"credential_kind"`
	// Source: usage_cmd | inband | manual. WindowKind: the provider's window
	// (seven_day, five_hour, …).
	Source        string     `json:"source"`
	WindowKind    string     `json:"window_kind"`
	Supported     bool       `json:"supported"`
	WindowPercent *float64   `json:"window_percent"`
	WeekPercent   *float64   `json:"week_percent"`
	WindowResets  string     `json:"window_resets,omitempty"`
	WeekResets    string     `json:"week_resets,omitempty"`
	ResetsAt      *time.Time `json:"resets_at"`
	Status        string     `json:"status"`
	// Reason is covey's code for a reading without a fresh figure
	// (no_runtime_config, no_reporter, exec_failed, unparsed, stale).
	Reason     string    `json:"reason"`
	Stale      bool      `json:"stale"`
	FetchedAt  time.Time `json:"fetched_at"`
	ReceivedAt time.Time `json:"received_at"`
	// CostUSD is the measured list price on this credential, from the start
	// of the requested window up to this reading: the per-line usage
	// (model_usage, Claude Code's own price per line) of the runs that went
	// out on the same secret value. Not the booked cost, and nothing of any
	// other credential. Nil for a reading that names no secret.
	CostUSD *float64 `json:"cost_usd"`
}

// UsageReadings returns the readings since `since`, oldest first — the
// unsupported ones too, with their reason, so a credential that cannot be read
// shows as such — each with the measured cost on its credential up to it.
//
// One window-function stream: the credential's model_usage lines and its
// readings in time order, the lines' cost summed as it goes. A line at the
// same instant as a reading counts before it.
func (s *Store) UsageReadings(ctx context.Context, orgID uuid.UUID, since time.Time) ([]UsageReading, error) {
	rows, err := s.pool.Query(ctx, `WITH ev AS (
		SELECT mu.secret_key, mu.secret_slot, mu.created_at AS at, 0 AS o, mu.engine_usd AS usd, NULL::bigint AS reading
		FROM model_usage mu
		WHERE mu.org_id = $1 AND mu.created_at >= $2 AND mu.secret_key IS NOT NULL
		UNION ALL
		SELECT r.secret_key, r.secret_slot, r.fetched_at, 1, 0, r.id
		FROM usage_readings r
		WHERE r.org_id = $1 AND r.fetched_at >= $2),
	c AS (
		SELECT reading, SUM(usd) OVER (PARTITION BY secret_key, secret_slot ORDER BY at, o
			ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS cum
		FROM ev)
	SELECT r.runtime_id, r.credential_ord, r.secret_key, r.secret_slot, r.credential_kind, r.source, r.window_kind,
		r.supported, r.window_percent, r.week_percent, r.window_resets, r.week_resets, r.resets_at,
		r.status, r.reason, r.stale, r.fetched_at, r.received_at,
		CASE WHEN r.secret_key IS NOT NULL THEN c.cum::float8 END
	FROM c JOIN usage_readings r ON r.id = c.reading
	ORDER BY r.fetched_at, r.id`, orgID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageReading{}
	for rows.Next() {
		var u UsageReading
		if err := rows.Scan(&u.RuntimeID, &u.CredentialOrd, &u.SecretKey, &u.SecretSlot, &u.CredentialKind,
			&u.Source, &u.WindowKind, &u.Supported, &u.WindowPercent, &u.WeekPercent,
			&u.WindowResets, &u.WeekResets, &u.ResetsAt, &u.Status, &u.Reason, &u.Stale,
			&u.FetchedAt, &u.ReceivedAt, &u.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
