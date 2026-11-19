-- Back to plans only.
--
-- Both tables STAY, as credential_plans did in 0145's down: their rows are
-- what a person entered by hand (how a contract charges, what an invoice
-- said), found nowhere else and not derivable from any other table. Nothing
-- before 0151 reads them, so leaving them costs the older schema nothing, and
-- the up migration (CREATE ... IF NOT EXISTS) finds them with their rows
-- intact. Drop them by hand if the bases and charges are really meant to go:
--   DROP TABLE credential_usage_charges; DROP TABLE credential_cost_bases;
SELECT 1;
