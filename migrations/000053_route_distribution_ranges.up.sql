-- Numeric inputs for the route editor's «Распределение клиентов» block:
--   sticky_table_entries: any <n>k (1..10000) or <n>m (1..10) instead of the
--     10k/100k/1m presets (which stay valid and render unchanged);
--   balance_random_draws: 1..5 instead of 1..2 (HAProxy balance random(<n>)).
-- Per-IP leastping cost lives inside route_servers.ip_weights (jsonb) and
-- needs no schema change. See internal/panel/route_validation.go.
ALTER TABLE routes DROP CONSTRAINT IF EXISTS routes_sticky_table_entries_check;
ALTER TABLE routes ADD CONSTRAINT routes_sticky_table_entries_check
    CHECK (sticky_table_entries ~ '^(|[1-9][0-9]{0,4}k|([1-9]|10)m)$');

ALTER TABLE routes DROP CONSTRAINT IF EXISTS routes_balance_random_draws_check;
ALTER TABLE routes ADD CONSTRAINT routes_balance_random_draws_check
    CHECK (balance_random_draws BETWEEN 0 AND 5);
