-- Cost gaps: the spend of a process that ended without a result line.
--
-- Every Claude Code result line carries the session's running total
-- (modelUsage, total_cost_usd) and the line's own share
-- (model_usage_since_last_result). model_usage files the share, and a run is
-- booked from it. A process that dies without a result line (killed, timed
-- out, its sandbox gone) spent money that no share ever reports: Claude Code
-- keeps it in the session's running total, and the next line's share starts
-- after it. Session 5eeb07c2 booked $0.9644 of $2.3385.
--
-- From here on a line also files, per model, how much more the running total
-- grew since the session's previous line than the line's own share says, as
-- a row of basis 'gap', and books those rows as a cost entry of basis 'gap'
-- (observability.modelUsageGapInsert, gapBooking). A line then has up to two
-- rows per model, told apart by basis.
ALTER TABLE model_usage DROP CONSTRAINT model_usage_event_id_model_key;
ALTER TABLE model_usage ADD CONSTRAINT model_usage_event_id_model_basis_key UNIQUE (event_id, model, basis);

-- The lines filed so far, in the order they were filed, with exactly the
-- statements the code runs per line. Only lines that left a since_last row
-- and whose recording, and their previous line's, is still there; the
-- credential is the one their rows were filed with.
DO $$
DECLARE
    ln record;
