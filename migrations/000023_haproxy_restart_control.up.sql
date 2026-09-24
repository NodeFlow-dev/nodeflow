ALTER TABLE node_haproxy_control
    ADD COLUMN IF NOT EXISTS restart_generation bigint NOT NULL DEFAULT 0 CHECK (restart_generation >= 0),
    ADD COLUMN IF NOT EXISTS restart_report_generation bigint NOT NULL DEFAULT 0 CHECK (restart_report_generation >= 0);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'node_haproxy_control'::regclass
          AND conname = 'node_haproxy_control_restart_report_generation_check'
    ) THEN
        ALTER TABLE node_haproxy_control
            ADD CONSTRAINT node_haproxy_control_restart_report_generation_check
            CHECK (restart_report_generation <= restart_generation);
    END IF;
END $$;
