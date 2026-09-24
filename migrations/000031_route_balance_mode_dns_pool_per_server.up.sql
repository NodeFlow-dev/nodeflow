-- balance_mode: 'pool' (round-robin / consistent-hash) or 'failover' (order-based backup).
-- Per-server dns_pool: resolve all A/AAAA records for this server's hostname.
-- preferred_ip: optional primary IP hint for a failover dns-pool first server.
ALTER TABLE routes
    ADD COLUMN balance_mode text NOT NULL DEFAULT 'pool'
        CHECK (balance_mode IN ('pool', 'failover'));

ALTER TABLE route_servers
    ADD COLUMN dns_pool    boolean NOT NULL DEFAULT false,
    ADD COLUMN preferred_ip text   NOT NULL DEFAULT '';

-- Migrate existing routes.dns_pool=true → servers[0].dns_pool=true.
UPDATE route_servers rs
SET dns_pool = true
FROM routes r
WHERE rs.route_id = r.id
  AND r.dns_pool = true
  AND rs.position = (
      SELECT MIN(rs2.position) FROM route_servers rs2 WHERE rs2.route_id = r.id
  );
