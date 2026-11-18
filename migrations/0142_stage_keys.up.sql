-- The three default columns of an agent's board get a key: open, in_progress,
-- done. Until now they were found by their German names ("Backlog", "In
-- Arbeit", "Erledigt"), which made those names data: the interface showed
-- them in every language, and an agent that called set_stage "Done" got a
-- second column of the same meaning beside "Erledigt".
--
-- With the key, auto-follow (backlog.syncStage) and set_stage find the column
-- whatever it is called. The name becomes NULL as long as nobody renamed the
-- column; the interface then shows the label of the reader's language
-- (agent.backlog.defaultStage.<key> in web/src/locales). A column a human
-- renames keeps the key and carries the new name in every language.
--
-- NULL rather than an empty string because of UNIQUE (agent_id, name)
-- (0003): NULLs never collide there, so three unnamed keyed columns per agent
-- fit the constraint as it is.
ALTER TABLE agent_stages
    ADD COLUMN key TEXT CHECK (key IN ('open', 'in_progress', 'done')),
    ALTER COLUMN name DROP NOT NULL,
    ADD CONSTRAINT agent_stages_name_or_key CHECK (name IS NOT NULL OR key IS NOT NULL);

CREATE UNIQUE INDEX agent_stages_agent_key ON agent_stages (agent_id, key) WHERE key IS NOT NULL;

-- 1. The seeded columns: the names 0004 and SeedDefaultStages wrote, and only
--    on a column the platform laid out. created_by cannot tell them apart from
--    a human's column — seeding wrote 'human' too, the default since 0021 — so
--    the signal is the time, in one of two forms:
--
--    - SeedDefaultStages runs right after the agent is created, in every path
--      that creates one. The two minutes are room for what the hiring path
--      does between the two inserts, not a guess at a human's speed.
--    - 0004 backfilled the agents older than itself, long after they were
--      created, with one statement inside the migrator's transaction: all
--      columns it wrote for an agent share one created_at (now(), the default
--      from 0003). Nothing else writes two columns of one agent at the same
--      instant — a human makes one per request, and SeedDefaultStages made
--      three separate inserts — so a sibling with the same created_at marks
--      the backfill. The sibling may have been renamed since; it still proves
--      the statement.
--
--    A column of a default name made later (after the seeded one was renamed
--    or deleted) is a human's and stays a custom column; so does a renamed
--    default, which did not take part in auto-follow before either.
--
--    The keyed columns are marked created_by = 'seed', as SeedDefaultStages
--    does from now on, so a later migration does not have to guess again.
UPDATE agent_stages s SET created_by = 'seed', key = CASE s.name
        WHEN 'Backlog'   THEN 'open'
        WHEN 'In Arbeit' THEN 'in_progress'
        WHEN 'Erledigt'  THEN 'done'
    END
FROM agents a
WHERE a.id = s.agent_id
  AND s.name IN ('Backlog', 'In Arbeit', 'Erledigt')
  AND (
      (s.created_at >= a.created_at AND s.created_at < a.created_at + interval '2 minutes')
      -- 0004's backfill: the columns of one agent, written by one statement.
      OR EXISTS (SELECT 1 FROM agent_stages o
                 WHERE o.agent_id = s.agent_id AND o.id <> s.id
                   AND o.created_at = s.created_at)
  );

-- 2. Look-alikes an agent created via set_stage ("Done", "in progress",
--    "erledigt" …) merge into the keyed column of the same meaning: their
--    tasks move over, then the column goes. The labels are those of the ten
--    catalogues at the time of this migration plus the keys themselves,
--    compared case-insensitively.
--
--    Only created_by = 'agent' is merged. 'human' is never deleted — and that
--    includes columns from before 0021, which got 'human' as the default even
--    if an agent made them. An agent without the keyed column of that meaning
--    (a human deleted or renamed it, or made it later) keeps its look-alike as
--    it is.
CREATE TEMPORARY TABLE stage_key_labels (key TEXT, label TEXT) ON COMMIT DROP;
INSERT INTO stage_key_labels (key, label) VALUES
    ('open', 'open'), ('open', 'backlog'), ('open', 'バックログ'), ('open', '待办'),
    ('in_progress', 'in_progress'), ('in_progress', 'in progress'), ('in_progress', 'in arbeit'),
    ('in_progress', 'en curso'), ('in_progress', 'en cours'), ('in_progress', 'in corso'),
    ('in_progress', 'bezig'), ('in_progress', 'w toku'), ('in_progress', 'em curso'),
    ('in_progress', '進行中'), ('in_progress', '进行中'),
    ('done', 'done'), ('done', 'erledigt'), ('done', 'hecho'), ('done', 'terminé'),
    ('done', 'fatto'), ('done', 'klaar'), ('done', 'gotowe'), ('done', 'concluído'),
    ('done', '完了'), ('done', '已完成');

CREATE TEMPORARY TABLE stage_key_merge ON COMMIT DROP AS
SELECT a.id AS from_id, k.id AS to_id
FROM agent_stages a
JOIN stage_key_labels l ON l.label = lower(btrim(a.name))
JOIN agent_stages k ON k.agent_id = a.agent_id AND k.key = l.key
WHERE a.created_by = 'agent' AND a.key IS NULL;

UPDATE backlog_tasks t SET stage_id = m.to_id
FROM stage_key_merge m
WHERE t.stage_id = m.from_id;

DELETE FROM agent_stages s USING stage_key_merge m WHERE s.id = m.from_id;

-- 3. Not renamed: the keyed columns show the label of the reader's language.
UPDATE agent_stages SET name = NULL WHERE key IS NOT NULL;
