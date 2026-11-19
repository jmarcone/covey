-- Model calls made outside covey (#120): the Claude Code sessions that drive
-- covey from an operator's machine run on the same subscription meter as
-- covey's agents, and the cost page knew nothing of them.
--
-- An uploader (`covey usage push`) reads the session transcripts locally and
-- sends token counts only, one row per model call, through
-- POST /api/v1/cost/external-usage. No prompt, response or tool text has a
-- column here, and none may get one: the table holds exactly the fields the
-- endpoint accepts.
--
-- These rows are kept apart from the agents' figures on purpose. Nothing is
-- written into cost_entries or model_usage, no budget or guard rail reads
-- them, and every figure the cost endpoints returned before is unchanged
-- (observability/external.go).
--
-- One row per (org, source, message_id): a re-upload replaces its row and
-- never adds a second one, so the uploader can run on a schedule.
CREATE TABLE external_usage (
    org_id         UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    -- A free label for where the calls came from ("operator: home").
    source         TEXT NOT NULL CHECK (char_length(source) BETWEEN 1 AND 64),
    host           TEXT NOT NULL,
    session_id     TEXT NOT NULL,
    message_id     TEXT NOT NULL,
    ts             TIMESTAMPTZ NOT NULL,
    model          TEXT NOT NULL,
    input          BIGINT NOT NULL CHECK (input >= 0),
    output         BIGINT NOT NULL CHECK (output >= 0),
    cache_read     BIGINT NOT NULL CHECK (cache_read >= 0),
    cache_write_5m BIGINT NOT NULL CHECK (cache_write_5m >= 0),
    cache_write_1h BIGINT NOT NULL CHECK (cache_write_1h >= 0),
    -- NULL when the uploader did not say.
    origin         TEXT CHECK (origin IN ('main', 'subagent', 'workflow')),
    -- What Claude Code priced the call at, where the transcript says.
    reported_usd   NUMERIC(14,6) CHECK (reported_usd >= 0),
    PRIMARY KEY (org_id, source, message_id)
);

-- The cost page reads a window of the organisation's rows.
CREATE INDEX idx_external_usage_org_ts ON external_usage (org_id, ts);
-- The meter share asks per machine whether it had a call in the 7 days
-- before a reading (external_usage_hosts below).
CREATE INDEX idx_external_usage_host_ts ON external_usage (org_id, source, host, ts);

-- The subscription credential a source shares its meter with, if it does:
-- what the meter's real cost is split by (formula F3 in #120). Named by the
-- upload, the last one wins; a source without a row here (a machine billed
-- per use) gets no share of any meter.
--
-- The link is not dated: it applies to every call of the source, also the
-- ones uploaded before it was set. Re-linking a source to another seat moves
-- its whole history onto that seat's meter.
CREATE TABLE external_usage_meters (
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    source      TEXT NOT NULL,
    secret_key  TEXT NOT NULL,
    secret_slot SMALLINT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, source)
);

-- Which calls of a machine have been sent (#120, F3): every push says per
-- host how far it has read its transcripts (reported_through); every call of
-- that host with ts <= it has been uploaded. The server clamps it to the
-- time the push arrived and keeps the maximum, so it never moves back. A
-- meter reading is split between covey and the outside sessions only when
-- every active machine of the linked sources has reported through it;
-- otherwise the outside calls before it may still be missing.
--
-- pushed_at is the latest push that named the host, in a row or in
-- reported_through: a machine that pushed or had a call in the 7 days before
-- a reading is active for it. NULL reported_through: the host's rows came
-- without one.
--
-- first_reported_at is the push that brought the host's first watermark, set
-- once and never moved: since when the source's uploader claims watermarks.
-- A host of the source that never sent one and has neither pushed nor had a
-- call since then is a name from before (not waited for); one that has is an
-- older uploader still running, and is waited for.
CREATE TABLE external_usage_hosts (
    org_id            UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    source            TEXT NOT NULL,
    host              TEXT NOT NULL,
    reported_through  TIMESTAMPTZ,
    first_reported_at TIMESTAMPTZ,
    pushed_at         TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (org_id, source, host),
    CHECK ((reported_through IS NULL) = (first_reported_at IS NULL))
);
