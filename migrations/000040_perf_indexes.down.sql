CREATE INDEX IF NOT EXISTS traffic_quota_usage_current_idx
    ON traffic_quota_usage(node_id,proxy_name,period,window_start,window_end);

DROP INDEX IF EXISTS traffic_quota_usage_node_window_idx;
DROP INDEX IF EXISTS enrollment_tokens_node_active_idx;
DROP INDEX IF EXISTS audit_log_node_detail_idx;
DROP INDEX IF EXISTS audit_log_resource_id_idx;
