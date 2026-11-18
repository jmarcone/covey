-- Billed money: what Anthropic actually charged a metered credential, next to
-- what Claude Code reported and what covey's list recomputes (spec/06).
--
-- The source is the Usage and Cost Analytics API (/v1/organizations/analytics
-- /cost and /usage), read with the organisation's anthropic_admin_key by the
-- control plane only. It reports per API key (api_key_id), not per covey
-- credential; credential_billing_keys says which secret value IS which key.
--
-- Money stays exact: the API sends cents as decimal strings ("123.456789"),
-- they are inserted as $n::numeric from that string and leave the database as
-- text. A float never touches them.
--
-- A row is one bucket x dimension combination of one read. The poller
-- replaces a whole read window at once (DELETE the window, INSERT the new set,
-- one transaction, only after every page arrived), so the table always holds
-- one complete answer per window and never a mixture of two. Dimensions the
-- API reports as null are stored as '' so that they can be part of the key.

-- (1) /cost: amount (after discounts, before credits) and list_amount, cents.
-- bucket_width: '1h' (the hourly read of the last 48 h) or '1d' (the daily
-- re-read of 35 days, which picks up revisions). A reader takes ONE width per
-- day (observability/billing.go), never both.
CREATE TABLE IF NOT EXISTS billed_cost (
    org_id            UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    bucket_width      TEXT NOT NULL CHECK (bucket_width IN ('1h', '1d')),
    bucket_start      TIMESTAMPTZ NOT NULL,
    bucket_end        TIMESTAMPTZ NOT NULL,
    api_key_id        TEXT NOT NULL DEFAULT '',
    workspace_id      TEXT NOT NULL DEFAULT '',
    model             TEXT NOT NULL DEFAULT '',
    service_tier      TEXT NOT NULL DEFAULT '',
    cost_type         TEXT NOT NULL DEFAULT '',
    token_type        TEXT NOT NULL DEFAULT '',
    amount_cents      NUMERIC(24,8) NOT NULL,
    list_amount_cents NUMERIC(24,8) NOT NULL,
    currency          TEXT NOT NULL DEFAULT 'USD',
    -- The response's data_refreshed_at: complete data ends there. NULL when
    -- the API did not say.
    data_refreshed_at TIMESTAMPTZ,
    fetched_at        TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (org_id, bucket_width, bucket_start, api_key_id, workspace_id, model, service_tier, cost_type, token_type)
);

-- (2) /usage: tokens per type, as the API names them.
CREATE TABLE IF NOT EXISTS billed_usage (
    org_id              UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    bucket_width        TEXT NOT NULL CHECK (bucket_width IN ('1h', '1d')),
    bucket_start        TIMESTAMPTZ NOT NULL,
    bucket_end          TIMESTAMPTZ NOT NULL,
    api_key_id          TEXT NOT NULL DEFAULT '',
    workspace_id        TEXT NOT NULL DEFAULT '',
    model               TEXT NOT NULL DEFAULT '',
    service_tier        TEXT NOT NULL DEFAULT '',
    uncached_input      BIGINT NOT NULL DEFAULT 0,
    cache_write_5m      BIGINT NOT NULL DEFAULT 0,
    cache_write_1h      BIGINT NOT NULL DEFAULT 0,
    cache_read          BIGINT NOT NULL DEFAULT 0,
    output              BIGINT NOT NULL DEFAULT 0,
    web_search_requests BIGINT NOT NULL DEFAULT 0,
    requests            BIGINT NOT NULL DEFAULT 0,
    data_refreshed_at   TIMESTAMPTZ,
    fetched_at          TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (org_id, bucket_width, bucket_start, api_key_id, workspace_id, model, service_tier)
);

-- (3) The poller's state per organisation. last_error never holds a secret:
-- the client's errors carry the status and at most 300 bytes of the body,
-- with the key scrubbed out (internal/billing).
-- last_daily_at: when the 35-day re-read last succeeded.
CREATE TABLE IF NOT EXISTS billing_sync (
    org_id            UUID PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    last_attempt_at   TIMESTAMPTZ,
    last_success_at   TIMESTAMPTZ,
    last_daily_at     TIMESTAMPTZ,
    data_refreshed_at TIMESTAMPTZ,
    last_status       TEXT NOT NULL DEFAULT '',
    last_error        TEXT NOT NULL DEFAULT ''
);

-- (4) Which secret value is which Anthropic API key. Keyed on the value
-- (secret_key, secret_slot), like the plans of 0145: a Reorder moves places,
-- not values. valid_from is when the value was written; valid_to closes when
-- the value is overwritten, rotated or removed (the trigger below), because a
-- new value is a new key and the old mapping would be a lie beside it.
-- source: manual (entered by a person through the API).
CREATE TABLE IF NOT EXISTS credential_billing_keys (
    org_id       UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    secret_key   TEXT NOT NULL,
    secret_slot  SMALLINT NOT NULL DEFAULT 0,
    api_key_id   TEXT NOT NULL CHECK (api_key_id <> ''),
    workspace_id TEXT NOT NULL DEFAULT '',
    valid_from   TIMESTAMPTZ NOT NULL,
    valid_to     TIMESTAMPTZ,
    source       TEXT NOT NULL DEFAULT 'manual',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, secret_key, secret_slot, valid_from),
    CONSTRAINT credential_billing_keys_validity_chk CHECK (valid_to IS NULL OR valid_to >= valid_from)
);
-- At most one open mapping per value.
CREATE UNIQUE INDEX IF NOT EXISTS idx_credential_billing_keys_open
    ON credential_billing_keys (org_id, secret_key, secret_slot) WHERE valid_to IS NULL;
CREATE INDEX IF NOT EXISTS idx_credential_billing_keys_api_key
    ON credential_billing_keys (org_id, api_key_id);
-- At most one open mapping per API key: one key mapped to two credentials
-- would count its bill twice. Overlapping closed intervals are refused where
-- the mapping is made (SetBillingKey); an index cannot say that.
CREATE UNIQUE INDEX IF NOT EXISTS idx_credential_billing_keys_api_key_open
    ON credential_billing_keys (org_id, api_key_id) WHERE valid_to IS NULL;

-- The mapping ends with its value: a new ciphertext in the slot (Put over an
-- existing value, Replace by a rotation) or the row gone (DeleteValue,
-- Delete). In the database rather than in each store method, so that no path
-- that changes a value can forget it. Org-wide values only: an agent's own
-- secret is never a runtime credential.
CREATE OR REPLACE FUNCTION close_credential_billing_key() RETURNS trigger AS $$
BEGIN
    IF OLD.agent_id IS NULL THEN
        UPDATE credential_billing_keys SET valid_to = GREATEST(now(), valid_from)
        WHERE org_id = OLD.org_id AND secret_key = OLD.key AND secret_slot = OLD.slot AND valid_to IS NULL;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS secrets_close_billing_key_upd ON secrets;
CREATE TRIGGER secrets_close_billing_key_upd
    AFTER UPDATE OF ciphertext ON secrets
    FOR EACH ROW WHEN (OLD.ciphertext IS DISTINCT FROM NEW.ciphertext)
    EXECUTE FUNCTION close_credential_billing_key();

DROP TRIGGER IF EXISTS secrets_close_billing_key_del ON secrets;
CREATE TRIGGER secrets_close_billing_key_del
    AFTER DELETE ON secrets
    FOR EACH ROW EXECUTE FUNCTION close_credential_billing_key();
