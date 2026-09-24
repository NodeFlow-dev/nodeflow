-- F4: optional sticky sessions by client IP for TCP routes.
ALTER TABLE routes
    ADD COLUMN sticky_enabled    boolean NOT NULL DEFAULT false,
    ADD COLUMN sticky_table_size text    NOT NULL DEFAULT '1m',
    ADD COLUMN sticky_expire     text    NOT NULL DEFAULT '30m';
