ALTER TABLE enrollment_tokens
    ADD COLUMN activated_at timestamptz,
    ADD COLUMN certificate_sha256 char(64),
    ADD COLUMN certificate_serial text,
    ADD COLUMN certificate_not_after timestamptz,
    ADD COLUMN renewal_id uuid,
    ADD COLUMN predecessor_id uuid REFERENCES enrollment_tokens(id),
    ADD COLUMN csr_sha256 char(64),
    ADD COLUMN csr_der bytea,
    ADD COLUMN certificate_der bytea,
    ADD COLUMN confirm_by timestamptz;

-- Existing credentials remain usable for a rolling migration. The first
-- verified Agent call binds a legacy NULL fingerprint to that exact leaf cert.
UPDATE enrollment_tokens SET activated_at=created_at;
ALTER TABLE enrollment_tokens ALTER COLUMN activated_at SET DEFAULT now();

ALTER TABLE enrollment_tokens
    ADD CONSTRAINT enrollment_tokens_certificate_sha256_check CHECK (
        certificate_sha256 IS NULL OR certificate_sha256 ~ '^[0-9a-f]{64}$'
    ),
    ADD CONSTRAINT enrollment_tokens_csr_sha256_check CHECK (
        csr_sha256 IS NULL OR csr_sha256 ~ '^[0-9a-f]{64}$'
    ),
    ADD CONSTRAINT enrollment_tokens_renewal_shape_check CHECK (
        renewal_id IS NULL OR (
            predecessor_id IS NOT NULL AND
            csr_sha256 IS NOT NULL AND
            csr_der IS NOT NULL AND octet_length(csr_der) BETWEEN 1 AND 8192 AND
            certificate_sha256 IS NOT NULL AND
            certificate_serial IS NOT NULL AND certificate_serial <> '' AND
            certificate_not_after IS NOT NULL AND
            certificate_der IS NOT NULL AND octet_length(certificate_der) BETWEEN 1 AND 16384 AND
            confirm_by IS NOT NULL AND confirm_by <= certificate_not_after AND
            expires_at <= certificate_not_after
        )
    ),
    ADD CONSTRAINT enrollment_tokens_pending_is_renewal_check CHECK (
        activated_at IS NOT NULL OR renewal_id IS NOT NULL
    );

ALTER TABLE enrollment_tokens
    ADD CONSTRAINT enrollment_tokens_node_renewal_unique UNIQUE (node_id, renewal_id);

CREATE UNIQUE INDEX enrollment_tokens_one_pending_renewal_idx
    ON enrollment_tokens(node_id)
    WHERE renewal_id IS NOT NULL AND activated_at IS NULL AND revoked_at IS NULL;

DROP INDEX enrollment_tokens_active_idx;
CREATE INDEX enrollment_tokens_active_idx
    ON enrollment_tokens(token_hash)
    WHERE activated_at IS NOT NULL AND revoked_at IS NULL;

CREATE INDEX enrollment_tokens_certificate_idx
    ON enrollment_tokens(node_id,certificate_sha256)
    WHERE activated_at IS NOT NULL AND revoked_at IS NULL;
