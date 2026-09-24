package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const renewalTestNodeID = "11111111-1111-4111-8111-111111111111"

type renewalTestPanel struct {
	t               *testing.T
	now             time.Time
	ca              *x509.Certificate
	caKey           ed25519.PrivateKey
	active          *x509.Certificate
	activeKey       ed25519.PrivateKey
	activeToken     string
	server          *httptest.Server
	caPath          string
	activeCertPath  string
	activeKeyPath   string
	stateDir        string
	responseMutator func(*credentialRenewalResponse, *x509.CertificateRequest)
	failIssueOnce   bool
	failConfirmOnce bool
	issueErrorCode  string
	onConfirm       func()

	mu                 sync.Mutex
	issueCalls         int
	confirmCalls       int
	probeCalls         int
	pairingErrors      []string
	issuedRequest      credentialRenewalRequest
	issuedResponse     credentialRenewalResponse
	candidateTokenHash string
	candidateCertHash  string
}

func newRenewalTestPanel(t *testing.T) *renewalTestPanel {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	ca, caKey := renewalTestCertificate(t, nil, nil, pkix.Name{CommonName: "renewal-test-ca"}, now.Add(-time.Hour), now.Add(2*365*24*time.Hour), true, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageAny}, nil)
	serverCert, serverKey := renewalTestCertificate(t, ca, caKey, pkix.Name{CommonName: "panel.test"}, now.Add(-time.Hour), now.Add(365*24*time.Hour), false, []string{"panel.test"}, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, nil)
	active, activeKey := renewalTestCertificate(t, ca, caKey, pkix.Name{CommonName: renewalTestNodeID}, now.Add(-time.Hour), now.Add(5*time.Minute), false, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil)

	directory := t.TempDir()
	stateDir := filepath.Join(directory, "credentials")
	require.NoError(t, os.Mkdir(stateDir, 0o700))
	panel := &renewalTestPanel{
		t: t, now: now, ca: ca, caKey: caKey, active: active, activeKey: activeKey,
		activeToken: "nfe_active_test_token_0123456789", stateDir: stateDir,
	}
	panel.caPath = writeRenewalTestCertificate(t, directory, "ca.pem", ca.Raw)
	panel.activeCertPath = writeRenewalTestCertificate(t, directory, "active.pem", active.Raw)
	panel.activeKeyPath = writeRenewalTestPrivateKey(t, directory, "active.key", activeKey)

	serverPair, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCert.Raw}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: renewalTestPKCS8(t, serverKey)}),
	)
	require.NoError(t, err)
	clientRoots := x509.NewCertPool()
	clientRoots.AddCert(ca)
	server := httptest.NewUnstartedServer(http.HandlerFunc(panel.serveHTTP))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverPair}, ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs: clientRoots, MinVersion: tls.VersionTLS13,
	}
	server.StartTLS()
	panel.server = server
	t.Cleanup(server.Close)
	return panel
}

func (p *renewalTestPanel) config(mode CredentialRenewalMode) Config {
	return Config{
		Token: p.activeToken, PanelURL: p.server.URL, PanelTLSCA: p.caPath,
		PanelTLSCert: p.activeCertPath, PanelTLSKey: p.activeKeyPath,
		PanelTLSServerName: "panel.test", CredentialMode: mode,
		CredentialStateDir: p.stateDir, CredentialRenewBefore: 8 * 24 * time.Hour,
	}
}

