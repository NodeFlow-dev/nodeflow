package panel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nodeflow/nodeflow/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestReleaseService(t *testing.T, store Store) *ReleaseService {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	rawKey, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	keyPath := filepath.Join(t.TempDir(), "update-signing.key")
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rawKey}), 0o600))
	directory := t.TempDir()
	service, err := NewReleaseService(directory, keyPath, store)
	require.NoError(t, err)
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func TestReleaseServiceStoresAndSignsImmutableArtifact(t *testing.T) {
	store := &fakeStore{}
	service := newTestReleaseService(t, store)
	release, err := service.Create(context.Background(), "0.3.0", "linux", "amd64", strings.NewReader("Agent binary"))
	require.NoError(t, err)
	assert.Equal(t, int64(1), release.Sequence)
	assert.Equal(t, int64(len("Agent binary")), release.SizeBytes)

	manifest := agent.UpdateManifest{
		Version: release.Version, OS: release.OS, Arch: release.Arch, SHA256: release.SHA256,
		Size: release.SizeBytes, Sequence: uint64(release.Sequence), Signature: release.Signature, ArtifactPath: release.ArtifactPath,
	}
	canonical, err := agent.CanonicalUpdateManifest(manifest)
	require.NoError(t, err)
	publicKey, err := base64.StdEncoding.DecodeString(service.PublicKeyBase64())
	require.NoError(t, err)
	signature, err := base64.StdEncoding.DecodeString(release.Signature)
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(ed25519.PublicKey(publicKey), canonical, signature))

	artifact, err := service.OpenArtifact(release.ArtifactPath)
	require.NoError(t, err)
	defer artifact.Close()
	payload, err := io.ReadAll(artifact)
	require.NoError(t, err)
	assert.Equal(t, []byte("Agent binary"), payload)
}

func TestReleaseServiceCloneVerifiedCreatesNewSignedSequence(t *testing.T) {
	store := &fakeStore{}
	service := newTestReleaseService(t, store)
	source, err := service.Create(context.Background(), "0.3.0", "linux", "amd64", strings.NewReader("known good Agent"))
	require.NoError(t, err)

	clone, err := service.CloneVerified(context.Background(), source)
	require.NoError(t, err)
	assert.Equal(t, source.Version, clone.Version)
	assert.Equal(t, source.OS, clone.OS)
	assert.Equal(t, source.Arch, clone.Arch)
	assert.Equal(t, source.SHA256, clone.SHA256)
	assert.Equal(t, source.SizeBytes, clone.SizeBytes)
	assert.Greater(t, clone.Sequence, source.Sequence)
	assert.NotEqual(t, source.ArtifactPath, clone.ArtifactPath)
}

func TestReleaseServiceCloneVerifiedRejectsTamperedArtifact(t *testing.T) {
	store := &fakeStore{}
	service := newTestReleaseService(t, store)
	source, err := service.Create(context.Background(), "0.3.0", "linux", "amd64", strings.NewReader("known good Agent"))
	require.NoError(t, err)
	path := filepath.Join(service.directory, source.ArtifactPath)
	require.NoError(t, os.Chmod(path, 0o600))
	require.NoError(t, os.WriteFile(path, []byte("tampered Agent!!"), 0o600))

	_, err = service.CloneVerified(context.Background(), source)
	assert.ErrorIs(t, err, ErrReleaseArtifactIntegrity)
	assert.Len(t, store.releases, 1)
}

func TestBootstrapReleaseSelectionUsesNewestCompatibleOrExplicitRelease(t *testing.T) {
	store := &fakeStore{}
	service := newTestReleaseService(t, store)
	old, err := service.Create(context.Background(), "0.3.0", "linux", "amd64", strings.NewReader("old Agent"))
	require.NoError(t, err)
	_, err = service.Create(context.Background(), "0.4.0", "linux", "arm64", strings.NewReader("arm Agent"))
	require.NoError(t, err)
	latest, err := service.Create(context.Background(), "0.5.0", "linux", "amd64", strings.NewReader("latest Agent"))
	require.NoError(t, err)

	selected, err := service.OpenBootstrapRelease(context.Background(), "", "linux", "amd64")
	require.NoError(t, err)
	defer selected.Content.Close()
	assert.Equal(t, latest.ID, selected.ID)
	payload, err := io.ReadAll(selected.Content)
	require.NoError(t, err)
	assert.Equal(t, "latest Agent", string(payload))

	explicit, err := service.OpenBootstrapRelease(context.Background(), old.ID, "linux", "amd64")
	require.NoError(t, err)
	defer explicit.Content.Close()
	assert.Equal(t, old.ID, explicit.ID)
	_, err = service.OpenBootstrapRelease(context.Background(), old.ID, "linux", "arm64")
	assert.ErrorIs(t, err, ErrReleasePlatform)
}

