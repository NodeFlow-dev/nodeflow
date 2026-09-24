-- F1: conditional expect-proxy by source IP/CIDR on a per-route basis.
-- The renderer unions accept_proxy_from across all routes on the same listener.
ALTER TABLE routes
    ADD COLUMN accept_proxy_from text[] NOT NULL DEFAULT '{}';
