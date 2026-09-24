-- Last HAProxy counters and all-time reset-safe accumulated totals. A row is
-- kept per node total and per HAProxy backend proxy.
CREATE TABLE traffic_counter_state (
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    scope varchar(16) NOT NULL CHECK (scope IN ('node','backend')),
    proxy_name varchar(128) NOT NULL,
    last_raw_bytes_in bigint NOT NULL CHECK (last_raw_bytes_in >= 0),
    last_raw_bytes_out bigint NOT NULL CHECK (last_raw_bytes_out >= 0),
    accumulated_bytes_in bigint NOT NULL CHECK (accumulated_bytes_in >= 0),
    accumulated_bytes_out bigint NOT NULL CHECK (accumulated_bytes_out >= 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (node_id,scope,proxy_name),
    CHECK (
        (scope = 'node' AND proxy_name = '') OR
        (scope = 'backend' AND length(proxy_name) BETWEEN 1 AND 128)
    )
);

-- Calendar-month buckets use UTC. Deltas are attributed to the month in
-- which Panel received the heartbeat.
CREATE TABLE traffic_monthly (
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    month date NOT NULL CHECK (month = date_trunc('month',month)::date),
    scope varchar(16) NOT NULL CHECK (scope IN ('node','backend')),
    proxy_name varchar(128) NOT NULL,
    bytes_in bigint NOT NULL DEFAULT 0 CHECK (bytes_in >= 0),
    bytes_out bigint NOT NULL DEFAULT 0 CHECK (bytes_out >= 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (node_id,month,scope,proxy_name),
    CHECK (
        (scope = 'node' AND proxy_name = '') OR
        (scope = 'backend' AND length(proxy_name) BETWEEN 1 AND 128)
    )
);

-- Supports retention jobs such as DELETE ... WHERE month < $cutoff without a
-- full table scan. Node/month reads use the primary key prefix.
CREATE INDEX traffic_monthly_retention_idx ON traffic_monthly(month);
