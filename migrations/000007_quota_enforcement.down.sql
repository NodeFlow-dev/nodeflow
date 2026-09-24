BEGIN;

ALTER TABLE routes
    DROP CONSTRAINT routes_quota_action_requires_limit,
    DROP CONSTRAINT routes_quota_action_check,
    DROP COLUMN quota_action;

COMMIT;
