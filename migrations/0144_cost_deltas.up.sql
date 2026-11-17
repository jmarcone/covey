-- Cost deltas: model_usage holds what each result line ADDED, not the
-- session's running total, and cost_entries is corrected and linked to it.
--
-- Every Claude Code result line reports the session's running totals
-- (total_cost_usd, modelUsage), restored across --resume. 0142 copied those
-- totals per line, 0143 hid the duplicates behind a DISTINCT ON view — which
-- put a whole session on its last task and hour and sorted the whole table on
-- every request. And the daemon booked the running total per run, the
-- turn-limit handover a second time: resumed sessions were booked about twice
-- (x1.95 over one evening's runs).
--
-- From here on a row is one line's own share per model: the line's
-- model_usage_since_last_result where the CLI writes it (basis since_last),
-- otherwise the running total minus what the session's earlier rows already
-- hold (basis derived). Plain SUMs are then right over any window, and every
-- row sits on the task and the time of the line that produced it.

-- (1) The view is no longer needed, and it pins the columns below.
DROP VIEW model_usage_latest;

-- (2) Where a line came from: which credential paid for it, which price
-- modifiers the API applied, and which cost entry booked it.
ALTER TABLE model_usage
    ADD COLUMN basis           TEXT NOT NULL DEFAULT 'since_last',
    -- created_at is when the line was filed, i.e. when the invocation ended.
    ADD COLUMN started_at      TIMESTAMPTZ,
    ADD COLUMN runtime_id      UUID,
    ADD COLUMN credential_ord  SMALLINT,
    ADD COLUMN secret_key      TEXT,
    ADD COLUMN secret_slot     SMALLINT,
    ADD COLUMN credential_kind TEXT NOT NULL DEFAULT '',
    ADD COLUMN service_tier    TEXT NOT NULL DEFAULT '',
    ADD COLUMN speed           TEXT NOT NULL DEFAULT '',
    ADD COLUMN inference_geo   TEXT NOT NULL DEFAULT '',
    -- The line's own uuid: the same line filed twice (a re-sent event) is
    -- the same consumption, whatever event id it got.
    ADD COLUMN line_uuid       TEXT,
    ADD COLUMN cost_entry_id   BIGINT REFERENCES cost_entries(id) ON DELETE SET NULL;

-- (3) The running totals become deltas: each row minus the row before it of
-- the same session and model. A line without a session stands on its own.
UPDATE model_usage mu SET
    input_tokens          = mu.input_tokens          - d.input_tokens,
    output_tokens         = mu.output_tokens         - d.output_tokens,
    thinking_tokens       = mu.thinking_tokens       - d.thinking_tokens,
    cache_read_tokens     = mu.cache_read_tokens     - d.cache_read_tokens,
    cache_write_5m_tokens = mu.cache_write_5m_tokens - d.cache_write_5m_tokens,
    cache_write_1h_tokens = mu.cache_write_1h_tokens - d.cache_write_1h_tokens,
    web_search_requests   = mu.web_search_requests   - d.web_search_requests,
    engine_usd            = mu.engine_usd            - d.engine_usd,
    basis                 = 'derived'
FROM (
    SELECT id,
        COALESCE(LAG(input_tokens)          OVER w, 0) AS input_tokens,
        COALESCE(LAG(output_tokens)         OVER w, 0) AS output_tokens,
        COALESCE(LAG(thinking_tokens)       OVER w, 0) AS thinking_tokens,
        COALESCE(LAG(cache_read_tokens)     OVER w, 0) AS cache_read_tokens,
        COALESCE(LAG(cache_write_5m_tokens) OVER w, 0) AS cache_write_5m_tokens,
        COALESCE(LAG(cache_write_1h_tokens) OVER w, 0) AS cache_write_1h_tokens,
        COALESCE(LAG(web_search_requests)   OVER w, 0) AS web_search_requests,
        COALESCE(LAG(engine_usd)            OVER w, 0) AS engine_usd
    FROM model_usage
    WINDOW w AS (PARTITION BY COALESCE(NULLIF(session_id, ''), event_id::text), model ORDER BY event_id)
) d
WHERE d.id = mu.id;

-- A model the session used once stays in every later running total; its rows
-- after that add nothing. The down migration restores them from the recording.
DELETE FROM model_usage
WHERE basis = 'derived'
  AND input_tokens = 0 AND output_tokens = 0 AND thinking_tokens = 0
  AND cache_read_tokens = 0 AND cache_write_5m_tokens = 0 AND cache_write_1h_tokens = 0
  AND web_search_requests = 0 AND engine_usd = 0;

-- (4) When the invocation started (the line says how long it took), and the
-- line's uuid — only on the first event that carried it, so that the unique
-- index below can be built on whatever was filed twice.
UPDATE model_usage mu SET
    started_at = CASE WHEN jsonb_typeof(e.payload->'duration_ms') = 'number'
        THEN mu.created_at - ((e.payload->>'duration_ms')::numeric::float8 * interval '1 millisecond') END,
    line_uuid = CASE WHEN e.first THEN e.uuid END
FROM (
    SELECT id, payload,
        CASE WHEN jsonb_typeof(payload->'uuid') = 'string' THEN payload->>'uuid' END AS uuid,
        jsonb_typeof(payload->'uuid') = 'string'
            AND row_number() OVER (PARTITION BY payload->>'uuid' ORDER BY id) = 1 AS first
    FROM recording_events
    WHERE id IN (SELECT event_id FROM model_usage)
) e
WHERE e.id = mu.event_id;

-- (5) cost_entries: what the booking covers, the figure it had before a
-- correction, and the engine session it belonged to.
--
-- basis: invocation (the run's own spend, from a daemon that says so),
-- from_deltas (an older daemon's booking, recomputed from the run's lines),
-- legacy (an older daemon's figure, no lines to recompute it from),
-- corrected (rewritten by this migration). Empty: booked before 0144 and not
-- corrected, or booked by an engine without result lines.
ALTER TABLE cost_entries
    ADD COLUMN basis         TEXT NOT NULL DEFAULT '',
    ADD COLUMN booked_usd    NUMERIC(14,6),
    ADD COLUMN booked_tokens JSONB,
    ADD COLUMN session_id    TEXT NOT NULL DEFAULT '';

-- Each line belongs to the first booking of its task that came after it: the
-- daemon sends the cost once the run's process (and its handover) ended.
UPDATE model_usage mu SET cost_entry_id = (
    SELECT ce.id FROM cost_entries ce
    WHERE ce.agent_id = mu.agent_id AND ce.task_id = mu.task_id AND ce.created_at >= mu.created_at
    ORDER BY ce.created_at, ce.id
    LIMIT 1)
WHERE mu.task_id IS NOT NULL;

-- The booking becomes the sum of its lines' deltas (cache writes: 5 min +
-- 1 h), and keeps what it said before. Only ever downwards: the old booking
-- counted running totals, so its lines cannot add up to more — where they
-- would, something else is wrong, and the entry stays as it was.
UPDATE cost_entries ce SET
    booked_usd    = ce.usd,
    booked_tokens = jsonb_build_object(
        'input_tokens', ce.input_tokens, 'output_tokens', ce.output_tokens,
        'cache_read_tokens', ce.cache_read_tokens, 'cache_creation_tokens', ce.cache_creation_tokens),
    usd                   = s.usd,
    input_tokens          = s.input_tokens,
    output_tokens         = s.output_tokens,
    cache_read_tokens     = s.cache_read_tokens,
    cache_creation_tokens = s.cache_creation_tokens,
    basis                 = 'corrected'
FROM (
    SELECT cost_entry_id,
        SUM(engine_usd) AS usd,
        SUM(input_tokens) AS input_tokens,
        SUM(output_tokens) AS output_tokens,
        SUM(cache_read_tokens) AS cache_read_tokens,
        SUM(cache_write_5m_tokens + cache_write_1h_tokens) AS cache_creation_tokens
    FROM model_usage
    WHERE cost_entry_id IS NOT NULL
    GROUP BY cost_entry_id
) s
WHERE s.cost_entry_id = ce.id
  AND s.usd <= ce.usd + 0.000001;

-- (6) The credential by its secret, not only by its place in the runtime:
-- runtime_id/credential_ord name a position, and Reorder moves values between
-- positions. Resolved through (runtime_id, ord) as the credentials stand NOW,
-- which is right only if no Reorder happened since the run. The instance this
-- was written against has a single credential, so there it holds.
UPDATE cost_entries ce SET secret_key = rc.secret_key, secret_slot = rc.secret_slot
FROM runtime_credentials rc
WHERE rc.runtime_id = ce.runtime_id AND rc.ord = ce.credential_ord AND ce.secret_key IS NULL;

UPDATE model_usage mu SET
    runtime_id      = ce.runtime_id,
    credential_ord  = ce.credential_ord,
    secret_key      = ce.secret_key,
    secret_slot     = ce.secret_slot,
    credential_kind = COALESCE(rc.kind, '')
FROM cost_entries ce
LEFT JOIN runtime_credentials rc ON rc.runtime_id = ce.runtime_id AND rc.ord = ce.credential_ord
WHERE mu.cost_entry_id = ce.id AND ce.runtime_id IS NOT NULL;

-- (7) The lookups the write and read paths run on.
-- The derived fallback: what a session's earlier rows already hold, per model.
CREATE INDEX idx_model_usage_session ON model_usage (session_id, model, event_id) WHERE session_id <> '';
-- Booking a run: its task's lines not yet booked.
CREATE INDEX idx_model_usage_unbooked ON model_usage (task_id) WHERE cost_entry_id IS NULL;
-- Lines of a booking (and ON DELETE SET NULL).
CREATE INDEX idx_model_usage_cost_entry ON model_usage (cost_entry_id) WHERE cost_entry_id IS NOT NULL;
-- Per credential over time.
CREATE INDEX idx_model_usage_secret ON model_usage (org_id, secret_key, secret_slot, created_at);
-- One line, filed once.
CREATE UNIQUE INDEX idx_model_usage_line ON model_usage (line_uuid, model) WHERE line_uuid IS NOT NULL;
