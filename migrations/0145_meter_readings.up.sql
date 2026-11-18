-- The subscription meter as a measurement: readings keyed on the secret value,
-- from two sources that can be told apart, with exact reset times; and the
-- plan price of a credential, dated, so the meter's points can be priced.
--
-- Until here usage_readings knew a credential only by its place in a runtime
-- (runtime_id/credential_ord — a Reorder moves values between places), had
-- one source (`claude -p /usage`, which the live path never got an answer
-- from: the daemon looked the engine up in a config that had not arrived
-- yet), kept the reset time as prose, and recorded neither why a reading was
-- unsupported nor when it arrived as opposed to when it was read.
--
-- Written so that it can run twice on the same data (IF NOT EXISTS, ON
-- CONFLICT DO NOTHING): the in-band backfill below is the part that has to be
-- idempotent, and the whole file being so is what lets a test prove it.

-- (1) usage_readings: the credential by its secret, the source, the window,
-- the reset as an instant, the provider's status, a reason code where there
-- is no figure, and when covey received the reading.
--
-- source: usage_cmd (`claude -p /usage`, asked by covey), inband (Claude
-- Code's rate_limit_event, in the run's own stream), manual (entered by hand).
-- window_kind: the provider's window name (five_hour, seven_day, ...). A
-- usage_cmd row carries both bars of the answer and is filed as seven_day:
-- week_percent is the weekly meter, window_percent the five-hour one beside it.
-- reason: no_runtime_config | no_reporter | exec_failed | unparsed | stale,
-- empty for a fresh figure (and for rows from before this migration).
-- received_at: when covey filed the row. Rows from before this migration carry
-- the migration's time: when they arrived is not known (some were entered by
-- hand hours after they were read), and fetched_at is not that.
ALTER TABLE usage_readings
    ADD COLUMN IF NOT EXISTS secret_key      TEXT,
    ADD COLUMN IF NOT EXISTS secret_slot     SMALLINT,
    ADD COLUMN IF NOT EXISTS credential_kind TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS source          TEXT NOT NULL DEFAULT 'usage_cmd',
    ADD COLUMN IF NOT EXISTS window_kind     TEXT NOT NULL DEFAULT 'seven_day',
    ADD COLUMN IF NOT EXISTS resets_at       TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS status          TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS reason          TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS received_at     TIMESTAMPTZ NOT NULL DEFAULT now();

ALTER TABLE usage_readings DROP CONSTRAINT IF EXISTS usage_readings_source_chk;
ALTER TABLE usage_readings ADD CONSTRAINT usage_readings_source_chk
    CHECK (source IN ('usage_cmd', 'inband', 'manual'));

-- The secret behind each existing reading, through (runtime_id, ord) as the
-- credentials stand NOW — right only if no Reorder happened since (the same
-- caveat as 0144; the instance this was written against has one credential).
--
-- The rows entered by hand stay source usage_cmd here: which rows those are is
-- known on the instance, not in a migration. The operator relabels them once
-- (see the pull request): UPDATE usage_readings SET source = 'manual' WHERE ...
UPDATE usage_readings ur SET secret_key = rc.secret_key, secret_slot = rc.secret_slot, credential_kind = rc.kind
FROM runtime_credentials rc
WHERE rc.runtime_id = ur.runtime_id AND rc.ord = ur.credential_ord AND ur.secret_key IS NULL;

-- Readings filed twice before this migration (a backfill that ran beside the
-- poller left exact copies: the same runtime, ord and fetched_at) now share
-- the key the index below makes unique, and would stop it from being built.
-- One reading per key stays, the lowest id; the others are moved aside, not
-- dropped, so the down migration can put them back. Where two copies differ
-- in their figures, the first one filed is the one kept.
CREATE TABLE IF NOT EXISTS usage_readings_0145_duplicates (LIKE usage_readings);
WITH dup AS (
    DELETE FROM usage_readings a
    USING usage_readings b
    WHERE a.id > b.id AND a.org_id = b.org_id AND a.secret_key = b.secret_key AND a.secret_slot = b.secret_slot
      AND a.source = b.source AND a.window_kind = b.window_kind AND a.fetched_at = b.fetched_at
    RETURNING a.*)
INSERT INTO usage_readings_0145_duplicates SELECT * FROM dup;

-- One reading per credential, source, window and moment: filing the same one
-- twice (a re-sent event, the backfill run again) adds nothing.
CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_readings_once
    ON usage_readings (org_id, secret_key, secret_slot, source, window_kind, fetched_at);
-- The meter's arithmetic reads only supported, fresh readings, per credential.
CREATE INDEX IF NOT EXISTS idx_usage_readings_meter
    ON usage_readings (org_id, secret_key, secret_slot, source, window_kind, fetched_at)
    WHERE supported AND NOT stale;

