-- Back to running totals per line and the bookings as they were.

-- The bookings this migration corrected get their figures back.
UPDATE cost_entries SET
    usd                   = booked_usd,
    input_tokens          = COALESCE((booked_tokens->>'input_tokens')::bigint, input_tokens),
    output_tokens         = COALESCE((booked_tokens->>'output_tokens')::bigint, output_tokens),
    cache_read_tokens     = COALESCE((booked_tokens->>'cache_read_tokens')::bigint, cache_read_tokens),
    cache_creation_tokens = COALESCE((booked_tokens->>'cache_creation_tokens')::bigint, cache_creation_tokens)
WHERE booked_usd IS NOT NULL;

-- Deltas become running totals again: each row plus everything before it of
-- the same session and model.
UPDATE model_usage mu SET
    input_tokens          = c.input_tokens,
    output_tokens         = c.output_tokens,
    thinking_tokens       = c.thinking_tokens,
    cache_read_tokens     = c.cache_read_tokens,
    cache_write_5m_tokens = c.cache_write_5m_tokens,
    cache_write_1h_tokens = c.cache_write_1h_tokens,
    web_search_requests   = c.web_search_requests,
    engine_usd            = c.engine_usd
FROM (
    SELECT id,
        SUM(input_tokens)          OVER w AS input_tokens,
        SUM(output_tokens)         OVER w AS output_tokens,
        SUM(thinking_tokens)       OVER w AS thinking_tokens,
        SUM(cache_read_tokens)     OVER w AS cache_read_tokens,
        SUM(cache_write_5m_tokens) OVER w AS cache_write_5m_tokens,
        SUM(cache_write_1h_tokens) OVER w AS cache_write_1h_tokens,
        SUM(web_search_requests)   OVER w AS web_search_requests,
        SUM(engine_usd)            OVER w AS engine_usd
    FROM model_usage
    WINDOW w AS (PARTITION BY COALESCE(NULLIF(session_id, ''), event_id::text), model ORDER BY event_id
                 ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
) c
WHERE c.id = mu.id;

-- The rows that added nothing were deleted on the way up; the running totals
-- they held are still in the recording (where it has not been pruned). Same
-- expressions as 0142's backfill, with the casts guarded.
INSERT INTO model_usage (org_id, agent_id, task_id, event_id, session_id, model,
    input_tokens, output_tokens, thinking_tokens, cache_read_tokens,
    cache_write_5m_tokens, cache_write_1h_tokens, web_search_requests,
    engine_usd, cost_basis, created_at)
SELECT e.org_id, e.agent_id, e.task_id, e.id, COALESCE(e.payload->>'session_id', ''), m.key,
    CASE WHEN jsonb_typeof(m.value->'inputTokens') = 'number' THEN (m.value->>'inputTokens')::numeric::bigint ELSE 0 END,
    CASE WHEN jsonb_typeof(m.value->'outputTokens') = 'number' THEN (m.value->>'outputTokens')::numeric::bigint ELSE 0 END,
    CASE WHEN jsonb_typeof(m.value->'thinkingTokens') = 'number' THEN (m.value->>'thinkingTokens')::numeric::bigint ELSE 0 END,
    CASE WHEN jsonb_typeof(m.value->'cacheReadInputTokens') = 'number' THEN (m.value->>'cacheReadInputTokens')::numeric::bigint ELSE 0 END,
    CASE WHEN jsonb_typeof(m.value->'cacheCreationEphemeral5mInputTokens') = 'number' THEN (m.value->>'cacheCreationEphemeral5mInputTokens')::numeric::bigint ELSE 0 END,
    CASE WHEN jsonb_typeof(m.value->'cacheCreationEphemeral1hInputTokens') = 'number' THEN (m.value->>'cacheCreationEphemeral1hInputTokens')::numeric::bigint ELSE 0 END,
    CASE WHEN jsonb_typeof(m.value->'webSearchRequests') = 'number' THEN (m.value->>'webSearchRequests')::numeric::bigint ELSE 0 END,
    CASE WHEN jsonb_typeof(m.value->'costUSD') = 'number' THEN (m.value->>'costUSD')::numeric ELSE 0 END,
    CASE WHEN jsonb_typeof(m.value->'costBasis') = 'string' THEN m.value->>'costBasis' ELSE '' END,
    e.created_at
FROM recording_events e
CROSS JOIN LATERAL jsonb_each(e.payload->'modelUsage') AS m(key, value)
WHERE e.kind = 'runtime'
  AND e.payload->>'type' = 'result'
  AND jsonb_typeof(e.payload->'modelUsage') = 'object'
  AND jsonb_typeof(m.value) = 'object'
ON CONFLICT (event_id, model) DO NOTHING;

DROP INDEX IF EXISTS idx_model_usage_session;
DROP INDEX IF EXISTS idx_model_usage_unbooked;
DROP INDEX IF EXISTS idx_model_usage_cost_entry;
DROP INDEX IF EXISTS idx_model_usage_secret;
DROP INDEX IF EXISTS idx_model_usage_line;

ALTER TABLE model_usage
    DROP COLUMN basis,
    DROP COLUMN started_at,
    DROP COLUMN runtime_id,
    DROP COLUMN credential_ord,
    DROP COLUMN secret_key,
    DROP COLUMN secret_slot,
    DROP COLUMN credential_kind,
    DROP COLUMN service_tier,
    DROP COLUMN speed,
    DROP COLUMN inference_geo,
    DROP COLUMN line_uuid,
    DROP COLUMN cost_entry_id;

-- secret_key/secret_slot on cost_entries are 0047's columns; the values filled
-- in on the way up are right and stay.
ALTER TABLE cost_entries
    DROP COLUMN basis,
    DROP COLUMN booked_usd,
    DROP COLUMN booked_tokens,
    DROP COLUMN session_id;

-- 0143, verbatim.
-- A session can end more than once: when a run hits its turn limit, the daemon
-- resumes the SAME Claude Code session for a short handover, and that second
-- `result` line reports the session's usage CUMULATIVELY. Summing both lines
-- counted the main run twice. What a session consumed is its LAST line, per
-- model; lines without a session id stand on their own.
CREATE VIEW model_usage_latest AS
SELECT DISTINCT ON (COALESCE(NULLIF(session_id, ''), event_id::text), model) *
FROM model_usage
ORDER BY COALESCE(NULLIF(session_id, ''), event_id::text), model, event_id DESC;
