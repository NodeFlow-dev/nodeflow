BEGIN;

ALTER TABLE node_heartbeats
    DROP CONSTRAINT node_heartbeats_node_id_key;

CREATE INDEX node_heartbeats_node_received_idx
    ON node_heartbeats(node_id, received_at DESC);

COMMIT;
