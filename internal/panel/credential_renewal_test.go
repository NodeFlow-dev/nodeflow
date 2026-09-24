package panel

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nodeflow/nodeflow/internal/bootstrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testRenewalID = "33333333-3333-4333-8333-333333333333"

func TestCredentialRenewalIssuesNodeOwnedCertificateAndIsIdempotent(t *testing.T) {
	store := &fakeStore{}
	issuer := testCredentialIssuer(t)
	handler := NewHandlerWithServicesAndIssuer(store, Config{AdminToken: testAdminToken, RequireAgentMTLS: true}, nil, nil, issuer)
	csrPEM, requestedPublicKey := testEd25519CSR(t, "attacker", []string{"attacker.example"})
	nextTokenHash := sha256.Sum256([]byte("next-token"))
	body, err := json.Marshal(credentialRenewalInput{
		RenewalID: testRenewalID, CSRPEM: csrPEM,
		NextTokenHash: hex.EncodeToString(nextTokenHash[:]), NextTokenPrefix: "nfe_12345678",
	})
	require.NoError(t, err)
	leaf := testAgentLeaf(testNodeID, "current-leaf", 7)

	response := credentialRequest(t, handler, http.MethodPost, "/agent/v1/credential-renewals", string(body), "current-token", leaf)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var output credentialRenewalOutput
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &output))
	assert.Equal(t, testRenewalID, output.RenewalID)
	assert.Equal(t, store.renewalCandidate.CertificateSHA256, output.CertificateSHA256)
	assert.Equal(t, store.renewalCandidate.CertificateSerial, output.Serial)
	assert.False(t, output.NotBefore.IsZero())
	assert.True(t, output.NotAfter.After(time.Now()))
	assert.True(t, output.ConfirmBy.After(time.Now()))
	assert.NotContains(t, response.Body.String(), "next-token")
	assert.NotContains(t, response.Body.String(), "next_token_sha256")

	block, rest := pem.Decode([]byte(output.CertificatePEM))
	require.NotNil(t, block)
	assert.Empty(t, strings.TrimSpace(string(rest)))
	certificate, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, testNodeID, certificate.Subject.CommonName)
	assert.Empty(t, certificate.DNSNames)
	assert.Equal(t, requestedPublicKey, certificate.PublicKey)
	currentFingerprint := sha256.Sum256(leaf.Raw)
	assert.Equal(t, hex.EncodeToString(currentFingerprint[:]), store.renewalIdentity.CertificateSHA256)

	store.renewalExisting = true
	response = credentialRequest(t, handler, http.MethodPost, "/agent/v1/credential-renewals", string(body), "current-token", leaf)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var repeated credentialRenewalOutput
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &repeated))
	assert.Equal(t, output, repeated)
}

func TestCredentialRenewalRequiresVerifiedMTLSAndValidEd25519CSR(t *testing.T) {
	store := &fakeStore{}
	handler := NewHandlerWithServicesAndIssuer(store, Config{AdminToken: testAdminToken, RequireAgentMTLS: true}, nil, nil, testCredentialIssuer(t))
	validCSR, _ := testEd25519CSR(t, "ignored", nil)
	nextHash := sha256.Sum256([]byte("next-token"))
	input := credentialRenewalInput{
		RenewalID: testRenewalID, CSRPEM: validCSR,
		NextTokenHash: hex.EncodeToString(nextHash[:]), NextTokenPrefix: "nfe_12345678",
	}
	body, err := json.Marshal(input)
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodPost, "/agent/v1/credential-renewals", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer current-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assert.Equal(t, http.StatusUnauthorized, response.Code)

	wrongLeaf := testAgentLeaf("44444444-4444-4444-8444-444444444444", "wrong-node", 8)
	store.renewalErr = ErrNotFound
	response = credentialRequest(t, handler, http.MethodPost, "/agent/v1/credential-renewals", string(body), "current-token", wrongLeaf)
	require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
	assert.Equal(t, "44444444-4444-4444-8444-444444444444", store.renewalIdentity.NodeID)
	store.renewalErr = nil

	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	rawCSR, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, ecdsaKey)
	require.NoError(t, err)
	input.CSRPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: rawCSR}))
	body, err = json.Marshal(input)
	require.NoError(t, err)
	response = credentialRequest(t, handler, http.MethodPost, "/agent/v1/credential-renewals", string(body), "current-token", testAgentLeaf(testNodeID, "current", 9))
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code)
	assert.Contains(t, response.Body.String(), `"code":"invalid_csr"`)

	input.CSRPEM = strings.Repeat("x", maxCredentialCSRBytes+1)
	body, err = json.Marshal(input)
	require.NoError(t, err)
	response = credentialRequest(t, handler, http.MethodPost, "/agent/v1/credential-renewals", string(body), "current-token", testAgentLeaf(testNodeID, "current", 10))
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code)
}

