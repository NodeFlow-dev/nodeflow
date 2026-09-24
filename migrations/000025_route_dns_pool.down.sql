ALTER TABLE routes
    DROP CONSTRAINT routes_dns_pool_shape_check,
    DROP COLUMN dns_pool;
