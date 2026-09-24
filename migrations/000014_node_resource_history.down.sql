DROP TABLE IF EXISTS traffic_daily;

ALTER TABLE node_traffic_rate_samples
    DROP CONSTRAINT IF EXISTS node_traffic_rate_samples_memory_percent_check,
    DROP CONSTRAINT IF EXISTS node_traffic_rate_samples_cpu_percent_check,
    DROP COLUMN IF EXISTS memory_percent,
    DROP COLUMN IF EXISTS cpu_percent;
