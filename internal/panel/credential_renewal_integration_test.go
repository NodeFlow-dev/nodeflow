package panel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPGStoreCredentialRenewalIntegration(t *testing.T) {
	databaseURL := os.Getenv("NODEFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NODEFLOW_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, pool.Ping(ctx))
	var migrated bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version='000017')`).Scan(&migrated))
	require.True(t, migrated, "database must include migration 000017")
	store := NewPGStore(pool)

	t.Run("pending is isolated and confirm atomically switches the pair", func(t *testing.T) {
		fixture := newCredentialRenewalFixture(t, ctx, pool, store)
		record, created, err := store.CreateCredentialRenewal(ctx, fixture.currentToken, fixture.currentIdentity, fixture.candidate)
		require.NoError(t, err)
		require.True(t, created)
		assert.Nil(t, record.ActivatedAt)

		repeated, created, err := store.CreateCredentialRenewal(ctx, fixture.currentToken, fixture.currentIdentity, fixture.candidate)
		require.NoError(t, err)
		assert.False(t, created)
		assert.Equal(t, record.ID, repeated.ID)

		conflict := fixture.candidate
		conflict.NextTokenHash = hex.EncodeToString(sha256Bytes([]byte("different-next-token")))
		_, _, err = store.CreateCredentialRenewal(ctx, fixture.currentToken, fixture.currentIdentity, conflict)
		assert.ErrorIs(t, err, ErrCredentialRenewalIdempotency)

		_, err = store.IngestHeartbeat(ctx, fixture.candidateToken, Heartbeat{
			Version: "integration", Status: "online", Metrics: map[string]any{}, Credential: fixture.candidateIdentity,
		})
		assert.ErrorIs(t, err, ErrNotFound, "pending candidate must not authenticate ordinary Agent APIs")

		wrongFingerprint := fixture.candidateIdentity
		wrongFingerprint.CertificateSHA256 = hex.EncodeToString(sha256Bytes([]byte("wrong-leaf")))
		_, err = store.ConfirmCredentialRenewal(ctx, fixture.candidateToken, wrongFingerprint, fixture.candidate.RenewalID)
		assert.ErrorIs(t, err, ErrNotFound)
		wrongCurrentFingerprint := fixture.currentIdentity
		wrongCurrentFingerprint.CertificateSHA256 = hex.EncodeToString(sha256Bytes([]byte("wrong-current-leaf")))
		_, err = store.AuthorizeCredentialRenewal(ctx, fixture.currentToken, wrongCurrentFingerprint, fixture.candidate.CredentialRenewalRequest)
		assert.ErrorIs(t, err, ErrNotFound)

		wrongNode := fixture.currentIdentity
		wrongNode.NodeID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		_, err = store.AuthorizeCredentialRenewal(ctx, fixture.currentToken, wrongNode, fixture.candidate.CredentialRenewalRequest)
		assert.ErrorIs(t, err, ErrNotFound)

		confirmed, err := store.ConfirmCredentialRenewal(ctx, fixture.candidateToken, fixture.candidateIdentity, fixture.candidate.RenewalID)
		require.NoError(t, err)
		require.NotNil(t, confirmed.ActivatedAt)
		confirmedAgain, err := store.ConfirmCredentialRenewal(ctx, fixture.candidateToken, fixture.candidateIdentity, fixture.candidate.RenewalID)
		require.NoError(t, err)
		assert.Equal(t, confirmed.ID, confirmedAgain.ID)

		var currentRevoked, candidateActive bool
		require.NoError(t, pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM enrollment_tokens WHERE token_hash=$1`, tokenHash(fixture.currentToken)).Scan(&currentRevoked))
		require.NoError(t, pool.QueryRow(ctx, `SELECT activated_at IS NOT NULL AND revoked_at IS NULL FROM enrollment_tokens WHERE token_hash=$1`, tokenHash(fixture.candidateToken)).Scan(&candidateActive))
		assert.True(t, currentRevoked)
		assert.True(t, candidateActive)

		_, err = store.IngestHeartbeat(ctx, fixture.currentToken, Heartbeat{
			Version: "integration", Status: "online", Metrics: map[string]any{}, Credential: fixture.currentIdentity,
		})
		assert.ErrorIs(t, err, ErrNotFound)
		_, err = store.IngestHeartbeat(ctx, fixture.candidateToken, Heartbeat{
			Version: "integration", Status: "online", Metrics: map[string]any{}, Credential: fixture.candidateIdentity,
		})
		require.NoError(t, err)

		next := fixture.candidate.CredentialRenewalRequest
		next.RenewalID = randomUUIDv4(t)
		_, err = store.AuthorizeCredentialRenewal(ctx, fixture.candidateToken, fixture.candidateIdentity, next)
		assert.ErrorIs(t, err, ErrCredentialRenewalNotDue)
	})

	t.Run("confirmation rollback keeps predecessor active", func(t *testing.T) {
		fixture := newCredentialRenewalFixture(t, ctx, pool, store)
		_, _, err := store.CreateCredentialRenewal(ctx, fixture.currentToken, fixture.currentIdentity, fixture.candidate)
		require.NoError(t, err)

		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		functionName := pgx.Identifier{"fail_credential_confirm_" + suffix}.Sanitize()
		triggerName := pgx.Identifier{"fail_credential_confirm_trigger_" + suffix}.Sanitize()
		_, err = pool.Exec(ctx, fmt.Sprintf(`
			CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				IF NEW.action='credential.renewal.confirmed' AND NEW.actor_id='%s' THEN
					RAISE EXCEPTION 'injected credential confirmation failure';
				END IF;
				RETURN NEW;
			END $$`, functionName, fixture.nodeID))
		require.NoError(t, err)
		_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION %s()`, triggerName, functionName))
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON audit_log`, triggerName))
			_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
		})

		_, err = store.ConfirmCredentialRenewal(ctx, fixture.candidateToken, fixture.candidateIdentity, fixture.candidate.RenewalID)
		require.Error(t, err)
		var currentActive, candidatePending bool
		require.NoError(t, pool.QueryRow(ctx, `SELECT activated_at IS NOT NULL AND revoked_at IS NULL FROM enrollment_tokens WHERE token_hash=$1`, tokenHash(fixture.currentToken)).Scan(&currentActive))
		require.NoError(t, pool.QueryRow(ctx, `SELECT activated_at IS NULL AND revoked_at IS NULL FROM enrollment_tokens WHERE token_hash=$1`, tokenHash(fixture.candidateToken)).Scan(&candidatePending))
		assert.True(t, currentActive, "predecessor revoke must roll back")
		assert.True(t, candidatePending, "candidate activation must roll back")
	})

	t.Run("different concurrent renewal ids produce one candidate", func(t *testing.T) {
		fixture := newCredentialRenewalFixture(t, ctx, pool, store)
		other := fixture.candidate
		other.RenewalID = randomUUIDv4(t)
		other.NextTokenHash = hex.EncodeToString(sha256Bytes([]byte("nfe_other_candidate_token" + other.RenewalID)))
		other.NextTokenPrefix = "nfe_other123"
		var wait sync.WaitGroup
		errorsSeen := make(chan error, 2)
		for _, candidate := range []CredentialRenewalCandidate{fixture.candidate, other} {
			candidate := candidate
			wait.Add(1)
			go func() {
				defer wait.Done()
				_, _, createErr := store.CreateCredentialRenewal(ctx, fixture.currentToken, fixture.currentIdentity, candidate)
				errorsSeen <- createErr
			}()
		}
		wait.Wait()
		close(errorsSeen)
		successes, rejected := 0, 0
		for createErr := range errorsSeen {
			switch {
			case createErr == nil:
				successes++
			case errors.Is(createErr, ErrCredentialRenewalInProgress), errors.Is(createErr, ErrCredentialRenewalRateLimited):
				rejected++
			default:
				t.Fatalf("unexpected concurrent renewal result: %v", createErr)
			}
		}
		assert.Equal(t, 1, successes)
		assert.Equal(t, 1, rejected)
	})
}