func TestAgentReleaseDeleteRemovesUnusedArtifactAndRejectsInUse(t *testing.T) {
	store := &fakeStore{}
	service := newTestReleaseService(t, store)
	release, err := service.Create(context.Background(), "0.3.0", "linux", "amd64", strings.NewReader("Agent binary"))
	require.NoError(t, err)
	handler := NewHandlerWithServices(store, Config{AdminToken: testAdminToken}, nil, service)
	w := request(t, handler, http.MethodDelete, "/api/v1/agent-releases/"+release.ID, "", testAdminToken)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	assert.Empty(t, store.releases)
	_, err = os.Stat(filepath.Join(service.directory, release.ArtifactPath))
	assert.ErrorIs(t, err, os.ErrNotExist)
	replacement, err := service.Create(context.Background(), "0.3.1", "linux", "amd64", strings.NewReader("replacement Agent"))
	require.NoError(t, err)
	assert.Greater(t, replacement.Sequence, release.Sequence, "deleted release sequences must never be reused")

	store.deleteReleaseErr = ErrReleaseInUse
	w = request(t, handler, http.MethodDelete, "/api/v1/agent-releases/"+release.ID, "", testAdminToken)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"release_in_use"`)
}

func TestAgentUpdateRollbackClonesOlderArtifactAndAudits(t *testing.T) {
	store := &fakeStore{}
	service := newTestReleaseService(t, store)
	target, err := service.Create(context.Background(), "0.3.0", "linux", "amd64", strings.NewReader("old known good Agent"))
	require.NoError(t, err)
	current, err := service.Create(context.Background(), "0.4.3-dev", "linux", "amd64", strings.NewReader("current Agent"))
	require.NoError(t, err)
	store.updateState = NodeAgentUpdateState{
		NodeID: testNodeID, DesiredRelease: &current, ActualSequence: current.Sequence, State: "installed",
	}
	handler := NewHandlerWithServices(store, Config{AdminToken: testAdminToken}, nil, service)
	body := `{"target_release_id":"` + target.ID + `","expected_actual_sequence":2,"expected_desired_sequence":2}`
	w := request(t, handler, http.MethodPost, "/api/v1/nodes/"+testNodeID+"/agent-update/rollback", body, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, store.releases, 3)
	rollback := store.releases[2]
	assert.Equal(t, int64(3), rollback.Sequence)
	assert.Equal(t, target.SHA256, rollback.SHA256)
	require.NotNil(t, store.updateState.DesiredRelease)
	assert.Equal(t, rollback.ID, store.updateState.DesiredRelease.ID)
	assert.Equal(t, int64(2), store.assignExpectedActual)
	assert.Equal(t, int64(2), store.assignExpectedDesired)
	require.Len(t, store.auditEvents, 1)
	assert.Equal(t, "agent.update.rollback", store.auditEvents[0].Action)
	assert.Equal(t, testNodeID, store.auditEvents[0].ResourceID)
}

func TestAgentUpdateRollbackValidatesStateAndTarget(t *testing.T) {
	t.Run("release service required", func(t *testing.T) {
		w := request(t, handler(&fakeStore{}), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/agent-update/rollback",
			`{"target_release_id":"`+testRouteID+`","expected_actual_sequence":2,"expected_desired_sequence":0}`, testAdminToken)
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})

	t.Run("desired sequence is required", func(t *testing.T) {
		store := &fakeStore{}
		service := newTestReleaseService(t, store)
		w := request(t, NewHandlerWithServices(store, Config{AdminToken: testAdminToken}, nil, service), http.MethodPost,
			"/api/v1/nodes/"+testNodeID+"/agent-update/rollback",
			`{"target_release_id":"`+testRouteID+`","expected_actual_sequence":2}`, testAdminToken)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), `"code":"validation_error"`)
	})

	t.Run("desired sequence must be nonnegative", func(t *testing.T) {
		store := &fakeStore{}
		service := newTestReleaseService(t, store)
		w := request(t, NewHandlerWithServices(store, Config{AdminToken: testAdminToken}, nil, service), http.MethodPost,
			"/api/v1/nodes/"+testNodeID+"/agent-update/rollback",
			`{"target_release_id":"`+testRouteID+`","expected_actual_sequence":2,"expected_desired_sequence":-1}`, testAdminToken)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), `"code":"validation_error"`)
	})

	t.Run("actual sequence is optimistic concurrency guard", func(t *testing.T) {
		store := &fakeStore{updateState: NodeAgentUpdateState{NodeID: testNodeID, ActualSequence: 3, State: "installed"}}
		service := newTestReleaseService(t, store)
		w := request(t, NewHandlerWithServices(store, Config{AdminToken: testAdminToken}, nil, service), http.MethodPost,
			"/api/v1/nodes/"+testNodeID+"/agent-update/rollback",
			`{"target_release_id":"`+testRouteID+`","expected_actual_sequence":2,"expected_desired_sequence":0}`, testAdminToken)
		assert.Equal(t, http.StatusConflict, w.Code)
		assert.Contains(t, w.Body.String(), "actual_sequence_changed")
	})

	t.Run("desired sequence is optimistic concurrency guard", func(t *testing.T) {
		desired := AgentRelease{Sequence: 3}
		store := &fakeStore{updateState: NodeAgentUpdateState{
			NodeID: testNodeID, DesiredRelease: &desired, ActualSequence: 3, State: "pending",
		}}
		service := newTestReleaseService(t, store)
		w := request(t, NewHandlerWithServices(store, Config{AdminToken: testAdminToken}, nil, service), http.MethodPost,
			"/api/v1/nodes/"+testNodeID+"/agent-update/rollback",
			`{"target_release_id":"`+testRouteID+`","expected_actual_sequence":3,"expected_desired_sequence":2}`, testAdminToken)
		assert.Equal(t, http.StatusConflict, w.Code)
		assert.Contains(t, w.Body.String(), `"code":"update_state_changed"`)
		assert.Empty(t, store.releases, "stale rollback must not clone an artifact")
	})

	t.Run("update in progress", func(t *testing.T) {
		store := &fakeStore{updateState: NodeAgentUpdateState{NodeID: testNodeID, ActualSequence: 2, State: "activating"}}
		service := newTestReleaseService(t, store)
		w := request(t, NewHandlerWithServices(store, Config{AdminToken: testAdminToken}, nil, service), http.MethodPost,
			"/api/v1/nodes/"+testNodeID+"/agent-update/rollback",
			`{"target_release_id":"`+testRouteID+`","expected_actual_sequence":2,"expected_desired_sequence":0}`, testAdminToken)
		assert.Equal(t, http.StatusConflict, w.Code)
		assert.Contains(t, w.Body.String(), "update_in_progress")
	})

	t.Run("target does not exist", func(t *testing.T) {
		store := &fakeStore{updateState: NodeAgentUpdateState{NodeID: testNodeID, ActualSequence: 2, State: "idle"}}
		service := newTestReleaseService(t, store)
		w := request(t, NewHandlerWithServices(store, Config{AdminToken: testAdminToken}, nil, service), http.MethodPost,
			"/api/v1/nodes/"+testNodeID+"/agent-update/rollback",
			`{"target_release_id":"`+testRouteID+`","expected_actual_sequence":2,"expected_desired_sequence":0}`, testAdminToken)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestAgentUpdateRollbackReturnsStableConflictWhenStateChangesDuringClone(t *testing.T) {
	store := &fakeStore{}
	service := newTestReleaseService(t, store)
	target, err := service.Create(context.Background(), "0.3.0", "linux", "amd64", strings.NewReader("old known good Agent"))
	require.NoError(t, err)
	current, err := service.Create(context.Background(), "0.4.3-dev", "linux", "amd64", strings.NewReader("current Agent"))
	require.NoError(t, err)
	store.updateState = NodeAgentUpdateState{
		NodeID: testNodeID, DesiredRelease: &current, ActualSequence: current.Sequence, State: "installed",
	}
	store.assignReleaseErr = ErrAgentUpdateStateChanged

	handler := NewHandlerWithServices(store, Config{AdminToken: testAdminToken}, nil, service)
	body := `{"target_release_id":"` + target.ID + `","expected_actual_sequence":2,"expected_desired_sequence":2}`
	w := request(t, handler, http.MethodPost, "/api/v1/nodes/"+testNodeID+"/agent-update/rollback", body, testAdminToken)

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"update_state_changed"`)
	assert.Len(t, store.releases, 3, "clone remains immutable even when final assignment CAS loses")
	assert.Equal(t, int64(2), store.assignExpectedActual)
	assert.Equal(t, int64(2), store.assignExpectedDesired)
}

func TestAgentReleaseUploadAndMTLSArtifactDownload(t *testing.T) {
	store := &fakeStore{}
	service := newTestReleaseService(t, store)
	handler := NewHandlerWithServices(store, Config{AdminToken: testAdminToken, RequireAgentMTLS: true}, nil, service)

	upload := httptest.NewRequest(http.MethodPost, "/api/v1/agent-releases?version=0.3.0&os=linux&arch=amd64", strings.NewReader("Agent binary"))
	upload.Header.Set("Authorization", "Bearer "+testAdminToken)
	upload.Header.Set("Content-Type", "application/octet-stream")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, upload)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Len(t, store.releases, 1)

	leaf := &x509.Certificate{Subject: pkix.Name{CommonName: testNodeID}}
	download := httptest.NewRequest(http.MethodGet, "/agent/v1/updates/1/artifact", nil)
	download.Header.Set("Authorization", "Bearer enrollment-secret")
	download.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{leaf},
		VerifiedChains:   [][]*x509.Certificate{{leaf}},
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, download)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "Agent binary", w.Body.String())
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}