func TestCredentialRenewalConfirmationUsesCandidatePairOnly(t *testing.T) {
	store := &fakeStore{}
	handler := NewHandlerWithServicesAndIssuer(store, Config{AdminToken: testAdminToken, RequireAgentMTLS: true}, nil, nil, testCredentialIssuer(t))
	leaf := testAgentLeaf(testNodeID, "candidate-leaf", 11)
	response := credentialRequest(t, handler, http.MethodPost, "/agent/v1/credential-renewals/"+testRenewalID+"/confirm", "", "candidate-token", leaf)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, testRenewalID, store.confirmedRenewalID)
	assert.Equal(t, "candidate-token", store.confirmToken)
	expectedFingerprint := sha256.Sum256(leaf.Raw)
	assert.Equal(t, hex.EncodeToString(expectedFingerprint[:]), store.confirmIdentity.CertificateSHA256)

	store.confirmErr = ErrNotFound
	response = credentialRequest(t, handler, http.MethodPost, "/agent/v1/credential-renewals/"+testRenewalID+"/confirm", "", "old-token", leaf)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestCredentialRenewalStableErrors(t *testing.T) {
	csrPEM, _ := testEd25519CSR(t, "ignored", nil)
	nextHash := sha256.Sum256([]byte("next-token"))
	body, err := json.Marshal(credentialRenewalInput{
		RenewalID: testRenewalID, CSRPEM: csrPEM,
		NextTokenHash: hex.EncodeToString(nextHash[:]), NextTokenPrefix: "nfe_12345678",
	})
	require.NoError(t, err)
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrCredentialRenewalNotDue, http.StatusConflict, "renewal_not_due"},
		{ErrCredentialRenewalInProgress, http.StatusConflict, "renewal_in_progress"},
		{ErrCredentialRenewalIdempotency, http.StatusConflict, "idempotency_conflict"},
		{ErrCredentialRenewalRateLimited, http.StatusTooManyRequests, "renewal_rate_limited"},
	} {
		store := &fakeStore{renewalErr: test.err}
		handler := NewHandlerWithServicesAndIssuer(store, Config{AdminToken: testAdminToken, RequireAgentMTLS: true}, nil, nil, testCredentialIssuer(t))
		response := credentialRequest(t, handler, http.MethodPost, "/agent/v1/credential-renewals", string(body), "current-token", testAgentLeaf(testNodeID, "current", 12))
		assert.Equal(t, test.status, response.Code, test.code+": "+response.Body.String())
		assert.Contains(t, response.Body.String(), `"code":"`+test.code+`"`)
	}

	handler := NewHandlerWithServicesAndIssuer(&fakeStore{}, Config{AdminToken: testAdminToken, RequireAgentMTLS: true}, nil, nil, nil)
	response := credentialRequest(t, handler, http.MethodPost, "/agent/v1/credential-renewals", string(body), "current-token", testAgentLeaf(testNodeID, "current", 13))
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
	assert.Contains(t, response.Body.String(), `"code":"credential_issuer_unavailable"`)

	handler = NewHandlerWithServicesAndIssuer(&fakeStore{}, Config{AdminToken: testAdminToken, RequireAgentMTLS: true}, nil, nil, errorCredentialIssuer{err: bootstrap.ErrAgentCAExpiresSoon})
	response = credentialRequest(t, handler, http.MethodPost, "/agent/v1/credential-renewals", string(body), "current-token", testAgentLeaf(testNodeID, "current", 14))
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
	assert.Contains(t, response.Body.String(), `"code":"ca_expires_soon"`)
}

type errorCredentialIssuer struct{ err error }

func (issuer errorCredentialIssuer) IssueCSR(string, *x509.CertificateRequest) ([]byte, error) {
	return nil, issuer.err
}

func credentialRequest(t *testing.T, handler http.Handler, method, path, body, token string, leaf *x509.Certificate) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}},
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func testAgentLeaf(nodeID, raw string, serial int64) *x509.Certificate {
	return &x509.Certificate{
		Raw: []byte(raw), Subject: pkix.Name{CommonName: nodeID}, SerialNumber: big.NewInt(serial),
		NotAfter: time.Now().UTC().Add(30 * 24 * time.Hour),
	}
}

func testEd25519CSR(t *testing.T, commonName string, dnsNames []string) (string, ed25519.PublicKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	raw, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: commonName}, DNSNames: dnsNames,
	}, privateKey)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: raw})), publicKey
}

func testCredentialIssuer(t *testing.T) *bootstrap.MTLSIssuer {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Agent CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true, IsCA: true,
	}
	rawCertificate, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	require.NoError(t, err)
	rawKey, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	directory := t.TempDir()
	certificatePath := filepath.Join(directory, "ca.crt")
	keyPath := filepath.Join(directory, "ca.key")
	require.NoError(t, os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rawCertificate}), 0o600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rawKey}), 0o600))
	issuer, err := bootstrap.LoadMTLSIssuer(certificatePath, keyPath, "panel.test")
	require.NoError(t, err)
	return issuer
}
