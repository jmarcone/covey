-- A session can end more than once: when a run hits its turn limit, the daemon
-- resumes the SAME Claude Code session for a short handover, and that second
-- `result` line reports the session's usage CUMULATIVELY. Summing both lines
-- counted the main run twice. What a session consumed is its LAST line, per
-- model; lines without a session id stand on their own.
CREATE VIEW model_usage_latest AS
SELECT DISTINCT ON (COALESCE(NULLIF(session_id, ''), event_id::text), model) *
FROM model_usage
ORDER BY COALESCE(NULLIF(session_id, ''), event_id::text), model, event_id DESC;
