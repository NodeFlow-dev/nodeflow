ALTER TABLE routes
    ADD COLUMN quota_period varchar(32) NOT NULL DEFAULT 'calendar_month',
    ADD CONSTRAINT routes_quota_period_check
        CHECK (quota_period IN ('hourly','daily','calendar_month','monthly_from_creation'));

ALTER TABLE node_heartbeats
    ADD COLUMN traffic_instance_id uuid,
    ADD COLUMN traffic_instance_started_at timestamptz,
    ADD COLUMN traffic_sample_seq bigint,
    ADD CONSTRAINT node_heartbeats_traffic_order_check CHECK (
        (traffic_instance_id IS NULL AND traffic_instance_started_at IS NULL AND traffic_sample_seq IS NULL) OR
        (traffic_instance_id IS NOT NULL AND traffic_instance_started_at IS NOT NULL AND traffic_sample_seq > 0)
    );

-- Reset-safe HAProxy deltas are also accumulated into explicit UTC quota
-- windows. traffic_monthly remains the reporting source for calendar-month
-- charts; this table is the enforcement source and can mix periods per route.
CREATE TABLE traffic_quota_usage (
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    period varchar(32) NOT NULL
        CHECK (period IN ('hourly','daily','calendar_month','monthly_from_creation')),
    window_start timestamptz NOT NULL,
    window_end timestamptz NOT NULL,
    proxy_name varchar(128) NOT NULL CHECK (length(proxy_name) BETWEEN 1 AND 128),
    bytes_in bigint NOT NULL DEFAULT 0 CHECK (bytes_in >= 0),
    bytes_out bigint NOT NULL DEFAULT 0 CHECK (bytes_out >= 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (node_id,period,window_start,proxy_name),
    CHECK (window_end > window_start)
);

CREATE INDEX traffic_quota_usage_retention_idx ON traffic_quota_usage(window_end);
CREATE INDEX traffic_quota_usage_current_idx
    ON traffic_quota_usage(node_id,proxy_name,period,window_start,window_end);

-- Preserve all existing default calendar-month quota usage exactly. Finer and
-- anniversary windows begin accumulating after this migration because monthly
-- aggregates cannot be split safely into smaller windows.
INSERT INTO traffic_quota_usage(
    node_id,period,window_start,window_end,proxy_name,bytes_in,bytes_out,updated_at
)
SELECT node_id,'calendar_month',
       (month::timestamp AT TIME ZONE 'UTC'),
       ((month + interval '1 month')::timestamp AT TIME ZONE 'UTC'),
       proxy_name,bytes_in,bytes_out,updated_at
FROM traffic_monthly
WHERE scope='backend';
