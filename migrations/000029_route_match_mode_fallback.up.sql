-- F6: normalize legacy match_mode any_tcp/destination_ip to 'fallback'.
-- Both modes produce identical HAProxy config (default_backend); the
-- distinction (wildcard vs concrete listener_ip) is already captured by
-- listener_ip. The read path also normalizes legacy values on scan.
--
-- The 000016 CHECK constraints must accept 'fallback' before rows are
-- rewritten. Legacy values stay allowed so the down migration and older
-- Panel builds keep working.

ALTER TABLE routes
    DROP CONSTRAINT IF EXISTS routes_match_mode_check,
    DROP CONSTRAINT IF EXISTS routes_match_shape_check;

ALTER TABLE routes
    ADD CONSTRAINT routes_match_mode_check CHECK (
        match_mode IN ('any_tcp','sni','destination_ip','fallback')
    ),
    ADD CONSTRAINT routes_match_shape_check CHECK (
        (match_mode = 'sni' AND NOT fallback AND hostname <> '') OR
        (match_mode = 'fallback' AND fallback AND hostname = '') OR
        (match_mode = 'any_tcp' AND fallback AND hostname = '' AND listener_ip IN ('*','0.0.0.0','::')) OR
        (match_mode = 'destination_ip' AND fallback AND hostname = '' AND listener_ip NOT IN ('*','0.0.0.0','::'))
    );

UPDATE routes SET match_mode = 'fallback'
WHERE match_mode IN ('any_tcp', 'destination_ip');
