-- F3: multiple server endpoints per route backend with optional backup flag.
-- Each existing route's single target is visible as server #1 via the view;
-- the route_servers table holds explicit multi-server overrides.
CREATE TABLE route_servers (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    route_id      uuid        NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
    node_id       uuid        NOT NULL,
    position      integer     NOT NULL CHECK (position >= 1 AND position <= 16),
    name          text        NOT NULL CHECK (length(name) >= 1 AND length(name) <= 64),
    target_type   text        NOT NULL CHECK (target_type IN ('tcp', 'unix')),
    host          text        NOT NULL DEFAULT '',
    port          integer     NOT NULL DEFAULT 0,
    unix_socket_path text     NOT NULL DEFAULT '',
    backup        boolean     NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (route_id, position),
    UNIQUE (route_id, name)
);
CREATE INDEX route_servers_route_id_idx ON route_servers(route_id);
CREATE INDEX route_servers_node_id_idx  ON route_servers(node_id);
