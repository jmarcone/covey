-- Back to the duplicate check by identical title only. The routed notes stay
-- on their tasks as ordinary notes; only the column naming where they came
-- from goes, and with it the width limit's count of them.
DROP INDEX IF EXISTS idx_task_notes_routed_from;
ALTER TABLE task_notes DROP COLUMN IF EXISTS routed_from;
DROP INDEX IF EXISTS idx_backlog_tasks_agent_topic;
ALTER TABLE backlog_tasks DROP COLUMN IF EXISTS topic_key;
