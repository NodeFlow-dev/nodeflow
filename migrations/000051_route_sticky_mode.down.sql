ALTER TABLE routes
    DROP CONSTRAINT IF EXISTS routes_sticky_mode_check,
    DROP COLUMN IF EXISTS sticky_ttl,
    DROP COLUMN IF EXISTS sticky_mode;
