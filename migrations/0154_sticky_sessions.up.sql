-- A sticky session per topic for the developer in fix rounds (#130).
--
-- Every task used to start a cold runtime session, a fix round on the same pull
-- request by the same developer included: it read the code and the review again
-- and paid the cache write again. A task now names the topic it belongs to
-- (pr:<n> or issue:<n>), and an agent whose policy allows it resumes the session
-- of its last finished task on that topic instead of starting a new one.

-- (1) The topic. ADD COLUMN IF NOT EXISTS because #45 (topic routing) adds the
-- same column with keys derived from titles; the sticky lookup only ever matches
-- the explicit form pr:<n> / issue:<n>, so both can live in one column.
ALTER TABLE backlog_tasks ADD COLUMN IF NOT EXISTS topic_key TEXT;

-- (2) What a finished run leaves for the next one on its topic: the session, when
-- it ended, how large its context was in its last turn, on which credential it
-- ran, and the system prompt and memory context it was started with — a resumed
-- session is only read from the cache when that prefix is byte-identical.
ALTER TABLE backlog_tasks
    ADD COLUMN session_finished_at    TIMESTAMPTZ,
    ADD COLUMN session_context_tokens BIGINT,
    ADD COLUMN session_credential     TEXT,
    ADD COLUMN session_prompt         TEXT,
    ADD COLUMN session_memory         TEXT,
    -- A hash of the prompt the platform would compile for the session's
    -- first task: when a later round compiles another one (a changed SOUL,
    -- ACCESS or platform protocol), its session starts fresh rather than
    -- carrying the old instructions on.
    ADD COLUMN session_basis          TEXT,
    -- The one task that continues this task's session. Claimed with a
    -- conditional UPDATE, so two rounds on one topic never resume the same
    -- session at once.
    ADD COLUMN session_successor UUID REFERENCES backlog_tasks(id) ON DELETE SET NULL;

-- (3) The dispatch decision, recorded on the task it was made for:
-- sticky_resumed, fresh (with its reason) or sticky_pending (asked the daemon
-- to resume; its answer has not arrived). The gap and the predecessor's context
-- are what the decision was based on.
ALTER TABLE backlog_tasks
    ADD COLUMN sticky_decision TEXT,
    ADD COLUMN sticky_reason   TEXT,
    ADD COLUMN sticky_policy   TEXT,
    ADD COLUMN sticky_from     UUID REFERENCES backlog_tasks(id) ON DELETE SET NULL,
    ADD COLUMN sticky_gap_s    BIGINT,
    ADD COLUMN sticky_context  BIGINT;

-- (4) The first assistant turn of the task's first run: whether a resume was read
-- from the cache or written to it, and at which lifetime (a 5-minute write
-- inside a 55-minute window is a full rewrite).
ALTER TABLE backlog_tasks
    ADD COLUMN first_turn_input          BIGINT,
    ADD COLUMN first_turn_cache_read     BIGINT,
    ADD COLUMN first_turn_cache_write_5m BIGINT,
    ADD COLUMN first_turn_cache_write_1h BIGINT;

-- The lookup: an agent's last finished task on a topic.
CREATE INDEX IF NOT EXISTS idx_backlog_tasks_sticky
    ON backlog_tasks (agent_id, topic_key, session_finished_at DESC)
    WHERE state = 'done' AND topic_key IS NOT NULL;
-- The handover's review: the latest finished report on a topic in an
-- organisation, by any agent.
CREATE INDEX IF NOT EXISTS idx_backlog_tasks_topic_done
    ON backlog_tasks (org_id, topic_key)
    WHERE state = 'done' AND topic_key IS NOT NULL;
-- The two new references are ON DELETE SET NULL: without an index every
-- deleted task would scan the table for each of them.
CREATE INDEX IF NOT EXISTS idx_backlog_tasks_session_successor
    ON backlog_tasks (session_successor) WHERE session_successor IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_backlog_tasks_sticky_from
    ON backlog_tasks (sticky_from) WHERE sticky_from IS NOT NULL;

-- (5) The policy per agent. fresh = never resume; conditional = resume inside the
-- window and under the context cap; always = resume whatever the gap and size;
-- ab = odd pull requests always, even ones and issues conditional (an A/B test
-- on one developer's own fix rounds).
ALTER TABLE agents
    ADD COLUMN sticky_policy TEXT NOT NULL DEFAULT 'fresh'
        CHECK (sticky_policy IN ('fresh', 'conditional', 'always', 'ab')),
    ADD COLUMN sticky_window_minutes INT NOT NULL DEFAULT 55
        CHECK (sticky_window_minutes BETWEEN 1 AND 10080),
    ADD COLUMN sticky_context_cap BIGINT NOT NULL DEFAULT 200000
        CHECK (sticky_context_cap > 0);

-- The developers keep their session; every other agent, the reviewer above all,
-- starts fresh (a review round is a fresh adversarial review by charter).
UPDATE agents SET sticky_policy = 'conditional' WHERE slug IN ('covey-dev', 'covey-frontend');
