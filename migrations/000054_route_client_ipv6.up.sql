-- Per-route switch for IPv6 client keys. true (the default, and the value
-- for every existing route) keeps the byte-identical render: type ipv6
-- stick-tables keyed by src,ipmask(32,<prefix>). false renders IPv4-only
-- client tables (type ip, key src) for nodes without IPv6; the IPv6
-- grouping prefix is then meaningless and must be 0.
ALTER TABLE routes
    ADD COLUMN client_ipv6 boolean NOT NULL DEFAULT true;

ALTER TABLE routes
    ADD CONSTRAINT routes_client_ipv6_prefix_check
        CHECK (client_ipv6 OR sticky_ipv6_prefix = 0);
