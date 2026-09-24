BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM routes r
        WHERE r.listener_ip <> '*'
           OR r.listener_port <> 443
           OR r.fallback
           OR r.target_type <> 'tcp'
           OR r.unix_socket_path <> ''
           OR r.proxy_protocol <> 'none'
           OR r.quota_bytes IS NOT NULL
           OR r.custom_fragment <> ''
           OR (SELECT count(*) FROM route_snis s WHERE s.route_id=r.id) <> 1
    ) THEN
        RAISE EXCEPTION 'cannot roll back route builder migration while extended routes exist';
    END IF;
END $$;

DROP INDEX routes_listener_fallback_unique;
DROP INDEX routes_node_listener_idx;
DROP TABLE route_snis;

ALTER TABLE routes
    DROP CONSTRAINT routes_identity_listener_unique,
    DROP CONSTRAINT routes_custom_fragment_check,
    DROP CONSTRAINT routes_quota_bytes_check,
    DROP CONSTRAINT routes_proxy_protocol_check,
    DROP CONSTRAINT routes_target_shape_check,
    DROP CONSTRAINT routes_target_type_check,
    DROP CONSTRAINT routes_hostname_legacy_check,
    DROP CONSTRAINT routes_listener_port_check,
    DROP CONSTRAINT routes_listener_ip_check,
    DROP COLUMN custom_fragment,
    DROP COLUMN quota_bytes,
    DROP COLUMN proxy_protocol,
    DROP COLUMN unix_socket_path,
    DROP COLUMN target_type,
    DROP COLUMN fallback,
    DROP COLUMN listener_port,
    DROP COLUMN listener_ip,
    ADD CONSTRAINT routes_hostname_check CHECK (length(hostname) BETWEEN 1 AND 253),
    ADD CONSTRAINT routes_target_host_check CHECK (length(target_host) BETWEEN 1 AND 253),
    ADD CONSTRAINT routes_target_port_check CHECK (target_port BETWEEN 1 AND 65535),
    ADD CONSTRAINT routes_node_id_hostname_key UNIQUE (node_id,hostname);

COMMIT;