func (p *renewalTestPanel) serveHTTP(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var peer *x509.Certificate
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		peer = r.TLS.PeerCertificates[0]
	}
	p.recordPair(peer, token)

	switch {
	case r.URL.Path == "/agent/v1/credential-renewals":
		p.handleIssue(w, r)
	case strings.HasPrefix(r.URL.Path, "/agent/v1/credential-renewals/") && strings.HasSuffix(r.URL.Path, "/confirm"):
		p.handleConfirm(w, token)
	case r.URL.Path == "/probe":
		p.mu.Lock()
		p.probeCalls++
		p.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (p *renewalTestPanel) handleIssue(w http.ResponseWriter, r *http.Request) {
	var request credentialRenewalRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.issueCalls++
	call := p.issueCalls
	errorCode := p.issueErrorCode
	if errorCode != "" {
		p.mu.Unlock()
		writeRenewalTestError(w, http.StatusConflict, errorCode)
		return
	}
	if p.issuedRequest.RenewalID != "" {
		if request != p.issuedRequest {
			p.pairingErrors = append(p.pairingErrors, "renewal retry changed its idempotency payload")
		}
		response := p.issuedResponse
		mutator := p.responseMutator
		fail := p.failIssueOnce && call == 1
		p.mu.Unlock()
		if fail {
			http.Error(w, "stored but response lost", http.StatusServiceUnavailable)
			return
		}
		if mutator != nil {
			csr := parseRenewalTestCSR(p.t, request.CSRPEM)
			mutator(&response, csr)
		}
		writeRenewalTestJSON(w, response)
		return
	}
	p.issuedRequest = request
	p.candidateTokenHash = request.NextTokenHash
	p.mu.Unlock()

	csr := parseRenewalTestCSR(p.t, request.CSRPEM)
	certificate, _ := renewalTestCertificate(p.t, p.ca, p.caKey, csr.Subject, p.now.Add(-time.Minute), p.now.Add(365*24*time.Hour), false, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, csr.PublicKey)
	fingerprint := sha256.Sum256(certificate.Raw)
	response := credentialRenewalResponse{
		RenewalID:         request.RenewalID,
		CertificatePEM:    string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})),
		CertificateSHA256: hex.EncodeToString(fingerprint[:]), Serial: certificate.SerialNumber.String(),
		NotBefore: certificate.NotBefore, NotAfter: certificate.NotAfter, ConfirmBy: p.now.Add(30 * time.Minute),
	}
	p.mu.Lock()
	p.issuedResponse = response
	p.candidateCertHash = response.CertificateSHA256
	mutator := p.responseMutator
	fail := p.failIssueOnce && call == 1
	p.mu.Unlock()
	if fail {
		http.Error(w, "stored but response lost", http.StatusServiceUnavailable)
		return
	}
	if mutator != nil {
		mutator(&response, csr)
	}
	writeRenewalTestJSON(w, response)
}

