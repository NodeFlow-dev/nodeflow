ALTER TABLE nodes ADD COLUMN sort_order bigint;

WITH ranked AS (
    SELECT id,row_number() OVER (ORDER BY created_at DESC,id) AS position
    FROM nodes
)
UPDATE nodes SET sort_order=ranked.position FROM ranked WHERE nodes.id=ranked.id;

ALTER TABLE nodes ALTER COLUMN sort_order SET NOT NULL;
CREATE UNIQUE INDEX nodes_sort_order_unique ON nodes(sort_order);

ALTER TABLE routes ADD COLUMN sort_order bigint;

WITH ranked AS (
    SELECT id,row_number() OVER (PARTITION BY node_id ORDER BY created_at DESC,id) AS position
    FROM routes
)
UPDATE routes SET sort_order=ranked.position FROM ranked WHERE routes.id=ranked.id;

ALTER TABLE routes ALTER COLUMN sort_order SET NOT NULL;
CREATE UNIQUE INDEX routes_node_sort_order_unique ON routes(node_id,sort_order);

CREATE TABLE node_haproxy_control (
    node_id uuid PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    desired_enabled boolean NOT NULL DEFAULT true,
    supported boolean NOT NULL DEFAULT false,
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    actual_enabled boolean,
    active_state varchar(32) NOT NULL DEFAULT 'unknown',
    report_generation bigint NOT NULL DEFAULT 0 CHECK (report_generation >= 0),
    last_error varchar(200) NOT NULL DEFAULT '',
    reported_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (active_state IN ('unknown','active','reloading','inactive','failed','activating','deactivating')),
    CHECK (report_generation <= generation)
);

INSERT INTO node_haproxy_control(node_id) SELECT id FROM nodes;
