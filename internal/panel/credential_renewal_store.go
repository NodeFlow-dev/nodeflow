package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	credentialRenewalDueWindow       = 45 * 24 * time.Hour
	credentialRenewalMinimumInterval = time.Minute
)

type activeAgentCredential struct {
	ID                  string
	NodeID              string
	ExpiresAt           time.Time
	CertificateSHA256   *string
	CertificateSerial   *string
	CertificateNotAfter *time.Time
	LegacyUnbound       bool
}

const credentialRenewalColumns = `
	id::text,node_id::text,renewal_id::text,predecessor_id::text,
	csr_sha256::text,csr_der,token_hash::text,token_prefix,
	certificate_sha256::text,certificate_serial,certificate_der,certificate_not_after,
	confirm_by,activated_at,revoked_at,created_at`

func scanCredentialRenewal(row pgx.Row) (CredentialRenewalRecord, error) {
	var record CredentialRenewalRecord
	err := row.Scan(
		&record.ID, &record.NodeID, &record.RenewalID, &record.PredecessorID,
		&record.CSRHash, &record.CSRDER, &record.NextTokenHash, &record.NextTokenPrefix,
		&record.CertificateSHA256, &record.CertificateSerial, &record.CertificateDER, &record.CertificateNotAfter,
		&record.ConfirmBy, &record.ActivatedAt, &record.RevokedAt, &record.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CredentialRenewalRecord{}, ErrNotFound
	}
	return record, err
}