func (p *renewalTestPanel) handleConfirm(w http.ResponseWriter, token string) {
	p.mu.Lock()
	p.confirmCalls++
	call := p.confirmCalls
	expectedHash := p.candidateTokenHash
	fail := p.failConfirmOnce && call == 1
	onConfirm := p.onConfirm
	p.mu.Unlock()
	actualHash := sha256.Sum256([]byte(token))
	if hex.EncodeToString(actualHash[:]) != expectedHash {
		p.mu.Lock()
		p.pairingErrors = append(p.pairingErrors, "confirm did not use the candidate bearer")
		p.mu.Unlock()
	}
	if onConfirm != nil {
		onConfirm()
	}
	if fail {
		http.Error(w, "activated but response lost", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *renewalTestPanel) recordPair(peer *x509.Certificate, token string) {
	if peer == nil {
		p.mu.Lock()
		p.pairingErrors = append(p.pairingErrors, "request had no client certificate")
		p.mu.Unlock()
		return
	}
	fingerprint := sha256.Sum256(peer.Raw)
	fingerprintHex := hex.EncodeToString(fingerprint[:])
	p.mu.Lock()
	defer p.mu.Unlock()
	switch fingerprintHex {
	case renewalTestFingerprint(p.active):
		if token != p.activeToken {
			p.pairingErrors = append(p.pairingErrors, "active certificate was paired with a different bearer")
		}
	case p.candidateCertHash:
		tokenHash := sha256.Sum256([]byte(token))
		if hex.EncodeToString(tokenHash[:]) != p.candidateTokenHash {
			p.pairingErrors = append(p.pairingErrors, "candidate certificate was paired with a different bearer")
		}
	default:
		p.pairingErrors = append(p.pairingErrors, "request used an unknown client certificate")
	}
}

func (p *renewalTestPanel) counts() (int, int, int, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.issueCalls, p.confirmCalls, p.probeCalls, append([]string(nil), p.pairingErrors...)
}

func TestCredentialRenewalEndToEndPersistsAndAtomicallySwapsPair(t *testing.T) {
	panel := newRenewalTestPanel(t)
	manager, client, err := NewPanelCredentialManager(panel.config(CredentialRenewalApply))
	require.NoError(t, err)

	probeRenewalTestPanel(t, client, panel.server.URL)
	require.NoError(t, manager.RunOnce(context.Background()))
	probeRenewalTestPanel(t, client, panel.server.URL)

	state, err := manager.store.load()
	require.NoError(t, err)
	assert.Nil(t, state.Pending)
	assert.NotEqual(t, panel.activeToken, state.Active.Token)
	assert.NotEmpty(t, state.Active.CertificateSHA256)
	assert.Empty(t, state.Active.CSRPEM)
	assert.Empty(t, state.Active.RenewalID)
	assert.True(t, state.Active.ConfirmBy.IsZero())
	info, err := os.Stat(manager.store.path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// A restart must prefer canonical state over stale legacy env/files.
	restarted, restartedClient, err := NewPanelCredentialManager(panel.config(CredentialRenewalApply))
	require.NoError(t, err)
	assert.Equal(t, state.Active.Token, restarted.state.Active.Token)
	probeRenewalTestPanel(t, restartedClient, panel.server.URL)

	issues, confirms, probes, pairErrors := panel.counts()
	assert.Equal(t, 1, issues)
	assert.Equal(t, 1, confirms)
	assert.Equal(t, 3, probes)
	assert.Empty(t, pairErrors)
}

func TestCredentialRenewalConcurrentTrafficNeverMixesCertificateAndBearer(t *testing.T) {
	panel := newRenewalTestPanel(t)
	manager, client, err := NewPanelCredentialManager(panel.config(CredentialRenewalApply))
	require.NoError(t, err)

	start := make(chan struct{})
	errorsFound := make(chan error, 128)
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			for request := 0; request < 20; request++ {
				response, requestErr := client.Get(panel.server.URL + "/probe")
				if requestErr != nil {
					errorsFound <- requestErr
					continue
				}
				_ = response.Body.Close()
				if response.StatusCode != http.StatusNoContent {
					errorsFound <- fmt.Errorf("probe status %d", response.StatusCode)
				}
			}
		}()
	}
	close(start)
	require.NoError(t, manager.RunOnce(context.Background()))
	wait.Wait()
	close(errorsFound)
	for requestErr := range errorsFound {
		assert.NoError(t, requestErr)
	}
	_, _, _, pairErrors := panel.counts()
	assert.Empty(t, pairErrors)
}

func TestCredentialRenewalCrashResumeIsIdempotent(t *testing.T) {
	t.Run("issue response lost", func(t *testing.T) {
		panel := newRenewalTestPanel(t)
		panel.failIssueOnce = true
		manager, _, err := NewPanelCredentialManager(panel.config(CredentialRenewalApply))
		require.NoError(t, err)
		require.Error(t, manager.RunOnce(context.Background()))
		require.NotNil(t, manager.state.Pending)
		assert.Empty(t, manager.state.Pending.CertificatePEM)

		restarted, _, err := NewPanelCredentialManager(panel.config(CredentialRenewalApply))
		require.NoError(t, err)
		require.NoError(t, restarted.RunOnce(context.Background()))
		issues, confirms, _, pairErrors := panel.counts()
		assert.Equal(t, 2, issues)
		assert.Equal(t, 1, confirms)
		assert.Empty(t, pairErrors)
	})

	t.Run("confirm response lost", func(t *testing.T) {
		panel := newRenewalTestPanel(t)
		panel.failConfirmOnce = true
		manager, _, err := NewPanelCredentialManager(panel.config(CredentialRenewalApply))
		require.NoError(t, err)
		require.Error(t, manager.RunOnce(context.Background()))
		require.NotNil(t, manager.state.Pending)
		assert.NotEmpty(t, manager.state.Pending.CertificatePEM)

		restarted, _, err := NewPanelCredentialManager(panel.config(CredentialRenewalApply))
		require.NoError(t, err)
		require.NoError(t, restarted.RunOnce(context.Background()))
		issues, confirms, _, pairErrors := panel.counts()
		assert.Equal(t, 1, issues)
		assert.Equal(t, 2, confirms)
		assert.Empty(t, pairErrors)
	})
}

func TestCredentialRenewalDiscardsExpiredPendingAndStartsFresh(t *testing.T) {
	panel := newRenewalTestPanel(t)
	panel.failIssueOnce = true
	manager, _, err := NewPanelCredentialManager(panel.config(CredentialRenewalApply))
	require.NoError(t, err)
	require.Error(t, manager.RunOnce(context.Background()))
	require.NotNil(t, manager.state.Pending)
	assert.Empty(t, manager.state.Pending.CertificatePEM)

	panel.mu.Lock()
	panel.issueErrorCode = "renewal_expired"
	panel.mu.Unlock()
	require.ErrorContains(t, manager.RunOnce(context.Background()), "renewal_expired")
	assert.Nil(t, manager.state.Pending)
	persisted, err := manager.store.load()
	require.NoError(t, err)
	assert.Nil(t, persisted.Pending)

	panel.mu.Lock()
	panel.failIssueOnce = false
	panel.issueErrorCode = ""
	panel.issuedRequest = credentialRenewalRequest{}
	panel.issuedResponse = credentialRenewalResponse{}
	panel.candidateTokenHash = ""
	panel.candidateCertHash = ""
	panel.mu.Unlock()
	require.NoError(t, manager.RunOnce(context.Background()))
	assert.Nil(t, manager.state.Pending)
	issues, confirms, _, pairErrors := panel.counts()
	assert.Equal(t, 3, issues)
	assert.Equal(t, 1, confirms)
	assert.Empty(t, pairErrors)
}

func TestCredentialRenewalUsesConfirmedCandidateWhenPromotionWriteFails(t *testing.T) {
	panel := newRenewalTestPanel(t)
	statePath := filepath.Join(panel.stateDir, credentialStateFileName)
	panel.onConfirm = func() {
		require.NoError(t, os.Chmod(statePath, 0o644))
	}
	manager, client, err := NewPanelCredentialManager(panel.config(CredentialRenewalApply))
	require.NoError(t, err)
	require.ErrorContains(t, manager.RunOnce(context.Background()), "promote renewed credential")

	// The Panel has activated/revoked atomically; normal traffic must already
	// use the confirmed candidate even though state cleanup needs a retry.
	probeRenewalTestPanel(t, client, panel.server.URL)
	require.NotNil(t, manager.state.Pending)
	assert.Equal(t, manager.state.Pending.Token, manager.current.Load().token)

	panel.onConfirm = nil
	require.NoError(t, os.Chmod(statePath, 0o600))
	require.NoError(t, manager.RunOnce(context.Background()))
	assert.Nil(t, manager.state.Pending)
	_, confirms, _, pairErrors := panel.counts()
	assert.Equal(t, 2, confirms)
	assert.Empty(t, pairErrors)
}

func TestCredentialRenewalInvalidIssueDoesNotPoisonPendingState(t *testing.T) {
	tests := map[string]func(*credentialRenewalResponse, *x509.CertificateRequest){
		"fingerprint": func(response *credentialRenewalResponse, _ *x509.CertificateRequest) {
			response.CertificateSHA256 = strings.Repeat("0", 64)
		},
		"serial": func(response *credentialRenewalResponse, _ *x509.CertificateRequest) { response.Serial = "999999" },
		"not before": func(response *credentialRenewalResponse, _ *x509.CertificateRequest) {
			response.NotBefore = response.NotBefore.Add(time.Second)
		},
		"not after": func(response *credentialRenewalResponse, _ *x509.CertificateRequest) {
			response.NotAfter = response.NotAfter.Add(time.Second)
		},
		"confirm by": func(response *credentialRenewalResponse, _ *x509.CertificateRequest) {
			response.ConfirmBy = time.Unix(1, 0).UTC()
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			panel := newRenewalTestPanel(t)
			panel.responseMutator = mutate
			manager, _, err := NewPanelCredentialManager(panel.config(CredentialRenewalApply))
			require.NoError(t, err)
			require.Error(t, manager.RunOnce(context.Background()))
			require.NotNil(t, manager.state.Pending)
			assert.Empty(t, manager.state.Pending.CertificatePEM)
			persisted, err := manager.store.load()
			require.NoError(t, err)
			require.NotNil(t, persisted.Pending)
			assert.Empty(t, persisted.Pending.CertificatePEM)

			panel.responseMutator = nil
			require.NoError(t, manager.RunOnce(context.Background()))
		})
	}
}

