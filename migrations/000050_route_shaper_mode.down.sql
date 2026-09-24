ALTER TABLE routes
    DROP CONSTRAINT IF EXISTS routes_shaper_mode_check,
    DROP COLUMN IF EXISTS shaper_mode;
