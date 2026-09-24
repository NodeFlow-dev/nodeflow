ALTER TABLE routes
    ADD COLUMN quota_action varchar(16) NOT NULL DEFAULT 'observe';

ALTER TABLE routes
    ADD CONSTRAINT routes_quota_action_check
        CHECK (quota_action IN ('observe','block_new')),
    ADD CONSTRAINT routes_quota_action_requires_limit
        CHECK (quota_action = 'observe' OR quota_bytes IS NOT NULL);
