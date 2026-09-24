package agent

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	credentialStateVersion       = 1
	credentialStateFileName      = "state.json"
	maxCredentialStateBytes      = 256 << 10
	maxCredentialResponseBytes   = 64 << 10
	credentialRenewalCheckPeriod = time.Hour
	credentialPendingRetryPeriod = time.Minute
)

type CredentialRenewalMode string

const (
	CredentialRenewalOff     CredentialRenewalMode = "off"
	CredentialRenewalObserve CredentialRenewalMode = "observe"
	CredentialRenewalApply   CredentialRenewalMode = "apply"
)

func NormalizeCredentialRenewalMode(value string) (CredentialRenewalMode, error) {
	switch CredentialRenewalMode(strings.ToLower(strings.TrimSpace(value))) {
	case "", CredentialRenewalObserve:
		return CredentialRenewalObserve, nil
	case CredentialRenewalOff:
		return CredentialRenewalOff, nil
	case CredentialRenewalApply:
		return CredentialRenewalApply, nil
	default:
		return "", errors.New("credential renewal mode must be off, observe or apply")
	}
}

type StoredCredential struct {
	Token             string    `json:"token"`
	CertificatePEM    string    `json:"certificate_pem"`
	PrivateKeyPEM     string    `json:"private_key_pem"`
	CSRPEM            string    `json:"csr_pem,omitempty"`
	CertificateSHA256 string    `json:"certificate_sha256,omitempty"`
	CertificateSerial string    `json:"certificate_serial,omitempty"`
	NotBefore         time.Time `json:"not_before,omitempty"`
	NotAfter          time.Time `json:"not_after,omitempty"`
	RenewalID         string    `json:"renewal_id,omitempty"`
	ConfirmBy         time.Time `json:"confirm_by,omitempty"`
}

type CredentialState struct {
	Version int               `json:"version"`
	Active  StoredCredential  `json:"active"`
	Pending *StoredCredential `json:"pending,omitempty"`
}

type credentialSnapshot struct {
	token       string
	certificate tls.Certificate
	leaf        *x509.Certificate
	transport   *http.Transport
}

type credentialStateStore struct {
	directory string
	path      string
}

type PanelCredentialManager struct {
	cfg    Config
	store  credentialStateStore
	roots  *x509.CertPool
	origin *url.URL
	client *http.Client
	logger *log.Logger
	now    func() time.Time

	mu      sync.Mutex
	state   CredentialState
	current atomic.Pointer[credentialSnapshot]
}

type credentialAuthTransport struct {
	origin  *url.URL
	current func() *credentialSnapshot
}

func (t *credentialAuthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || !sameCredentialOrigin(t.origin, request.URL) {
		return nil, errors.New("credential transport rejected a non-Panel origin")
	}
	snapshot := t.current()
	if snapshot == nil || snapshot.token == "" {
		return nil, errors.New("Panel credential is unavailable")
	}
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+snapshot.token)
	if snapshot.transport == nil {
		return nil, errors.New("Panel credential transport is unavailable")
	}
	return snapshot.transport.RoundTrip(clone)
}

func (t *credentialAuthTransport) CloseIdleConnections() {
	if snapshot := t.current(); snapshot != nil && snapshot.transport != nil {
		snapshot.transport.CloseIdleConnections()
	}
}

func sameCredentialOrigin(expected, actual *url.URL) bool {
	if expected == nil || actual == nil {
		return false
	}
	return strings.EqualFold(expected.Scheme, actual.Scheme) && strings.EqualFold(expected.Host, actual.Host)
}