func TestCredentialRenewalRejectsWrongCertificateIdentityKeyAndUsage(t *testing.T) {
	tests := map[string]func(*renewalTestPanel, *credentialRenewalResponse, *x509.CertificateRequest){
		"identity": func(panel *renewalTestPanel, response *credentialRenewalResponse, csr *x509.CertificateRequest) {
			replaceRenewalTestCertificate(panel, response, pkix.Name{CommonName: "different-node"}, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, csr.PublicKey)
		},
		"key": func(panel *renewalTestPanel, response *credentialRenewalResponse, csr *x509.CertificateRequest) {
			publicKey, _, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			replaceRenewalTestCertificate(panel, response, csr.Subject, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, publicKey)
		},
		"usage": func(panel *renewalTestPanel, response *credentialRenewalResponse, csr *x509.CertificateRequest) {
			replaceRenewalTestCertificate(panel, response, csr.Subject, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, csr.PublicKey)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			panel := newRenewalTestPanel(t)
			panel.responseMutator = func(response *credentialRenewalResponse, csr *x509.CertificateRequest) {
				mutate(panel, response, csr)
			}
			manager, _, err := NewPanelCredentialManager(panel.config(CredentialRenewalApply))
			require.NoError(t, err)
			require.Error(t, manager.RunOnce(context.Background()))
			require.NotNil(t, manager.state.Pending)
			assert.Empty(t, manager.state.Pending.CertificatePEM)
		})
	}
}

