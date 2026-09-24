-- Follow-up to 000031 (balance_mode, per-server DNS pool).
--
-- 1. Routes that already have backup servers were created with explicit
--    backup flags (1.1.0-rc API). 000031 defaulted them to balance_mode='pool',
--    so the editor loaded them as a pool and the first save turned every
--    backup into a primary. Mark them as failover; the stored backup flags
--    are kept as they are and remain authoritative for rendering.
UPDATE routes r
SET balance_mode = 'failover'
WHERE r.balance_mode <> 'failover'
  AND EXISTS (SELECT 1 FROM route_servers rs WHERE rs.route_id = r.id AND rs.backup);

-- 2. servers[] is the source of truth for multi-server routes. Route-level
--    dns_pool is legacy-only (no servers, or the single migrated server), so
--    it must not stay set on routes with several servers: the per-server flag
--    already carries it (000031 copied it to the first server).
UPDATE routes r
SET dns_pool = false
WHERE r.dns_pool
  AND (SELECT count(*) FROM route_servers rs WHERE rs.route_id = r.id) > 1;

-- 3. A single explicit server without preferred_ip is the legacy
--    single-target route. The 1.1.0 editor always sent one synthetic server,
--    which renamed the HAProxy server away from nf_srv_<id> and broke runtime
--    quota enforcement. Fold such rows back into the route target when the
--    route target already mirrors the server (it does for every editor save).
UPDATE routes r
SET dns_pool = true, health_check = true
FROM route_servers rs
WHERE rs.route_id = r.id
  AND rs.dns_pool AND rs.preferred_ip = '' AND NOT rs.backup
  AND r.target_type = rs.target_type AND r.target_host = rs.host
  AND r.target_port = rs.port AND r.unix_socket_path = rs.unix_socket_path
  AND (SELECT count(*) FROM route_servers x WHERE x.route_id = r.id) = 1;

DELETE FROM route_servers rs
USING routes r
WHERE rs.route_id = r.id
  AND rs.preferred_ip = '' AND NOT rs.backup
  AND r.target_type = rs.target_type AND r.target_host = rs.host
  AND r.target_port = rs.port AND r.unix_socket_path = rs.unix_socket_path
  AND r.dns_pool = rs.dns_pool
  AND (SELECT count(*) FROM route_servers x WHERE x.route_id = r.id) = 1;

UPDATE routes r
SET balance_mode = 'pool'
WHERE NOT EXISTS (SELECT 1 FROM route_servers rs WHERE rs.route_id = r.id)
  AND r.balance_mode <> 'pool';

-- 4. Consistency constraints the API already enforces. Values the API never
--    accepted are cleared first so the constraints can be validated.
UPDATE route_servers SET preferred_ip = '' WHERE preferred_ip <> '' AND NOT dns_pool;
UPDATE route_servers SET dns_pool = false, preferred_ip = '' WHERE dns_pool AND target_type <> 'tcp';

ALTER TABLE route_servers
    ADD CONSTRAINT route_servers_preferred_ip_requires_pool CHECK (preferred_ip = '' OR dns_pool),
    ADD CONSTRAINT route_servers_dns_pool_tcp CHECK (NOT dns_pool OR target_type = 'tcp');
