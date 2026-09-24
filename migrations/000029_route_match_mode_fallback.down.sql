-- Restore legacy match_mode values from listener_ip:
--   wildcard listener_ip => any_tcp
--   concrete listener_ip => destination_ip
-- then restore the original 000016 CHECK constraints.

UPDATE routes SET match_mode =
    CASE
        WHEN listener_ip IN ('*', '0.0.0.0', '::') THEN 'any_tcp'
        ELSE 'destination_ip'
    END
WHERE match_mode = 'fallback';

ALTER TABLE routes
    DROP CONSTRAINT IF EXISTS routes_match_mode_check,
    DROP CONSTRAINT IF EXISTS routes_match_shape_check;

ALTER TABLE routes
    ADD CONSTRAINT routes_match_mode_check CHECK (
        match_mode IN ('any_tcp','sni','destination_ip')
    ),
    ADD CONSTRAINT routes_match_shape_check CHECK (
        (match_mode = 'sni' AND NOT fallback AND hostname <> '') OR
        (match_mode = 'any_tcp' AND fallback AND hostname = '' AND listener_ip IN ('*','0.0.0.0','::')) OR
        (match_mode = 'destination_ip' AND fallback AND hostname = '' AND listener_ip NOT IN ('*','0.0.0.0','::'))
    );
