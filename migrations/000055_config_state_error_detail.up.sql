-- Operator-facing excerpt of the last failed apply (for example the
-- [ALERT] lines of haproxy -c) reported by newer Agents. NULL/empty for
-- older Agents and cleared whenever last_error is cleared.
ALTER TABLE node_config_state
    ADD COLUMN IF NOT EXISTS last_error_detail text NULL;
