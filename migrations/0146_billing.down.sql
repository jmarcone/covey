-- Back to no billed money: the poller's tables, the mapping and its trigger go.
-- What was read from the Usage and Cost Analytics API can be read again.

DROP TRIGGER IF EXISTS secrets_close_billing_key_del ON secrets;
DROP TRIGGER IF EXISTS secrets_close_billing_key_upd ON secrets;
DROP FUNCTION IF EXISTS close_credential_billing_key();
DROP TABLE IF EXISTS credential_billing_keys;
DROP TABLE IF EXISTS billing_sync;
DROP TABLE IF EXISTS billed_usage;
DROP TABLE IF EXISTS billed_cost;
