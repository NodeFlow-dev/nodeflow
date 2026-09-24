ALTER TABLE routes
    DROP CONSTRAINT routes_node_id_hostname_key,
    DROP CONSTRAINT routes_hostname_check,
    DROP CONSTRAINT routes_target_host_check,
    DROP CONSTRAINT routes_target_port_check,
    ADD COLUMN listener_ip text NOT NULL DEFAULT '*',
    ADD COLUMN listener_port integer NOT NULL DEFAULT 443,
    ADD COLUMN fallback boolean NOT NULL DEFAULT false,
    ADD COLUMN target_type text NOT NULL DEFAULT 'tcp',
    ADD COLUMN unix_socket_path text NOT NULL DEFAULT '',
    ADD COLUMN proxy_protocol text NOT NULL DEFAULT 'none',
    ADD COLUMN quota_bytes bigint,
    ADD COLUMN custom_fragment text NOT NULL DEFAULT '';

-- The old HTTP API already emitted lowercase, dot-trimmed hostnames. Normalize
-- direct legacy inserts as well before applying the stricter SNI constraints.
UPDATE routes
SET hostname = lower(trim(trailing '.' FROM btrim(hostname)));

ALTER TABLE routes
    ADD CONSTRAINT routes_listener_ip_check CHECK (
        CASE
            WHEN listener_ip = '*' THEN true
            ELSE listener_ip = host(listener_ip::inet)
        END
    ),
    ADD CONSTRAINT routes_listener_port_check CHECK (listener_port BETWEEN 1 AND 65535),
    ADD CONSTRAINT routes_hostname_legacy_check CHECK (
        (fallback AND hostname = '') OR
        (NOT fallback AND length(hostname) BETWEEN 1 AND 253 AND hostname = lower(hostname))
    ),
    ADD CONSTRAINT routes_target_type_check CHECK (target_type IN ('tcp','unix')),
    ADD CONSTRAINT routes_target_shape_check CHECK (
        (
            target_type = 'tcp' AND
            length(target_host) BETWEEN 1 AND 253 AND
            target_host !~ '[[:space:][:cntrl:]]' AND
            target_host ~ '^[A-Za-z0-9.:-]+$' AND
            target_port BETWEEN 1 AND 65535 AND
            unix_socket_path = ''
        ) OR (
            target_type = 'unix' AND
            target_host = '' AND
            target_port = 0 AND
            octet_length(unix_socket_path) BETWEEN 2 AND 107 AND
            unix_socket_path ~ '^/[A-Za-z0-9._/-]+$' AND
            unix_socket_path !~ '(^|/)[.][.]?(/|$)|//|/$'
        )
    ),
    ADD CONSTRAINT routes_proxy_protocol_check CHECK (proxy_protocol IN ('none','v1','v2')),
    ADD CONSTRAINT routes_quota_bytes_check CHECK (quota_bytes IS NULL OR quota_bytes > 0),
    ADD CONSTRAINT routes_custom_fragment_check CHECK (
        octet_length(custom_fragment) <= 8192 AND
        regexp_replace(custom_fragment, E'[\n\t]', '', 'g') !~ '[[:cntrl:]]'
    ),
    ADD CONSTRAINT routes_identity_listener_unique UNIQUE (id,node_id,listener_ip,listener_port,fallback);

CREATE TABLE route_snis (
    route_id uuid NOT NULL,
    node_id uuid NOT NULL,
    listener_ip text NOT NULL,
    listener_port integer NOT NULL CHECK (listener_port BETWEEN 1 AND 65535),
    fallback boolean NOT NULL DEFAULT false CHECK (NOT fallback),
    position smallint NOT NULL CHECK (position BETWEEN 0 AND 63),
    sni text NOT NULL CHECK (
        length(sni) BETWEEN 1 AND 253 AND
        sni = lower(sni) AND
        sni ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?[.])*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'
    ),
    PRIMARY KEY (route_id,position),
    UNIQUE (route_id,sni),
    CONSTRAINT route_snis_route_listener_fk
        FOREIGN KEY (route_id,node_id,listener_ip,listener_port,fallback)
        REFERENCES routes(id,node_id,listener_ip,listener_port,fallback)
        ON UPDATE CASCADE ON DELETE CASCADE
);

INSERT INTO route_snis(route_id,node_id,listener_ip,listener_port,position,sni)
SELECT id,node_id,listener_ip,listener_port,0,lower(trim(trailing '.' FROM hostname))
FROM routes;

CREATE UNIQUE INDEX route_snis_listener_sni_unique
    ON route_snis(node_id,listener_ip,listener_port,sni);
CREATE INDEX route_snis_node_listener_idx
    ON route_snis(node_id,listener_ip,listener_port);
CREATE INDEX routes_node_listener_idx
    ON routes(node_id,listener_ip,listener_port);
CREATE UNIQUE INDEX routes_listener_fallback_unique
    ON routes(node_id,listener_ip,listener_port) WHERE fallback;
