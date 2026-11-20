-- Back to a cold session for every task.
ALTER TABLE agents
    DROP COLUMN sticky_context_cap,
    DROP COLUMN sticky_window_minutes,
    DROP COLUMN sticky_sessions;

DROP INDEX IF EXISTS idx_backlog_tasks_topic;

ALTER TABLE backlog_tasks
    DROP COLUMN session_reason,
    DROP COLUMN session_decision,
    DROP COLUMN last_turn_context_tokens,
    DROP COLUMN finished_at,
    DROP COLUMN topic_key;
