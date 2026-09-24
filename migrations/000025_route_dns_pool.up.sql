ALTER TABLE routes
    ADD COLUMN dns_pool boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT routes_dns_pool_shape_check
        CHECK (NOT dns_pool OR (target_type = 'tcp' AND health_check));
