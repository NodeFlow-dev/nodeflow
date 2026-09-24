package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func activationFixture(t *testing.T) (UpdateActivationConfig, UpdateManifest, string, string) {
	t.Helper()
	directory := t.TempDir()
	targetDirectory := t.TempDir()
	target := filepath.Join(targetDirectory, "node-agent")
	require.NoError(t, os.WriteFile(target, []byte("old-agent"), 0o755))
	candidate := []byte("new-agent")
	digest := sha256.Sum256(candidate)
	manifest := UpdateManifest{
		Version: "0.3.0", OS: runtime.GOOS, Arch: runtime.GOARCH, SHA256: hex.EncodeToString(digest[:]),
		Size: int64(len(candidate)), Sequence: 3, ArtifactPath: "candidate.bin",
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	canonical, err := CanonicalUpdateManifest(manifest)
	require.NoError(t, err)
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(private, canonical))
	require.NoError(t, os.WriteFile(filepath.Join(directory, manifest.ArtifactPath), candidate, 0o500))
	pendingPath := filepath.Join(directory, "pending.json")
	require.NoError(t, WritePendingUpdate(pendingPath, PendingUpdate{Manifest: manifest, RequestedAt: time.Now().UTC()}))
	config := UpdateActivationConfig{
		StagingDir: directory, PendingFile: pendingPath, StateFile: filepath.Join(directory, "state.json"),
		ResultFile: filepath.Join(directory, "result.json"), ActivationFile: filepath.Join(directory, "activation.json"), TargetBinary: target,
		PublicKeyBase64: base64.StdEncoding.EncodeToString(public),
		AgentService:    "agent.service", Runner: &updateRunner{}, Now: time.Now,
	}
	return config, manifest, target, directory
}

func TestUpdateActivatorIndependentlyRejectsTamperedSignature(t *testing.T) {
	config, manifest, target, _ := activationFixture(t)
	manifest.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	require.NoError(t, WritePendingUpdate(config.PendingFile, PendingUpdate{Manifest: manifest, RequestedAt: time.Now().UTC()}))
	config.HealthCheck = func(context.Context, string) error { return nil }

	result, err := (UpdateActivator{Config: config}).Activate(context.Background())
	require.Error(t, err)
	assert.Equal(t, UpdateVerificationRejected, result.Status)
	assert.Equal(t, "updater_verification_failed", result.Code)
	installed, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	assert.Equal(t, []byte("old-agent"), installed)
}

func TestUpdateActivatorInstallsHealthyCandidate(t *testing.T) {
	config, manifest, target, directory := activationFixture(t)
	config.HealthCheck = func(_ context.Context, version string) error {
		assert.Equal(t, manifest.Version, version)
		return nil
	}
	result, err := (UpdateActivator{Config: config}).Activate(context.Background())
	require.NoError(t, err)
	assert.Equal(t, UpdateVerificationInstalled, result.Status)
	installed, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, []byte("new-agent"), installed)
	state, err := LoadInstalledUpdateState(config.StateFile)
	require.NoError(t, err)
	assert.Equal(t, manifest.Sequence, state.Sequence)
	_, err = os.Stat(config.PendingFile)
	assert.True(t, errors.Is(err, os.ErrNotExist))
	_, err = os.Stat(filepath.Join(directory, manifest.ArtifactPath))
	assert.True(t, errors.Is(err, os.ErrNotExist))
	_, err = os.Stat(config.ActivationFile)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestUpdateActivatorRecoversInterruptedActivationBeforeRetry(t *testing.T) {
	config, manifest, target, directory := activationFixture(t)
	config.HealthCheck = func(context.Context, string) error { return nil }
	backupDirectory := filepath.Join(directory, "backups")
	require.NoError(t, os.Mkdir(backupDirectory, 0o700))
	backup := filepath.Join(backupDirectory, "node-agent-before-interruption")
	require.NoError(t, os.WriteFile(backup, []byte("old-agent"), 0o500))
	require.NoError(t, os.WriteFile(target, []byte("broken-candidate"), 0o755))
	require.NoError(t, WriteUpdateActivationJournal(config.ActivationFile, UpdateActivationJournal{
		Manifest: manifest, BackupBinary: backup, StartedAt: time.Now().UTC(),
	}))

	result, err := (UpdateActivator{Config: config}).Activate(context.Background())
	require.NoError(t, err)
	assert.Equal(t, UpdateVerificationRolledBack, result.Status)
	assert.Equal(t, "interrupted_activation", result.Code)
	installed, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	assert.Equal(t, []byte("old-agent"), installed)
}

func TestUpdateActivatorCompletesCleanupAfterCommittedState(t *testing.T) {
	config, manifest, target, _ := activationFixture(t)
	config.HealthCheck = func(context.Context, string) error { return nil }
	require.NoError(t, os.WriteFile(target, []byte("new-agent"), 0o755))
	require.NoError(t, WriteInstalledUpdateState(config.StateFile, InstalledUpdateState{
		Version: manifest.Version, Sequence: manifest.Sequence, SHA256: manifest.SHA256, InstalledAt: time.Now().UTC(),
	}))

	result, err := (UpdateActivator{Config: config}).Activate(context.Background())
	require.NoError(t, err)
	assert.Equal(t, UpdateVerificationInstalled, result.Status)
	_, statErr := os.Stat(config.PendingFile)
	assert.True(t, errors.Is(statErr, os.ErrNotExist))
}

func TestUpdateActivatorRollsBackUnhealthyCandidate(t *testing.T) {
	config, manifest, target, directory := activationFixture(t)
	config.HealthCheck = func(_ context.Context, version string) error {
		if version == manifest.Version {
			return errors.New("candidate failed")
		}
		return nil
	}
	result, err := (UpdateActivator{Config: config}).Activate(context.Background())
	require.NoError(t, err)
	assert.Equal(t, UpdateVerificationRolledBack, result.Status)
	assert.Equal(t, "activation_failed", result.Code)
	installed, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, []byte("old-agent"), installed)
	_, err = os.Stat(config.StateFile)
	assert.True(t, errors.Is(err, os.ErrNotExist))
	storedResult, err := LoadUpdateResult(config.ResultFile)
	require.NoError(t, err)
	assert.Equal(t, UpdateVerificationRolledBack, storedResult.Status)
	_, err = os.Stat(filepath.Join(directory, manifest.ArtifactPath))
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestUpdateActivatorRetainsOnlyNewestBackups(t *testing.T) {
	config, _, _, directory := activationFixture(t)
	config.HealthCheck = func(context.Context, string) error { return nil }
	backupDirectory := filepath.Join(directory, "backups")
	require.NoError(t, os.Mkdir(backupDirectory, 0o700))
	for _, name := range []string{
		"node-agent-before-00000000000000000000-1",
		"node-agent-before-00000000000000000001-1",
		"node-agent-before-00000000000000000002-1",
		"node-agent-before-00000000000000000002-2",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(backupDirectory, name), []byte("old"), 0o500))
	}

	_, err := (UpdateActivator{Config: config}).Activate(context.Background())
	require.NoError(t, err)
	entries, err := os.ReadDir(backupDirectory)
	require.NoError(t, err)
	assert.Len(t, entries, maxRetainedUpdateBackups)
}
