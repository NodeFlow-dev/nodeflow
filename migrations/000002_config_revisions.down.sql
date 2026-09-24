DROP TRIGGER IF EXISTS config_apply_reports_immutable ON config_apply_reports;
DROP TRIGGER IF EXISTS config_revisions_immutable ON config_revisions;
DROP FUNCTION IF EXISTS reject_config_ledger_mutation();
DROP TABLE IF EXISTS config_apply_reports;
DROP TABLE IF EXISTS node_config_state;
DROP TABLE IF EXISTS config_revisions;
