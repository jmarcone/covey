-- Back to readings by place only, one source.
--
-- credential_plans STAYS. Its rows are plan prices a person entered by hand
-- (what a seat cost, from when), found nowhere else and not derivable from any
-- other table — unlike everything else this migration added, which the down
-- below gives back or which can be read again. Nothing before 0145 reads the
-- table, so leaving it costs the older schema nothing, and the up migration
-- (CREATE ... IF NOT EXISTS) finds it with its rows intact. Drop it by hand
-- if the prices are really meant to go.

-- The in-band readings did not exist before; the rows of the other sources
-- stay (they fit the old columns as they are).
DELETE FROM usage_readings WHERE source = 'inband';

DROP INDEX IF EXISTS idx_usage_readings_meter;
DROP INDEX IF EXISTS idx_usage_readings_once;

-- The copies the up migration moved aside come back, under their own ids.
INSERT INTO usage_readings SELECT * FROM usage_readings_0145_duplicates;
DROP TABLE IF EXISTS usage_readings_0145_duplicates;
ALTER TABLE usage_readings DROP CONSTRAINT IF EXISTS usage_readings_source_chk;
ALTER TABLE usage_readings
    DROP COLUMN IF EXISTS secret_key,
    DROP COLUMN IF EXISTS secret_slot,
    DROP COLUMN IF EXISTS credential_kind,
    DROP COLUMN IF EXISTS source,
    DROP COLUMN IF EXISTS window_kind,
    DROP COLUMN IF EXISTS resets_at,
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS reason,
    DROP COLUMN IF EXISTS received_at;
