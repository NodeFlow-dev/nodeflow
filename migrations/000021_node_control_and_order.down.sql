DROP TABLE IF EXISTS node_haproxy_control;
DROP INDEX IF EXISTS routes_node_sort_order_unique;
ALTER TABLE routes DROP COLUMN IF EXISTS sort_order;
DROP INDEX IF EXISTS nodes_sort_order_unique;
ALTER TABLE nodes DROP COLUMN IF EXISTS sort_order;
