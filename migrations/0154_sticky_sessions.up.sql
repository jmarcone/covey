-- A developer's session carries over from one task to the next on the same
-- topic (#130).
--
-- Every task used to start a cold session, a fix round on the developer's own
-- PR included: the run read the code and the reasons again and paid the cache
-- write again. A task now names its topic (pr:<n> or issue:<n>), and the next
-- task of the same agent on that topic resumes the previous one's session
-- while its prompt cache is still warm and its context still small; otherwise
-- it starts fresh with a short handover.
--
-- topic_key is a column of its own and not the correlation key: Block
-- overwrites that with the key the task waits on, Complete clears it, and
-- CorrelateWake wakes tasks by it. IF NOT EXISTS: routing create_task by
-- topic (#45) adds the same column, and either may come first.
ALTER TABLE backlog_tasks
    ADD COLUMN IF NOT EXISTS topic_key TEXT,
    -- When the task ended (done or failed), and the context its run's last
    -- turn carried: input + cache read + cache write of the last assistant
    -- turn. With runtime_session_id these are what the next task of the topic
    -- decides on. runtime_session_id was written only when a task blocked;
    -- it is now written at the end of a run too.
    ADD COLUMN finished_at TIMESTAMPTZ,
    ADD COLUMN last_turn_context_tokens BIGINT,
    -- How the run of a task with a topic started: 'sticky_resumed', or
    -- 'fresh' with session_reason window, cap, off, no_session or
    -- no_previous. NULL for a task without a topic.
    ADD COLUMN session_decision TEXT,
    ADD COLUMN session_reason TEXT;

-- The lookup at dispatch: this agent's last task on this topic.
CREATE INDEX idx_backlog_tasks_topic ON backlog_tasks (agent_id, topic_key, finished_at DESC)
    WHERE topic_key IS NOT NULL;

-- Per agent: whether its tasks resume the topic's session, for how long after
-- the previous task ended (inside the prompt cache's one-hour lifetime), and
-- up to which context. Off for every agent; the reviewer stays fresh on
-- purpose, every review round is a review of its own.
ALTER TABLE agents
    ADD COLUMN sticky_sessions BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN sticky_window_minutes INTEGER NOT NULL DEFAULT 55
        CHECK (sticky_window_minutes BETWEEN 1 AND 1440),
    ADD COLUMN sticky_context_cap INTEGER NOT NULL DEFAULT 200000
        CHECK (sticky_context_cap BETWEEN 1 AND 1000000);

-- On for the two developers whose fix rounds this is for.
UPDATE agents SET sticky_sessions = true WHERE slug IN ('covey-dev', 'covey-frontend');
