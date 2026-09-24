DROP INDEX IF EXISTS routes_pending_delete_idx;
DROP INDEX IF EXISTS routes_node_deployment_idx;

ALTER TABLE routes
    DROP CONSTRAINT IF EXISTS routes_delete_pending_check,
    DROP CONSTRAINT IF EXISTS routes_applied_revision_fk,
    DROP CONSTRAINT IF EXISTS routes_desired_revision_fk,
    DROP COLUMN IF EXISTS delete_pending,
    DROP COLUMN IF EXISTS applied_revision,
    DROP COLUMN IF EXISTS desired_revision,
    DROP COLUMN IF EXISTS deployed_fingerprint,
    DROP COLUMN IF EXISTS desired_fingerprint,
    DROP COLUMN IF EXISTS deployment_error,
    DROP COLUMN IF EXISTS deployment_state,
    DROP COLUMN IF EXISTS deployed,
    DROP COLUMN IF EXISTS version;