func TestCredentialRenewalObserveAndOffMakeNoRenewalRequest(t *testing.T) {
	for _, mode := range []CredentialRenewalMode{CredentialRenewalObserve, CredentialRenewalOff, ""} {
		t.Run(string(mode), func(t *testing.T) {
			panel := newRenewalTestPanel(t)
			manager, _, err := NewPanelCredentialManager(panel.config(mode))
			require.NoError(t, err)
			require.NoError(t, manager.RunOnce(context.Background()))
			issues, confirms, _, pairErrors := panel.counts()
			assert.Zero(t, issues)
			assert.Zero(t, confirms)
			assert.Empty(t, pairErrors)
			assert.Nil(t, manager.state.Pending)
		})
	}

	panel := newRenewalTestPanel(t)
	_, _, err := NewPanelCredentialManager(panel.config("invalid"))
	require.Error(t, err)
}

func TestCredentialRenewalPendingUsesShortRetryCadence(t *testing.T) {
	panel := newRenewalTestPanel(t)
	manager, _, err := NewPanelCredentialManager(panel.config(CredentialRenewalApply))
	require.NoError(t, err)
	assert.Equal(t, credentialRenewalCheckPeriod, manager.nextCredentialCheckPeriod())
	pending, err := newPendingCredential(renewalTestNodeID)
	require.NoError(t, err)
	manager.state.Pending = &pending
	assert.Equal(t, credentialPendingRetryPeriod, manager.nextCredentialCheckPeriod())
	manager.cfg.CredentialMode = CredentialRenewalObserve
	assert.Equal(t, credentialRenewalCheckPeriod, manager.nextCredentialCheckPeriod())
}

