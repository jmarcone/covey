-- Back to columns found by name. A keyed column nobody renamed gets the
-- German name back that the previous code matches ("Backlog", "In Arbeit",
-- "Erledigt"), so auto-follow keeps working after the rollback. A column a
-- human renamed keeps its name — as before this migration, a renamed default
-- column is a custom column.
--
-- Since 0142 a human could create a column of that very name next to the
-- unnamed keyed one. That column steps aside (its id is appended) so that the
-- keyed column gets the name the old code looks for.
UPDATE agent_stages o SET name = o.name || ' (' || left(o.id::text, 8) || ')'
FROM agent_stages k
WHERE k.agent_id = o.agent_id AND k.name IS NULL AND o.id <> k.id
  AND o.name = CASE k.key
        WHEN 'open'        THEN 'Backlog'
        WHEN 'in_progress' THEN 'In Arbeit'
        WHEN 'done'        THEN 'Erledigt'
    END;

UPDATE agent_stages SET name = CASE key
        WHEN 'open'        THEN 'Backlog'
        WHEN 'in_progress' THEN 'In Arbeit'
        WHEN 'done'        THEN 'Erledigt'
    END
WHERE name IS NULL;

DROP INDEX agent_stages_agent_key;
ALTER TABLE agent_stages
    DROP CONSTRAINT agent_stages_name_or_key,
    ALTER COLUMN name SET NOT NULL,
    DROP COLUMN key;
