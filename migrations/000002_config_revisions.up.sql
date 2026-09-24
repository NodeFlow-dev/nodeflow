CREATE TABLE config_revisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    revision bigint NOT NULL CHECK (revision > 0),
    config text NOT NULL CHECK (length(config) > 0),
    sha256 char(64) NOT NULL,
    note varchar(500) NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (node_id, revision)
);

CREATE INDEX config_revisions_node_created_idx ON config_revisions(node_id, created_at DESC);

CREATE TABLE node_config_state (
    node_id uuid PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    desired_revision bigint,
    actual_revision bigint,
    state varchar(32) NOT NULL DEFAULT 'unassigned'
        CHECK (state IN ('unassigned','pending','applying','in_sync','failed','rolled_back','drifted')),
    last_error text NOT NULL DEFAULT '',
    last_report_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (node_id, desired_revision) REFERENCES config_revisions(node_id, revision),
    FOREIGN KEY (node_id, actual_revision) REFERENCES config_revisions(node_id, revision)
);

CREATE TABLE config_apply_reports (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    revision bigint NOT NULL,
    state varchar(32) NOT NULL CHECK (state IN ('applying','applied','failed','rolled_back')),
    actual_revision bigint,
    error varchar(2000) NOT NULL DEFAULT '',
    rollback_attempted boolean NOT NULL DEFAULT false,
    rollback_succeeded boolean,
    details jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(details) = 'object'),
    received_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (node_id, revision) REFERENCES config_revisions(node_id, revision),
    FOREIGN KEY (node_id, actual_revision) REFERENCES config_revisions(node_id, revision)
);

CREATE INDEX config_apply_reports_node_received_idx ON config_apply_reports(node_id, received_at DESC);

CREATE FUNCTION reject_config_ledger_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'configuration ledger rows are immutable';
END;
$$;

CREATE TRIGGER config_revisions_immutable
    BEFORE UPDATE ON config_revisions
    FOR EACH ROW EXECUTE FUNCTION reject_config_ledger_mutation();

CREATE TRIGGER config_apply_reports_immutable
    BEFORE UPDATE ON config_apply_reports
    FOR EACH ROW EXECUTE FUNCTION reject_config_ledger_mutation();