func TestCredentialRenewalRejectsTamperedPendingMaterialBeforeNetwork(t *testing.T) {
	tests := map[string]func(StoredCredential) StoredCredential{
		"key mismatch": func(pending StoredCredential) StoredCredential {
			other, err := newPendingCredential(renewalTestNodeID)
			require.NoError(t, err)
			pending.PrivateKeyPEM = other.PrivateKeyPEM
			return pending
		},
		"identity mismatch": func(pending StoredCredential) StoredCredential {
			other, err := newPendingCredential("22222222-2222-4222-8222-222222222222")
			require.NoError(t, err)
			pending.PrivateKeyPEM = other.PrivateKeyPEM
			pending.CSRPEM = other.CSRPEM
			return pending
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			panel := newRenewalTestPanel(t)
			manager, _, err := NewPanelCredentialManager(panel.config(CredentialRenewalApply))
			require.NoError(t, err)
			pending, err := newPendingCredential(renewalTestNodeID)
			require.NoError(t, err)
			pending = mutate(pending)
			manager.state.Pending = &pending
			require.NoError(t, manager.store.write(manager.state))
			require.Error(t, manager.RunOnce(context.Background()))
			issues, confirms, _, _ := panel.counts()
			assert.Zero(t, issues)
			assert.Zero(t, confirms)
		})
	}
}

func TestCredentialTransportRejectsOtherOrigins(t *testing.T) {
	panel := newRenewalTestPanel(t)
	_, client, err := NewPanelCredentialManager(panel.config(CredentialRenewalObserve))
	require.NoError(t, err)
	evilCalls := 0
	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { evilCalls++ }))
	defer evil.Close()
	_, err = client.Get(evil.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-Panel origin")
	assert.Zero(t, evilCalls)
}

func TestCredentialManagerRejectsAmbiguousPanelBaseURLs(t *testing.T) {
	panel := newRenewalTestPanel(t)
	for name, panelURL := range map[string]string{
		"path":     panel.server.URL + "/api",
		"query":    panel.server.URL + "?tenant=other",
		"fragment": panel.server.URL + "#other",
		"userinfo": strings.Replace(panel.server.URL, "https://", "https://user@", 1),
	} {
		t.Run(name, func(t *testing.T) {
			cfg := panel.config(CredentialRenewalObserve)
			cfg.PanelURL = panelURL
			_, _, err := NewPanelCredentialManager(cfg)
			require.ErrorContains(t, err, "must not contain")
		})
	}
}

