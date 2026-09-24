ALTER TABLE routes
    ADD COLUMN sticky_table_size text NOT NULL DEFAULT '1m',
    ADD COLUMN sticky_expire     text NOT NULL DEFAULT '30m';
