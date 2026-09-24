-- shaper_mode selects who enforces client_upload_mbps/client_download_mbps:
--   'haproxy' (default): HAProxy bwlim filters (disables splice on the frontend).
--   'kernel': Node Agent nftables per-client token buckets; HAProxy keeps splice.
ALTER TABLE routes
    ADD COLUMN shaper_mode text NOT NULL DEFAULT 'haproxy'
        CONSTRAINT routes_shaper_mode_check CHECK (shaper_mode IN ('haproxy', 'kernel'));
