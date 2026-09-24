-- Heartbeat history is intentionally not retained here: monthly and reset-safe
-- HAProxy counters already live in the traffic tables. Block concurrent Agent
-- writes while historical rows are collapsed and uniqueness is established.
LOCK TABLE node_heartbeats IN ACCESS EXCLUSIVE MODE;

WITH ranked AS (
    SELECT
        id,
        row_number() OVER (
            PARTITION BY node_id
            ORDER BY received_at DESC, id DESC
        ) AS position
    FROM node_heartbeats
)
DELETE FROM node_heartbeats AS heartbeat
USING ranked
WHERE heartbeat.id = ranked.id
  AND ranked.position > 1;

DROP INDEX node_heartbeats_node_received_idx;

ALTER TABLE node_heartbeats
    ADD CONSTRAINT node_heartbeats_node_id_key UNIQUE (node_id);