BEGIN
    FOR ln IN
        SELECT DISTINCT ON (mu.event_id) mu.event_id, mu.runtime_id, mu.credential_ord,
            mu.secret_key, mu.secret_slot, mu.credential_kind
        FROM model_usage mu
        WHERE mu.basis = 'since_last' AND mu.session_id <> ''
        ORDER BY mu.event_id, mu.id
    LOOP
        WITH e AS (
        	SELECT id, org_id, agent_id, task_id, payload, created_at, payload->>'session_id' AS session_id
        	FROM recording_events
        	WHERE id = ln.event_id AND payload->>'type' = 'result'
        	  AND jsonb_typeof(payload->'session_id') = 'string' AND payload->>'session_id' <> ''
        	  AND jsonb_typeof(payload->'model_usage_since_last_result') = 'object'
        	  AND jsonb_typeof(payload->'modelUsage') = 'object'),
        prev AS (
        	SELECT r.payload->'modelUsage' AS usage
        	FROM recording_events r
        	WHERE r.id = (SELECT max(p.event_id) FROM model_usage p, e
        	              WHERE p.session_id = e.session_id AND p.event_id < e.id)
        	  AND jsonb_typeof(r.payload->'modelUsage') = 'object'),
        g AS (
        	SELECT e.id, e.org_id, e.agent_id, e.task_id, e.created_at, e.session_id, m.key AS model,
        		CASE WHEN jsonb_typeof(m.value->'inputTokens') = 'number' THEN (m.value->>'inputTokens')::numeric::bigint ELSE 0 END AS c_input_tokens,
        		CASE WHEN jsonb_typeof(m.value->'outputTokens') = 'number' THEN (m.value->>'outputTokens')::numeric::bigint ELSE 0 END AS c_output_tokens,
        		CASE WHEN jsonb_typeof(m.value->'thinkingTokens') = 'number' THEN (m.value->>'thinkingTokens')::numeric::bigint ELSE 0 END AS c_thinking_tokens,
        		CASE WHEN jsonb_typeof(m.value->'cacheReadInputTokens') = 'number' THEN (m.value->>'cacheReadInputTokens')::numeric::bigint ELSE 0 END AS c_cache_read_tokens,
        		CASE WHEN jsonb_typeof(m.value->'cacheCreationEphemeral5mInputTokens') = 'number' THEN (m.value->>'cacheCreationEphemeral5mInputTokens')::numeric::bigint ELSE 0 END AS c_cache_write_5m_tokens,
        		CASE WHEN jsonb_typeof(m.value->'cacheCreationEphemeral1hInputTokens') = 'number' THEN (m.value->>'cacheCreationEphemeral1hInputTokens')::numeric::bigint ELSE 0 END AS c_cache_write_1h_tokens,
        		CASE WHEN jsonb_typeof(m.value->'webSearchRequests') = 'number' THEN (m.value->>'webSearchRequests')::numeric::bigint ELSE 0 END AS c_web_search_requests,
        		CASE WHEN jsonb_typeof(m.value->'costUSD') = 'number' THEN (m.value->>'costUSD')::numeric ELSE 0 END AS c_engine_usd,
        		CASE WHEN jsonb_typeof((prev.usage->m.key)->'inputTokens') = 'number' THEN ((prev.usage->m.key)->>'inputTokens')::numeric::bigint ELSE 0 END AS p_input_tokens,
        		CASE WHEN jsonb_typeof((prev.usage->m.key)->'outputTokens') = 'number' THEN ((prev.usage->m.key)->>'outputTokens')::numeric::bigint ELSE 0 END AS p_output_tokens,
        		CASE WHEN jsonb_typeof((prev.usage->m.key)->'thinkingTokens') = 'number' THEN ((prev.usage->m.key)->>'thinkingTokens')::numeric::bigint ELSE 0 END AS p_thinking_tokens,
        		CASE WHEN jsonb_typeof((prev.usage->m.key)->'cacheReadInputTokens') = 'number' THEN ((prev.usage->m.key)->>'cacheReadInputTokens')::numeric::bigint ELSE 0 END AS p_cache_read_tokens,
        		CASE WHEN jsonb_typeof((prev.usage->m.key)->'cacheCreationEphemeral5mInputTokens') = 'number' THEN ((prev.usage->m.key)->>'cacheCreationEphemeral5mInputTokens')::numeric::bigint ELSE 0 END AS p_cache_write_5m_tokens,
        		CASE WHEN jsonb_typeof((prev.usage->m.key)->'cacheCreationEphemeral1hInputTokens') = 'number' THEN ((prev.usage->m.key)->>'cacheCreationEphemeral1hInputTokens')::numeric::bigint ELSE 0 END AS p_cache_write_1h_tokens,
        		CASE WHEN jsonb_typeof((prev.usage->m.key)->'webSearchRequests') = 'number' THEN ((prev.usage->m.key)->>'webSearchRequests')::numeric::bigint ELSE 0 END AS p_web_search_requests,
        		CASE WHEN jsonb_typeof((prev.usage->m.key)->'costUSD') = 'number' THEN ((prev.usage->m.key)->>'costUSD')::numeric ELSE 0 END AS p_engine_usd,
        		CASE WHEN jsonb_typeof((e.payload->'model_usage_since_last_result'->m.key)->'inputTokens') = 'number' THEN ((e.payload->'model_usage_since_last_result'->m.key)->>'inputTokens')::numeric::bigint ELSE 0 END AS s_input_tokens,
        		CASE WHEN jsonb_typeof((e.payload->'model_usage_since_last_result'->m.key)->'outputTokens') = 'number' THEN ((e.payload->'model_usage_since_last_result'->m.key)->>'outputTokens')::numeric::bigint ELSE 0 END AS s_output_tokens,
        		CASE WHEN jsonb_typeof((e.payload->'model_usage_since_last_result'->m.key)->'thinkingTokens') = 'number' THEN ((e.payload->'model_usage_since_last_result'->m.key)->>'thinkingTokens')::numeric::bigint ELSE 0 END AS s_thinking_tokens,
        		CASE WHEN jsonb_typeof((e.payload->'model_usage_since_last_result'->m.key)->'cacheReadInputTokens') = 'number' THEN ((e.payload->'model_usage_since_last_result'->m.key)->>'cacheReadInputTokens')::numeric::bigint ELSE 0 END AS s_cache_read_tokens,
        		CASE WHEN jsonb_typeof((e.payload->'model_usage_since_last_result'->m.key)->'cacheCreationEphemeral5mInputTokens') = 'number' THEN ((e.payload->'model_usage_since_last_result'->m.key)->>'cacheCreationEphemeral5mInputTokens')::numeric::bigint ELSE 0 END AS s_cache_write_5m_tokens,
        		CASE WHEN jsonb_typeof((e.payload->'model_usage_since_last_result'->m.key)->'cacheCreationEphemeral1hInputTokens') = 'number' THEN ((e.payload->'model_usage_since_last_result'->m.key)->>'cacheCreationEphemeral1hInputTokens')::numeric::bigint ELSE 0 END AS s_cache_write_1h_tokens,
        		CASE WHEN jsonb_typeof((e.payload->'model_usage_since_last_result'->m.key)->'webSearchRequests') = 'number' THEN ((e.payload->'model_usage_since_last_result'->m.key)->>'webSearchRequests')::numeric::bigint ELSE 0 END AS s_web_search_requests,
        		CASE WHEN jsonb_typeof((e.payload->'model_usage_since_last_result'->m.key)->'costUSD') = 'number' THEN ((e.payload->'model_usage_since_last_result'->m.key)->>'costUSD')::numeric ELSE 0 END AS s_engine_usd
        	FROM e
        	CROSS JOIN prev
        	CROSS JOIN LATERAL jsonb_each(e.payload->'modelUsage') AS m(key, value)
        	WHERE jsonb_typeof(m.value) = 'object')
        INSERT INTO model_usage (org_id, agent_id, task_id, event_id, session_id, model,
        	input_tokens, output_tokens, thinking_tokens, cache_read_tokens,
        	cache_write_5m_tokens, cache_write_1h_tokens, web_search_requests,
        	engine_usd, created_at, basis,
        	runtime_id, credential_ord, secret_key, secret_slot, credential_kind)
        SELECT g.org_id, g.agent_id, g.task_id, g.id, g.session_id, g.model,
        	GREATEST(g.c_input_tokens - g.p_input_tokens - g.s_input_tokens, 0),
        	GREATEST(g.c_output_tokens - g.p_output_tokens - g.s_output_tokens, 0),
        	GREATEST(g.c_thinking_tokens - g.p_thinking_tokens - g.s_thinking_tokens, 0),
        	GREATEST(g.c_cache_read_tokens - g.p_cache_read_tokens - g.s_cache_read_tokens, 0),
        	GREATEST(g.c_cache_write_5m_tokens - g.p_cache_write_5m_tokens - g.s_cache_write_5m_tokens, 0),
        	GREATEST(g.c_cache_write_1h_tokens - g.p_cache_write_1h_tokens - g.s_cache_write_1h_tokens, 0),
        	GREATEST(g.c_web_search_requests - g.p_web_search_requests - g.s_web_search_requests, 0),
        	g.c_engine_usd - g.p_engine_usd - g.s_engine_usd, g.created_at, 'gap',
        	ln.runtime_id::uuid, ln.credential_ord::smallint, ln.secret_key::text, ln.secret_slot::smallint, ln.credential_kind::text
        FROM g
        WHERE g.c_engine_usd - g.p_engine_usd - g.s_engine_usd > 0.000001
        ON CONFLICT DO NOTHING;
        WITH g AS (
        	SELECT id FROM model_usage WHERE event_id = ln.event_id AND basis = 'gap' AND cost_entry_id IS NULL
        	FOR UPDATE),
        s AS (
        	SELECT mu.agent_id, mu.task_id, mu.session_id, mu.runtime_id, mu.credential_ord, mu.secret_key, mu.secret_slot,
        		SUM(mu.engine_usd) AS usd, SUM(mu.input_tokens) AS input_tokens, SUM(mu.output_tokens) AS output_tokens,
        		SUM(mu.cache_read_tokens) AS cache_read_tokens,
        		SUM(mu.cache_write_5m_tokens + mu.cache_write_1h_tokens) AS cache_creation_tokens,
        		(array_agg(mu.model ORDER BY mu.engine_usd DESC))[1] AS model, MAX(mu.created_at) AS created_at
        	FROM model_usage mu WHERE mu.id IN (SELECT id FROM g)
        	GROUP BY 1, 2, 3, 4, 5, 6, 7),
        ins AS (
        	INSERT INTO cost_entries (agent_id, task_id, usd, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
        		model, runtime_id, credential_ord, secret_key, secret_slot, basis, session_id, created_at)
        	SELECT agent_id, task_id, usd, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
        		model, runtime_id, credential_ord, secret_key, secret_slot, 'gap', session_id, created_at
        	FROM s
        	RETURNING id)
        UPDATE model_usage mu SET cost_entry_id = (SELECT id FROM ins)
        WHERE mu.id IN (SELECT id FROM g);
    END LOOP;
END $$;