-- (2) The in-band readings the recordings still hold. Claude Code writes a
-- rate_limit_event into the run's stream whenever the provider's rate-limit
-- headers say something: per window (rate_limit_info.unifiedWindows) the
-- status, the utilisation (0..1) and the reset (epoch seconds). One row per
-- window; a line from before unifiedWindows names its one window in
-- rateLimitType. Five-hour windows go into window_percent, everything weekly
-- into week_percent — the columns the meter has always been read from.
--
-- The credential is the one the agent's phase was brokered: the latest
-- `credential` event of the same agent before the line, by its runtime and
-- ord, resolved to the secret as the credentials stand now. A line without
-- one is not filed — a reading nobody can attribute measures nothing.
INSERT INTO usage_readings (org_id, runtime_id, credential_ord, secret_key, secret_slot, credential_kind,
    source, window_kind, supported, window_percent, week_percent, resets_at, status,
    stale, fetched_at, received_at)
SELECT e.org_id, rc.runtime_id, rc.ord, rc.secret_key, rc.secret_slot, rc.kind,
    'inband', w.key, true,
    CASE WHEN w.key LIKE 'five_hour%' THEN w.pct END,
    CASE WHEN w.key NOT LIKE 'five_hour%' THEN w.pct END,
    w.resets_at, w.status, false, e.created_at, e.created_at
FROM recording_events e
CROSS JOIN LATERAL (
    SELECT c.payload->>'runtime' AS runtime, c.payload->>'ord' AS ord
    FROM recording_events c
    WHERE c.agent_id = e.agent_id AND c.kind = 'credential' AND c.id < e.id
      AND c.payload->>'system' = 'anthropic' AND jsonb_typeof(c.payload->'runtime') = 'string'
      AND jsonb_typeof(c.payload->'ord') = 'number'
    ORDER BY c.id DESC
    LIMIT 1) cred
JOIN runtime_credentials rc ON rc.runtime_id::text = cred.runtime AND rc.ord::text = cred.ord
CROSS JOIN LATERAL (
    SELECT m.key,
        round((m.value->>'utilization')::numeric * 100, 4)::float8 AS pct,
        CASE WHEN jsonb_typeof(m.value->'resetsAt') = 'number'
            THEN to_timestamp((m.value->>'resetsAt')::float8) END AS resets_at,
        CASE WHEN jsonb_typeof(m.value->'status') = 'string' THEN m.value->>'status' ELSE '' END AS status
    FROM jsonb_each(CASE
        WHEN jsonb_typeof(e.payload->'rate_limit_info'->'unifiedWindows') = 'object'
            THEN e.payload->'rate_limit_info'->'unifiedWindows'
        WHEN jsonb_typeof(e.payload->'rate_limit_info'->'rateLimitType') = 'string'
            THEN jsonb_build_object(e.payload->'rate_limit_info'->>'rateLimitType', e.payload->'rate_limit_info')
        ELSE '{}'::jsonb END) AS m(key, value)
    WHERE jsonb_typeof(m.value) = 'object' AND jsonb_typeof(m.value->'utilization') = 'number') w
WHERE e.kind = 'runtime' AND e.payload->>'type' = 'rate_limit_event'
ON CONFLICT DO NOTHING;

-- (3) credential_plans: what a credential costs, dated. For measurement only —
-- the meter's points priced at the plan's weekly share — never for budgets or
-- limits, which stay on the list-price figure (spec/07 D13, spec/18).
--
-- Keyed on the secret value like the readings (a Reorder moves places, not
-- values), within the organisation that holds it: secret keys are names per
-- organisation. runtime_id/ord say where it was entered, nothing more.
-- A plan change adds a row and closes the one before (valid_to); a row is never
-- overwritten, so a past period stays priced at what it cost then.
--
-- amount is the plan's price in its currency per period, as invoiced: net or
-- gross of tax (tax_basis). fx_to_usd is the rate the reader used (1 for USD),
-- fx_date the day it is from.
CREATE TABLE IF NOT EXISTS credential_plans (
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    runtime_id  UUID REFERENCES runtimes(id) ON DELETE SET NULL,
    ord         SMALLINT,
    secret_key  TEXT NOT NULL,
    secret_slot SMALLINT NOT NULL DEFAULT 0,
    plan_name   TEXT NOT NULL DEFAULT '',
    amount      NUMERIC(12,2) NOT NULL CHECK (amount >= 0),
    currency    CHAR(3) NOT NULL DEFAULT 'USD',
    period      TEXT NOT NULL CHECK (period IN ('month', 'year')),
    tax_basis   TEXT NOT NULL DEFAULT 'net' CHECK (tax_basis IN ('net', 'gross')),
    fx_to_usd   NUMERIC(12,6) NOT NULL DEFAULT 1 CHECK (fx_to_usd > 0),
    fx_date     DATE,
    valid_from  TIMESTAMPTZ NOT NULL,
    valid_to    TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, secret_key, secret_slot, valid_from),
    CONSTRAINT credential_plans_validity_chk CHECK (valid_to IS NULL OR valid_to > valid_from)
);
-- At most one open plan per credential.
CREATE UNIQUE INDEX IF NOT EXISTS idx_credential_plans_open
    ON credential_plans (org_id, secret_key, secret_slot) WHERE valid_to IS NULL;