// NewPanelCredentialManager migrates the legacy env/TLS files into one
// root-owned atomic state and returns a client whose certificate and bearer are
// swapped as one credential pair. HTTP-only development configurations keep the
// legacy static client and do not enable renewal.
func NewPanelCredentialManager(cfg Config) (*PanelCredentialManager, *http.Client, error) {
	mode, err := NormalizeCredentialRenewalMode(string(cfg.CredentialMode))
	if err != nil {
		return nil, nil, err
	}
	cfg.CredentialMode = mode
	tlsConfigured := cfg.PanelTLSCA != "" || cfg.PanelTLSCert != "" || cfg.PanelTLSKey != "" || cfg.PanelTLSServerName != ""
	if !tlsConfigured {
		client, err := NewPanelHTTPClient(cfg)
		return nil, client, err
	}
	if cfg.PanelTLSCA == "" || cfg.PanelTLSCert == "" || cfg.PanelTLSKey == "" {
		return nil, nil, errors.New("Panel mTLS requires CA, client certificate and client key")
	}
	origin, err := url.Parse(cfg.PanelURL)
	if err != nil || !strings.EqualFold(origin.Scheme, "https") || origin.Host == "" {
		return nil, nil, errors.New("Panel mTLS requires an absolute https NODE_AGENT_PANEL_URL")
	}
	if origin.User != nil || (origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, nil, errors.New("Panel mTLS NODE_AGENT_PANEL_URL must not contain credentials, a path, query or fragment")
	}
	caPEM, err := readCredentialFile(cfg.PanelTLSCA, maxCredentialStateBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("read Panel CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, nil, errors.New("Panel CA file contains no certificates")
	}
	store, err := newCredentialStateStore(cfg.CredentialStateDir)
	if err != nil {
		return nil, nil, err
	}
	state, err := store.loadOrMigrate(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("load credential state: %w", err)
	}
	manager := &PanelCredentialManager{
		cfg: cfg, store: store, roots: roots, origin: origin,
		logger: log.Default(), now: func() time.Time { return time.Now().UTC() }, state: state,
	}
	snapshot, err := manager.snapshot(state.Active)
	if err != nil {
		return nil, nil, fmt.Errorf("validate active credential: %w", err)
	}
	manager.current.Store(snapshot)

	manager.client = &http.Client{
		Transport:     &credentialAuthTransport{origin: origin, current: manager.current.Load},
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return manager, manager.client, nil
}

func (m *PanelCredentialManager) Run(ctx context.Context) {
	if m == nil || m.cfg.CredentialMode == CredentialRenewalOff {
		return
	}
	run := func() {
		if err := m.RunOnce(ctx); err != nil && ctx.Err() == nil {
			m.logf("credential renewal failed: %v", err)
		}
	}
	run()
	timer := time.NewTimer(m.nextCredentialCheckPeriod())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			run()
			timer.Reset(m.nextCredentialCheckPeriod())
		}
	}
}

func (m *PanelCredentialManager) nextCredentialCheckPeriod() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.CredentialMode == CredentialRenewalApply && m.state.Pending != nil {
		return credentialPendingRetryPeriod
	}
	return credentialRenewalCheckPeriod
}

