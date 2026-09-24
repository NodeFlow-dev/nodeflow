-- Values outside the pre-000053 presets fall back to the defaults (100k
-- entries, 2 random draws) before the narrow CHECKs are restored.
UPDATE routes SET sticky_table_entries = '' WHERE sticky_table_entries NOT IN ('', '10k', '100k', '1m');
UPDATE routes SET balance_random_draws = 2 WHERE balance_random_draws > 2;

ALTER TABLE routes DROP CONSTRAINT IF EXISTS routes_sticky_table_entries_check;
ALTER TABLE routes ADD CONSTRAINT routes_sticky_table_entries_check
    CHECK (sticky_table_entries IN ('', '10k', '100k', '1m'));

ALTER TABLE routes DROP CONSTRAINT IF EXISTS routes_balance_random_draws_check;
ALTER TABLE routes ADD CONSTRAINT routes_balance_random_draws_check
    CHECK (balance_random_draws BETWEEN 0 AND 2);
