DROP INDEX IF EXISTS idx_backlog_tasks_sticky;
DROP INDEX IF EXISTS idx_backlog_tasks_topic_done;
DROP INDEX IF EXISTS idx_backlog_tasks_session_successor;
DROP INDEX IF EXISTS idx_backlog_tasks_sticky_from;
ALTER TABLE agents
    DROP COLUMN IF EXISTS sticky_policy,
    DROP COLUMN IF EXISTS sticky_window_minutes,
    DROP COLUMN IF EXISTS sticky_context_cap;
ALTER TABLE backlog_tasks
    DROP COLUMN IF EXISTS first_turn_input,
    DROP COLUMN IF EXISTS first_turn_cache_read,
    DROP COLUMN IF EXISTS first_turn_cache_write_5m,
    DROP COLUMN IF EXISTS first_turn_cache_write_1h,
    DROP COLUMN IF EXISTS sticky_decision,
    DROP COLUMN IF EXISTS sticky_reason,
    DROP COLUMN IF EXISTS sticky_policy,
    DROP COLUMN IF EXISTS sticky_from,
    DROP COLUMN IF EXISTS sticky_gap_s,
    DROP COLUMN IF EXISTS sticky_context,
    DROP COLUMN IF EXISTS session_finished_at,
    DROP COLUMN IF EXISTS session_context_tokens,
    DROP COLUMN IF EXISTS session_credential,
    DROP COLUMN IF EXISTS session_prompt,
    DROP COLUMN IF EXISTS session_memory,
    DROP COLUMN IF EXISTS session_basis,
    DROP COLUMN IF EXISTS session_successor;
-- topic_key stays: #45 (topic routing) owns it as well, and dropping it here
-- would take that feature's data along.