func (m *PanelCredentialManager) RunOnce(ctx context.Context) error {
	if m == nil || m.cfg.CredentialMode == CredentialRenewalOff {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Pending != nil {
		if m.cfg.CredentialMode != CredentialRenewalApply {
			return nil
		}
		return m.resumePendingWithRecovery(ctx)
	}
	current := m.current.Load()
	if current == nil || current.leaf == nil {
		return errors.New("active credential certificate is unavailable")
	}
	now := m.now()
	renewBefore := m.cfg.CredentialRenewBefore
	if renewBefore <= 0 {
		renewBefore = 45 * 24 * time.Hour
	}
	if !credentialRenewalDue(now, current.leaf.NotAfter, renewBefore, current.leaf.Subject.CommonName) {
		return nil
	}
	if m.cfg.CredentialMode == CredentialRenewalObserve {
		m.logf("credential renewal is due; observe mode keeps the current credential")
		return nil
	}
	pending, err := newPendingCredential(current.leaf.Subject.CommonName)
	if err != nil {
		return err
	}
	m.state.Pending = &pending
	if err := m.store.write(m.state); err != nil {
		m.state.Pending = nil
		return fmt.Errorf("persist pending credential: %w", err)
	}
	return m.resumePendingWithRecovery(ctx)
}

func stableCredentialJitter(nodeID string) time.Duration {
	sum := sha256.Sum256([]byte(nodeID))
	days := int(sum[0]) % 8
	return time.Duration(days) * 24 * time.Hour
}

func credentialRenewalDue(now, notAfter time.Time, renewBefore time.Duration, nodeID string) bool {
	jitter := stableCredentialJitter(nodeID)
	if jitter >= renewBefore {
		jitter = renewBefore / 2
	}
	// The Panel accepts renewal only inside its configured due window. Jitter
	// therefore delays nodes within that window instead of requesting before it.
	return !notAfter.After(now.Add(renewBefore - jitter))
}

type credentialRenewalRequest struct {
	RenewalID       string `json:"renewal_id"`
	CSRPEM          string `json:"csr_pem"`
	NextTokenHash   string `json:"next_token_sha256"`
	NextTokenPrefix string `json:"next_token_prefix"`
}

type credentialRenewalResponse struct {
	RenewalID         string    `json:"renewal_id"`
	CertificatePEM    string    `json:"certificate_pem"`
	CertificateSHA256 string    `json:"certificate_sha256"`
	Serial            string    `json:"serial"`
	NotBefore         time.Time `json:"not_before"`
	NotAfter          time.Time `json:"not_after"`
	ConfirmBy         time.Time `json:"confirm_by"`
}

type panelCredentialError struct {
	status int
	code   string
}

func (e *panelCredentialError) Error() string {
	return fmt.Sprintf("Panel returned HTTP %d (%s)", e.status, e.code)
}

func (m *PanelCredentialManager) resumePending(ctx context.Context) error {
	pending := m.state.Pending
	if pending == nil {
		return nil
	}
	if err := m.validatePendingCredentialMaterial(*pending); err != nil {
		return fmt.Errorf("validate pending credential: %w", err)
	}
	if pending.CertificatePEM == "" {
		hash := sha256.Sum256([]byte(pending.Token))
		request := credentialRenewalRequest{
			RenewalID: pending.RenewalID, CSRPEM: pending.CSRPEM,
			NextTokenHash: hex.EncodeToString(hash[:]), NextTokenPrefix: credentialTokenPrefix(pending.Token),
		}
		var response credentialRenewalResponse
		if err := m.postJSON(ctx, m.client, "/agent/v1/credential-renewals", request, &response); err != nil {
			return fmt.Errorf("request credential certificate: %w", err)
		}
		if response.RenewalID != pending.RenewalID {
			return errors.New("Panel returned a mismatched credential renewal id")
		}
		issued := *pending
		issued.CertificatePEM = response.CertificatePEM
		issued.CertificateSHA256 = strings.ToLower(response.CertificateSHA256)
		issued.CertificateSerial = response.Serial
		issued.NotBefore = response.NotBefore
		issued.NotAfter = response.NotAfter
		issued.ConfirmBy = response.ConfirmBy
		if err := m.validateIssuedCredential(issued); err != nil {
			return fmt.Errorf("verify renewed credential: %w", err)
		}
		issuedState := m.state
		issuedState.Pending = &issued
		if err := m.store.write(issuedState); err != nil {
			return fmt.Errorf("persist issued credential: %w", err)
		}
		m.state = issuedState
		pending = m.state.Pending
	}
	candidate, err := m.snapshot(*pending)
	if err != nil {
		return fmt.Errorf("load pending credential: %w", err)
	}
	candidateClient := m.candidateClient(candidate)
	defer candidateClient.CloseIdleConnections()
	confirmPath := "/agent/v1/credential-renewals/" + url.PathEscape(pending.RenewalID) + "/confirm"
	if err := m.postJSON(ctx, candidateClient, confirmPath, struct{}{}, nil); err != nil {
		return fmt.Errorf("confirm renewed credential: %w", err)
	}
	active := *pending
	active.CSRPEM = ""
	active.RenewalID = ""
	active.ConfirmBy = time.Time{}
	next := CredentialState{Version: credentialStateVersion, Active: active}
	if err := m.store.write(next); err != nil {
		// Confirm is the server-side activation point. The issued pending
		// credential is already durable, so keep the Agent online with the now
		// active pair even if local cleanup/promotion must be retried.
		m.swapCurrentCredential(candidate)
		return fmt.Errorf("promote renewed credential: %w", err)
	}
	m.state = next
	m.swapCurrentCredential(candidate)
	m.logf("credential renewal completed; certificate valid until %s", candidate.leaf.NotAfter.UTC().Format(time.RFC3339))
	return nil
}

func (m *PanelCredentialManager) resumePendingWithRecovery(ctx context.Context) error {
	err := m.resumePending(ctx)
	var panelErr *panelCredentialError
	if !errors.As(err, &panelErr) || panelErr.code != "renewal_expired" {
		return err
	}
	next := m.state
	next.Pending = nil
	if writeErr := m.store.write(next); writeErr != nil {
		return errors.Join(err, fmt.Errorf("discard expired pending credential: %w", writeErr))
	}
	m.state = next
	m.logf("discarded expired pending credential; a new renewal will be attempted")
	return err
}

func (m *PanelCredentialManager) swapCurrentCredential(candidate *credentialSnapshot) {
	previous := m.current.Swap(candidate)
	if previous != nil && previous.transport != nil {
		previous.transport.CloseIdleConnections()
	}
}

func (m *PanelCredentialManager) postJSON(ctx context.Context, client *http.Client, path string, input any, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return errors.New("encode credential renewal request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.cfg.PanelURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxCredentialResponseBytes+1))
	if readErr != nil {
		return readErr
	}
	if len(responseBody) > maxCredentialResponseBytes {
		return errors.New("credential renewal response exceeds size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload struct {
			Code  string `json:"code"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(responseBody, &payload)
		if payload.Code == "" {
			payload.Code = payload.Error.Code
		}
		payload.Code = sanitizePanelErrorCode(payload.Code)
		return &panelCredentialError{status: response.StatusCode, code: payload.Code}
	}
	if output != nil {
		if len(responseBody) == 0 || json.Unmarshal(responseBody, output) != nil {
			return errors.New("invalid credential renewal response")
		}
	}
	return nil
}

func sanitizePanelErrorCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return "http_error"
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' {
			return "http_error"
		}
	}
	return value
}

func (m *PanelCredentialManager) candidateClient(snapshot *credentialSnapshot) *http.Client {
	return &http.Client{
		Transport:     &credentialAuthTransport{origin: m.origin, current: func() *credentialSnapshot { return snapshot }},
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (m *PanelCredentialManager) snapshot(material StoredCredential) (*credentialSnapshot, error) {
	if strings.TrimSpace(material.Token) == "" || len(material.Token) > 512 {
		return nil, errors.New("credential token is missing or too large")
	}
	certificate, err := tls.X509KeyPair([]byte(material.CertificatePEM), []byte(material.PrivateKeyPEM))
	if err != nil {
		return nil, errors.New("credential certificate and key do not match")
	}
	if len(certificate.Certificate) != 1 {
		return nil, errors.New("credential must contain one leaf certificate")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, errors.New("credential certificate is invalid")
	}
	if leaf.PublicKeyAlgorithm != x509.Ed25519 {
		return nil, errors.New("credential certificate must use Ed25519")
	}
	if strings.TrimSpace(leaf.Subject.CommonName) == "" {
		return nil, errors.New("credential certificate node identity is missing")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: m.roots, CurrentTime: m.now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, errors.New("credential certificate is not a valid client certificate")
	}
	current := m.current.Load()
	if current != nil && current.leaf != nil && leaf.Subject.CommonName != current.leaf.Subject.CommonName {
		return nil, errors.New("credential certificate node identity changed")
	}
	signer, ok := certificate.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("credential private key cannot sign")
	}
	certificateKey, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil {
		return nil, errors.New("credential public key is invalid")
	}
	privateKey, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || !bytes.Equal(certificateKey, privateKey) {
		return nil, errors.New("credential private key does not match certificate")
	}
	fingerprint := sha256.Sum256(leaf.Raw)
	fingerprintHex := hex.EncodeToString(fingerprint[:])
	if material.CertificateSHA256 != "" && !strings.EqualFold(material.CertificateSHA256, fingerprintHex) {
		return nil, errors.New("credential certificate fingerprint mismatch")
	}
	if material.CertificateSerial != "" && material.CertificateSerial != leaf.SerialNumber.String() {
		return nil, errors.New("credential certificate serial mismatch")
	}
	if !material.NotBefore.IsZero() && !material.NotBefore.Equal(leaf.NotBefore) {
		return nil, errors.New("credential certificate validity start mismatch")
	}
	if !material.NotAfter.IsZero() && !material.NotAfter.Equal(leaf.NotAfter) {
		return nil, errors.New("credential certificate expiry mismatch")
	}
	certificate.Leaf = leaf
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 10 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.TLSClientConfig = &tls.Config{
		Certificates: []tls.Certificate{certificate}, RootCAs: m.roots,
		ServerName: m.cfg.PanelTLSServerName, MinVersion: tls.VersionTLS13,
	}
	return &credentialSnapshot{token: material.Token, certificate: certificate, leaf: leaf, transport: transport}, nil
}

func (m *PanelCredentialManager) validateIssuedCredential(material StoredCredential) error {
	if material.CertificateSHA256 == "" || material.CertificateSerial == "" || material.NotBefore.IsZero() || material.NotAfter.IsZero() || material.ConfirmBy.IsZero() {
		return errors.New("credential renewal response is incomplete")
	}
	snapshot, err := m.snapshot(material)
	if err != nil {
		return err
	}
	now := m.now()
	if !material.ConfirmBy.After(now) || material.ConfirmBy.After(snapshot.leaf.NotAfter) {
		return errors.New("credential confirmation deadline is invalid")
	}
	return nil
}

func (m *PanelCredentialManager) validatePendingCredentialMaterial(material StoredCredential) error {
	if strings.TrimSpace(material.Token) == "" || len(material.Token) > 512 {
		return errors.New("pending credential token is missing or too large")
	}
	if !validCredentialRenewalID(material.RenewalID) {
		return errors.New("pending credential renewal id is invalid")
	}
	keyBlock, keyRest := pem.Decode([]byte(material.PrivateKeyPEM))
	if keyBlock == nil || keyBlock.Type != "PRIVATE KEY" || strings.TrimSpace(string(keyRest)) != "" {
		return errors.New("pending credential private key is invalid")
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return errors.New("pending credential private key is invalid")
	}
	privateKey, ok := parsedKey.(ed25519.PrivateKey)
	if !ok {
		return errors.New("pending credential private key must use Ed25519")
	}
	requestBlock, requestRest := pem.Decode([]byte(material.CSRPEM))
	if requestBlock == nil || requestBlock.Type != "CERTIFICATE REQUEST" || strings.TrimSpace(string(requestRest)) != "" {
		return errors.New("pending credential CSR is invalid")
	}
	request, err := x509.ParseCertificateRequest(requestBlock.Bytes)
	if err != nil || request.CheckSignature() != nil {
		return errors.New("pending credential CSR is invalid")
	}
	requestKey, ok := request.PublicKey.(ed25519.PublicKey)
	if !ok || !bytes.Equal(requestKey, privateKey.Public().(ed25519.PublicKey)) {
		return errors.New("pending credential CSR does not match its private key")
	}
	current := m.current.Load()
	if current == nil || current.leaf == nil || request.Subject.CommonName != current.leaf.Subject.CommonName {
		return errors.New("pending credential CSR node identity changed")
	}
	return nil
}

func (m *PanelCredentialManager) logf(format string, args ...any) {
	if m.logger != nil {
		m.logger.Printf(format, args...)
	}
}

func newPendingCredential(nodeID string) (StoredCredential, error) {
	if strings.TrimSpace(nodeID) == "" {
		return StoredCredential{}, errors.New("node identity is missing")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return StoredCredential{}, fmt.Errorf("generate credential key: %w", err)
	}
	requestDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: nodeID}}, privateKey)
	if err != nil {
		return StoredCredential{}, fmt.Errorf("generate credential CSR: %w", err)
	}
	parsedRequest, err := x509.ParseCertificateRequest(requestDER)
	parsedPublicKey, keyOK := parsedRequestPublicKey(parsedRequest)
	if err != nil || parsedRequest.CheckSignature() != nil || !keyOK || !bytes.Equal(parsedPublicKey, publicKey) {
		return StoredCredential{}, errors.New("generated credential CSR is invalid")
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return StoredCredential{}, fmt.Errorf("encode credential key: %w", err)
	}
	rawToken := make([]byte, 32)
	if _, err := rand.Read(rawToken); err != nil {
		return StoredCredential{}, fmt.Errorf("generate credential token: %w", err)
	}
	renewalID, err := randomCredentialUUID()
	if err != nil {
		return StoredCredential{}, err
	}
	return StoredCredential{
		Token:         "nfe_" + base64.RawURLEncoding.EncodeToString(rawToken),
		PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})),
		CSRPEM:        string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: requestDER})),
		RenewalID:     renewalID,
	}, nil
}

func parsedRequestPublicKey(request *x509.CertificateRequest) (ed25519.PublicKey, bool) {
	if request == nil {
		return nil, false
	}
	key, ok := request.PublicKey.(ed25519.PublicKey)
	return key, ok
}

func randomCredentialUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate credential renewal id: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func validCredentialRenewalID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value[14] != '4' {
		return false
	}
	for index, r := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return value[19] == '8' || value[19] == '9' || value[19] == 'a' || value[19] == 'b'
}

func credentialTokenPrefix(token string) string {
	if len(token) <= 12 {
		return token
	}
	return token[:12]
}

func newCredentialStateStore(directory string) (credentialStateStore, error) {
	directory = filepath.Clean(strings.TrimSpace(directory))
	if !filepath.IsAbs(directory) || directory == "/" {
		return credentialStateStore{}, errors.New("credential state directory must be an absolute dedicated path")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return credentialStateStore{}, fmt.Errorf("create credential state directory: %w", err)
	}
	if err := validateCredentialPath(directory, true); err != nil {
		return credentialStateStore{}, err
	}
	return credentialStateStore{directory: directory, path: filepath.Join(directory, credentialStateFileName)}, nil
}

func (s credentialStateStore) loadOrMigrate(cfg Config) (CredentialState, error) {
	state, err := s.load()
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return CredentialState{}, err
	}
	certificatePEM, err := readCredentialFile(cfg.PanelTLSCert, maxCredentialStateBytes)
	if err != nil {
		return CredentialState{}, err
	}
	privateKeyPEM, err := readCredentialFile(cfg.PanelTLSKey, maxCredentialStateBytes)
	if err != nil {
		return CredentialState{}, err
	}
	certificateBlock, _ := pem.Decode(certificatePEM)
	if certificateBlock == nil || certificateBlock.Type != "CERTIFICATE" {
		return CredentialState{}, errors.New("legacy credential certificate is invalid")
	}
	leaf, err := x509.ParseCertificate(certificateBlock.Bytes)
	if err != nil {
		return CredentialState{}, errors.New("legacy credential certificate is invalid")
	}
	fingerprint := sha256.Sum256(leaf.Raw)
	state = CredentialState{Version: credentialStateVersion, Active: StoredCredential{
		Token: cfg.Token, CertificatePEM: string(certificatePEM), PrivateKeyPEM: string(privateKeyPEM),
		CertificateSHA256: hex.EncodeToString(fingerprint[:]), CertificateSerial: leaf.SerialNumber.String(),
		NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter,
	}}
	if err := s.write(state); err != nil {
		return CredentialState{}, err
	}
	return state, nil
}

func (s credentialStateStore) load() (CredentialState, error) {
	if err := validateCredentialPath(s.path, false); err != nil {
		return CredentialState{}, err
	}
	file, err := os.OpenFile(s.path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return CredentialState{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return CredentialState{}, err
	}
	if err := validateCredentialInfo(info, false); err != nil {
		return CredentialState{}, err
	}
	limited := io.LimitReader(file, maxCredentialStateBytes+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return CredentialState{}, err
	}
	if len(content) > maxCredentialStateBytes {
		return CredentialState{}, errors.New("credential state exceeds size limit")
	}
	var state CredentialState
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return CredentialState{}, errors.New("credential state is invalid")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return CredentialState{}, errors.New("credential state is invalid")
	}
	if err := validateCredentialStateStructure(state); err != nil {
		return CredentialState{}, err
	}
	return state, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing credential state data")
	}
	return nil
}

func validateCredentialStateStructure(state CredentialState) error {
	if state.Version != credentialStateVersion || strings.TrimSpace(state.Active.Token) == "" || len(state.Active.Token) > 512 || strings.TrimSpace(state.Active.CertificatePEM) == "" || strings.TrimSpace(state.Active.PrivateKeyPEM) == "" {
		return errors.New("credential state is incomplete")
	}
	if state.Pending != nil {
		pending := state.Pending
		if strings.TrimSpace(pending.Token) == "" || len(pending.Token) > 512 || strings.TrimSpace(pending.PrivateKeyPEM) == "" || strings.TrimSpace(pending.CSRPEM) == "" || !validCredentialRenewalID(pending.RenewalID) {
			return errors.New("pending credential state is incomplete")
		}
		if pending.CertificatePEM != "" && (pending.CertificateSHA256 == "" || pending.CertificateSerial == "" || pending.NotBefore.IsZero() || pending.NotAfter.IsZero() || pending.ConfirmBy.IsZero()) {
			return errors.New("issued pending credential state is incomplete")
		}
	}
	return nil
}

func (s credentialStateStore) write(state CredentialState) error {
	if err := validateCredentialPath(s.directory, true); err != nil {
		return err
	}
	if err := validateExistingCredentialStatePath(s.path); err != nil {
		return err
	}
	state.Version = credentialStateVersion
	if err := validateCredentialStateStructure(state); err != nil {
		return err
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return errors.New("encode credential state")
	}
	if len(content) > maxCredentialStateBytes {
		return errors.New("credential state exceeds size limit")
	}
	temporary, err := os.CreateTemp(s.directory, ".state-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(append(content, '\n')); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := validateCredentialPath(s.directory, true); err != nil {
		return err
	}
	if err := validateExistingCredentialStatePath(s.path); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return err
	}
	directory, err := os.Open(s.directory)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	committed = true
	return nil
}

func validateExistingCredentialStatePath(path string) error {
	err := validateCredentialPath(path, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func validateCredentialPath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("credential state path must not be a symlink")
	}
	return validateCredentialInfo(info, directory)
}

func validateCredentialInfo(info os.FileInfo, directory bool) error {
	if directory && !info.IsDir() {
		return errors.New("credential state path is not a directory")
	}
	if !directory && !info.Mode().IsRegular() {
		return errors.New("credential state file is not regular")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("credential state permissions are too broad")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		return errors.New("credential state owner does not match Agent user")
	}
	return nil
}

func readCredentialFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, errors.New("credential file exceeds size limit")
	}
	return content, nil
}
