-- Back to one row per line and model: the gap rows and the entries that
-- booked them go. What a process without a result line spent is then missing
-- from both sides again, as it was before 0147.
DELETE FROM cost_entries WHERE basis = 'gap';
DELETE FROM model_usage WHERE basis = 'gap';
ALTER TABLE model_usage DROP CONSTRAINT model_usage_event_id_model_basis_key;
ALTER TABLE model_usage ADD CONSTRAINT model_usage_event_id_model_key UNIQUE (event_id, model);
