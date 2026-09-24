DROP TABLE IF EXISTS route_traffic_rate_samples;

ALTER TABLE traffic_counter_state
    DROP COLUMN IF EXISTS source_generation;

-- The old /proc/net/dev samples were deliberately discarded on upgrade and
-- cannot be restored by the down migration.
TRUNCATE TABLE node_traffic_rate_samples;