func TestCredentialStateStoreRejectsUnsafeOrCorruptState(t *testing.T) {
	t.Run("broad directory", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "credentials")
		require.NoError(t, os.Mkdir(directory, 0o755))
		_, err := newCredentialStateStore(directory)
		require.ErrorContains(t, err, "permissions")
	})
	t.Run("directory symlink", func(t *testing.T) {
		root := t.TempDir()
		realDirectory := filepath.Join(root, "real")
		require.NoError(t, os.Mkdir(realDirectory, 0o700))
		link := filepath.Join(root, "link")
		require.NoError(t, os.Symlink(realDirectory, link))
		_, err := newCredentialStateStore(link)
		require.ErrorContains(t, err, "symlink")
	})

	validState := CredentialState{Version: credentialStateVersion, Active: StoredCredential{Token: "token", CertificatePEM: "certificate", PrivateKeyPEM: "key"}}
	for name, content := range map[string]string{
		"truncated":  `{"version":1,"active":`,
		"trailing":   `{"version":1,"active":{"token":"t","certificate_pem":"c","private_key_pem":"k"}} {}`,
		"unknown":    `{"version":1,"active":{"token":"t","certificate_pem":"c","private_key_pem":"k"},"surprise":true}`,
		"incomplete": `{"version":1,"active":{"token":"t","certificate_pem":"c","private_key_pem":"k"},"pending":{"token":"next"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			store := newRenewalTestStore(t)
			require.NoError(t, os.WriteFile(store.path, []byte(content), 0o600))
			_, err := store.load()
			require.Error(t, err)
		})
	}
	t.Run("state symlink", func(t *testing.T) {
		store := newRenewalTestStore(t)
		target := filepath.Join(t.TempDir(), "target")
		require.NoError(t, os.WriteFile(target, []byte("{}"), 0o600))
		require.NoError(t, os.Symlink(target, store.path))
		_, err := store.load()
		require.ErrorContains(t, err, "symlink")
	})
	t.Run("broad state file", func(t *testing.T) {
		store := newRenewalTestStore(t)
		content, err := json.Marshal(validState)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(store.path, content, 0o644))
		_, err = store.load()
		require.ErrorContains(t, err, "permissions")
	})
	t.Run("write refuses swapped state", func(t *testing.T) {
		store := newRenewalTestStore(t)
		require.NoError(t, store.write(validState))
		require.NoError(t, os.Chmod(store.path, 0o644))
		require.ErrorContains(t, store.write(validState), "permissions")
		require.NoError(t, os.Remove(store.path))
		target := filepath.Join(t.TempDir(), "target")
		require.NoError(t, os.WriteFile(target, []byte("do not touch"), 0o600))
		require.NoError(t, os.Symlink(target, store.path))
		require.ErrorContains(t, store.write(validState), "symlink")
		content, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "do not touch", string(content))
	})
	t.Run("write refuses broadened directory", func(t *testing.T) {
		store := newRenewalTestStore(t)
		require.NoError(t, os.Chmod(store.directory, 0o755))
		require.ErrorContains(t, store.write(validState), "permissions")
	})
}

func TestCredentialStateWriteIsAtomicAndLeavesNoTemporaryFiles(t *testing.T) {
	store := newRenewalTestStore(t)
	for i := 0; i < 50; i++ {
		state := CredentialState{Version: credentialStateVersion, Active: StoredCredential{
			Token: fmt.Sprintf("token-%d", i), CertificatePEM: "certificate", PrivateKeyPEM: "key",
		}}
		require.NoError(t, store.write(state))
		loaded, err := store.load()
		require.NoError(t, err)
		assert.Equal(t, state.Active.Token, loaded.Active.Token)
	}
	entries, err := os.ReadDir(store.directory)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, credentialStateFileName, entries[0].Name())
}

func TestCredentialRenewalJitterIsStableAndBounded(t *testing.T) {
	seen := map[time.Duration]bool{}
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("node-%d", i)
		first := stableCredentialJitter(id)
		second := stableCredentialJitter(id)
		assert.Equal(t, first, second)
		assert.GreaterOrEqual(t, first, time.Duration(0))
		assert.LessOrEqual(t, first, 7*24*time.Hour)
		assert.Zero(t, first%(24*time.Hour))
		seen[first] = true
	}
	assert.Len(t, seen, 8)

	now := time.Now().UTC()
	renewBefore := 45 * 24 * time.Hour
	threshold := renewBefore - stableCredentialJitter(renewalTestNodeID)
	assert.False(t, credentialRenewalDue(now, now.Add(threshold+time.Second), renewBefore, renewalTestNodeID))
	assert.True(t, credentialRenewalDue(now, now.Add(threshold), renewBefore, renewalTestNodeID))
	assert.False(t, credentialRenewalDue(now, now.Add(time.Minute), time.Second, renewalTestNodeID))
}

func TestCredentialRenewalIdentifiersAndErrorCodeSanitization(t *testing.T) {
	for i := 0; i < 100; i++ {
		id, err := randomCredentialUUID()
		require.NoError(t, err)
		assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`), id)
	}
	assert.Equal(t, "safe_code-1.2", sanitizePanelErrorCode("safe_code-1.2"))
	for _, unsafe := range []string{"", "line\nbreak", strings.Repeat("a", 65), "код"} {
		assert.Equal(t, "http_error", sanitizePanelErrorCode(unsafe))
	}
}

