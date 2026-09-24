package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type updateRunner struct {
	name string
	args []string
	err  error
}

func (r *updateRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name, r.args = name, append([]string(nil), args...)
	return nil, r.err
}

func TestUpdateCoordinatorDownloadsVerifiesAndQueuesActivation(t *testing.T) {
	artifact := []byte("signed Agent candidate")
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	digest := sha256.Sum256(artifact)
	manifest := UpdateManifest{
		Version: "0.3.0", OS: runtime.GOOS, Arch: runtime.GOARCH,
		SHA256: hex.EncodeToString(digest[:]), Size: int64(len(artifact)), Sequence: 7,
		ArtifactPath: "00000000000000000007-linux-amd64-0123456789abcdef.bin",
	}
	canonical, err := CanonicalUpdateManifest(manifest)
	require.NoError(t, err)
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, canonical))

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/agent/v1/updates/7/artifact", r.URL.Path)
		assert.Equal(t, "Bearer enrollment-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "22")
		_, _ = w.Write(artifact)
	}))
	defer server.Close()
	directory := t.TempDir()
	verifier, err := NewUpdateVerifier(UpdateVerifierConfig{
		Mode: UpdateModeApply, StagingDir: directory,
		PublicKeyBase64: base64.StdEncoding.EncodeToString(publicKey),
	})
	require.NoError(t, err)
	runner := &updateRunner{}
	coordinator, err := NewUpdateCoordinator(UpdateCoordinatorConfig{
		Verifier: verifier, Client: server.Client(), PanelURL: server.URL, Token: "enrollment-token",
		StagingDir: directory, PendingFile: filepath.Join(directory, "pending.json"),
		StateFile: filepath.Join(directory, "state.json"), ResultFile: filepath.Join(directory, "result.json"),
		HelperService: "nodeflow-node-updater.service", Runner: runner,
	})
	require.NoError(t, err)

	result, err := coordinator.Process(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, UpdateVerificationActivating, result.Status)
	assert.Equal(t, "systemctl", runner.name)
	assert.Equal(t, []string{"start", "--no-block", "nodeflow-node-updater.service"}, runner.args)
	pending, err := LoadPendingUpdate(filepath.Join(directory, "pending.json"))
	require.NoError(t, err)
	assert.Equal(t, manifest, pending.Manifest)
	stored, err := os.ReadFile(filepath.Join(directory, manifest.ArtifactPath))
	require.NoError(t, err)
	assert.Equal(t, artifact, stored)
}

func TestUpdateCoordinatorDoesNotRetryRolledBackSequence(t *testing.T) {
	fixture := newUpdateFixture(t)
	verifier, err := NewUpdateVerifier(UpdateVerifierConfig{
		Mode: UpdateModeApply, StagingDir: fixture.staging,
		PublicKeyBase64: base64.StdEncoding.EncodeToString(fixture.public),
	})
	require.NoError(t, err)
	resultPath := filepath.Join(fixture.staging, "result.json")
	require.NoError(t, WriteUpdateResult(resultPath, UpdateResult{
		Status: UpdateVerificationRolledBack, Version: fixture.manifest.Version,
		Sequence: fixture.manifest.Sequence, SHA256: fixture.manifest.SHA256,
		Code: "activation_failed", FinishedAt: time.Now().UTC(),
	}))
	runner := &updateRunner{}
	coordinator, err := NewUpdateCoordinator(UpdateCoordinatorConfig{
		Verifier: verifier, Client: &http.Client{}, PanelURL: "https://panel.test", Token: "token",
		StagingDir: fixture.staging, PendingFile: filepath.Join(fixture.staging, "pending.json"),
		StateFile: filepath.Join(fixture.staging, "state.json"), ResultFile: resultPath,
		HelperService: "updater.service", Runner: runner,
	})
	require.NoError(t, err)
	result, err := coordinator.Process(context.Background(), fixture.manifest)
	require.NoError(t, err)
	assert.Equal(t, UpdateVerificationRolledBack, result.Status)
	assert.Empty(t, runner.name)
}

func TestUpdateCoordinatorVerifyOnlyTreatsVerifiedSequenceAsTerminal(t *testing.T) {
	fixture := newUpdateFixture(t)
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	coordinator, err := NewUpdateCoordinator(UpdateCoordinatorConfig{
		Verifier: fixture.verifier(t, 0), Client: server.Client(), PanelURL: server.URL, Token: "token",
		StagingDir: fixture.staging, StateFile: filepath.Join(fixture.staging, "state.json"),
		ResultFile: filepath.Join(fixture.staging, "result.json"),
	})
	require.NoError(t, err)

	first, err := coordinator.Process(context.Background(), fixture.manifest)
	require.NoError(t, err)
	assert.Equal(t, UpdateVerificationVerified, first.Status)
	require.NoError(t, os.Remove(filepath.Join(fixture.staging, fixture.manifest.ArtifactPath)))
	second, err := coordinator.Process(context.Background(), fixture.manifest)
	require.NoError(t, err)
	assert.Equal(t, UpdateVerificationVerified, second.Status)
	assert.Zero(t, requests, "terminal verify-only assignment was downloaded again")
}

func TestUpdateCoordinatorReplacesCorruptRegularStagedArtifact(t *testing.T) {
	artifact := []byte("signed replacement candidate")
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	digest := sha256.Sum256(artifact)
	manifest := UpdateManifest{
		Version: "0.3.1", OS: runtime.GOOS, Arch: runtime.GOARCH,
		SHA256: hex.EncodeToString(digest[:]), Size: int64(len(artifact)), Sequence: 8,
		ArtifactPath: "00000000000000000008-linux-amd64-0123456789abcdef.bin",
	}
	canonical, err := CanonicalUpdateManifest(manifest)
	require.NoError(t, err)
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, canonical))

	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(artifact)))
		_, _ = w.Write(artifact)
	}))
	defer server.Close()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, manifest.ArtifactPath), bytes.Repeat([]byte("x"), len(artifact)), 0o500))
	verifier, err := NewUpdateVerifier(UpdateVerifierConfig{
		Mode: UpdateModeApply, StagingDir: directory,
		PublicKeyBase64: base64.StdEncoding.EncodeToString(publicKey),
	})
	require.NoError(t, err)
	coordinator, err := NewUpdateCoordinator(UpdateCoordinatorConfig{
		Verifier: verifier, Client: server.Client(), PanelURL: server.URL, Token: "token", StagingDir: directory,
		PendingFile: filepath.Join(directory, "pending.json"), StateFile: filepath.Join(directory, "state.json"),
		ResultFile: filepath.Join(directory, "result.json"), HelperService: "updater.service", Runner: &updateRunner{},
	})
	require.NoError(t, err)

	result, err := coordinator.Process(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, UpdateVerificationActivating, result.Status)
	assert.Equal(t, 1, requests)
	stored, err := os.ReadFile(filepath.Join(directory, manifest.ArtifactPath))
	require.NoError(t, err)
	assert.Equal(t, artifact, stored)
}
