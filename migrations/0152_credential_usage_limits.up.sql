-- Pay-per-token seats (Enterprise, usage billed against a monthly limit).
--
-- (1) credential_usage_limits: the money limit a credential's monthly meter
-- counts against — one at a time per credential, naming the window it reads. Claude Code reports such a seat's meter in band as a window
-- of its own (rateLimitType "overage", resetting on the first of the month),
-- utilisation 0..1 of the limit. With a limit, a point of that window is
-- amount/100 in the limit's currency: real spend = Δutilisation × limit.
-- Dated and keyed on the secret value like the plans (0145) and the bases
-- (0151); a change adds a row and closes the one before. The amount stays in
-- its currency: a dollar figure only with a rate a person gave, and its day.
-- The weekly windows (seven_day*, five_hour*) are the seat meter's and never
-- take a limit.
CREATE TABLE IF NOT EXISTS credential_usage_limits (
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    runtime_id  UUID REFERENCES runtimes(id) ON DELETE SET NULL,
    ord         SMALLINT,
    secret_key  TEXT NOT NULL,
    secret_slot SMALLINT NOT NULL DEFAULT 0,
    window_kind TEXT NOT NULL DEFAULT 'overage',
    amount      NUMERIC(14,4) NOT NULL CHECK (amount > 0),
    currency    CHAR(3) NOT NULL,
    fx_to_usd   NUMERIC(12,6) CHECK (fx_to_usd > 0),
    fx_date     DATE,
    valid_from  TIMESTAMPTZ NOT NULL,
    valid_to    TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, secret_key, secret_slot, valid_from),
    CONSTRAINT credential_usage_limits_validity_chk CHECK (valid_to IS NULL OR valid_to > valid_from),
    CONSTRAINT credential_usage_limits_window_chk CHECK (window_kind <> ''
        AND window_kind NOT LIKE 'seven\_day%' AND window_kind NOT LIKE 'five\_hour%'),
    CONSTRAINT credential_usage_limits_fx_chk CHECK ((fx_to_usd IS NULL) = (fx_date IS NULL)
        AND (currency <> 'USD' OR fx_to_usd IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_credential_usage_limits_open
    ON credential_usage_limits (org_id, secret_key, secret_slot) WHERE valid_to IS NULL;

-- (2) A usage export: what the provider's own usage page or admin export said
-- for a span, entered to check the figures against — never counted (the
-- meter or the bill is the money), and so allowed beside a charge over the
-- same time. kind 'charge' is what 0151 stored.
ALTER TABLE credential_usage_charges ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'charge';
DO $$ BEGIN
    ALTER TABLE credential_usage_charges ADD CONSTRAINT credential_usage_charges_kind_chk CHECK (kind IN ('charge', 'export'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
