package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPanelHTTPClientRequiresCompleteMTLSConfiguration(t *testing.T) {
	_, err := NewPanelHTTPClient(Config{PanelURL: "https://panel.test", PanelTLSCA: "/ca.pem"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires CA")

	_, err = NewPanelHTTPClient(Config{
		PanelURL:     "http://panel.test",
		PanelTLSCA:   "/ca.pem",
		PanelTLSCert: "/client.pem",
		PanelTLSKey:  "/client.key",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "https")
}

func TestNewPanelHTTPClientAuthenticatesTLS13(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := issueTestCertificate(t, nil, nil, "test-ca", true, nil, nil)
	serverCert, serverKey := issueTestCertificate(t, caCert, caKey, "panel.test", false, []string{"panel.test"}, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	clientCert, clientKey := issueTestCertificate(t, caCert, caKey, "11111111-1111-4111-8111-111111111111", false, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})

	caPath := writeCertificate(t, dir, "ca.pem", caCert.Raw)
	clientCertPath := writeCertificate(t, dir, "client.pem", clientCert.Raw)
	clientKeyPath := writePrivateKey(t, dir, "client.key", clientKey)
	serverPair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCert.Raw}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(t, serverKey)}))
	require.NoError(t, err)
	clientRoots := x509.NewCertPool()
	clientRoots.AddCert(caCert)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotNil(t, r.TLS)
		require.NotEmpty(t, r.TLS.PeerCertificates)
		assert.Equal(t, "11111111-1111-4111-8111-111111111111", r.TLS.PeerCertificates[0].Subject.CommonName)
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverPair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientRoots,
		MinVersion:   tls.VersionTLS13,
	}
	server.StartTLS()
	defer server.Close()

	client, err := NewPanelHTTPClient(Config{
		PanelURL:           server.URL,
		PanelTLSCA:         caPath,
		PanelTLSCert:       clientCertPath,
		PanelTLSKey:        clientKeyPath,
		PanelTLSServerName: "panel.test",
	})
	require.NoError(t, err)
	response, err := client.Get(server.URL)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusNoContent, response.StatusCode)
	require.NotNil(t, response.TLS)
	assert.Equal(t, uint16(tls.VersionTLS13), response.TLS.Version)
}

func issueTestCertificate(t *testing.T, parent *x509.Certificate, parentKey ed25519.PrivateKey, commonName string, isCA bool, dnsNames []string, usages []x509.ExtKeyUsage) (*x509.Certificate, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		DNSNames:              dnsNames,
		ExtKeyUsage:           usages,
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}
	if isCA {
		template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
		parent, parentKey = template, privateKey
	} else {
		template.KeyUsage = x509.KeyUsageDigitalSignature
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, parent, publicKey, parentKey)
	require.NoError(t, err)
	certificate, err := x509.ParseCertificate(raw)
	require.NoError(t, err)
	return certificate, privateKey
}

func writeCertificate(t *testing.T, dir, name string, raw []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}), 0o600))
	return path
}

func writePrivateKey(t *testing.T, dir, name string, key ed25519.PrivateKey) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(t, key)}), 0o600))
	return path
}

func mustPKCS8(t *testing.T, key ed25519.PrivateKey) []byte {
	t.Helper()
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return raw
}
