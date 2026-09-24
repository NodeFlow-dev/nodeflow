-- Pre-000017 authentication has no activated_at state. Revoke every pending
-- candidate before dropping that column so rollback cannot promote an
-- unconfirmed bearer token to an active legacy credential.
UPDATE enrollment_tokens
SET revoked_at=COALESCE(revoked_at, clock_timestamp())
WHERE activated_at IS NULL;

DROP INDEX enrollment_tokens_certificate_idx;
DROP INDEX enrollment_tokens_active_idx;
CREATE INDEX enrollment_tokens_active_idx
    ON enrollment_tokens(token_hash) WHERE revoked_at IS NULL;
DROP INDEX enrollment_tokens_one_pending_renewal_idx;

ALTER TABLE enrollment_tokens
    DROP CONSTRAINT enrollment_tokens_node_renewal_unique,
    DROP CONSTRAINT enrollment_tokens_pending_is_renewal_check,
    DROP CONSTRAINT enrollment_tokens_renewal_shape_check,
    DROP CONSTRAINT enrollment_tokens_csr_sha256_check,
    DROP CONSTRAINT enrollment_tokens_certificate_sha256_check,
    DROP COLUMN confirm_by,
    DROP COLUMN certificate_der,
    DROP COLUMN csr_der,
    DROP COLUMN csr_sha256,
    DROP COLUMN predecessor_id,
    DROP COLUMN renewal_id,
    DROP COLUMN certificate_not_after,
    DROP COLUMN certificate_serial,
    DROP COLUMN certificate_sha256,
    DROP COLUMN activated_at;
