CREATE SEQUENCE agent_release_sequence AS bigint MINVALUE 1 START 1;

CREATE TABLE agent_releases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    version varchar(64) NOT NULL CHECK (version ~ '^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$'),
    os varchar(32) NOT NULL CHECK (os ~ '^[a-z0-9][a-z0-9_-]{0,31}$'),
    arch varchar(32) NOT NULL CHECK (arch ~ '^[a-z0-9][a-z0-9_-]{0,31}$'),
    sha256 char(64) NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 67108864),
    sequence bigint NOT NULL UNIQUE CHECK (sequence > 0),
    signature varchar(128) NOT NULL,
    artifact_path varchar(255) NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE node_agent_updates (
    node_id uuid PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    desired_release_id uuid REFERENCES agent_releases(id) ON DELETE RESTRICT,
    actual_sequence bigint NOT NULL DEFAULT 0 CHECK (actual_sequence >= 0),
    state varchar(24) NOT NULL DEFAULT 'idle'
        CHECK (state IN ('idle','pending','downloading','verified','activating','installed','failed','rolled_back')),
    last_error varchar(100) NOT NULL DEFAULT '',
    last_report_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX node_agent_updates_desired_idx ON node_agent_updates(desired_release_id)
    WHERE desired_release_id IS NOT NULL;
