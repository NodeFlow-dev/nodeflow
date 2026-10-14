-- Before 2.2.0 a balance_mode='failover' backend ignored its stored client
-- stickiness (sticky_mode) unless it was a legacy route-level DNS pool: the
-- renderer emitted only the balance_algorithm line. Migrations 000041, 000051
-- and 000052 could nevertheless leave sticky values on such rows (000051 set
-- sticky_mode='source' WHERE sticky_enabled for every row). 2.2.0 renders
-- stickiness for failover too, so those leftovers would silently change the
-- HAProxy config. Reset sticky_mode to the failover legacy value ''
-- ("no stickiness, balance_algorithm decides"), which renders exactly what
-- the old renderer produced. Every other column is kept: balance_algorithm
-- already drove the rendered balance line, and the remaining sticky columns
-- are inert for sticky_mode='' in failover (sticky_enabled is kept so the
-- route fingerprint of 000051 rows stays identical). Legacy route-level DNS
-- pools (dns_pool with no servers, or one server without preferred_ip)
-- always honoured sticky_mode and are left untouched.
UPDATE routes r
SET sticky_mode = ''
WHERE r.balance_mode = 'failover'
  AND r.sticky_mode IN ('source', 'source_table', 'leastconn')
  AND NOT (
    r.dns_pool AND (
      NOT EXISTS (SELECT 1 FROM route_servers s WHERE s.route_id = r.id)
      OR (
        (SELECT count(*) FROM route_servers s WHERE s.route_id = r.id) = 1
        AND NOT EXISTS (SELECT 1 FROM route_servers s WHERE s.route_id = r.id AND s.preferred_ip <> '')
      )
    )
  );
