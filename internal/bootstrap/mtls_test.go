package bootstrap

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMTLSIssuerCreatesNodeBoundClientCertificate(t *testing.T) {
	directory := t.TempDir()
	caCertificate, caKey := createTestCA(t)
	caPath := filepath.Join(directory, "ca.crt")
	keyPath := filepath.Join(directory, "ca.key")
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertificate.Raw}), 0o600))
	rawKey, err := x509.MarshalPKCS8PrivateKey(caKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rawKey}), 0o600))

	issuer, err := LoadMTLSIssuer(caPath, keyPath, "panel.test")
	require.NoError(t, err)
	identity, err := issuer.Issue("11111111-1111-4111-8111-111111111111")
	require.NoError(t, err)
	assert.Equal(t, "panel.test", identity.ServerName)
	require.NotEmpty(t, identity.PrivateKeyPEM)

	block, _ := pem.Decode(identity.CertificatePEM)
	require.NotNil(t, block)
	certificate, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "11111111-1111-4111-8111-111111111111", certificate.Subject.CommonName)
	assert.Contains(t, certificate.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
	roots := x509.NewCertPool()
	roots.AddCert(caCertificate)
	_, err = certificate.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	require.NoError(t, err)
}

func TestMTLSIssuerRejectsInvalidNodeIdentity(t *testing.T) {
	issuer := &MTLSIssuer{now: time.Now}
	_, err := issuer.Issue("not-a-uuid")
	require.Error(t, err)
}

func TestMTLSIssuerSignsAgentCSRWithoutTrustingRequestedIdentity(t *testing.T) {
	issuer := createTestMTLSIssuer(t)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	rawCSR, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "attacker", Organization: []string{"untrusted"}},
		DNSNames: []string{"attacker.example"}, EmailAddresses: []string{"root@attacker.example"},
	}, privateKey)
	require.NoError(t, err)
	csr, err := x509.ParseCertificateRequest(rawCSR)
	require.NoError(t, err)

	certificatePEM, err := issuer.IssueCSR("11111111-1111-4111-8111-111111111111", csr)
	require.NoError(t, err)
	block, rest := pem.Decode(certificatePEM)
	require.NotNil(t, block)
	assert.Empty(t, bytes.TrimSpace(rest))
	certificate, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "11111111-1111-4111-8111-111111111111", certificate.Subject.CommonName)
	assert.Empty(t, certificate.DNSNames)
	assert.Empty(t, certificate.EmailAddresses)
	assert.Equal(t, publicKey, certificate.PublicKey)
	assert.Contains(t, certificate.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
}

func TestMTLSIssuerRejectsInvalidOrNonEd25519CSR(t *testing.T) {
	issuer := createTestMTLSIssuer(t)
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	rawCSR, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, privateKey)
	require.NoError(t, err)
	csr, err := x509.ParseCertificateRequest(rawCSR)
	require.NoError(t, err)
	csr.Signature[0] ^= 0xff
	_, err = issuer.IssueCSR("11111111-1111-4111-8111-111111111111", csr)
	assert.ErrorIs(t, err, ErrInvalidNodeCSR)

	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	rawCSR, err = x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, ecdsaKey)
	require.NoError(t, err)
	csr, err = x509.ParseCertificateRequest(rawCSR)
	require.NoError(t, err)
	_, err = issuer.IssueCSR("11111111-1111-4111-8111-111111111111", csr)
	assert.ErrorIs(t, err, ErrInvalidNodeCSR)
}

func createTestMTLSIssuer(t *testing.T) *MTLSIssuer {
	t.Helper()
	directory := t.TempDir()
	caCertificate, caKey := createTestCA(t)
	caPath := filepath.Join(directory, "ca.crt")
	keyPath := filepath.Join(directory, "ca.key")
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertificate.Raw}), 0o600))
	rawKey, err := x509.MarshalPKCS8PrivateKey(caKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rawKey}), 0o600))
	issuer, err := LoadMTLSIssuer(caPath, keyPath, "panel.test")
	require.NoError(t, err)
	return issuer
}

func createTestCA(t *testing.T) (*x509.Certificate, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"NodeFlow"}, CommonName: "Test Agent CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	require.NoError(t, err)
	certificate, err := x509.ParseCertificate(raw)
	require.NoError(t, err)
	return certificate, privateKey
}
