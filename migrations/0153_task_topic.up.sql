-- One topic, one open task (#45).
--
-- covey/create_task on a topic that already has an open, running or blocked
-- task at the target agent creates no second task: it adds the request to
-- that task as a note, and resumes it when it waits on something a note may
-- end. Every addendum used to become a task of its own with a session of its
-- own, because covey had no notion of a topic and only refused an identical
-- title.
--
-- (1) backlog_tasks.topic_key: the topic a task is about (backlog.TopicKey —
-- the first "#<n>" of the title, else the title without its first word, the
-- step), or the topic its creator named. NULL for a title too short to name
-- one, and for every task from before this migration: there is no backfill,
-- the lookup computes the key of such a row from its title in Go, so the rule
-- has one implementation. The index serves that lookup, per agent and over
-- the tasks still in play.
ALTER TABLE backlog_tasks ADD COLUMN IF NOT EXISTS topic_key TEXT;
CREATE INDEX IF NOT EXISTS idx_backlog_tasks_agent_topic
    ON backlog_tasks (agent_id, topic_key)
    WHERE topic_key IS NOT NULL AND archived_at IS NULL AND state IN ('open', 'in_progress', 'blocked');

-- (2) task_notes.routed_from: the task whose run routed this note onto the
-- task instead of creating one. Routing creates no child task, so the width
-- limit of that run (tasks per run) would never move, and two parked tasks
-- could wake each other with notes forever; the limit counts these notes as
-- well. A note outlives the task it came from.
ALTER TABLE task_notes ADD COLUMN IF NOT EXISTS routed_from UUID REFERENCES backlog_tasks(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_task_notes_routed_from
    ON task_notes (routed_from) WHERE routed_from IS NOT NULL;
