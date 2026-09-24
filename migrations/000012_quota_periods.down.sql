DROP TABLE traffic_quota_usage;

ALTER TABLE node_heartbeats
    DROP CONSTRAINT node_heartbeats_traffic_order_check,
    DROP COLUMN traffic_sample_seq,
    DROP COLUMN traffic_instance_started_at,
    DROP COLUMN traffic_instance_id;

ALTER TABLE routes
    DROP CONSTRAINT routes_quota_period_check,
    DROP COLUMN quota_period;