func writeRenewalTestError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
}

func replaceRenewalTestCertificate(panel *renewalTestPanel, response *credentialRenewalResponse, subject pkix.Name, usages []x509.ExtKeyUsage, publicKey any) {
	certificate, _ := renewalTestCertificate(panel.t, panel.ca, panel.caKey, subject, panel.now.Add(-time.Minute), panel.now.Add(365*24*time.Hour), false, nil, usages, publicKey)
	fingerprint := sha256.Sum256(certificate.Raw)
	response.CertificatePEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}))
	response.CertificateSHA256 = hex.EncodeToString(fingerprint[:])
	response.Serial = certificate.SerialNumber.String()
	response.NotBefore = certificate.NotBefore
	response.NotAfter = certificate.NotAfter
}

func renewalTestCertificate(t *testing.T, parent *x509.Certificate, parentKey ed25519.PrivateKey, subject pkix.Name, notBefore, notAfter time.Time, isCA bool, dnsNames []string, usages []x509.ExtKeyUsage, publicKey any) (*x509.Certificate, ed25519.PrivateKey) {
	t.Helper()
	generatedPublicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	if publicKey == nil {
		publicKey = generatedPublicKey
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 120)
	serial, err := rand.Int(rand.Reader, serialLimit)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: serial, Subject: subject, NotBefore: notBefore, NotAfter: notAfter,
		DNSNames: dnsNames, ExtKeyUsage: usages, BasicConstraintsValid: true, IsCA: isCA,
		KeyUsage: x509.KeyUsageDigitalSignature,
	}
	if isCA {
		template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
		parent, parentKey = template, privateKey
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, parent, publicKey, parentKey)
	require.NoError(t, err)
	certificate, err := x509.ParseCertificate(raw)
	require.NoError(t, err)
	return certificate, privateKey
}

func parseRenewalTestCSR(t *testing.T, value string) *x509.CertificateRequest {
	t.Helper()
	block, rest := pem.Decode([]byte(value))
	require.NotNil(t, block)
	require.Empty(t, rest)
	require.Equal(t, "CERTIFICATE REQUEST", block.Type)
	request, err := x509.ParseCertificateRequest(block.Bytes)
	require.NoError(t, err)
	require.NoError(t, request.CheckSignature())
	return request
}

func renewalTestFingerprint(certificate *x509.Certificate) string {
	sum := sha256.Sum256(certificate.Raw)
	return hex.EncodeToString(sum[:])
}

func writeRenewalTestJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeRenewalTestCertificate(t *testing.T, directory, name string, raw []byte) string {
	t.Helper()
	path := filepath.Join(directory, name)
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}), 0o600))
	return path
}

func writeRenewalTestPrivateKey(t *testing.T, directory, name string, key ed25519.PrivateKey) string {
	t.Helper()
	path := filepath.Join(directory, name)
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: renewalTestPKCS8(t, key)}), 0o600))
	return path
}

func renewalTestPKCS8(t *testing.T, key ed25519.PrivateKey) []byte {
	t.Helper()
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return raw
}

func probeRenewalTestPanel(t *testing.T, client *http.Client, baseURL string) {
	t.Helper()
	response, err := client.Get(baseURL + "/probe")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusNoContent, response.StatusCode)
}

func newRenewalTestStore(t *testing.T) credentialStateStore {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "credentials")
	require.NoError(t, os.Mkdir(directory, 0o700))
	store, err := newCredentialStateStore(directory)
	require.NoError(t, err)
	return store
}

func TestParsedRequestPublicKeyIsPanicSafe(t *testing.T) {
	key, ok := parsedRequestPublicKey(nil)
	assert.False(t, ok)
	assert.Nil(t, key)
	key, ok = parsedRequestPublicKey(&x509.CertificateRequest{PublicKey: errors.New("not a key")})
	assert.False(t, ok)
	assert.Nil(t, key)
}
