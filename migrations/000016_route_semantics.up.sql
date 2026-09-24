ALTER TABLE routes
    ADD COLUMN name varchar(80) NOT NULL DEFAULT '',
    ADD COLUMN match_mode varchar(32) NOT NULL DEFAULT 'sni',
    ADD COLUMN health_check boolean NOT NULL DEFAULT true;

UPDATE routes
SET match_mode = CASE
        WHEN NOT fallback THEN 'sni'
        WHEN listener_ip IN ('*','0.0.0.0','::') THEN 'any_tcp'
        ELSE 'destination_ip'
    END,
    name = CASE
        WHEN NOT fallback THEN hostname
        WHEN listener_ip IN ('*','0.0.0.0','::') THEN 'tcp-' || listener_port::text
        ELSE 'ip-' || listener_ip || '-' || listener_port::text
    END;

ALTER TABLE routes
    ADD CONSTRAINT routes_name_check CHECK (
        length(name) BETWEEN 1 AND 80 AND name !~ '[[:cntrl:]]'
    ),
    ADD CONSTRAINT routes_match_mode_check CHECK (
        match_mode IN ('any_tcp','sni','destination_ip')
    ),
    ADD CONSTRAINT routes_match_shape_check CHECK (
        (match_mode = 'sni' AND NOT fallback AND hostname <> '') OR
        (match_mode = 'any_tcp' AND fallback AND hostname = '' AND listener_ip IN ('*','0.0.0.0','::')) OR
        (match_mode = 'destination_ip' AND fallback AND hostname = '' AND listener_ip NOT IN ('*','0.0.0.0','::'))
    );

-- Disabled routes are drafts and may overlap. The serialized enable/update
-- transaction validates the complete enabled route set before publishing a
-- revision, so database-wide uniqueness would incorrectly reject safe drafts.
DROP INDEX route_snis_listener_sni_unique;
DROP INDEX routes_listener_fallback_unique;

CREATE INDEX route_snis_listener_sni_idx
    ON route_snis(node_id,listener_ip,listener_port,sni);
CREATE INDEX routes_listener_fallback_idx
    ON routes(node_id,listener_ip,listener_port) WHERE fallback;
