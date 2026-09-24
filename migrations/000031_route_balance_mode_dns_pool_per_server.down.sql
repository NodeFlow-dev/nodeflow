ALTER TABLE route_servers
    DROP COLUMN IF EXISTS dns_pool,
    DROP COLUMN IF EXISTS preferred_ip;

ALTER TABLE routes
    DROP COLUMN IF EXISTS balance_mode;

-- Note: per-server DNS pools and preferred_ip are dropped without a fallback.
-- Routes that used a per-server pool render afterwards as a static
-- "server ... resolvers nf_dns" (one resolved address). balance_mode is lost;
-- stored route_servers.backup flags keep the failover layout.
