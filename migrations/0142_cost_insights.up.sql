-- Cost insights: what a run consumed PER MODEL, and what the provider said
-- about the subscription while it ran.
--
-- model_usage: Claude Code's final `result` event carries `modelUsage` — tokens
-- per model (an Opus run also makes small Haiku calls), the cache writes split
-- into 5-minute and 1-hour, thinking and web searches, and the engine's own
-- price per model. cost_entries keeps one model per run, so the split was only
-- ever in the recording, and recordings are pruned after their retention. One
-- row per (result event, model); event_id makes filing idempotent.
CREATE TABLE model_usage (
    id               BIGSERIAL PRIMARY KEY,
    org_id           UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    agent_id         UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    task_id          UUID,
    event_id         BIGINT NOT NULL,
    session_id       TEXT NOT NULL DEFAULT '',
    model            TEXT NOT NULL,
    input_tokens     BIGINT NOT NULL DEFAULT 0,
    output_tokens    BIGINT NOT NULL DEFAULT 0,
    thinking_tokens  BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    cache_write_5m_tokens BIGINT NOT NULL DEFAULT 0,
    cache_write_1h_tokens BIGINT NOT NULL DEFAULT 0,
    web_search_requests BIGINT NOT NULL DEFAULT 0,
    engine_usd       NUMERIC(14,6) NOT NULL DEFAULT 0,
    cost_basis       TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (event_id, model)
);
CREATE INDEX idx_model_usage_org ON model_usage (org_id, created_at);
CREATE INDEX idx_model_usage_task ON model_usage (task_id);

-- Backfill from the recordings that are still there.
INSERT INTO model_usage (org_id, agent_id, task_id, event_id, session_id, model,
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
WHERE e.kind = 'runtime'
  AND e.payload->>'type' = 'result'
  AND jsonb_typeof(e.payload->'modelUsage') = 'object'
  AND jsonb_typeof(m.value) = 'object'
ON CONFLICT (event_id, model) DO NOTHING;

-- usage_readings: the provider's own utilisation figure for a credential
-- (`claude -p /usage`), which covey already asks for on every wake but kept
-- only in memory for five minutes. Kept here it becomes a history that can be
-- laid next to the list-price cost of the same hours.
CREATE TABLE usage_readings (
    id             BIGSERIAL PRIMARY KEY,
    org_id         UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    runtime_id     UUID NOT NULL,
    credential_ord INT NOT NULL,
    supported      BOOLEAN NOT NULL,
    window_percent DOUBLE PRECISION,
    week_percent   DOUBLE PRECISION,
    window_resets  TEXT NOT NULL DEFAULT '',
    week_resets    TEXT NOT NULL DEFAULT '',
    stale          BOOLEAN NOT NULL DEFAULT false,
    fetched_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_usage_readings_org ON usage_readings (org_id, fetched_at);
