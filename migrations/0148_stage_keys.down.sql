-- Back to columns found by name. The names were never emptied, so a keyed
-- column nobody renamed still carries the German name the previous code
-- matches ("Backlog", "In Arbeit", "Erledigt"); a renamed one keeps its name
-- and is a custom column again, as it was before the key.

-- The look-alikes step 2 merged come back with their id, name, place and
-- origin, and take back those of their tasks that are still in the column they
-- were moved into. One whose name was taken since (by a column an agent made
-- after a human deleted the keyed one) stays merged.
INSERT INTO agent_stages (id, agent_id, name, position, color, created_by, created_at)
SELECT id, agent_id, name, position, color, created_by, created_at
FROM stage_keys_merged
ON CONFLICT DO NOTHING;

UPDATE backlog_tasks t SET stage_id = m.id
FROM stage_keys_merged m
WHERE t.id = ANY (m.task_ids) AND t.stage_id = m.merged_into
  AND EXISTS (SELECT 1 FROM agent_stages s WHERE s.id = m.id);

DROP TABLE stage_keys_merged;

-- 'seed' is new with 0148, and only columns seeded since carry it; the code
-- before it wrote 'human' for them.
UPDATE agent_stages SET created_by = 'human' WHERE created_by = 'seed';

DROP INDEX agent_stages_agent_key;
ALTER TABLE agent_stages DROP COLUMN key;
