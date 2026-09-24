ALTER TABLE node_traffic_rate_samples
    ADD COLUMN cpu_percent double precision,
    ADD COLUMN memory_percent double precision,
    ADD CONSTRAINT node_traffic_rate_samples_cpu_percent_check CHECK (
        cpu_percent IS NULL OR (cpu_percent >= 0 AND cpu_percent <= 100 AND cpu_percent < 'Infinity'::double precision)
    ),
    ADD CONSTRAINT node_traffic_rate_samples_memory_percent_check CHECK (
        memory_percent IS NULL OR (memory_percent >= 0 AND memory_percent <= 100 AND memory_percent < 'Infinity'::double precision)
    );

-- UTC daily node totals are reset-safe HAProxy deltas, like traffic_monthly.
-- Existing monthly rows cannot be split into days safely, so daily accounting
-- starts when this migration is applied.
CREATE TABLE traffic_daily (
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    day date NOT NULL,
    bytes_in bigint NOT NULL DEFAULT 0 CHECK (bytes_in >= 0),
    bytes_out bigint NOT NULL DEFAULT 0 CHECK (bytes_out >= 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (node_id,day)
);

CREATE INDEX traffic_daily_retention_idx ON traffic_daily(day);
