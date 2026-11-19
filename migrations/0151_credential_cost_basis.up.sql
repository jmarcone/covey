-- How the real money of a credential is formed (#120), chosen per credential:
-- by its seat price (the plans of 0145, which price the subscription meter),
-- by usage charges (money charged for what was used), or by both. An
-- Enterprise seat can be charged either way, and which one is a fact about
-- the contract, not something covey can read.
--
-- (1) credential_cost_bases: the basis, dated like the plans. Keyed on the
-- secret value (a Reorder moves places, not values), within the
-- organisation; runtime_id/ord say where it was entered. A change adds a row
-- and closes the one before; a row is never overwritten. A credential without
-- a row keeps the behaviour from before this migration: a subscription seat is
-- priced by its plan, an API key by its bill.
--
--   seat           the plan prices the meter (as before this migration);
--   usage          the usage charges are the money; the plan prices nothing;
--   seat_and_usage the plan prices the meter and the usage charges add to it.
CREATE TABLE IF NOT EXISTS credential_cost_bases (
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    runtime_id  UUID REFERENCES runtimes(id) ON DELETE SET NULL,
    ord         SMALLINT,
    secret_key  TEXT NOT NULL,
    secret_slot SMALLINT NOT NULL DEFAULT 0,
    basis       TEXT NOT NULL CHECK (basis IN ('seat', 'usage', 'seat_and_usage')),
    valid_from  TIMESTAMPTZ NOT NULL,
    valid_to    TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, secret_key, secret_slot, valid_from),
    CONSTRAINT credential_cost_bases_validity_chk CHECK (valid_to IS NULL OR valid_to > valid_from)
);
-- At most one open basis per credential.
CREATE UNIQUE INDEX IF NOT EXISTS idx_credential_cost_bases_open
    ON credential_cost_bases (org_id, secret_key, secret_slot) WHERE valid_to IS NULL;

-- (2) credential_usage_charges: money charged for usage over a span
-- [period_from, period_to), entered by hand — what a person read off an
-- invoice or measured (EUR 180 for 24 hours), beside what the billing sync
-- reads for an API key. The amount stays in its currency. fx_to_usd and
-- fx_date are the rate a person gave and the day it is from; without them a
-- charge in another currency has no dollar figure at all — covey never
-- converts on its own. A USD charge carries neither.
CREATE TABLE IF NOT EXISTS credential_usage_charges (
    id          UUID PRIMARY KEY,
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    runtime_id  UUID REFERENCES runtimes(id) ON DELETE SET NULL,
    ord         SMALLINT,
    secret_key  TEXT NOT NULL,
    secret_slot SMALLINT NOT NULL DEFAULT 0,
    amount      NUMERIC(14,4) NOT NULL CHECK (amount >= 0),
    currency    CHAR(3) NOT NULL,
    period_from TIMESTAMPTZ NOT NULL,
    period_to   TIMESTAMPTZ NOT NULL,
    fx_to_usd   NUMERIC(12,6) CHECK (fx_to_usd > 0),
    fx_date     DATE,
    note        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT credential_usage_charges_span_chk CHECK (period_to > period_from),
    -- A rate only with its date, and a USD charge needs none.
    CONSTRAINT credential_usage_charges_fx_chk CHECK ((fx_to_usd IS NULL) = (fx_date IS NULL)
        AND (currency <> 'USD' OR fx_to_usd IS NULL))
);
CREATE INDEX IF NOT EXISTS idx_credential_usage_charges_span
    ON credential_usage_charges (org_id, secret_key, secret_slot, period_to);
