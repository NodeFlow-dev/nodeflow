-- Splits the old flat sticky_mode into two orthogonal pool-mode settings:
--   balance_algorithm: choice of server for a NEW client (roundrobin,
--     static-rr, random, leastconn, leastping).
--   sticky_mode: client stickiness (none, source, source_table). The CHECK
--     keeps the old leastconn/roundrobin/sni values for safety, but fresh
--     writes only ever store '', none, source or source_table.
-- Plus their sub-fields and the common slowstart/per-server weight/cost/
-- ip_weights settings. See internal/panel/route_validation.go
-- (resolvePoolDistribution) and internal/panel/haproxy_renderer.go.
ALTER TABLE routes
    ADD COLUMN balance_algorithm text NOT NULL DEFAULT ''
        CONSTRAINT routes_balance_algorithm_check
        CHECK (balance_algorithm IN ('', 'roundrobin', 'static-rr', 'random', 'leastconn', 'leastping')),
    ADD COLUMN balance_random_draws smallint NOT NULL DEFAULT 0
        CONSTRAINT routes_balance_random_draws_check
        CHECK (balance_random_draws BETWEEN 0 AND 2),
    ADD COLUMN leastping_tolerance numeric(3,2) NOT NULL DEFAULT 0
        CONSTRAINT routes_leastping_tolerance_check
        CHECK (leastping_tolerance >= 0 AND leastping_tolerance <= 1),
    ADD COLUMN leastping_tolerance_ms integer NOT NULL DEFAULT 0
        CONSTRAINT routes_leastping_tolerance_ms_check
        CHECK (leastping_tolerance_ms BETWEEN 0 AND 1000),
    ADD COLUMN sticky_hash text NOT NULL DEFAULT ''
        CONSTRAINT routes_sticky_hash_check
        CHECK (sticky_hash IN ('', 'consistent', 'map-based')),
    ADD COLUMN sticky_hash_balance_factor integer NOT NULL DEFAULT 0
        CONSTRAINT routes_sticky_hash_balance_factor_check
        CHECK (sticky_hash_balance_factor = 0 OR sticky_hash_balance_factor BETWEEN 101 AND 1000),
    ADD COLUMN sticky_table_entries text NOT NULL DEFAULT ''
        CONSTRAINT routes_sticky_table_entries_check
        CHECK (sticky_table_entries IN ('', '10k', '100k', '1m')),
    ADD COLUMN sticky_ipv6_prefix smallint NOT NULL DEFAULT 0
        CONSTRAINT routes_sticky_ipv6_prefix_check
        CHECK (sticky_ipv6_prefix = 0 OR sticky_ipv6_prefix BETWEEN 32 AND 128),
    ADD COLUMN slowstart text NOT NULL DEFAULT '';

-- sticky_mode keeps its old allowed values too (rows that somehow still carry
-- them, e.g. a restored backup): the backfill below re-maps every existing
-- row, but the CHECK stays permissive for safety.
ALTER TABLE routes DROP CONSTRAINT IF EXISTS routes_sticky_mode_check;
ALTER TABLE routes ADD CONSTRAINT routes_sticky_mode_check
    CHECK (sticky_mode IN ('', 'none', 'source', 'source_table', 'leastconn', 'roundrobin', 'sni'));

-- Backfill: fold the old flat sticky_mode into the new two-axis model.
UPDATE routes SET balance_algorithm = 'leastconn', sticky_mode = 'none' WHERE sticky_mode = 'leastconn';
UPDATE routes SET balance_algorithm = 'roundrobin', sticky_mode = 'none' WHERE sticky_mode = 'roundrobin';
-- sni balanced whole domains, not clients; the closest surviving mode is a
-- per-client consistent hash. The render path for sni is removed and cannot
-- be restored by the down migration.
UPDATE routes SET sticky_mode = 'source' WHERE sticky_mode = 'sni';
-- rc12 source_table clients left balance_algorithm at its column default '':
-- the renderer always picked the first server by leastconn for them.
UPDATE routes SET balance_algorithm = 'leastconn' WHERE sticky_mode = 'source_table' AND balance_algorithm = '';

-- route_servers: per-server weight, cost (leastping) and agent-applied
-- ip_weights runtime overrides for DNS-pool template slots.
ALTER TABLE route_servers
    ADD COLUMN weight integer NOT NULL DEFAULT 0
        CONSTRAINT route_servers_weight_check
        CHECK (weight BETWEEN 0 AND 256),
    ADD COLUMN cost numeric(6,2) NOT NULL DEFAULT 0
        CONSTRAINT route_servers_cost_check
        CHECK (cost >= 0 AND cost <= 100),
    ADD COLUMN ip_weights jsonb NOT NULL DEFAULT '[]';
