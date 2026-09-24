-- sticky_mode selects how a pool backend distributes clients:
--   ''             : legacy rows; derived at render time from sticky_enabled
--                    and DNS pools (byte-identical to earlier renders).
--   'source'       : consistent hash of the client address (balance source).
--   'source_table' : remembered client (stick-table keyed by client /32 or
--                    /64), first choice by leastconn; sticky_ttl is the expiry.
--   'leastconn'    : no stickiness, least connections.
--   'roundrobin'   : no stickiness, HAProxy default.
--   'sni'          : consistent hash of the TLS SNI (SNI routes only).
ALTER TABLE routes
    ADD COLUMN sticky_mode text NOT NULL DEFAULT ''
        CONSTRAINT routes_sticky_mode_check
        CHECK (sticky_mode IN ('', 'source', 'source_table', 'leastconn', 'roundrobin', 'sni')),
    ADD COLUMN sticky_ttl text NOT NULL DEFAULT '';

UPDATE routes SET sticky_mode = 'source' WHERE sticky_enabled;
