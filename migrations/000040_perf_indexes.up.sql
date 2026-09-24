-- Performance indexes (audit 04-panel.md §4).
--
-- ListAudit filters by resource_id or details->>'node_id' ordered by id DESC.
-- Without these indexes the per-node audit view walks the primary key
-- backwards and filters every row until LIMIT is satisfied.
CREATE INDEX IF NOT EXISTS audit_log_resource_id_idx
    ON audit_log(resource_id, id DESC)
    WHERE resource_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS audit_log_node_detail_idx
    ON audit_log((details->>'node_id'), id DESC)
    WHERE details->>'node_id' IS NOT NULL;

-- Active credential lookup in GetNodeOperationalDetail sorts by activation
-- time; this partial index returns the newest active token directly.
CREATE INDEX IF NOT EXISTS enrollment_tokens_node_active_idx
    ON enrollment_tokens(node_id, activated_at DESC, created_at DESC)
    WHERE revoked_at IS NULL AND activated_at IS NOT NULL;

-- currentQuotaUsage filters by node and window bounds across all proxies and
-- periods. The old (node_id,proxy_name,period,window_start,window_end) index
-- put proxy_name before the window columns; the primary key already covers
-- the per-proxy heartbeat lookups, so the old index is replaced.
CREATE INDEX IF NOT EXISTS traffic_quota_usage_node_window_idx
    ON traffic_quota_usage(node_id, window_end, window_start);

DROP INDEX IF EXISTS traffic_quota_usage_current_idx;