type credentialRenewalFixture struct {
	nodeID            string
	currentToken      string
	currentIdentity   AgentCredentialIdentity
	candidateToken    string
	candidateIdentity AgentCredentialIdentity
	candidate         CredentialRenewalCandidate
}

func newCredentialRenewalFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, store *PGStore) credentialRenewalFixture {
	t.Helper()
	addressBytes := make([]byte, 6)
	_, err := rand.Read(addressBytes)
	require.NoError(t, err)
	address := fmt.Sprintf("2001:db8:%x:%x::%x", addressBytes[:2], addressBytes[2:4], addressBytes[4:])
	var nodeID string
	err = pool.QueryRow(ctx, `INSERT INTO nodes(name,address) VALUES($1,$2) RETURNING id::text`, "credential-renewal-"+hex.EncodeToString(addressBytes), address).Scan(&nodeID)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM nodes WHERE id=$1`, nodeID) })

	currentToken := "nfe_current_" + randomUUIDv4(t)
	_, err = store.CreateEnrollmentToken(ctx, nodeID, tokenHash(currentToken), currentToken[:12], time.Now().Add(90*24*time.Hour))
	require.NoError(t, err)
	currentLeaf := testAgentLeaf(nodeID, "current-"+nodeID, time.Now().UnixNano())
	currentFingerprint := sha256.Sum256(currentLeaf.Raw)
	currentIdentity := AgentCredentialIdentity{
		NodeID: nodeID, CertificateSHA256: hex.EncodeToString(currentFingerprint[:]),
		CertificateSerial: currentLeaf.SerialNumber.String(), CertificateNotAfter: currentLeaf.NotAfter,
	}

	csrPEM, _ := testEd25519CSR(t, "ignored", []string{"ignored.example"})
	csr, csrDER, err := parseCredentialCSR(csrPEM)
	require.NoError(t, err)
	certificatePEM, err := testCredentialIssuer(t).IssueCSR(nodeID, csr)
	require.NoError(t, err)
	certificateBlock, rest := pem.Decode(certificatePEM)
	require.NotNil(t, certificateBlock)
	assert.Empty(t, rest)
	certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
	require.NoError(t, err)
	certificateFingerprint := sha256.Sum256(certificate.Raw)
	csrHash := sha256.Sum256(csrDER)
	candidateToken := "nfe_candidate_" + randomUUIDv4(t)
	candidateIdentity := AgentCredentialIdentity{
		NodeID: nodeID, CertificateSHA256: hex.EncodeToString(certificateFingerprint[:]),
		CertificateSerial: certificate.SerialNumber.String(), CertificateNotAfter: certificate.NotAfter,
	}
	candidate := CredentialRenewalCandidate{
		CredentialRenewalRequest: CredentialRenewalRequest{
			RenewalID: randomUUIDv4(t), CSRHash: hex.EncodeToString(csrHash[:]), CSRDER: csrDER,
			NextTokenHash: tokenHash(candidateToken), NextTokenPrefix: candidateToken[:12],
		},
		CertificateSHA256: candidateIdentity.CertificateSHA256, CertificateSerial: candidateIdentity.CertificateSerial,
		CertificateDER: certificate.Raw, CertificateNotAfter: certificate.NotAfter,
		ConfirmBy: time.Now().UTC().Add(24 * time.Hour),
	}
	return credentialRenewalFixture{
		nodeID: nodeID, currentToken: currentToken, currentIdentity: currentIdentity,
		candidateToken: candidateToken, candidateIdentity: candidateIdentity, candidate: candidate,
	}
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func sha256Bytes(value []byte) []byte {
	sum := sha256.Sum256(value)
	return sum[:]
}

func randomUUIDv4(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 16)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}
