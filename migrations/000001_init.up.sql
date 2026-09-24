CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE nodes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    address inet NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','online','offline','error')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    last_seen_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX nodes_name_unique ON nodes (lower(name));
CREATE UNIQUE INDEX nodes_address_unique ON nodes (address);

CREATE TABLE routes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    hostname text NOT NULL CHECK (length(hostname) BETWEEN 1 AND 253),
    target_host text NOT NULL CHECK (length(target_host) BETWEEN 1 AND 253),
    target_port integer NOT NULL CHECK (target_port BETWEEN 1 AND 65535),
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (node_id, hostname)
);

CREATE INDEX routes_node_id_idx ON routes(node_id);

CREATE TABLE enrollment_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    token_hash char(64) NOT NULL UNIQUE,
    token_prefix varchar(16) NOT NULL,
    expires_at timestamptz NOT NULL,
    last_used_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX enrollment_tokens_node_id_idx ON enrollment_tokens(node_id);
CREATE INDEX enrollment_tokens_active_idx ON enrollment_tokens(token_hash) WHERE revoked_at IS NULL;

CREATE TABLE node_heartbeats (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    agent_version varchar(100) NOT NULL DEFAULT '',
    status varchar(32) NOT NULL,
    metrics jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metrics) = 'object'),
    routes_ok boolean,
    received_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX node_heartbeats_node_received_idx ON node_heartbeats(node_id, received_at DESC);

CREATE TABLE jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id uuid REFERENCES nodes(id) ON DELETE CASCADE,
    kind varchar(100) NOT NULL,
    status varchar(32) NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','succeeded','failed','cancelled')),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    result jsonb,
    error text,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX jobs_claim_idx ON jobs(status, available_at) WHERE status = 'queued';

CREATE TABLE audit_log (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_type varchar(32) NOT NULL,
    actor_id text,
    action varchar(100) NOT NULL,
    resource_type varchar(100) NOT NULL,
    resource_id text,
    details jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(details) = 'object'),
    source_ip inet,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_log_created_idx ON audit_log(created_at DESC);
