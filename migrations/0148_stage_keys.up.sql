-- The three default columns of an agent's board get a key: open, in_progress,
-- done. Until now they were found by their German names ("Backlog", "In
-- Arbeit", "Erledigt"), which made those names data: the interface showed
-- them in every language, and an agent that called set_stage "Done" got a
-- second column of the same meaning beside "Erledigt".
--
-- With the key, auto-follow (backlog.syncStage) and set_stage find the column
-- whatever it is called. The interface shows the label of the reader's
-- language (agent.backlog.defaultStage.<key> in web/src/locales) as long as
-- the column still carries its old German name; a column a human renamed
-- keeps the key and shows the new name in every language.
--
-- The names stay as they are in this release. An instance that still runs the
-- code from before the key (a rolling start, two instances on one database:
-- internal/db/migrate.go runs this at the first start of the new code) reads
-- name into a string, finds the default columns by exactly these names and
-- creates "Erledigt" if it does not find it. With the names emptied it would
-- answer the column list with an error, open a second "Erledigt" beside the
-- keyed one and stop moving tasks. A later release can empty the names of
-- the unrenamed keyed columns once no such instance is left; it should key
-- exact default names again first (step 1), since the old code seeds boards
-- without a key until then.
ALTER TABLE agent_stages
    ADD COLUMN key TEXT CHECK (key IN ('open', 'in_progress', 'done'));

CREATE UNIQUE INDEX agent_stages_agent_key ON agent_stages (agent_id, key) WHERE key IS NOT NULL;

-- 1. Every column that carries a default name exactly is that default column,
--    whoever made it and whenever: the seeded ones, 0004's backfill, and one a
--    human or an agent made again after the seeded one was deleted. Each of
--    them took part in the name-based auto-follow until now, so each keeps it.
--    UNIQUE (agent_id, name) (0003) leaves at most one per name and agent, so
--    no agent gets two columns of one key. A renamed default column carries
--    another name; it did not take part in auto-follow before and stays a
--    custom column.
--
--    created_by stays as it is. Seeding records 'seed' from now on, but what
--    a column of the past was made by cannot be told from the row: seeding
--    wrote 'human' too, the default since 0021.
UPDATE agent_stages SET key = CASE name
        WHEN 'Backlog'   THEN 'open'
        WHEN 'In Arbeit' THEN 'in_progress'
        WHEN 'Erledigt'  THEN 'done'
    END
WHERE name IN ('Backlog', 'In Arbeit', 'Erledigt');

-- 2. Look-alikes an agent created via set_stage ("Done", "in progress",
--    "erledigt" …) merge into the keyed column of the same meaning: their
--    tasks move over, then the column goes. The labels are those of the ten
--    catalogues at the time of this migration plus the keys themselves,
--    compared case-insensitively.
--
--    Only created_by = 'agent' is merged. 'human' is never deleted — and that
--    includes columns from before 0021, which got 'human' as the default even
--    if an agent made them. An agent without the keyed column of that meaning
--    (a human deleted or renamed it) keeps its look-alike as it is.
--
--    stage_keys_merged keeps each merged column and the tasks it held, so the
--    rollback can put them back. The release that empties the names drops it.
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

CREATE TABLE stage_keys_merged (
    id          UUID PRIMARY KEY,
    agent_id    UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    position    INTEGER NOT NULL,
    color       TEXT NOT NULL,
    created_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    merged_into UUID NOT NULL,
    task_ids    UUID[] NOT NULL
);

INSERT INTO stage_keys_merged (id, agent_id, name, position, color, created_by, created_at, merged_into, task_ids)
SELECT a.id, a.agent_id, a.name, a.position, a.color, a.created_by, a.created_at, k.id,
       ARRAY(SELECT t.id FROM backlog_tasks t WHERE t.stage_id = a.id)
FROM agent_stages a
JOIN stage_key_labels l ON l.label = lower(btrim(a.name))
JOIN agent_stages k ON k.agent_id = a.agent_id AND k.key = l.key
WHERE a.created_by = 'agent' AND a.key IS NULL;

UPDATE backlog_tasks t SET stage_id = m.merged_into
FROM stage_keys_merged m
WHERE t.stage_id = m.id;

DELETE FROM agent_stages s USING stage_keys_merged m WHERE s.id = m.id;
