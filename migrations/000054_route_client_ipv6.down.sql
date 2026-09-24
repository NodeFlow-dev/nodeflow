-- IPv4-only routes fall back to the default IPv6-capable client tables
-- (their sticky_ipv6_prefix is already 0, i.e. the /64 default).
ALTER TABLE routes
    DROP CONSTRAINT IF EXISTS routes_client_ipv6_prefix_check,
    DROP COLUMN IF EXISTS client_ipv6;
