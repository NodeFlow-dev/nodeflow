-- Only the constraints are reverted. The data normalisation of the up
-- migration (failover mode for routes with backups, legacy single-target
-- folding, route-level dns_pool cleared for multi-server routes) is kept: it
-- matches what the API writes and renders identically on older panels.
ALTER TABLE route_servers
    DROP CONSTRAINT IF EXISTS route_servers_preferred_ip_requires_pool,
    DROP CONSTRAINT IF EXISTS route_servers_dns_pool_tcp;
