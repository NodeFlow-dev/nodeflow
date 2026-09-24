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

	"github.com/stretchr/testify/require"
)

type updateFixture struct {
	staging  string
	public   ed25519.PublicKey
	private  ed25519.PrivateKey
	manifest UpdateManifest
}

func newUpdateFixture(t *testing.T) updateFixture {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	staging := t.TempDir()
	artifact := []byte("nodeflow-agent-candidate\n")
	require.NoError(t, os.WriteFile(filepath.Join(staging, "node-agent"), artifact, 0751))
	digest := sha256.Sum256(artifact)
	manifest := UpdateManifest{
		Version:      "0.2.0-dev",
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		SHA256:       hex.EncodeToString(digest[:]),
		Size:         int64(len(artifact)),
		Sequence:     12,
		ArtifactPath: "node-agent",
	}
	signUpdateManifest(t, private, &manifest)
	return updateFixture{staging: staging, public: public, private: private, manifest: manifest}
}

func (fixture updateFixture) verifier(t *testing.T, currentSequence uint64) *UpdateVerifier {
	t.Helper()
	verifier, err := NewUpdateVerifier(UpdateVerifierConfig{
		Mode:            UpdateModeVerifyOnly,
		StagingDir:      fixture.staging,
		PublicKeyBase64: base64.StdEncoding.EncodeToString(fixture.public),
		CurrentSequence: currentSequence,
	})
	require.NoError(t, err)
	return verifier
}

func signUpdateManifest(t *testing.T, private ed25519.PrivateKey, manifest *UpdateManifest) {
	t.Helper()
	canonical, err := CanonicalUpdateManifest(*manifest)
	require.NoError(t, err)
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(private, canonical))
}

func requireUpdateErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var verificationError *UpdateVerificationError
	require.ErrorAs(t, err, &verificationError)
	require.Equal(t, code, verificationError.Code)
}

func TestUpdateVerifierVerifiesSignedLocalArtifactWithoutMutation(t *testing.T) {
	fixture := newUpdateFixture(t)
	artifactPath := filepath.Join(fixture.staging, fixture.manifest.ArtifactPath)
	before, err := os.Stat(artifactPath)
	require.NoError(t, err)

	verifier := fixture.verifier(t, 11)
	result, err := verifier.Verify(context.Background(), fixture.manifest)
	require.NoError(t, err)
	require.Equal(t, UpdateVerificationVerified, result.Status)
	require.Equal(t, fixture.manifest.Version, result.Version)
	require.Equal(t, fixture.manifest.Sequence, result.Sequence)
	require.Equal(t, fixture.manifest.SHA256, result.SHA256)
	require.Equal(t, fixture.manifest.Size, result.Size)

	after, err := os.Stat(artifactPath)
	require.NoError(t, err)
	require.Equal(t, before.Mode(), after.Mode())
	// verify-only deliberately does not advance local state.
	second, err := verifier.Verify(context.Background(), fixture.manifest)
	require.NoError(t, err)
	require.Equal(t, UpdateVerificationVerified, second.Status)
}

func TestUpdateVerifierRejectsSignedFieldTampering(t *testing.T) {
	fixture := newUpdateFixture(t)
	fixture.manifest.Version = "0.2.1-dev"

	result, err := fixture.verifier(t, 0).Verify(context.Background(), fixture.manifest)
	requireUpdateErrorCode(t, err, "signature_invalid")
	require.Equal(t, UpdateVerificationRejected, result.Status)
}

func TestUpdateVerifierRejectsArtifactHashMismatch(t *testing.T) {
	fixture := newUpdateFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(fixture.staging, "node-agent"), []byte("nodeflow-agent-tampered!\n"), 0751))
	// Preserve the signed size so verification reaches the SHA-256 check.
	tampered, err := os.ReadFile(filepath.Join(fixture.staging, "node-agent"))
	require.NoError(t, err)
	if int64(len(tampered)) != fixture.manifest.Size {
		require.NoError(t, os.WriteFile(filepath.Join(fixture.staging, "node-agent"), make([]byte, fixture.manifest.Size), 0751))
	}

	result, err := fixture.verifier(t, 0).Verify(context.Background(), fixture.manifest)
	requireUpdateErrorCode(t, err, "artifact_hash_mismatch")
	require.Equal(t, UpdateVerificationRejected, result.Status)
}

func TestUpdateVerifierRejectsPathsOutsideStaging(t *testing.T) {
	fixture := newUpdateFixture(t)
	verifier := fixture.verifier(t, 0)
	for _, path := range []string{"../node-agent", "/tmp/node-agent", "nested/../node-agent", "nested//node-agent", `nested\node-agent`} {
		t.Run(path, func(t *testing.T) {
			manifest := fixture.manifest
			manifest.ArtifactPath = path
			manifest.Signature = ""
			result, err := verifier.Verify(context.Background(), manifest)
			requireUpdateErrorCode(t, err, "invalid_manifest")
			require.Equal(t, UpdateVerificationRejected, result.Status)
		})
	}
}

