CREATE TABLE panel_settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    theme text NOT NULL DEFAULT 'rose' CHECK (theme IN ('dark', 'green', 'rose', 'cyan', 'amber', 'system')),
    accent text NOT NULL DEFAULT '#C27087' CHECK (accent ~ '^#[0-9A-Fa-f]{6}$'),
    session_timeout_minutes integer NOT NULL DEFAULT 30
        CHECK (session_timeout_minutes BETWEEN 5 AND 1440),
    max_sessions integer NOT NULL DEFAULT 5 CHECK (max_sessions BETWEEN 1 AND 100),
    audit_retention_days integer NOT NULL DEFAULT 90
        CHECK (audit_retention_days BETWEEN 7 AND 3650),
    next_audit_cleanup_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO panel_settings(singleton, theme, accent) VALUES (true, 'rose', '#C27087');