func (s *PGStore) AuthorizeCredentialRenewal(ctx context.Context, token string, identity AgentCredentialIdentity, request CredentialRenewalRequest) (*CredentialRenewalRecord, error) {
	if token == "" || !validAgentCredentialIdentity(identity) {
		return nil, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	current, err := authenticateActiveAgentCredential(ctx, tx, token, identity, false)
	if err != nil {
		return nil, err
	}
	if err = lockCredentialNode(ctx, tx, current.NodeID); err != nil {
		return nil, err
	}
	now, err := credentialClock(ctx, tx)
	if err != nil {
		return nil, err
	}
	existing, err := findCredentialRenewal(ctx, tx, current.NodeID, request.RenewalID)
	if err == nil {
		if !sameCredentialRenewalRequest(existing, current.ID, request) {
			return nil, ErrCredentialRenewalIdempotency
		}
		if existing.RevokedAt != nil || (existing.ActivatedAt == nil && !existing.ConfirmBy.After(now)) {
			return nil, ErrCredentialRenewalExpired
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return &existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err = validateNewCredentialRenewal(ctx, tx, current, identity, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *PGStore) CreateCredentialRenewal(ctx context.Context, token string, identity AgentCredentialIdentity, candidate CredentialRenewalCandidate) (CredentialRenewalRecord, bool, error) {
	if token == "" || !validAgentCredentialIdentity(identity) {
		return CredentialRenewalRecord{}, false, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CredentialRenewalRecord{}, false, err
	}
	defer tx.Rollback(ctx)
	current, err := authenticateActiveAgentCredential(ctx, tx, token, identity, true)
	if err != nil {
		return CredentialRenewalRecord{}, false, err
	}
	if err = lockCredentialNode(ctx, tx, current.NodeID); err != nil {
		return CredentialRenewalRecord{}, false, err
	}
	now, err := credentialClock(ctx, tx)
	if err != nil {
		return CredentialRenewalRecord{}, false, err
	}
	existing, err := findCredentialRenewal(ctx, tx, current.NodeID, candidate.RenewalID)
	if err == nil {
		if !sameCredentialRenewalRequest(existing, current.ID, candidate.CredentialRenewalRequest) {
			return CredentialRenewalRecord{}, false, ErrCredentialRenewalIdempotency
		}
		if existing.RevokedAt != nil || (existing.ActivatedAt == nil && !existing.ConfirmBy.After(now)) {
			return CredentialRenewalRecord{}, false, ErrCredentialRenewalExpired
		}
		if err = tx.Commit(ctx); err != nil {
			return CredentialRenewalRecord{}, false, err
		}
		return existing, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return CredentialRenewalRecord{}, false, err
	}
	if err = validateCredentialRenewalCandidate(candidate, now); err != nil {
		return CredentialRenewalRecord{}, false, err
	}
	if err = validateNewCredentialRenewal(ctx, tx, current, identity, now); err != nil {
		return CredentialRenewalRecord{}, false, err
	}
	_, err = tx.Exec(ctx, `
		UPDATE enrollment_tokens SET revoked_at=$2
		WHERE node_id=$1 AND renewal_id IS NOT NULL AND activated_at IS NULL
		  AND revoked_at IS NULL AND confirm_by<=$2`, current.NodeID, now)
	if err != nil {
		return CredentialRenewalRecord{}, false, err
	}
	record, err := scanCredentialRenewal(tx.QueryRow(ctx, `
		INSERT INTO enrollment_tokens(
			node_id,token_hash,token_prefix,expires_at,activated_at,
			certificate_sha256,certificate_serial,certificate_not_after,
			renewal_id,predecessor_id,csr_sha256,csr_der,certificate_der,confirm_by
		)
		VALUES($1,$2,$3,$4,NULL,$5,$6,$4,$7,$8,$9,$10,$11,$12)
		RETURNING `+credentialRenewalColumns,
		current.NodeID, candidate.NextTokenHash, candidate.NextTokenPrefix, candidate.CertificateNotAfter,
		candidate.CertificateSHA256, candidate.CertificateSerial, candidate.RenewalID, current.ID,
		candidate.CSRHash, candidate.CSRDER, candidate.CertificateDER, candidate.ConfirmBy,
	))
	if err != nil {
		return CredentialRenewalRecord{}, false, err
	}
	if err = appendCredentialAudit(ctx, tx, "credential.renewal.requested", record, now); err != nil {
		return CredentialRenewalRecord{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return CredentialRenewalRecord{}, false, err
	}
	return record, true, nil
}

func (s *PGStore) ConfirmCredentialRenewal(ctx context.Context, token string, identity AgentCredentialIdentity, renewalID string) (CredentialRenewalRecord, error) {
	if token == "" || !validAgentCredentialIdentity(identity) {
		return CredentialRenewalRecord{}, ErrNotFound
	}
	sum := sha256.Sum256([]byte(token))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CredentialRenewalRecord{}, err
	}
	defer tx.Rollback(ctx)
	record, err := scanCredentialRenewal(tx.QueryRow(ctx, `
		SELECT `+credentialRenewalColumns+`
		FROM enrollment_tokens
		WHERE node_id=$1 AND renewal_id=$2 AND token_hash=$3
		  AND certificate_sha256=$4 AND certificate_serial=$5
		FOR UPDATE`, identity.NodeID, renewalID, hex.EncodeToString(sum[:]), identity.CertificateSHA256, identity.CertificateSerial))
	if err != nil {
		return CredentialRenewalRecord{}, err
	}
	if len(token) < len(record.NextTokenPrefix) || token[:len(record.NextTokenPrefix)] != record.NextTokenPrefix {
		return CredentialRenewalRecord{}, ErrNotFound
	}
	// Lock every predecessor before the node row. Normal Agent calls lock their
	// credential before that same node row, so confirmation cannot deadlock while
	// atomically revoking a credential currently serving a heartbeat.
	rows, err := tx.Query(ctx, `SELECT id::text FROM enrollment_tokens WHERE node_id=$1 ORDER BY id FOR UPDATE`, record.NodeID)
	if err != nil {
		return CredentialRenewalRecord{}, err
	}
	for rows.Next() {
		var ignored string
		if err = rows.Scan(&ignored); err != nil {
			rows.Close()
			return CredentialRenewalRecord{}, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return CredentialRenewalRecord{}, err
	}
	if err = lockCredentialNode(ctx, tx, record.NodeID); err != nil {
		return CredentialRenewalRecord{}, err
	}
	now, err := credentialClock(ctx, tx)
	if err != nil {
		return CredentialRenewalRecord{}, err
	}
	if record.RevokedAt != nil {
		return CredentialRenewalRecord{}, ErrNotFound
	}
	if record.ActivatedAt != nil {
		if _, err = tx.Exec(ctx, `UPDATE enrollment_tokens SET last_used_at=$2 WHERE id=$1`, record.ID, now); err != nil {
			return CredentialRenewalRecord{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return CredentialRenewalRecord{}, err
		}
		return record, nil
	}
	if !record.ConfirmBy.After(now) || !record.CertificateNotAfter.After(now) {
		if _, err = tx.Exec(ctx, `UPDATE enrollment_tokens SET revoked_at=$2 WHERE id=$1 AND revoked_at IS NULL`, record.ID, now); err != nil {
			return CredentialRenewalRecord{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return CredentialRenewalRecord{}, err
		}
		return CredentialRenewalRecord{}, ErrCredentialRenewalExpired
	}
	if _, err = tx.Exec(ctx, `
		UPDATE enrollment_tokens SET activated_at=$2,last_used_at=$2
		WHERE id=$1 AND activated_at IS NULL AND revoked_at IS NULL`, record.ID, now); err != nil {
		return CredentialRenewalRecord{}, err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE enrollment_tokens SET revoked_at=$3
		WHERE node_id=$1 AND id<>$2 AND revoked_at IS NULL`, record.NodeID, record.ID, now); err != nil {
		return CredentialRenewalRecord{}, err
	}
	activatedAt := now
	record.ActivatedAt = &activatedAt
	if err = appendCredentialAudit(ctx, tx, "credential.renewal.confirmed", record, now); err != nil {
		return CredentialRenewalRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return CredentialRenewalRecord{}, err
	}
	return record, nil
}

func authenticateActiveAgentCredential(ctx context.Context, tx pgx.Tx, token string, identity AgentCredentialIdentity, bindLegacy bool) (activeAgentCredential, error) {
	if token == "" {
		return activeAgentCredential{}, ErrNotFound
	}
	sum := sha256.Sum256([]byte(token))
	var current activeAgentCredential
	err := tx.QueryRow(ctx, `
		SELECT id::text,node_id::text,expires_at,certificate_sha256::text,
		       certificate_serial,certificate_not_after
		FROM enrollment_tokens
		WHERE token_hash=$1 AND activated_at IS NOT NULL
		  AND revoked_at IS NULL AND expires_at>clock_timestamp()
		  AND ($2='' OR node_id::text=$2)
		  AND (certificate_sha256 IS NULL OR ($3<>'' AND certificate_sha256=$3))
		FOR UPDATE`, hex.EncodeToString(sum[:]), identity.NodeID, identity.CertificateSHA256).
		Scan(&current.ID, &current.NodeID, &current.ExpiresAt, &current.CertificateSHA256, &current.CertificateSerial, &current.CertificateNotAfter)
	if errors.Is(err, pgx.ErrNoRows) {
		return activeAgentCredential{}, ErrNotFound
	}
	if err != nil {
		return activeAgentCredential{}, err
	}
	current.LegacyUnbound = current.CertificateSHA256 == nil
	if bindLegacy {
		_, err = tx.Exec(ctx, `
			UPDATE enrollment_tokens SET
				last_used_at=clock_timestamp(),
				certificate_sha256=COALESCE(certificate_sha256,NULLIF($2,'')),
				certificate_serial=COALESCE(certificate_serial,NULLIF($3,'')),
				certificate_not_after=COALESCE(certificate_not_after,$4)
			WHERE id=$1`, current.ID, identity.CertificateSHA256, identity.CertificateSerial, nullableCertificateNotAfter(identity))
	}
	return current, err
}

func nullableCertificateNotAfter(identity AgentCredentialIdentity) any {
	if identity.CertificateNotAfter.IsZero() {
		return nil
	}
	return identity.CertificateNotAfter
}

func validAgentCredentialIdentity(identity AgentCredentialIdentity) bool {
	return validID(identity.NodeID) && validSHA256Hex(identity.CertificateSHA256) &&
		identity.CertificateSerial != "" && !identity.CertificateNotAfter.IsZero()
}

func lockCredentialNode(ctx context.Context, tx pgx.Tx, nodeID string) error {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT true FROM nodes WHERE id=$1 FOR UPDATE`, nodeID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func credentialClock(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now.UTC(), err
}

func findCredentialRenewal(ctx context.Context, tx pgx.Tx, nodeID, renewalID string) (CredentialRenewalRecord, error) {
	return scanCredentialRenewal(tx.QueryRow(ctx, `
		SELECT `+credentialRenewalColumns+`
		FROM enrollment_tokens WHERE node_id=$1 AND renewal_id=$2`, nodeID, renewalID))
}

func sameCredentialRenewalRequest(record CredentialRenewalRecord, predecessorID string, request CredentialRenewalRequest) bool {
	return record.PredecessorID == predecessorID && record.CSRHash == request.CSRHash &&
		record.NextTokenHash == request.NextTokenHash && record.NextTokenPrefix == request.NextTokenPrefix
}

func validateNewCredentialRenewal(ctx context.Context, tx pgx.Tx, current activeAgentCredential, identity AgentCredentialIdentity, now time.Time) error {
	var pending bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM enrollment_tokens
			WHERE node_id=$1 AND renewal_id IS NOT NULL AND activated_at IS NULL
			  AND revoked_at IS NULL AND confirm_by>$2
		)`, current.NodeID, now).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return ErrCredentialRenewalInProgress
	}
	if !credentialRenewalDue(current, identity, now) {
		return ErrCredentialRenewalNotDue
	}
	var recent bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM enrollment_tokens
			WHERE node_id=$1 AND renewal_id IS NOT NULL AND created_at>$2
		)`, current.NodeID, now.Add(-credentialRenewalMinimumInterval)).Scan(&recent); err != nil {
		return err
	}
	if recent {
		return ErrCredentialRenewalRateLimited
	}
	return nil
}

func credentialRenewalDue(current activeAgentCredential, identity AgentCredentialIdentity, now time.Time) bool {
	if current.LegacyUnbound {
		return true
	}
	deadline := current.ExpiresAt
	certificateNotAfter := identity.CertificateNotAfter
	if current.CertificateNotAfter != nil {
		certificateNotAfter = *current.CertificateNotAfter
	}
	if !certificateNotAfter.IsZero() && certificateNotAfter.Before(deadline) {
		deadline = certificateNotAfter
	}
	return !deadline.After(now.Add(credentialRenewalDueWindow))
}

func validateCredentialRenewalCandidate(candidate CredentialRenewalCandidate, now time.Time) error {
	if !validID(candidate.RenewalID) || !validSHA256Hex(candidate.CSRHash) ||
		len(candidate.CSRDER) == 0 || len(candidate.CSRDER) > 8192 ||
		!validSHA256Hex(candidate.NextTokenHash) || !validTokenPrefix(candidate.NextTokenPrefix) ||
		!validSHA256Hex(candidate.CertificateSHA256) || candidate.CertificateSerial == "" ||
		len(candidate.CertificateDER) == 0 || len(candidate.CertificateDER) > 16384 ||
		!candidate.CertificateNotAfter.After(now) || !candidate.ConfirmBy.After(now) ||
		candidate.ConfirmBy.After(candidate.CertificateNotAfter) {
		return ErrCredentialRenewalIdempotency
	}
	return nil
}

func appendCredentialAudit(ctx context.Context, tx pgx.Tx, action string, record CredentialRenewalRecord, at time.Time) error {
	details, err := json.Marshal(map[string]any{
		"node_id": record.NodeID, "renewal_id": record.RenewalID,
		"certificate_sha256": record.CertificateSHA256, "certificate_not_after": record.CertificateNotAfter,
		"at": at,
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_log(actor_type,actor_id,action,resource_type,resource_id,details)
		VALUES('node_credential',$1,$2,'node_credential',$3,$4)`,
		record.NodeID, action, record.ID, details)
	return err
}

func validSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 || value != lowerASCII(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func lowerASCII(value string) string {
	raw := []byte(value)
	for index, character := range raw {
		if character >= 'A' && character <= 'Z' {
			raw[index] = character + ('a' - 'A')
		}
	}
	return string(raw)
}

func validTokenPrefix(value string) bool {
	if len(value) != 12 || value[:4] != "nfe_" {
		return false
	}
	for _, character := range value[4:] {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_') {
			return false
		}
	}
	return true
}
