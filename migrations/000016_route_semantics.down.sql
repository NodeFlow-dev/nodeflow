DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM route_snis
        GROUP BY node_id,listener_ip,listener_port,sni
        HAVING count(*) > 1
    ) OR EXISTS (
        SELECT 1
        FROM routes
        WHERE fallback
        GROUP BY node_id,listener_ip,listener_port
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'cannot roll back route semantics migration while overlapping drafts exist';
    END IF;
END $$;

DROP INDEX route_snis_listener_sni_idx;
DROP INDEX routes_listener_fallback_idx;

CREATE UNIQUE INDEX route_snis_listener_sni_unique
    ON route_snis(node_id,listener_ip,listener_port,sni);
CREATE UNIQUE INDEX routes_listener_fallback_unique
    ON routes(node_id,listener_ip,listener_port) WHERE fallback;

ALTER TABLE routes
    DROP CONSTRAINT routes_match_shape_check,
    DROP CONSTRAINT routes_match_mode_check,
    DROP CONSTRAINT routes_name_check,
    DROP COLUMN health_check,
    DROP COLUMN match_mode,
    DROP COLUMN name;
