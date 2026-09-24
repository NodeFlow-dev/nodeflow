-- Sticky sessions now use balance source + hash-type consistent (no in-memory
-- table). Remove the previously persisted size/expire columns that are no
-- longer needed.
ALTER TABLE routes
    DROP COLUMN IF EXISTS sticky_table_size,
    DROP COLUMN IF EXISTS sticky_expire;
