-- Reverse the two-axis model back into the pre-000052 flat sticky_mode.
-- sni cannot be restored: 000052's up migration already folded every 'sni'
-- row into 'source' (the render path for sni is gone), so a down migration
-- run after 'sni' routes existed loses that distinction permanently.
UPDATE routes SET sticky_mode = 'leastconn' WHERE sticky_mode = 'none' AND balance_algorithm = 'leastconn';
UPDATE routes SET sticky_mode = 'roundrobin' WHERE sticky_mode = 'none' AND balance_algorithm <> 'leastconn';
-- source and source_table already match the pre-000052 shape unchanged.

ALTER TABLE routes DROP CONSTRAINT IF EXISTS routes_sticky_mode_check;
ALTER TABLE routes ADD CONSTRAINT routes_sticky_mode_check
    CHECK (sticky_mode IN ('', 'source', 'source_table', 'leastconn', 'roundrobin', 'sni'));

ALTER TABLE routes
    DROP CONSTRAINT IF EXISTS routes_balance_algorithm_check,
    DROP CONSTRAINT IF EXISTS routes_balance_random_draws_check,
    DROP CONSTRAINT IF EXISTS routes_leastping_tolerance_check,
    DROP CONSTRAINT IF EXISTS routes_leastping_tolerance_ms_check,
    DROP CONSTRAINT IF EXISTS routes_sticky_hash_check,
    DROP CONSTRAINT IF EXISTS routes_sticky_hash_balance_factor_check,
    DROP CONSTRAINT IF EXISTS routes_sticky_table_entries_check,
    DROP CONSTRAINT IF EXISTS routes_sticky_ipv6_prefix_check,
    DROP COLUMN IF EXISTS balance_algorithm,
    DROP COLUMN IF EXISTS balance_random_draws,
    DROP COLUMN IF EXISTS leastping_tolerance,
    DROP COLUMN IF EXISTS leastping_tolerance_ms,
    DROP COLUMN IF EXISTS sticky_hash,
    DROP COLUMN IF EXISTS sticky_hash_balance_factor,
    DROP COLUMN IF EXISTS sticky_table_entries,
    DROP COLUMN IF EXISTS sticky_ipv6_prefix,
    DROP COLUMN IF EXISTS slowstart;

ALTER TABLE route_servers
    DROP CONSTRAINT IF EXISTS route_servers_weight_check,
    DROP CONSTRAINT IF EXISTS route_servers_cost_check,
    DROP COLUMN IF EXISTS weight,
    DROP COLUMN IF EXISTS cost,
    DROP COLUMN IF EXISTS ip_weights;