func TestUpdateVerifierRejectsArtifactSymlinks(t *testing.T) {
	fixture := newUpdateFixture(t)
	require.NoError(t, os.Mkdir(filepath.Join(fixture.staging, "release"), 0750))
	require.NoError(t, os.Symlink("../node-agent", filepath.Join(fixture.staging, "release", "candidate")))
	manifest := fixture.manifest
	manifest.ArtifactPath = "release/candidate"
	signUpdateManifest(t, fixture.private, &manifest)

	result, err := fixture.verifier(t, 0).Verify(context.Background(), manifest)
	requireUpdateErrorCode(t, err, "artifact_path_rejected")
	require.Equal(t, UpdateVerificationRejected, result.Status)
}

func TestUpdateVerifierRejectsSymlinkInArtifactParent(t *testing.T) {
	fixture := newUpdateFixture(t)
	require.NoError(t, os.Symlink(fixture.staging, filepath.Join(fixture.staging, "release")))
	manifest := fixture.manifest
	manifest.ArtifactPath = "release/node-agent"
	signUpdateManifest(t, fixture.private, &manifest)

	_, err := fixture.verifier(t, 0).Verify(context.Background(), manifest)
	requireUpdateErrorCode(t, err, "artifact_path_rejected")
}

func TestNewUpdateVerifierRejectsSymlinkedStagingDirectory(t *testing.T) {
	fixture := newUpdateFixture(t)
	link := filepath.Join(t.TempDir(), "staging")
	require.NoError(t, os.Symlink(fixture.staging, link))

	_, err := NewUpdateVerifier(UpdateVerifierConfig{
		Mode:            UpdateModeVerifyOnly,
		StagingDir:      link,
		PublicKeyBase64: base64.StdEncoding.EncodeToString(fixture.public),
	})
	require.Error(t, err)
}

func TestUpdateVerifierRejectsOversizeManifest(t *testing.T) {
	fixture := newUpdateFixture(t)
	manifest := fixture.manifest
	manifest.Size = MaxUpdateArtifactSize + 1
	signUpdateManifest(t, fixture.private, &manifest)

	result, err := fixture.verifier(t, 0).Verify(context.Background(), manifest)
	requireUpdateErrorCode(t, err, "artifact_too_large")
	require.Equal(t, UpdateVerificationRejected, result.Status)
}

func TestUpdateVerifierRejectsPlatformMismatch(t *testing.T) {
	fixture := newUpdateFixture(t)
	fixture.manifest.OS = "not-this-os"
	signUpdateManifest(t, fixture.private, &fixture.manifest)

	result, err := fixture.verifier(t, 0).Verify(context.Background(), fixture.manifest)
	requireUpdateErrorCode(t, err, "platform_mismatch")
	require.Equal(t, UpdateVerificationRejected, result.Status)
}

func TestUpdateVerifierRejectsReplay(t *testing.T) {
	fixture := newUpdateFixture(t)

	for _, current := range []uint64{fixture.manifest.Sequence, fixture.manifest.Sequence + 1} {
		result, err := fixture.verifier(t, current).Verify(context.Background(), fixture.manifest)
		requireUpdateErrorCode(t, err, "sequence_replay")
		require.Equal(t, UpdateVerificationRejected, result.Status)
	}
}

func TestUpdateVerifierOffDoesNotInspectManifestOrFilesystem(t *testing.T) {
	verifier, err := NewUpdateVerifier(UpdateVerifierConfig{Mode: UpdateModeOff})
	require.NoError(t, err)

	result, err := verifier.Verify(context.Background(), UpdateManifest{ArtifactPath: "/missing/outside"})
	require.NoError(t, err)
	require.Equal(t, UpdateVerificationOff, result.Status)
}

func TestUpdateVerifierRejectsCanceledContext(t *testing.T) {
	fixture := newUpdateFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := fixture.verifier(t, 0).Verify(ctx, fixture.manifest)
	require.True(t, errors.Is(err, context.Canceled))
	requireUpdateErrorCode(t, err, "verification_canceled")
	require.Equal(t, UpdateVerificationRejected, result.Status)
}

func TestNewUpdateVerifierStrictConfiguration(t *testing.T) {
	fixture := newUpdateFixture(t)
	validKey := base64.StdEncoding.EncodeToString(fixture.public)

	_, err := NewUpdateVerifier(UpdateVerifierConfig{Mode: "apply"})
	require.Error(t, err)
	_, err = NewUpdateVerifier(UpdateVerifierConfig{Mode: UpdateModeVerifyOnly, StagingDir: fixture.staging, PublicKeyBase64: validKey + "\n"})
	require.Error(t, err)
	_, err = NewUpdateVerifier(UpdateVerifierConfig{Mode: UpdateModeVerifyOnly, StagingDir: "relative", PublicKeyBase64: validKey})
	require.Error(t, err)
}
