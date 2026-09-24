-- node_traffic_rate_samples used to contain host /proc/net/dev rates. Those
-- counters include unrelated interfaces and can count forwarded traffic more
-- than once, so they cannot be mixed with HAProxy counter-derived samples.
TRUNCATE TABLE node_traffic_rate_samples;

ALTER TABLE traffic_counter_state
    ADD COLUMN source_generation text NOT NULL DEFAULT '';

CREATE TABLE route_traffic_rate_samples (
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    route_id uuid NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
    sampled_at timestamptz NOT NULL,
    rx_bytes_per_second double precision NOT NULL CHECK (
        rx_bytes_per_second >= 0 AND rx_bytes_per_second < 'Infinity'::double precision
    ),
    tx_bytes_per_second double precision NOT NULL CHECK (
        tx_bytes_per_second >= 0 AND tx_bytes_per_second < 'Infinity'::double precision
    ),
    PRIMARY KEY (node_id,route_id,sampled_at)
);

CREATE INDEX route_traffic_rate_samples_sampled_at_idx
    ON route_traffic_rate_samples(sampled_at);

CREATE INDEX route_traffic_rate_samples_route_sampled_at_idx
    ON route_traffic_rate_samples(route_id,sampled_at DESC);
