CREATE TABLE node_traffic_rate_samples (
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    sampled_at timestamptz NOT NULL,
    rx_bytes_per_second double precision NOT NULL CHECK (
        rx_bytes_per_second >= 0 AND rx_bytes_per_second < 'Infinity'::double precision
    ),
    tx_bytes_per_second double precision NOT NULL CHECK (
        tx_bytes_per_second >= 0 AND tx_bytes_per_second < 'Infinity'::double precision
    ),
    PRIMARY KEY (node_id, sampled_at)
);

CREATE INDEX node_traffic_rate_samples_sampled_at_idx
    ON node_traffic_rate_samples(sampled_at);

-- One row gates the global retention sweep so heartbeats do not all scan the
-- same timestamp index. Per-node pruning still runs for every inserted sample.
CREATE TABLE traffic_rate_retention_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    last_cleanup_at timestamptz NOT NULL DEFAULT '-infinity'::timestamptz
);

INSERT INTO traffic_rate_retention_state(singleton) VALUES (true);
