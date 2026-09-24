ALTER TABLE node_haproxy_control
    DROP CONSTRAINT IF EXISTS node_haproxy_control_restart_report_generation_check,
    DROP COLUMN IF EXISTS restart_report_generation,
    DROP COLUMN IF EXISTS restart_generation;
