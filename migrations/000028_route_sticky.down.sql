ALTER TABLE routes
    DROP COLUMN sticky_enabled,
    DROP COLUMN sticky_table_size,
    DROP COLUMN sticky_expire;
